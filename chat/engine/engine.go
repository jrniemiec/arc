// Package engine implements the workspace chat engine.
package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jrniemiec/arc/chat"
	"github.com/jrniemiec/arc/chat/corpusmap"
	"github.com/jrniemiec/arc/chat/prompt"
	"github.com/jrniemiec/arc/chat/provider"
	"github.com/jrniemiec/arc/chat/strategy"
	"github.com/jrniemiec/arc/chat/tools"
	"github.com/jrniemiec/arc/config"
	"github.com/jrniemiec/arc/store/fs"
)

// chatCfgWithDefaults merges workspace chatCfg with in-code fallbacks.
func chatCfgWithDefaults(chatCfg config.ChatConfig) config.ChatConfig {
	if chatCfg.Strategy == "" {
		chatCfg.Strategy = "tail"
	}
	if chatCfg.MaxUserMessages == 0 {
		chatCfg.MaxUserMessages = defaultMaxUserMessages
	}
	if chatCfg.VerbatimRatio == 0 {
		chatCfg.VerbatimRatio = 0.4
	}
	if chatCfg.GroundingMode == "" {
		chatCfg.GroundingMode = prompt.DefaultMode
	}
	return chatCfg
}

const defaultMaxUserMessages = 50
const defaultTokenBudget = 8000
const maxToolRounds = 5

// ChatCallbacks groups the callback functions used during a tool-aware chat turn.
type ChatCallbacks struct {
	OnTextDelta  func(string) error        // streaming text to TUI/CLI
	OnToolStart  func(toolName string)     // tool execution started
	OnToolDone   func(toolName string)     // tool execution finished
	OnRoundStart func(round int)           // new tool round started
}

// Engine manages a workspace chat session.
type Engine struct {
	cfg           config.Config
	workspaceName string
	profileName   string
	profile       config.Profile
	chatCfg       config.ChatConfig
	prov          chat.Provider
	st            *chat.ChatStore
	history       *chat.History
	systemPrompt  string

	// Tool-loop state.
	groundingMode  string
	corpusMap      corpusmap.Result
	mapFingerprint string
	wsDescription  string
	persona        string // user-authored system.txt content
	tools          []chat.ToolDef
	turnID         int

	mu        sync.Mutex
	streaming bool

	// session accounting
	sessionIn  int
	sessionOut int
}

// ChatOptions controls per-call behaviour.
type ChatOptions struct {
	SkipHistory      bool
	NoStream         bool
	StrategyOverride string
	BudgetOverride   int
	Out              io.Writer // for strategy notifications (e.g. compaction); nil = quiet
	Debug            bool
}

// ChatResult holds the outcome of a Chat() call.
type ChatResult struct {
	Usage   chat.Usage
	Elapsed time.Duration
	Rounds  int
}

// New creates an Engine for the given workspace, loading history and system prompt.
// profileName may be empty — falls back to chatCfg.Profile, then cfg.Ingest.FlashProfile.
//
// The system prompt is assembled from persona (system.txt), base instructions,
// grounding mode, and corpus map. It is rebuilt when the workspace changes.
func New(cfg config.Config, workspaceName, profileName string) (*Engine, error) {
	// Resolve chat config (apply in-code defaults for fields missing from file).
	rawChatCfg, _ := fs.ReadChatConfig(cfg.DataRoot, workspaceName)
	chatCfg := chatCfgWithDefaults(rawChatCfg)

	// Resolve profile.
	resolved := profileName
	if resolved == "" {
		resolved = chatCfg.Profile
	}
	if resolved == "" {
		resolved = cfg.Ingest.FlashProfile
	}
	prof, ok := cfg.Profiles[resolved]
	if !ok {
		// Fall back to any available profile.
		for name, p := range cfg.Profiles {
			prof = p
			resolved = name
			break
		}
		if resolved == "" {
			return nil, fmt.Errorf("no profiles configured")
		}
	}

	prov, err := provider.New(prof, chatCfg.MaxOutputTokens)
	if err != nil {
		return nil, fmt.Errorf("init provider for profile %q: %w", resolved, err)
	}

	st := chat.NewChatStore(cfg.DataRoot, workspaceName)

	history, err := st.LoadHistory()
	if err != nil {
		return nil, fmt.Errorf("load history: %w", err)
	}

	persona, err := st.LoadSystem()
	if err != nil {
		return nil, fmt.Errorf("load system: %w", err)
	}

	// Read workspace description for the corpus map header.
	var wsDescription string
	if wsMeta, err := fs.ReadWorkspaceMeta(cfg.DataRoot, workspaceName); err == nil {
		wsDescription = wsMeta.Description
	}

	// Build initial corpus map.
	cm, err := corpusmap.Build(cfg, workspaceName, wsDescription, 0)
	if err != nil {
		return nil, fmt.Errorf("build corpus map: %w", err)
	}

	groundingMode := chatCfg.GroundingMode
	systemPrompt := prompt.AssembleSystemPrompt(persona, groundingMode, cm.Text)

	// Build tool set for current grounding mode.
	tools := toolSet(groundingMode)

	return &Engine{
		cfg:            cfg,
		workspaceName:  workspaceName,
		profileName:    resolved,
		profile:        prof,
		chatCfg:        chatCfg,
		prov:           prov,
		st:             st,
		history:        history,
		systemPrompt:   systemPrompt,
		groundingMode:  groundingMode,
		corpusMap:      cm,
		mapFingerprint: cm.Fingerprint,
		wsDescription:  wsDescription,
		persona:        persona,
		tools:          tools,
	}, nil
}

// Chat sends prompt to the provider with the tool loop, applies the context
// strategy, and (unless SkipHistory) persists the exchange to history.
// onDelta is called for each streaming token; pass nil to suppress streaming.
func (e *Engine) Chat(ctx context.Context, userPrompt string, opts ChatOptions, onDelta func(string) error) (ChatResult, error) {
	cb := ChatCallbacks{OnTextDelta: onDelta}
	return e.ChatWithTools(ctx, userPrompt, opts, cb)
}

// ChatWithTools sends prompt to the provider with tool calling support.
// The tool loop runs up to maxToolRounds rounds; each round may execute
// multiple tool calls concurrently. Tool results are appended to the
// working message list and sent back to the model until it produces a
// final text response (stop_reason == "end_turn").
func (e *Engine) ChatWithTools(ctx context.Context, userPrompt string, opts ChatOptions, cb ChatCallbacks) (ChatResult, error) {
	e.mu.Lock()
	if e.streaming {
		e.mu.Unlock()
		return ChatResult{}, fmt.Errorf("a chat is already in progress")
	}
	e.streaming = true
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		e.streaming = false
		e.mu.Unlock()
	}()

	e.turnID++
	turnStart := time.Now()

	// --- 1. Check corpus map freshness ---
	if err := e.refreshCorpusMap(); err != nil {
		slog.Warn("corpus map refresh failed", "err", err)
	}

	// --- 2. Build context messages from collapsed history ---
	strat := e.buildStrategy(opts)
	contextMsgs := strat.Apply(e.history, userPrompt)
	workingMsgs := append(contextMsgs, chat.Message{Role: chat.RoleUser, Content: userPrompt})

	// Audit trail — everything to persist after the turn.
	auditMsgs := []chat.Message{{Role: chat.RoleUser, Content: userPrompt, Time: turnStart}}

	slog.Info("chat turn start",
		"workspace", e.workspaceName,
		"profile", e.profileName,
		"mode", e.groundingMode,
		"turn", e.turnID,
		"context_msgs", len(workingMsgs))

	// --- 3. Tool loop ---
	var turnUsage chat.Usage
	var finalText string
	round := 0

	for {
		round++
		if cb.OnRoundStart != nil {
			cb.OnRoundStart(round)
		}

		// Debug: save effective request snapshot.
		if opts.Debug {
			e.saveRequestSnapshot(workingMsgs)
		}

		// --- 3a. Send request ---
		resp, err := e.prov.ChatStreamWithTools(
			ctx,
			e.systemPrompt,
			workingMsgs,
			e.tools,
			cb.OnTextDelta,
			func(toolName string) error {
				if cb.OnToolStart != nil {
					cb.OnToolStart(toolName)
				}
				return nil
			},
		)
		if err != nil {
			return ChatResult{Usage: turnUsage, Elapsed: time.Since(turnStart), Rounds: round}, err
		}

		// Accumulate usage.
		turnUsage.InputTokens += resp.Usage.InputTokens
		turnUsage.OutputTokens += resp.Usage.OutputTokens

		slog.Info("chat api call",
			"workspace", e.workspaceName,
			"turn", e.turnID,
			"round", round,
			"input_tokens", resp.Usage.InputTokens,
			"output_tokens", resp.Usage.OutputTokens,
			"stop_reason", resp.StopReason)
		e.appendAPICallEvent(resp.Usage, round)

		// --- 3b. Extract text and tool calls ---
		var textParts []string
		var toolCalls []chat.ToolCall
		for _, block := range resp.Content {
			switch block.Type {
			case "text":
				textParts = append(textParts, block.Text)
			case "tool_use":
				toolCalls = append(toolCalls, chat.ToolCall{
					ID:    block.ToolUseID,
					Name:  block.ToolUseName,
					Input: block.ToolUseInput,
				})
			}
		}
		assistantText := strings.Join(textParts, "")

		// --- 3c. No tool calls — turn is done ---
		if resp.StopReason == "end_turn" || len(toolCalls) == 0 {
			finalText = assistantText
			now := time.Now()
			auditMsgs = append(auditMsgs, chat.Message{
				Role:    chat.RoleAssistant,
				Content: assistantText,
				Profile: e.profileName,
				Time:    now,
			})
			break
		}

		// --- 3d. Tool calls — record intermediate assistant, execute, loop ---
		now := time.Now()
		assistantMsg := chat.Message{
			Role:      chat.RoleAssistant,
			Content:   assistantText,
			Profile:   e.profileName,
			Time:      now,
			ToolCalls: toolCalls,
		}
		auditMsgs = append(auditMsgs, assistantMsg)
		workingMsgs = append(workingMsgs, assistantMsg)

		// Execute tool calls concurrently.
		results := e.parallelExec(ctx, toolCalls, cb)

		// Record results and append to working list.
		for i, call := range toolCalls {
			resultMsg := chat.Message{
				Role:       chat.RoleToolResult,
				Content:    results[i].content,
				ToolCallID: call.ID,
				Time:       time.Now(),
			}
			auditMsgs = append(auditMsgs, resultMsg)
			workingMsgs = append(workingMsgs, resultMsg)
		}

		// --- 3e. Cap check ---
		if round >= maxToolRounds {
			slog.Info("tool cap hit", "workspace", e.workspaceName, "turn", e.turnID, "round", round)
			e.appendToolCapEvent()
			// Inject nudge so the model answers with what it has.
			workingMsgs = append(workingMsgs, chat.Message{
				Role:    chat.RoleUser,
				Content: prompt.CapHitNudge,
			})
			// Allow one more round (the loop will send the nudge and the
			// model should respond with end_turn).
		}
	}

	elapsed := time.Since(turnStart)

	// --- 4. Persist to history ---
	if !opts.SkipHistory {
		for _, msg := range auditMsgs {
			e.history.Msgs = append(e.history.Msgs, msg)
		}
		if saveErr := e.st.SaveHistory(e.history); saveErr != nil {
			slog.Warn("save history failed", "err", saveErr)
		}
	}

	// --- 5. Log turn complete ---
	costUSD := e.cfg.CalcCost(e.profile.Model, turnUsage.InputTokens, turnUsage.OutputTokens)
	slog.Info("chat turn complete",
		"workspace", e.workspaceName,
		"turn", e.turnID,
		"rounds", round,
		"input_tokens", turnUsage.InputTokens,
		"output_tokens", turnUsage.OutputTokens,
		"cost_usd", fmt.Sprintf("%.6f", costUSD),
		"elapsed", elapsed)
	e.appendTurnCompleteEvent(turnUsage, round, elapsed)

	// --- 6. Update session accounting ---
	e.mu.Lock()
	e.sessionIn += turnUsage.InputTokens
	e.sessionOut += turnUsage.OutputTokens
	e.mu.Unlock()

	_ = finalText // response text is in history; callers read it from there

	return ChatResult{Usage: turnUsage, Elapsed: elapsed, Rounds: round}, nil
}

// toolResult holds the output of a single tool execution.
type toolResult struct {
	content string
	elapsed time.Duration
}

// parallelExec runs all tool calls concurrently, returns results in call order.
func (e *Engine) parallelExec(ctx context.Context, calls []chat.ToolCall, cb ChatCallbacks) []toolResult {
	results := make([]toolResult, len(calls))
	var wg sync.WaitGroup
	for i, call := range calls {
		wg.Add(1)
		go func(i int, call chat.ToolCall) {
			defer wg.Done()
			start := time.Now()
			content, err := execTool(ctx, e.cfg, e.workspaceName, call)
			if err != nil {
				content = "Tool error: " + err.Error()
			}
			el := time.Since(start)
			results[i] = toolResult{content: content, elapsed: el}

			slog.Info("tool result",
				"tool", call.Name,
				"size", len(content),
				"elapsed", el)

			if cb.OnToolDone != nil {
				cb.OnToolDone(call.Name)
			}
		}(i, call)
	}
	wg.Wait()
	return results
}

// execTool delegates to the tools package for execution.
func execTool(ctx context.Context, cfg config.Config, workspaceName string, call chat.ToolCall) (string, error) {
	return tools.ExecTool(ctx, cfg, workspaceName, call)
}

// refreshCorpusMap checks the corpus map fingerprint and rebuilds if changed.
func (e *Engine) refreshCorpusMap() error {
	fp, err := corpusmap.ComputeFingerprint(e.cfg, e.workspaceName)
	if err != nil {
		return err
	}
	if fp == e.mapFingerprint {
		return nil
	}
	cm, err := corpusmap.Build(e.cfg, e.workspaceName, e.wsDescription, 0)
	if err != nil {
		return err
	}
	e.corpusMap = cm
	e.mapFingerprint = cm.Fingerprint
	e.systemPrompt = prompt.AssembleSystemPrompt(e.persona, e.groundingMode, cm.Text)
	slog.Info("corpus map rebuilt", "workspace", e.workspaceName, "articles", cm.Articles)
	return nil
}

// SetGroundingMode changes the grounding mode for this session.
func (e *Engine) SetGroundingMode(mode string) error {
	if !prompt.ValidMode(mode) {
		return fmt.Errorf("unknown grounding mode: %q (valid: corpus-only, corpus-first, open)", mode)
	}
	e.groundingMode = mode
	e.systemPrompt = prompt.AssembleSystemPrompt(e.persona, e.groundingMode, e.corpusMap.Text)
	e.tools = toolSet(mode)
	slog.Info("grounding mode changed", "mode", mode)
	return nil
}

// GroundingMode returns the current grounding mode.
func (e *Engine) GroundingMode() string { return e.groundingMode }

// ForceMapRebuild forces a corpus map rebuild regardless of fingerprint.
func (e *Engine) ForceMapRebuild() error {
	cm, err := corpusmap.Build(e.cfg, e.workspaceName, e.wsDescription, 0)
	if err != nil {
		return err
	}
	e.corpusMap = cm
	e.mapFingerprint = cm.Fingerprint
	e.systemPrompt = prompt.AssembleSystemPrompt(e.persona, e.groundingMode, cm.Text)
	slog.Info("corpus map force rebuilt", "workspace", e.workspaceName, "articles", cm.Articles)
	return nil
}

// toolSet returns tool definitions filtered by grounding mode.
func toolSet(mode string) []chat.ToolDef {
	return tools.ToolSet(mode)
}

// saveRequestSnapshot writes the effective request to disk for debugging.
func (e *Engine) saveRequestSnapshot(msgs []chat.Message) {
	type snapshot struct {
		Timestamp     time.Time      `json:"timestamp"`
		GroundingMode string         `json:"grounding_mode"`
		SystemPrompt  string         `json:"system_prompt"`
		Tools         []chat.ToolDef `json:"tools"`
		Messages      []chat.Message `json:"messages"`
	}
	s := snapshot{
		Timestamp:     time.Now().UTC(),
		GroundingMode: e.groundingMode,
		SystemPrompt:  e.systemPrompt,
		Tools:         e.tools,
		Messages:      msgs,
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return
	}
	chatDir := filepath.Join(e.cfg.DataRoot, "workspaces", e.workspaceName, "chat")
	_ = os.MkdirAll(chatDir, 0755)
	_ = os.WriteFile(filepath.Join(chatDir, "request-effective-history.json"), data, 0644)
}

// ClearHistory resets history (in memory and on disk).
func (e *Engine) ClearHistory() error {
	if err := e.st.ClearHistory(); err != nil {
		return err
	}
	e.history = chat.NewHistory()
	return nil
}

// History returns the current conversation history.
func (e *Engine) History() *chat.History { return e.history }

// SystemPrompt returns the workspace system prompt.
func (e *Engine) SystemPrompt() string { return e.systemPrompt }

// ProfileName returns the active profile name.
func (e *Engine) ProfileName() string { return e.profileName }

// Profile returns the active profile config.
func (e *Engine) Profile() config.Profile { return e.profile }

// WorkspaceName returns the workspace name.
func (e *Engine) WorkspaceName() string { return e.workspaceName }

// SessionStats returns cumulative token counts and estimated cost for this session.
func (e *Engine) SessionStats() (inputTokens, outputTokens int, costUSD float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	costUSD = e.cfg.CalcCost(e.profile.Model, e.sessionIn, e.sessionOut)
	return e.sessionIn, e.sessionOut, costUSD
}

// buildStrategy constructs the context strategy for a call.
func (e *Engine) buildStrategy(opts ChatOptions) strategy.ContextStrategy {
	name := e.chatCfg.Strategy
	if opts.StrategyOverride != "" {
		name = opts.StrategyOverride
	}

	budget := e.chatCfg.ContextLimit
	if opts.BudgetOverride > 0 {
		budget = opts.BudgetOverride
	}

	if name == strategy.StrategySummarize {
		if budget <= 0 {
			budget = defaultTokenBudget
		}
		// Use a dedicated summarizer provider if SummarizerProfile is configured.
		summarizerProv := chat.Provider(e.prov)
		if e.chatCfg.SummarizerProfile != "" && e.chatCfg.SummarizerProfile != e.profileName {
			if sp, ok := e.cfg.Profiles[e.chatCfg.SummarizerProfile]; ok {
				if p, err := provider.New(sp, 0); err == nil {
					summarizerProv = p
				}
			}
		}
		return &strategy.SummarizeStrategy{
			SummarizerProvider: summarizerProv,
			SummarizerBudget:   budget,
			WorkspaceName:      e.workspaceName,
			Store:              e.st,
			Ctx:                context.Background(),
			Out:                opts.Out,
			Budget:             budget,
			VerbatimRatio:      e.chatCfg.VerbatimRatio,
		}
	}

	return strategy.New(name, budget, e.systemPrompt, e.chatCfg.MaxUserMessages)
}

// --- Event logging ---

type chatCostInfo struct {
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd,omitempty"`
}

func (e *Engine) appendEvent(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	f, err := os.OpenFile(e.cfg.EventsPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(data, '\n'))
}

// appendAPICallEvent logs a single API call within a turn.
func (e *Engine) appendAPICallEvent(u chat.Usage, round int) {
	costUSD := e.cfg.CalcCost(e.profile.Model, u.InputTokens, u.OutputTokens)
	e.appendEvent(struct {
		TS            time.Time    `json:"ts"`
		Type          string       `json:"type"`
		WorkspaceName string       `json:"workspace_name"`
		TurnID        int          `json:"turn_id"`
		Round         int          `json:"round"`
		Profile       string       `json:"profile"`
		Model         string       `json:"model"`
		Cost          chatCostInfo `json:"cost"`
	}{
		TS:            time.Now().UTC(),
		Type:          "chat_api_call",
		WorkspaceName: e.workspaceName,
		TurnID:        e.turnID,
		Round:         round,
		Profile:       e.profileName,
		Model:         e.profile.Model,
		Cost: chatCostInfo{
			InputTokens:  u.InputTokens,
			OutputTokens: u.OutputTokens,
			CostUSD:      costUSD,
		},
	})
}

// appendTurnCompleteEvent logs the completion of an entire turn (all rounds).
func (e *Engine) appendTurnCompleteEvent(u chat.Usage, rounds int, elapsed time.Duration) {
	costUSD := e.cfg.CalcCost(e.profile.Model, u.InputTokens, u.OutputTokens)
	e.appendEvent(struct {
		TS            time.Time    `json:"ts"`
		Type          string       `json:"type"`
		WorkspaceName string       `json:"workspace_name"`
		TurnID        int          `json:"turn_id"`
		Rounds        int          `json:"rounds"`
		Profile       string       `json:"profile"`
		Model         string       `json:"model"`
		Cost          chatCostInfo `json:"cost"`
		ElapsedMs     int64        `json:"elapsed_ms"`
	}{
		TS:            time.Now().UTC(),
		Type:          "chat_turn_complete",
		WorkspaceName: e.workspaceName,
		TurnID:        e.turnID,
		Rounds:        rounds,
		Profile:       e.profileName,
		Model:         e.profile.Model,
		Cost: chatCostInfo{
			InputTokens:  u.InputTokens,
			OutputTokens: u.OutputTokens,
			CostUSD:      costUSD,
		},
		ElapsedMs: elapsed.Milliseconds(),
	})
}

// appendToolCapEvent logs when the tool-round cap is hit.
func (e *Engine) appendToolCapEvent() {
	e.appendEvent(struct {
		TS            time.Time `json:"ts"`
		Type          string    `json:"type"`
		WorkspaceName string    `json:"workspace_name"`
		TurnID        int       `json:"turn_id"`
	}{
		TS:            time.Now().UTC(),
		Type:          "chat_tool_cap_hit",
		WorkspaceName: e.workspaceName,
		TurnID:        e.turnID,
	})
}

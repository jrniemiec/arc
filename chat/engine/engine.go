// Package engine implements the workspace chat engine.
package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jrniemiec/arc/chat"
	"github.com/jrniemiec/arc/chat/provider"
	"github.com/jrniemiec/arc/chat/strategy"
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
	return chatCfg
}

const defaultMaxUserMessages = 50
const defaultTokenBudget = 8000

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
}

// New creates an Engine for the given workspace, loading history and system prompt.
// profileName may be empty — falls back to chatCfg.Profile, then cfg.Ingest.FlashProfile.
//
// The system prompt is assembled once at init (system.txt + RAG instruction + knowledge base)
// and sent unchanged on every LLM call. See local/CHAT_ARCHITECTURE.md for full details.
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

	systemPrompt, err := st.LoadSystem()
	if err != nil {
		return nil, fmt.Errorf("load system: %w", err)
	}

	// Build RAG prefix from workspace corpus and prepend to system prompt.
	ragInstruction := chat.RAGModeInstruction(chatCfg.RAGMode, chatCfg.RAGInstruction)
	ragPrefix, err := chat.BuildRAGContext(cfg, workspaceName, ragInstruction)
	if err != nil {
		return nil, fmt.Errorf("build RAG context: %w", err)
	}
	if ragPrefix != "" {
		if systemPrompt != "" {
			systemPrompt = strings.TrimSpace(systemPrompt) + "\n\n" + ragPrefix
		} else {
			systemPrompt = ragPrefix
		}
	}

	return &Engine{
		cfg:           cfg,
		workspaceName: workspaceName,
		profileName:   resolved,
		profile:       prof,
		chatCfg:       chatCfg,
		prov:          prov,
		st:            st,
		history:       history,
		systemPrompt:  systemPrompt,
	}, nil
}

// Chat sends prompt to the provider, applies the context strategy, and
// (unless SkipHistory) persists the exchange to history.
// onDelta is called for each streaming token; pass nil to suppress streaming.
func (e *Engine) Chat(ctx context.Context, prompt string, opts ChatOptions, onDelta func(string) error) (ChatResult, error) {
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

	strat := e.buildStrategy(opts)
	contextMsgs := strat.Apply(e.history, prompt)
	allMsgs := append(contextMsgs, chat.Message{Role: chat.RoleUser, Content: prompt})

	if opts.Debug {
		fmt.Fprintf(os.Stderr, "[debug] profile=%s provider=%s model=%s\n",
			e.profileName, e.profile.Provider, e.profile.Model)
		fmt.Fprintf(os.Stderr, "[debug] strategy=%s context_messages=%d system=%v\n",
			strat.Name(), len(allMsgs), e.systemPrompt != "")
		for i, m := range allMsgs {
			preview := m.Content
			if len(preview) > 80 {
				preview = preview[:80] + "..."
			}
			fmt.Fprintf(os.Stderr, "[debug]   [%d] %s: %q\n", i, m.Role, preview)
		}
	}

	start := time.Now()
	var (
		response string
		usage    chat.Usage
		err      error
	)
	if opts.NoStream || onDelta == nil {
		response, usage, err = e.prov.Chat(ctx, e.systemPrompt, allMsgs)
	} else {
		response, usage, err = e.prov.ChatStream(ctx, e.systemPrompt, allMsgs, onDelta)
	}
	elapsed := time.Since(start)

	if err != nil {
		return ChatResult{Usage: usage, Elapsed: elapsed}, err
	}

	if !opts.SkipHistory {
		logTs := time.Now()
		e.history.Append(chat.RoleUser, prompt)
		e.history.AppendAssistant(response, e.profileName, logTs)
		if saveErr := e.st.SaveHistory(e.history); saveErr != nil {
			fmt.Fprintf(io.Discard, "save history: %v", saveErr)
		}
		e.appendChatEvent(usage, logTs)
	}

	e.mu.Lock()
	e.sessionIn += usage.InputTokens
	e.sessionOut += usage.OutputTokens
	e.mu.Unlock()

	return ChatResult{Usage: usage, Elapsed: elapsed}, nil
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

// chatEventRecord is the JSON structure written to events.jsonl for chat turns.
type chatEventRecord struct {
	TS            time.Time    `json:"ts"`
	Type          string       `json:"type"`
	WorkspaceName string       `json:"workspace_name"`
	Profile       string       `json:"profile"`
	Model         string       `json:"model"`
	Cost          chatCostInfo `json:"cost"`
}

type chatCostInfo struct {
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd,omitempty"`
}

func (e *Engine) appendChatEvent(u chat.Usage, ts time.Time) {
	costUSD := e.cfg.CalcCost(e.profile.Model, u.InputTokens, u.OutputTokens)
	ev := chatEventRecord{
		TS:            ts.UTC(),
		Type:          "chat",
		WorkspaceName: e.workspaceName,
		Profile:       e.profileName,
		Model:         e.profile.Model,
		Cost: chatCostInfo{
			InputTokens:  u.InputTokens,
			OutputTokens: u.OutputTokens,
			CostUSD:      costUSD,
		},
	}
	data, err := json.Marshal(ev)
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

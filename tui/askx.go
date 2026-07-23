package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/jrniemiec/arc/chat"
	"github.com/jrniemiec/arc/config"
	storefs "github.com/jrniemiec/arc/store/fs"
	"github.com/jrniemiec/arc/tts"
	"github.com/jrniemiec/llm"
)

// ── Toggle & lifecycle ──────────────────────────────────────────────────────

// toggleAskX toggles the global askX pane (Ctrl+X). Pre-fills input with "/AskX ".
func (m *Model) toggleAskX() {
	if m.askxOpen {
		m.closeAskX()
		m.clearAskXInput()
		return
	}
	// Mutual exclusion: close scratch and preview if open.
	if m.scratchOpen {
		m.closeScratch()
	}
	if m.previewOpen {
		m.closePreview()
	}
	m.askxGlobal = true
	m.askxOpen = true
	m.loadAskXHistory()
	m.rebuildAskXLines()
	if m.chatMode {
		m.chatScroll = m.chatTotalLines()
	}
	m.focus = paneCommand
	m.cursorVisible = true
	m.input.SetValue("/AskX ")
	m.input.CursorEnd()
	m.cmdComplete = nil
	m.cmdCompleteIdx = -1
	m.paramItems = nil
	m.paramIdx = -1
}

// closeAskX tears down the askX pane state.
func (m *Model) closeAskX() {
	if m.askxCancelStream != nil {
		m.askxCancelStream()
		m.askxCancelStream = nil
	}
	m.askxOpen = false
	m.askxFocused = false
	m.askxGlobal = false
	m.askxScroll = 0
	m.askxMsgs = nil
	m.askxDisplayLines = nil
	m.askxStreaming = false
	m.askxStreamBuf = ""
}

// clearAskXInput clears the input if it has the /askX or /AskX prefix.
func (m *Model) clearAskXInput() {
	if strings.HasPrefix(m.input.Value(), "/askX") || strings.HasPrefix(m.input.Value(), "/askx") || strings.HasPrefix(m.input.Value(), "/AskX") {
		m.input.SetValue("")
		m.input.CursorEnd()
		m.syncInputHeight()
	}
}

// askxWorkspace returns the workspace name for askX file operations.
// Returns "" (global) when askxGlobal is set (opened via Ctrl+X).
func (m *Model) askxWorkspace() string {
	if m.askxGlobal {
		return ""
	}
	// Nav cursor workspace takes priority — it reflects what the user is looking at.
	if m.navSubTab == navSubTabWorkspaces {
		if ws := m.selectedWorkspace(); ws != nil {
			return ws.name
		}
	}
	// Fall back to chatWorkspace when not on workspaces tab.
	if m.chatMode && m.chatWorkspace != "" {
		return m.chatWorkspace
	}
	return ""
}

// askxAsText formats askX message history as plain text for the resource overlay.
func (m *Model) askxAsText() string {
	if len(m.askxMsgs) == 0 {
		return ""
	}
	var b strings.Builder
	for i, msg := range m.askxMsgs {
		if i > 0 {
			b.WriteString("\n---\n\n")
		}
		label := "User"
		if msg.Role == "assistant" {
			label = "Assistant"
		}
		b.WriteString("## " + label + "\n\n")
		b.WriteString(msg.Content)
		b.WriteByte('\n')
	}
	return b.String()
}

// askxFilePath returns the path to the askX file.
func (m *Model) askxFilePath() string {
	return storefs.AskXPath(m.cfg.DataRoot, m.askxWorkspace())
}

// ── Data loading ────────────────────────────────────────────────────────────

// loadAskXHistory reads the askX JSON history and converts to chat.Messages.
func (m *Model) loadAskXHistory() {
	ws := m.askxWorkspace()
	h, err := storefs.ReadAskXHistory(m.cfg.DataRoot, ws)
	if err != nil {
		m.setStatusError("askX: " + err.Error())
		m.askxMsgs = nil
		return
	}
	m.askxMsgs = askxHistoryToMsgs(h)
}

// askxHistoryToMsgs converts storage messages to chat.Messages.
func askxHistoryToMsgs(h *storefs.AskXHistory) []chat.Message {
	if h == nil || len(h.Messages) == 0 {
		return nil
	}
	msgs := make([]chat.Message, len(h.Messages))
	for i, am := range h.Messages {
		msgs[i] = chat.Message{
			Role:    am.Role,
			Content: am.Content,
			Time:    am.Time,
		}
	}
	return msgs
}

// msgsToAskXHistory converts chat.Messages back to storage format.
func msgsToAskXHistory(msgs []chat.Message) *storefs.AskXHistory {
	h := &storefs.AskXHistory{
		Messages: make([]storefs.AskXMessage, len(msgs)),
	}
	for i, m := range msgs {
		h.Messages[i] = storefs.AskXMessage{
			Role:    m.Role,
			Content: m.Content,
			Time:    m.Time,
		}
	}
	return h
}

// saveAskXHistory persists the current askxMsgs to JSON.
func (m *Model) saveAskXHistory() {
	ws := m.askxWorkspace()
	h := msgsToAskXHistory(m.askxMsgs)
	if err := storefs.SaveAskXHistory(m.cfg.DataRoot, ws, h); err != nil {
		m.setStatusError("askX save: " + err.Error())
	}
}

// ── @token parsing ──────────────────────────────────────────────────────────

// askxTokenResult holds the parsed result of @token extraction from an askX prompt.
type askxTokenResult struct {
	profileOverride string   // profile name if an @profile was found
	fileContents    []string // contents of resolved @file tokens
	fileNames       []string // basenames of resolved files (for display)
	cleanPrompt     string   // prompt with @tokens stripped
}

// parseAskXTokens scans leading @tokens from the prompt.
// For each token: if it matches a known profile key, it's a profile override.
// Otherwise it's resolved as a file path. At most one profile is allowed.
func parseAskXTokens(prompt string, profiles map[string]config.Profile) (askxTokenResult, error) {
	var res askxTokenResult

	// Strip leading --profile <name> flag before @token parsing.
	if strings.HasPrefix(prompt, "--profile ") {
		rest := strings.TrimPrefix(prompt, "--profile ")
		idx := strings.IndexByte(rest, ' ')
		if idx < 0 {
			return res, fmt.Errorf("--profile requires a profile name")
		}
		res.profileOverride = rest[:idx]
		prompt = strings.TrimSpace(rest[idx:])
	}

	words := strings.Fields(prompt)
	consumed := 0
	for _, w := range words {
		if !strings.HasPrefix(w, "@") {
			break
		}
		token := w[1:] // strip @
		if token == "" {
			break
		}

		// Try profile match first.
		if _, ok := profiles[token]; ok {
			if res.profileOverride != "" {
				return res, fmt.Errorf("multiple profiles specified")
			}
			res.profileOverride = token
			consumed++
			continue
		}

		// Resolve as file path.
		path := expandHome(token)
		if !filepath.IsAbs(path) {
			path, _ = filepath.Abs(path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return res, fmt.Errorf("@%s: %w", token, err)
		}
		res.fileContents = append(res.fileContents, string(data))
		res.fileNames = append(res.fileNames, filepath.Base(path))
		consumed++
	}

	// Rebuild clean prompt by stripping consumed @tokens.
	if consumed > 0 {
		// Find the position after the last consumed token.
		remaining := prompt
		for i := 0; i < consumed; i++ {
			remaining = strings.TrimLeft(remaining, " ")
			idx := strings.IndexByte(remaining, ' ')
			if idx < 0 {
				remaining = ""
				break
			}
			remaining = remaining[idx:]
		}
		res.cleanPrompt = strings.TrimLeft(remaining, " ")
	} else {
		res.cleanPrompt = prompt
	}

	return res, nil
}

// expandHome expands a leading ~ to the user's home directory.
func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") || path == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[1:])
	}
	return path
}

// ── Command ─────────────────────────────────────────────────────────────────

// cmdAskX handles /askX <prompt>. Empty prompt toggles pane; non-empty sends query.
// global=true targets the global askX context; global=false uses workspace-local.
func (m *Model) cmdAskX(prompt string, global bool) tea.Cmd {
	if prompt == "" {
		// Toggle pane visibility.
		if m.askxOpen {
			m.closeAskX()
		} else {
			// Mutual exclusion: close scratch and preview.
			if m.scratchOpen {
				m.closeScratch()
			}
			if m.previewOpen {
				m.closePreview()
			}
			m.askxGlobal = global
			m.askxOpen = true
			m.loadAskXHistory()
			m.rebuildAskXLines()
		}
		return nil
	}

	if m.askxStreaming {
		m.setStatusError("askX: already streaming")
		return nil
	}

	// Set global flag early so askxWorkspace() resolves correctly.
	if !m.askxOpen {
		m.askxGlobal = global
	}

	// Resolve @<numID> article references before parsing other @tokens.
	// Keep a display version (truncated) separate from the full LLM version.
	displayBase := prompt
	if atRefPattern.MatchString(prompt) {
		resolved, err := m.resolveAtRefs(prompt)
		if err != nil {
			m.setStatusError("askX: " + err.Error())
			return nil
		}
		displayResolved, _ := m.resolveAtRefsDisplay(prompt, 128)
		prompt = resolved
		displayBase = displayResolved
	}

	// Parse @tokens (profile override, file inclusions).
	parsed, err := parseAskXTokens(prompt, m.cfg.Profiles)
	if err != nil {
		m.setStatusError("askX: " + err.Error())
		return nil
	}
	parsedDisplay, _ := parseAskXTokens(displayBase, m.cfg.Profiles)

	// Store the clean prompt (without @tokens) as the user message.
	displayPrompt := parsedDisplay.cleanPrompt
	if displayPrompt == "" {
		m.setStatusError("askX: empty prompt after @token parsing")
		return nil
	}

	// Build the LLM prompt: prepend file contents as context blocks.
	llmPrompt := parsed.cleanPrompt
	if len(parsed.fileContents) > 0 {
		var sb strings.Builder
		for i, content := range parsed.fileContents {
			sb.WriteString(fmt.Sprintf("--- file: %s ---\n%s\n---\n\n", parsed.fileNames[i], content))
		}
		sb.WriteString(parsed.cleanPrompt)
		llmPrompt = sb.String()
	}

	// Append user message (display version, with truncated @ref content).
	m.askxMsgs = append(m.askxMsgs, chat.Message{
		Role:    chat.RoleUser,
		Content: displayPrompt,
		Time:    time.Now(),
	})
	m.saveAskXHistory()

	// Mutual exclusion: close scratch and preview.
	if m.scratchOpen {
		m.closeScratch()
	}
	if m.previewOpen {
		m.closePreview()
	}
	m.askxOpen = true
	m.rebuildAskXLines()
	m.askxScrollToBottom()

	// Fire the streaming LLM call.
	return m.sendAskXQuery(llmPrompt, parsed.profileOverride)
}

// ── Streaming ───────────────────────────────────────────────────────────────

// sendAskXQuery sends a single-shot query to the LLM with streaming.
// profileOverride, if non-empty, takes precedence over config.
func (m *Model) sendAskXQuery(prompt string, profileOverride string) tea.Cmd {
	cfg := m.cfg

	// Resolve profile.
	profileName := profileOverride
	if profileName == "" {
		profileName = cfg.AskX.Profile
	}
	if profileName == "" {
		profileName = "haiku"
	}
	prof, ok := cfg.Profiles[profileName]
	if !ok {
		m.setStatusError(fmt.Sprintf("askX: profile %q not found", profileName))
		return nil
	}
	m.askxResolvedProfile = profileName

	systemPrompt := cfg.AskX.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = "You are a concise assistant."
	}

	apiKey := correctionResolveAPIKey(prof.Provider)

	ctx, cancel := context.WithCancel(context.Background())
	m.askxCancelStream = cancel
	m.askxStreaming = true
	m.askxStreamBuf = ""
	shared := &streamBuf{}
	m.askxSharedBuf = shared

	maxTokens := cfg.AskX.MaxOutputTokens

	return func() tea.Msg {
		prov, err := llm.New(llm.ProviderConfig{
			Provider:        prof.Provider,
			Model:           prof.Model,
			Host:            prof.Host,
			APIKey:          apiKey,
			MaxOutputTokens: maxTokens,
			Think:           prof.Think,
		})
		if err != nil {
			return askxStreamDoneMsg{err: fmt.Sprintf("askX: %v", err)}
		}

		msgs := []llm.Message{
			{Role: llm.RoleUser, Content: prompt},
		}

		start := time.Now()
		fullText, usage, err := prov.ChatStream(ctx, systemPrompt, msgs, func(delta string) error {
			shared.Append(delta)
			return nil
		})
		elapsed := time.Since(start)
		if err != nil {
			return askxStreamDoneMsg{err: fmt.Sprintf("askX: %v", err), fullText: fullText, elapsed: elapsed}
		}
		costUSD := cfg.CalcCost(prof.Model, usage.InputTokens, usage.OutputTokens)
		appendAskXEvent(cfg.EventsPath, prof.Model, usage.InputTokens, usage.OutputTokens, costUSD)
		return askxStreamDoneMsg{fullText: fullText, costUSD: costUSD, elapsed: elapsed, inputTokens: usage.InputTokens, outputTokens: usage.OutputTokens}
	}
}

// appendAskXEvent writes a cost event for an AskX call to events.jsonl.
func appendAskXEvent(eventsPath, model string, inputTokens, outputTokens int, costUSD float64) {
	type askxCost struct {
		CostUSD      float64 `json:"cost_usd,omitempty"`
		Model        string  `json:"model"`
		InputTokens  int     `json:"input_tokens,omitempty"`
		OutputTokens int     `json:"output_tokens,omitempty"`
	}
	ev := struct {
		TS    time.Time `json:"ts"`
		Type  string    `json:"type"`
		Model string    `json:"model"`
		Cost  askxCost  `json:"cost"`
	}{
		TS:    time.Now().UTC(),
		Type:  "askx_call",
		Model: model,
		Cost: askxCost{
			CostUSD:      costUSD,
			Model:        model,
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
		},
	}
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	f, err := os.OpenFile(eventsPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(data, '\n'))
}

// handleAskXStreamDone processes the completion of an askX streaming response.
func (m *Model) handleAskXStreamDone(msg askxStreamDoneMsg) {
	m.askxStreaming = false
	m.askxSharedBuf = nil
	m.askxCancelStream = nil

	if msg.err != "" {
		m.statusMsg = "✗ " + msg.err
		if msg.fullText != "" {
			m.askxMsgs = append(m.askxMsgs, chat.Message{
				Role:    chat.RoleAssistant,
				Content: msg.fullText,
				Profile: m.askxResolvedProfile,
				Time:    time.Now(),
			})
			m.saveAskXHistory()
		}
	} else {
		m.askxMsgs = append(m.askxMsgs, chat.Message{
			Role:    chat.RoleAssistant,
			Content: msg.fullText,
			Profile: m.askxResolvedProfile,
			Time:    time.Now(),
		})
		m.saveAskXHistory()
		// per-call stats
		m.askxLastInputTokens = msg.inputTokens
		m.askxLastOutputTokens = msg.outputTokens
		m.askxLastElapsed = msg.elapsed
		// session totals
		m.askxSessionQueries++
		m.askxSessionInputTokens += msg.inputTokens
		m.askxSessionOutputTokens += msg.outputTokens
		m.askxSessionCostUSD += msg.costUSD
		cost := formatUSD(msg.costUSD)
		if msg.costUSD == 0 {
			cost = "free"
		}
		m.statusMsg = fmt.Sprintf("✓ askX · %s · %s  %.1fs", m.askxResolvedProfile, cost, msg.elapsed.Seconds())
	}

	m.askxStreamBuf = ""
	m.rebuildAskXLines()
	m.askxScrollToBottom()
}

// ── Line rebuilding ─────────────────────────────────────────────────────────

// rebuildAskXLines builds displayable lines from askxMsgs + streaming buffer.
// Mirrors rebuildChatLines: ● user prompt, assistant markdown beneath, blank between exchanges.
func (m *Model) rebuildAskXLines() {
	w := m.width - m.navWidth() - 1
	if w < 10 {
		w = 10
	}

	msgs := m.askxMsgs
	if len(msgs) == 0 && !m.askxStreaming {
		m.askxDisplayLines = []chatLine{{chatLineAssistant, "(no queries yet — use /askX <prompt>)"}}
		return
	}

	const userPrefix = "● "
	const contPrefix = "  "
	userW := w - len([]rune(userPrefix))
	if userW < 10 {
		userW = 10
	}

	var lines []chatLine
	prevHadContent := false

	for _, msg := range msgs {
		switch msg.Role {
		case chat.RoleUser:
			if prevHadContent {
				lines = append(lines, chatLine{chatLineBlank, ""})
			}
			raw := strings.Split(msg.Content, "\n")
			first := true
			for _, rl := range raw {
				rl = strings.TrimRight(rl, " \t")
				if rl == "" {
					continue
				}
				prefix := contPrefix
				if first {
					prefix = userPrefix
				}
				for j, wl := range wordWrap(rl, userW) {
					p := contPrefix
					if first && j == 0 {
						p = prefix
					}
					lines = append(lines, chatLine{chatLineUser, p + wl})
				}
				first = false
			}
			lines = append(lines, chatLine{chatLineBlank, ""})
			prevHadContent = true

		case chat.RoleAssistant:
			mdLines := m.appendMarkdown(msg.Content, w)
			for len(mdLines) > 0 && mdLines[0].role == chatLineBlank {
				mdLines = mdLines[1:]
			}
			for len(mdLines) > 0 && mdLines[len(mdLines)-1].role == chatLineBlank {
				mdLines = mdLines[:len(mdLines)-1]
			}
			lines = append(lines, mdLines...)
			prevHadContent = len(mdLines) > 0
		}
	}

	// Streaming buffer: render in-progress markdown or waiting indicator.
	if m.askxStreaming {
		if m.askxStreamBuf != "" {
			streamLines := m.appendMarkdown(m.askxStreamBuf, w)
			lines = append(lines, streamLines...)
		} else {
			lines = append(lines, chatLine{chatLineAssistant, "…"})
		}
	}

	// Collapse consecutive blank lines.
	compacted := lines[:0]
	lastBlank := false
	for _, cl := range lines {
		isBlank := cl.role == chatLineBlank
		if isBlank && lastBlank {
			continue
		}
		compacted = append(compacted, cl)
		lastBlank = isBlank
	}
	m.askxDisplayLines = compacted
}

// ── Box info (mirrors chatBoxInfos) ─────────────────────────────────────────

// askxBoxInfo holds per-box metadata derived from askX message history.
type askxBoxInfo struct {
	ts       string
	profile  string
	msgStart int // inclusive index into askxMsgs
	msgEnd   int // exclusive index into askxMsgs
}

// askxBoxInfos walks askxMsgs and returns one entry per logical box.
// A box is a user→assistant exchange.
func (m *Model) askxBoxInfos() []askxBoxInfo {
	var infos []askxBoxInfo
	for i, msg := range m.askxMsgs {
		switch msg.Role {
		case chat.RoleUser:
			ts := ""
			if !msg.Time.IsZero() {
				ts = msg.Time.Format("Jan 2, 2006 · 15:04")
			}
			infos = append(infos, askxBoxInfo{ts: ts, msgStart: i, msgEnd: i + 1})
		case chat.RoleAssistant:
			if len(infos) > 0 {
				infos[len(infos)-1].msgEnd = i + 1
				if msg.Profile != "" && infos[len(infos)-1].profile == "" {
					infos[len(infos)-1].profile = msg.Profile
				}
			}
		}
	}
	return infos
}

// askxBoxCount returns the number of logical boxes in the current display.
func (m *Model) askxBoxCount() int {
	dl := m.askxDisplayLines
	count := 0
	for i, cl := range dl {
		if cl.role == chatLineUser && (i == 0 || dl[i-1].role != chatLineUser) {
			count++
		}
	}
	return count
}

// ── Navigation helpers ──────────────────────────────────────────────────────

func (m *Model) askxBoxPrev() {
	if m.askxBoxCursor > 0 {
		m.askxBoxCursor--
	}
}

func (m *Model) askxBoxNext() {
	max := m.askxBoxCount() - 1
	if max < 0 {
		max = 0
	}
	if m.askxBoxCursor < max {
		m.askxBoxCursor++
	}
}

func (m *Model) askxViewH() int {
	mainH := m.height - 6 - m.completionCount()
	h := mainH / 3
	if h < 3 {
		h = 3
	}
	return h - 1 // minus header
}

func (m *Model) askxScrollToBottom() {
	viewH := m.askxViewH()
	total := m.askxTotalVLines()
	maxScroll := total - viewH
	if maxScroll < 0 {
		maxScroll = 0
	}
	m.askxScroll = maxScroll
}

func (m *Model) askxTotalVLines() int {
	if vlines := m.buildAskXVLines(); vlines != nil {
		return len(vlines)
	}
	return len(m.askxDisplayLines)
}

func (m *Model) scrollToAskXBox(viewH int) {
	vlines := m.buildAskXVLines()
	if len(vlines) == 0 {
		return
	}
	first, last := -1, -1
	for i, vl := range vlines {
		if vl.boxIdx == m.askxBoxCursor {
			if first == -1 {
				first = i
			}
			last = i
		}
	}
	if first == -1 {
		return
	}
	if first >= m.askxScroll && last < m.askxScroll+viewH {
		return
	}
	m.askxScroll = first
	maxScroll := len(vlines) - viewH
	if maxScroll < 0 {
		maxScroll = 0
	}
	if m.askxScroll > maxScroll {
		m.askxScroll = maxScroll
	}
}

// ── Box operations ──────────────────────────────────────────────────────────

func (m *Model) cmdAskXCollapseBox(boxIdx int) {
	if m.askxCollapsed == nil {
		m.askxCollapsed = make(map[int]bool)
	}
	m.askxCollapsed[boxIdx] = !m.askxCollapsed[boxIdx]
}

func (m *Model) cmdAskXDeleteBox() tea.Cmd {
	infos := m.askxBoxInfos()
	if m.askxBoxCursor < 0 || m.askxBoxCursor >= len(infos) {
		m.setStatusError("nothing to delete")
		return nil
	}
	info := infos[m.askxBoxCursor]

	// Remove messages for this box.
	start, end := info.msgStart, info.msgEnd
	if start < 0 || end > len(m.askxMsgs) || start >= end {
		return nil
	}
	out := make([]chat.Message, 0, len(m.askxMsgs)-(end-start))
	out = append(out, m.askxMsgs[:start]...)
	out = append(out, m.askxMsgs[end:]...)
	m.askxMsgs = out

	m.saveAskXHistory()

	// Shift collapsed keys.
	if m.askxCollapsed != nil {
		newCollapsed := make(map[int]bool)
		for k, v := range m.askxCollapsed {
			if k < m.askxBoxCursor {
				newCollapsed[k] = v
			} else if k > m.askxBoxCursor {
				newCollapsed[k-1] = v
			}
		}
		m.askxCollapsed = newCollapsed
	}

	m.rebuildAskXLines()

	numBoxes := m.askxBoxCount()
	if numBoxes == 0 {
		m.askxBoxCursor = 0
	} else if m.askxBoxCursor >= numBoxes {
		m.askxBoxCursor = numBoxes - 1
	}

	m.statusMsg = "✓ exchange deleted"
	return nil
}

// ── TTS ─────────────────────────────────────────────────────────────────────

func (m *Model) cmdAskXTTS() tea.Cmd {
	if m.ttsPlayer.Playing() {
		m.stopTTS()
		m.statusMsg = ""
		return nil
	}
	// Find the assistant content for the selected box.
	infos := m.askxBoxInfos()
	if m.askxBoxCursor < 0 || m.askxBoxCursor >= len(infos) {
		m.statusMsg = "nothing to speak"
		return nil
	}
	info := infos[m.askxBoxCursor]
	// Gather assistant text from the box's messages.
	var parts []string
	for i := info.msgStart; i < info.msgEnd; i++ {
		if m.askxMsgs[i].Role == chat.RoleAssistant {
			parts = append(parts, m.askxMsgs[i].Content)
		}
	}
	if len(parts) == 0 {
		m.statusMsg = "no assistant response to speak"
		return nil
	}

	text := strings.Join(parts, "\n")
	stripped := tts.Strip(text)
	m.contentTTSText = text
	playFn := m.ttsPlayer.Play(stripped)
	m.ttsGen = m.ttsPlayer.Gen()
	m.ttsCurrentText = stripped

	return func() tea.Msg {
		done := playFn()
		return ttsDoneMsg{err: done.Err, gen: done.Gen}
	}
}

func (m *Model) cmdAskXTTSAdjustRate(delta int) tea.Cmd {
	if !m.ttsPlayer.Playing() || m.contentTTSText == "" {
		return nil
	}
	newRate := m.cfg.TTSRate + delta
	if m.cfg.TTSRate == 0 {
		newRate = 200 + delta
	}
	if newRate < 80 {
		newRate = 80
	}
	if newRate > 500 {
		newRate = 500
	}
	m.cfg.TTSRate = newRate
	m.ttsPlayer.SetRate(newRate)

	// Restart with new rate.
	text := m.contentTTSText
	m.ttsPlayer.Stop()
	stripped := tts.Strip(text)
	m.ttsCurrentText = stripped
	playFn := m.ttsPlayer.Play(stripped)
	m.ttsGen = m.ttsPlayer.Gen()
	return func() tea.Msg {
		done := playFn()
		return ttsDoneMsg{err: done.Err, gen: done.Gen}
	}
}

// ── Key handling ────────────────────────────────────────────────────────────

// handleAskXKey handles keys when the askX pane is focused.
func (m *Model) handleAskXKey(msg tea.KeyMsg) tea.Cmd {
	viewH := m.askxViewH()

	switch {
	case msg.Type == tea.KeyRunes:
		switch msg.String() {
		case "s":
			return m.cmdAskXTTS()
		case "v":
			if m.askxBoxCount() > 0 {
				m.cmdAskXCollapseBox(m.askxBoxCursor)
			}
		case "x":
			return m.cmdAskXDeleteBox()
		case "e":
			editor := os.Getenv("EDITOR")
			if editor == "" {
				m.setStatusError("$EDITOR is not set")
				return nil
			}
			path := m.askxFilePath()
			ws := m.askxWorkspace()
			label := "askX"
			if ws != "" {
				label = ws + "/askX"
			}
			m.openEditorInTerminal(editor, path, label)
		case "[":
			return m.cmdAskXTTSAdjustRate(-20)
		case "]":
			return m.cmdAskXTTSAdjustRate(+20)
		}
		return nil
	case key.Matches(msg, keys.NavUp):
		m.askxBoxPrev()
		m.scrollToAskXBox(viewH)
		return nil
	case key.Matches(msg, keys.NavDown):
		m.askxBoxNext()
		m.scrollToAskXBox(viewH)
		return nil
	case key.Matches(msg, keys.PageUp):
		for i := 0; i < viewH && m.askxBoxCursor > 0; i++ {
			m.askxBoxPrev()
		}
		m.scrollToAskXBox(viewH)
	case key.Matches(msg, keys.PageDown):
		max := m.askxBoxCount() - 1
		for i := 0; i < viewH && m.askxBoxCursor < max; i++ {
			m.askxBoxNext()
		}
		m.scrollToAskXBox(viewH)
	case key.Matches(msg, keys.Home):
		m.askxBoxCursor = 0
		m.scrollToAskXBox(viewH)
	case key.Matches(msg, keys.End):
		max := m.askxBoxCount() - 1
		if max >= 0 {
			m.askxBoxCursor = max
		}
		m.scrollToAskXBox(viewH)
	case key.Matches(msg, keys.Back):
		m.askxFocused = false
	case key.Matches(msg, keys.Command):
		m.focus = paneCommand
		m.cursorVisible = true
		if m.askxGlobal {
			m.input.SetValue("/AskX ")
		} else {
			m.input.SetValue("/askX ")
		}
		m.input.CursorEnd()
	}
	return nil
}

// ── Virtual lines (boxed view) ──────────────────────────────────────────────

// buildAskXVLines builds the virtual display list for boxed mode.
// Returns nil when askX is not focused. Uses chatVLine (same as chat window).
func (m Model) buildAskXVLines() []chatVLine {
	if !m.askxFocused || m.focus != paneContent {
		return nil
	}
	dl := m.askxDisplayLines
	n := len(dl)
	if n == 0 {
		return nil
	}

	// Detect box boundaries from display lines (same logic as buildChatVLines).
	type boxStart struct {
		idx int
	}
	var boxes []boxStart
	for i, cl := range dl {
		if cl.role == chatLineUser && (i == 0 || dl[i-1].role != chatLineUser) {
			boxes = append(boxes, boxStart{i})
		}
	}
	if len(boxes) == 0 {
		return nil
	}

	infos := m.askxBoxInfos()

	var vlines []chatVLine
	for e, box := range boxes {
		selected := e == m.askxBoxCursor
		collapsed := m.askxCollapsed != nil && m.askxCollapsed[e]

		var end int
		if e+1 < len(boxes) {
			end = boxes[e+1].idx
		} else {
			end = n
		}
		// Trim trailing blanks.
		trimEnd := end
		for trimEnd > box.idx && dl[trimEnd-1].role == chatLineBlank {
			trimEnd--
		}
		totalContent := trimEnd - box.idx

		if selected {
			vlines = append(vlines, chatVLine{isBoxTop: true, contentIdx: -1, boxIdx: e, isSelected: true})

			// Header: timestamp + profile + hints.
			var leftParts []string
			if e < len(infos) {
				if infos[e].ts != "" {
					leftParts = append(leftParts, infos[e].ts)
				}
				if infos[e].profile != "" {
					leftParts = append(leftParts, infos[e].profile)
				}
			}
			metaLeft := strings.Join(leftParts, "  ")
			vlines = append(vlines, chatVLine{isHeader: true, metaText: metaLeft, contentIdx: -1, boxIdx: e, isSelected: true})

			if collapsed {
				limit := box.idx + 3
				if limit > trimEnd {
					limit = trimEnd
				}
				for i := box.idx; i < limit; i++ {
					vlines = append(vlines, chatVLine{contentIdx: i, boxIdx: e, isSelected: true})
				}
				if limit < trimEnd {
					remaining := totalContent - (limit - box.idx)
					vlines = append(vlines, chatVLine{
						isEllipsis: true,
						metaText:   fmt.Sprintf("... (%d more lines)", remaining),
						contentIdx: -1, boxIdx: e, isSelected: true,
					})
				}
			} else {
				for i := box.idx; i < trimEnd; i++ {
					vlines = append(vlines, chatVLine{contentIdx: i, boxIdx: e, isSelected: true})
				}
			}

			vlines = append(vlines, chatVLine{isBoxBottom: true, contentIdx: -1, boxIdx: e, isSelected: true})
		} else {
			if collapsed {
				limit := box.idx + 3
				if limit > trimEnd {
					limit = trimEnd
				}
				for i := box.idx; i < limit; i++ {
					vlines = append(vlines, chatVLine{contentIdx: i, boxIdx: e})
				}
				if limit < trimEnd {
					remaining := totalContent - (limit - box.idx)
					vlines = append(vlines, chatVLine{
						isEllipsis: true,
						metaText:   fmt.Sprintf("... (%d more lines)", remaining),
						contentIdx: -1, boxIdx: e,
					})
				}
			} else {
				for i := box.idx; i < trimEnd; i++ {
					vlines = append(vlines, chatVLine{contentIdx: i, boxIdx: e})
				}
			}
		}

		if e < len(boxes)-1 {
			vlines = append(vlines, chatVLine{isSep: true, contentIdx: -1, boxIdx: e})
		}
	}
	return vlines
}

// ── Rendering ───────────────────────────────────────────────────────────────

// renderAskXStatusLine renders the three-section status bar for askX mode,
// mirroring renderChatStatusLine: left=streaming/status, center=per-call stats, right=session totals.
func (m Model) renderAskXStatusLine() string {
	t := ActiveTheme
	w := m.width

	// Left: streaming indicator > status message.
	var left string
	if m.askxStreaming {
		label := "askX streaming · " + m.askxResolvedProfile
		left = renderWaveIndicatorLeading(m.spinnerFrame, label, t.StreamingText, t.Dimmed)
	} else if m.statusMsg != "" {
		if m.statusErr || strings.HasPrefix(m.statusMsg, "✗") {
			left = fgBold(t.StatusError, " "+m.statusMsg)
		} else {
			left = fg(t.StatusText, " "+m.statusMsg)
		}
	}

	// Center: per-call token stats (available after first response).
	var center string
	if m.askxLastInputTokens > 0 || m.askxLastOutputTokens > 0 {
		center = fg(t.ContentDimmed, fmt.Sprintf("in:%d out:%d  %.1fs",
			m.askxLastInputTokens, m.askxLastOutputTokens, m.askxLastElapsed.Seconds()))
	}

	// Right: session totals.
	var right string
	if m.askxSessionQueries > 0 {
		right = fg(t.ContentDimmed, fmt.Sprintf("%d queries · %dk in · %dk out · %s",
			m.askxSessionQueries,
			(m.askxSessionInputTokens+500)/1000,
			(m.askxSessionOutputTokens+500)/1000,
			formatUSD(m.askxSessionCostUSD)))
	}

	// Compose: left | center (padded) | right
	leftW := lipgloss.Width(left)
	rightW := lipgloss.Width(right)
	centerW := lipgloss.Width(center)
	gap := w - leftW - rightW - centerW
	if gap < 2 {
		gap = 2
	}
	leftGap := gap / 2
	rightGap := gap - leftGap

	return left + strings.Repeat(" ", leftGap) + center + strings.Repeat(" ", rightGap) + right
}

// renderAskXPane renders the askX split pane content.
func (m Model) renderAskXPane(height, width int) []string {
	t := ActiveTheme
	var lines []string

	// Header separator with label.
	name := "askX"
	if m.askxGlobal {
		name = "AskX"
	}
	label := " " + name + " "
	ws := m.askxWorkspace()
	if ws != "" {
		label = " " + name + " [" + ws + "] "
	}
	if m.askxResolvedProfile != "" {
		label += "· " + m.askxResolvedProfile + " "
	}
	hint := " V view "
	labelLen := len([]rune(label))
	hintLen := len([]rune(hint))
	remaining := width - labelLen
	if remaining < 0 {
		remaining = 0
	}
	leftSep := remaining / 2
	rightFull := remaining - leftSep
	rightSep := rightFull - hintLen
	if rightSep < 1 {
		rightSep = 0
		hint = ""
	}
	headerColor := t.Dimmed
	if m.askxFocused && m.focus == paneContent {
		headerColor = t.Accent
	}
	header := fg(headerColor, strings.Repeat("─", leftSep)+label+strings.Repeat("─", rightSep)+hint)
	lines = append(lines, header)

	viewH := height - 1
	if viewH < 1 {
		viewH = 1
	}

	dl := m.askxDisplayLines

	// Boxed mode: when askX is focused, render with box borders around selected box.
	if vlines := m.buildAskXVLines(); vlines != nil {
		innerW := width - 4
		if innerW < 4 {
			innerW = 4
		}
		topRule := fg(t.BoxBorder, "╭"+strings.Repeat("─", width-2)+"╮")
		botRule := fg(t.BoxBorder, "╰"+strings.Repeat("─", width-2)+"╯")
		bL := fg(t.BoxBorder, "│ ")
		bR := fg(t.BoxBorder, " │")

		total := len(vlines)
		start := m.askxScroll
		if start > total-viewH {
			start = total - viewH
		}
		if start < 0 {
			start = 0
		}
		end := start + viewH
		if end > total {
			end = total
		}

		for _, vl := range vlines[start:end] {
			switch {
			case vl.isBoxTop:
				lines = append(lines, topRule)
			case vl.isBoxBottom:
				lines = append(lines, botRule)
			case vl.isSep:
				lines = append(lines, "")
			case vl.isHeader:
				// Left: timestamp, Right: hints.
				hints := "v expand · s speak · x delete"
				if m.askxCollapsed != nil && m.askxCollapsed[vl.boxIdx] {
					hints = "v collapse · s speak · x delete"
				}
				leftText := fg(t.ContentDimmed, vl.metaText)
				rightText := fg(t.ContentDimmed, hints)
				leftW := lipgloss.Width(vl.metaText)
				rightW := lipgloss.Width(hints)
				pad := innerW - leftW - rightW
				if pad < 1 {
					pad = 1
				}
				headerContent := leftText + strings.Repeat(" ", pad) + rightText
				lines = append(lines, bL+headerContent+bR)
			case vl.isEllipsis:
				if vl.isSelected {
					text := fg(t.ContentDimmed, vl.metaText)
					visW := lipgloss.Width(vl.metaText)
					if visW < innerW {
						text += strings.Repeat(" ", innerW-visW)
					}
					lines = append(lines, bL+text+bR)
				} else {
					lines = append(lines, fg(t.ContentDimmed, vl.metaText))
				}
			default:
				if vl.contentIdx < 0 || vl.contentIdx >= len(dl) {
					lines = append(lines, "")
					continue
				}
				cl := dl[vl.contentIdx]
				if vl.isSelected {
					text := cl.text
					visW := lipgloss.Width(text)
					if visW < innerW {
						text = text + strings.Repeat(" ", innerW-visW)
					} else if visW > innerW {
						text = truncate(text, innerW)
					}
					// Color inside the box.
					lines = append(lines, bL+colorChatLine(chatLine{cl.role, text}, t)+bR)
				} else {
					lines = append(lines, colorChatLine(cl, t))
				}
			}
		}

		for len(lines) < height {
			lines = append(lines, "")
		}
		return lines[:height]
	}

	// Flat mode: plain scroll (askX not focused).
	total := len(dl)
	start := m.askxScroll
	if start > total-viewH {
		start = total - viewH
	}
	if start < 0 {
		start = 0
	}
	end := start + viewH
	if end > total {
		end = total
	}
	for i := start; i < end; i++ {
		lines = append(lines, colorChatLine(dl[i], t))
	}

	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines[:height]
}

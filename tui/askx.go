package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/jrniemiec/arc/chat"
	storefs "github.com/jrniemiec/arc/store/fs"
	"github.com/jrniemiec/arc/tts"
	"github.com/jrniemiec/llm"
)

// ── Toggle & lifecycle ──────────────────────────────────────────────────────

// toggleAskX toggles the askX pane. When opening, pre-fills input with "/askX ".
func (m *Model) toggleAskX() {
	if m.askxOpen {
		m.askxOpen = false
		m.askxFocused = false
		m.clearAskXInput()
		return
	}
	// Mutual exclusion: close scratch if open.
	if m.scratchOpen {
		m.closeScratch()
	}
	m.askxOpen = true
	m.loadAskXHistory()
	m.rebuildAskXLines()
	m.focus = paneCommand
	m.cursorVisible = true
	m.inputValue = "/askX "
	m.inputCursor = len([]rune(m.inputValue))
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
	m.askxScroll = 0
	m.askxMsgs = nil
	m.askxDisplayLines = nil
	m.askxStreaming = false
	m.askxStreamBuf = ""
}

// clearAskXInput clears the input if it has the /askX prefix.
func (m *Model) clearAskXInput() {
	if strings.HasPrefix(m.inputValue, "/askX") || strings.HasPrefix(m.inputValue, "/askx") {
		m.inputValue = ""
		m.inputCursor = 0
	}
}

// askxWorkspace returns the workspace name for askX file operations.
func (m *Model) askxWorkspace() string {
	return m.scratchWorkspace() // same logic as scratch
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

// ── Command ─────────────────────────────────────────────────────────────────

// cmdAskX handles /askX <prompt>. Empty prompt toggles pane; non-empty sends query.
func (m *Model) cmdAskX(prompt string) tea.Cmd {
	if prompt == "" {
		// Toggle pane visibility.
		if m.askxOpen {
			m.askxOpen = false
		} else {
			// Mutual exclusion: close scratch.
			if m.scratchOpen {
				m.closeScratch()
			}
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

	// Append user message.
	m.askxMsgs = append(m.askxMsgs, chat.Message{
		Role:    chat.RoleUser,
		Content: prompt,
		Time:    time.Now(),
	})
	m.saveAskXHistory()

	// Mutual exclusion: close scratch.
	if m.scratchOpen {
		m.closeScratch()
	}
	m.askxOpen = true
	m.rebuildAskXLines()
	m.askxScrollToBottom()

	// Fire the streaming LLM call.
	return m.sendAskXQuery(prompt)
}

// ── Streaming ───────────────────────────────────────────────────────────────

// sendAskXQuery sends a single-shot query to the LLM with streaming.
func (m *Model) sendAskXQuery(prompt string) tea.Cmd {
	cfg := m.cfg
	send := m.programSend

	// Resolve profile.
	profileName := cfg.AskX.Profile
	if profileName == "" {
		profileName = "haiku"
	}
	prof, ok := cfg.Profiles[profileName]
	if !ok {
		m.setStatusError(fmt.Sprintf("askX: profile %q not found", profileName))
		return nil
	}

	systemPrompt := cfg.AskX.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = "You are a concise assistant."
	}

	apiKey := correctionResolveAPIKey(prof.Provider)

	ctx, cancel := context.WithCancel(context.Background())
	m.askxCancelStream = cancel
	m.askxStreaming = true
	m.askxStreamBuf = ""

	maxTokens := cfg.AskX.MaxOutputTokens

	return func() tea.Msg {
		prov, err := llm.New(llm.ProviderConfig{
			Provider:        prof.Provider,
			Model:           prof.Model,
			Host:            prof.Host,
			APIKey:          apiKey,
			MaxOutputTokens: maxTokens,
		})
		if err != nil {
			return askxStreamDoneMsg{err: fmt.Sprintf("askX: %v", err)}
		}

		msgs := []llm.Message{
			{Role: llm.RoleUser, Content: prompt},
		}

		fullText, _, err := prov.ChatStream(ctx, systemPrompt, msgs, func(delta string) error {
			(*send)(askxStreamDeltaMsg(delta))
			return nil
		})
		if err != nil {
			return askxStreamDoneMsg{err: fmt.Sprintf("askX: %v", err), fullText: fullText}
		}
		return askxStreamDoneMsg{fullText: fullText}
	}
}

// handleAskXStreamDelta processes a streaming token fragment.
func (m *Model) handleAskXStreamDelta(delta string) {
	m.askxStreamBuf += delta
	m.rebuildAskXLines()
	m.askxScrollToBottom()
}

// handleAskXStreamDone processes the completion of an askX streaming response.
func (m *Model) handleAskXStreamDone(msg askxStreamDoneMsg) {
	m.askxStreaming = false
	m.askxCancelStream = nil

	if msg.err != "" {
		m.statusMsg = "✗ " + msg.err
		if msg.fullText != "" {
			m.askxMsgs = append(m.askxMsgs, chat.Message{
				Role:    chat.RoleAssistant,
				Content: msg.fullText,
				Time:    time.Now(),
			})
			m.saveAskXHistory()
		}
	} else {
		m.askxMsgs = append(m.askxMsgs, chat.Message{
			Role:    chat.RoleAssistant,
			Content: msg.fullText,
			Time:    time.Now(),
		})
		m.saveAskXHistory()
		m.statusMsg = "✓ askX done"
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

	// Streaming buffer: render in-progress markdown.
	if m.askxStreaming && m.askxStreamBuf != "" {
		streamLines := m.appendMarkdown(m.askxStreamBuf, w)
		lines = append(lines, streamLines...)
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
		m.inputValue = "/askX "
		m.inputCursor = len([]rune(m.inputValue))
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

			// Header: timestamp + hints.
			var leftParts []string
			if e < len(infos) {
				if infos[e].ts != "" {
					leftParts = append(leftParts, infos[e].ts)
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

// renderAskXPane renders the askX split pane content.
func (m Model) renderAskXPane(height, width int) []string {
	t := ActiveTheme
	var lines []string

	// Header separator with label.
	label := " AskX "
	ws := m.askxWorkspace()
	if ws != "" {
		label = " AskX [" + ws + "] "
	}
	if m.askxStreaming {
		label += "● "
	}
	sepLen := width - len([]rune(label))
	if sepLen < 0 {
		sepLen = 0
	}
	leftSep := sepLen / 2
	rightSep := sepLen - leftSep
	headerColor := t.Dimmed
	if m.askxFocused && m.focus == paneContent {
		headerColor = t.Accent
	}
	header := fg(headerColor, strings.Repeat("─", leftSep)+label+strings.Repeat("─", rightSep))
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

package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/jrniemiec/arc/chat"
	chatengine "github.com/jrniemiec/arc/chat/engine"
	storefs "github.com/jrniemiec/arc/store/fs"
	"github.com/jrniemiec/arc/tts"
)

// ── Scan for chat indicators ──────────────────────────────────────────────

// scanArticleChatsCmd scans all articles to find which have chat history.
func scanArticleChatsCmd(articlesRoot string, slugs []string) tea.Cmd {
	return func() tea.Msg {
		result := make(map[string]bool, len(slugs))
		for _, slug := range slugs {
			if storefs.HasArticleChat(articlesRoot, slug) {
				result[slug] = true
			}
		}
		return achatScanDoneMsg{hasChat: result}
	}
}

// ── Message types ──────────────────────────────────────────────────────────

type achatHistoryLoadedMsg struct {
	slug           string
	msgs           []chat.Message
	profile        string
	err            string
	workspaceStats chatengine.WorkspaceStats // lifetime stats from events.jsonl
}

// achatWorkspaceStatsMsg carries refreshed lifetime stats for the current article.
type achatWorkspaceStatsMsg struct {
	stats chatengine.WorkspaceStats
}

type achatReadyMsg struct {
	engine *chatengine.Engine
	slug   string
	err    string
}

type achatStreamDoneMsg struct {
	usage chat.Usage
	elapsed time.Duration
	err     string
}

// ── Async commands ─────────────────────────────────────────────────────────

// loadArticleChatHistoryCmd loads chat history and lifetime stats for an article from disk.
func (m *Model) loadArticleChatHistoryCmd(slug string) tea.Cmd {
	cfg := m.cfg
	return func() tea.Msg {
		st := chat.NewArticleChatStore(cfg.ArticlesRoot, slug)
		history, err := st.LoadHistory()
		achatCfg, _ := storefs.ReadArticleChatConfig(cfg.ArticlesRoot, slug)
		wsStats, _ := chatengine.LoadWorkspaceStats(cfg.EventsPath, slug)
		if err != nil {
			return achatHistoryLoadedMsg{slug: slug, err: err.Error(), profile: achatCfg.Profile, workspaceStats: wsStats}
		}
		return achatHistoryLoadedMsg{slug: slug, msgs: history.Msgs, profile: achatCfg.Profile, workspaceStats: wsStats}
	}
}

// loadAchatWorkspaceStatsCmd reloads lifetime stats for the current article from events.jsonl.
func (m *Model) loadAchatWorkspaceStatsCmd() tea.Cmd {
	eventsPath := m.cfg.EventsPath
	slug := m.achatSlug
	return func() tea.Msg {
		stats, _ := chatengine.LoadWorkspaceStats(eventsPath, slug)
		return achatWorkspaceStatsMsg{stats: stats}
	}
}

// startArticleChatCmd constructs an article chat engine asynchronously.
func (m *Model) startArticleChatCmd(slug, profileName string) tea.Cmd {
	cfg := m.cfg
	svc := m.svc

	return func() tea.Msg {
		// Load article summary for system prompt.
		ctx := context.Background()
		a, err := svc.GetArticle(ctx, slug)
		if err != nil {
			return achatReadyMsg{err: fmt.Sprintf("article not found: %s", slug), slug: slug}
		}
		summary, err := svc.ReadSummary(a)
		if err != nil {
			summary = "(summary not available)"
		}

		// Build system prompt from template.
		tmpl := cfg.ArticleChatSystemPrompt()
		systemPrompt := strings.Replace(tmpl, "{summary}", summary, 1)

		eng, err := chatengine.NewArticleChat(cfg, cfg.ArticlesRoot, slug, profileName, systemPrompt)
		if err != nil {
			return achatReadyMsg{err: err.Error(), slug: slug}
		}
		return achatReadyMsg{engine: eng, slug: slug}
	}
}

// sendArticleChatMsg sends the user prompt to the article chat engine.
func (m *Model) sendArticleChatMsg(prompt string) tea.Cmd {
	eng := m.achatEngine
	ctx, cancel := context.WithCancel(context.Background())
	m.achatCancelStream = cancel
	m.achatStreaming = true
	m.achatStreamBuf = ""
	shared := &streamBuf{}
	m.achatSharedBuf = shared

	return func() tea.Msg {
		cb := chatengine.ChatCallbacks{
			OnTextDelta: func(delta string) error {
				shared.Append(delta)
				return nil
			},
		}
		result, err := eng.ChatWithTools(ctx, prompt, chatengine.ChatOptions{}, cb)
		if err != nil {
			return achatStreamDoneMsg{usage: result.Usage, elapsed: result.Elapsed, err: err.Error()}
		}
		return achatStreamDoneMsg{usage: result.Usage, elapsed: result.Elapsed}
	}
}

// ── Enter / Exit ───────────────────────────────────────────────────────────

// cmdArticleChat handles the /chat command: opens article chat in the askX pane area.
func (m *Model) cmdArticleChat() tea.Cmd {
	ni := m.selectedNavItem()
	if ni == nil {
		m.statusMsg = "✗ no article selected"
		m.statusErr = true
		return nil
	}
	slug := ni.id

	// Close any other split panes.
	m.closeScratch()
	m.closePreview()
	m.closeAskX()

	// Enter article chat mode.
	m.achatMode = true
	m.achatSlug = slug
	m.achatFocused = true
	m.achatAutoScroll = true
	m.achatBoxCursor = 0
	m.achatCollapsed = nil

	// Load history async.
	return m.loadArticleChatHistoryCmd(slug)
}

// exitArticleChat cleans up all article chat state.
func (m *Model) exitArticleChat() {
	if m.achatCancelStream != nil {
		m.achatCancelStream()
		m.achatCancelStream = nil
	}
	m.achatMode = false
	m.achatFocused = false
	m.achatSlug = ""
	m.achatEngine = nil
	m.achatProfile = ""
	m.achatPendingPrompt = ""
	m.achatDisplayLines = nil
	m.achatRawMsgs = nil
	m.achatScroll = 0
	m.achatStreaming = false
	m.achatStreamBuf = ""
	m.achatSharedBuf = nil
	m.achatWorkspaceStats = chatengine.WorkspaceStats{}
	m.achatSessionTurns = 0
	m.achatLastUsage = nil
	m.achatLastElapsed = 0
	m.achatAutoScroll = false
	m.achatBoxCursor = 0
	m.achatCollapsed = nil
	m.statusMsg = ""
	m.statusLines = nil
}

// ── Display line rebuilding ────────────────────────────────────────────────

// rebuildArticleChatLines builds displayable lines from history + streaming buffer.
// Same logic as rebuildChatLines but uses achat* state.
func (m *Model) rebuildArticleChatLines(width int) {
	if width < 10 {
		width = 10
	}

	var msgs []chat.Message
	if m.achatEngine != nil {
		msgs = m.achatEngine.History().Msgs
	} else {
		msgs = m.achatRawMsgs
	}

	const userPrefix = "● "
	const contPrefix = "  "
	userW := width - len([]rune(userPrefix))
	contW := width - len([]rune(contPrefix))
	if userW < 10 {
		userW = 10
	}
	if contW < 10 {
		contW = 10
	}

	var lines []chatLine
	prevHadContent := false

	appendUser := func(content string) {
		if prevHadContent {
			lines = append(lines, chatLine{chatLineBlank, ""})
		}
		raw := strings.Split(content, "\n")
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
	}

	appendNote := func(content string) {
		if prevHadContent {
			lines = append(lines, chatLine{chatLineBlank, ""})
		}
		first := true
		for _, rl := range strings.Split(content, "\n") {
			rl = strings.TrimRight(rl, " \t")
			if rl == "" {
				continue
			}
			prefix := "   "
			if first {
				prefix = "📌 "
				first = false
			}
			for _, wl := range wordWrap(rl, contW) {
				lines = append(lines, chatLine{chatLineNote, prefix + wl})
			}
		}
		prevHadContent = true
	}

	for _, msg := range msgs {
		switch msg.Role {
		case chat.RoleUser:
			appendUser(msg.Content)
		case chat.RoleAssistant:
			if len(msg.ToolCalls) > 0 {
				continue
			}
			mdLines := m.appendMarkdown(msg.Content, width)
			for len(mdLines) > 0 && mdLines[0].role == chatLineBlank {
				mdLines = mdLines[1:]
			}
			for len(mdLines) > 0 && mdLines[len(mdLines)-1].role == chatLineBlank {
				mdLines = mdLines[:len(mdLines)-1]
			}
			lines = append(lines, mdLines...)
			prevHadContent = len(mdLines) > 0
		case chat.RoleNote:
			appendNote(msg.Content)
		}
	}

	// Streaming buffer.
	if m.achatStreaming && m.achatStreamBuf != "" {
		streamLines := m.appendMarkdown(m.achatStreamBuf, width)
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
	m.achatDisplayLines = compacted
}

// ── Box navigation (mirrors workspace chat) ────────────────────────────────

// achatBoxInfos walks message history and returns one entry per logical box.
func (m *Model) achatBoxInfos() []chatBoxInfo {
	msgs := m.achatRawMsgs
	if m.achatEngine != nil {
		msgs = m.achatEngine.History().Msgs
	}
	var infos []chatBoxInfo
	for i, msg := range msgs {
		switch msg.Role {
		case chat.RoleUser:
			ts := ""
			if !msg.Time.IsZero() {
				ts = msg.Time.Format("Jan 2, 2006 · 15:04")
			}
			infos = append(infos, chatBoxInfo{role: chatLineUser, ts: ts, msgStart: i, msgEnd: i + 1, commented: msg.Commented})
		case chat.RoleAssistant:
			if len(infos) > 0 && infos[len(infos)-1].role == chatLineUser {
				infos[len(infos)-1].msgEnd = i + 1
				if msg.Profile != "" && infos[len(infos)-1].profile == "" {
					infos[len(infos)-1].profile = msg.Profile
				}
			}
		case chat.RoleNote:
			ts := ""
			if !msg.Time.IsZero() {
				ts = msg.Time.Format("Jan 2, 2006 · 15:04")
			}
			infos = append(infos, chatBoxInfo{role: chatLineNote, ts: ts, msgStart: i, msgEnd: i + 1})
		default:
			if len(infos) > 0 && infos[len(infos)-1].role == chatLineUser {
				infos[len(infos)-1].msgEnd = i + 1
			}
		}
	}
	return infos
}

// achatBoxCount returns the number of logical boxes.
func (m *Model) achatBoxCount() int {
	dl := m.achatDisplayLines
	count := 0
	for i, cl := range dl {
		if cl.role == chatLineUser && (i == 0 || dl[i-1].role != chatLineUser) {
			count++
		} else if cl.role == chatLineNote && (i == 0 || dl[i-1].role != chatLineNote) {
			count++
		}
	}
	return count
}

// achatTotalLines returns the scrollable line count for article chat.
func (m *Model) achatTotalLines() int {
	if vlines := m.buildArticleChatVLines(); vlines != nil {
		return len(vlines)
	}
	return len(m.achatDisplayLines)
}

// buildArticleChatVLines builds the virtual boxed display for article chat.
// Mirrors buildChatVLines using achat* state.
func (m *Model) buildArticleChatVLines() []chatVLine {
	if !m.achatMode || !m.achatFocused || m.focus != paneContent {
		return nil
	}
	dl := m.achatDisplayLines
	infos := m.achatBoxInfos()
	if len(dl) == 0 {
		return nil
	}

	collapsed := m.achatCollapsed
	if collapsed == nil {
		collapsed = map[int]bool{}
	}

	var vlines []chatVLine
	boxIdx := -1
	inBox := false

	for i, cl := range dl {
		isBoxStart := false
		if cl.role == chatLineUser && (i == 0 || dl[i-1].role != chatLineUser) {
			boxIdx++
			isBoxStart = true
		} else if cl.role == chatLineNote && (i == 0 || dl[i-1].role != chatLineNote) {
			boxIdx++
			isBoxStart = true
		}

		selected := boxIdx == m.achatBoxCursor
		commented := false
		if boxIdx >= 0 && boxIdx < len(infos) {
			commented = infos[boxIdx].commented
		}

		if isBoxStart {
			if inBox {
				// Close previous box.
				vlines = append(vlines, chatVLine{isBoxBottom: true, boxIdx: boxIdx - 1, isSelected: boxIdx-1 == m.achatBoxCursor, isCommented: boxIdx-1 < len(infos) && infos[boxIdx-1].commented})
				vlines = append(vlines, chatVLine{isSep: true, boxIdx: boxIdx})
			}
			if selected {
				vlines = append(vlines, chatVLine{isBoxTop: true, boxIdx: boxIdx, isSelected: true, isCommented: commented})
				// Header line.
				meta := ""
				if boxIdx < len(infos) {
					parts := []string{}
					if infos[boxIdx].ts != "" {
						parts = append(parts, infos[boxIdx].ts)
					}
					if infos[boxIdx].profile != "" {
						parts = append(parts, infos[boxIdx].profile)
					}
					meta = strings.Join(parts, " · ")
				}
				vlines = append(vlines, chatVLine{isHeader: true, metaText: meta, boxIdx: boxIdx, isSelected: true, isCommented: commented})
			}
			inBox = true
		}

		if selected && collapsed[boxIdx] {
			// Show first 3 content lines + ellipsis.
			boxStartVIdx := len(vlines)
			for ci := i; ci < len(dl); ci++ {
				cll := dl[ci]
				if ci > i {
					nextIsNewBox := false
					if cll.role == chatLineUser && (ci == 0 || dl[ci-1].role != chatLineUser) {
						nextIsNewBox = true
					} else if cll.role == chatLineNote && (ci == 0 || dl[ci-1].role != chatLineNote) {
						nextIsNewBox = true
					}
					if nextIsNewBox {
						break
					}
				}
				if len(vlines)-boxStartVIdx < 3 {
					vlines = append(vlines, chatVLine{contentIdx: ci, boxIdx: boxIdx, isSelected: true, isCommented: commented})
				}
			}
			remaining := 0
			for ci := i; ci < len(dl); ci++ {
				if ci > i {
					nextIsNewBox := false
					if dl[ci].role == chatLineUser && (ci == 0 || dl[ci-1].role != chatLineUser) {
						nextIsNewBox = true
					} else if dl[ci].role == chatLineNote && (ci == 0 || dl[ci-1].role != chatLineNote) {
						nextIsNewBox = true
					}
					if nextIsNewBox {
						break
					}
				}
				remaining++
			}
			if remaining > 3 {
				vlines = append(vlines, chatVLine{isEllipsis: true, boxIdx: boxIdx, isSelected: true, isCommented: commented})
			}
			// Skip to next box.
			for i+1 < len(dl) {
				next := dl[i+1]
				nextIsNewBox := false
				if next.role == chatLineUser && dl[i].role != chatLineUser {
					nextIsNewBox = true
				} else if next.role == chatLineNote && dl[i].role != chatLineNote {
					nextIsNewBox = true
				}
				if nextIsNewBox {
					break
				}
				i++ // Note: this doesn't work in a range loop — will be skipped naturally by content
			}
			continue
		}

		vlines = append(vlines, chatVLine{contentIdx: i, boxIdx: boxIdx, isSelected: selected, isCommented: commented})
	}

	// Close final box.
	if inBox && boxIdx >= 0 && boxIdx == m.achatBoxCursor {
		vlines = append(vlines, chatVLine{isBoxBottom: true, boxIdx: boxIdx, isSelected: true, isCommented: boxIdx < len(infos) && infos[boxIdx].commented})
	}

	return vlines
}

// scrollToArticleChatBox adjusts achatScroll so that box boxIdx is visible.
func (m *Model) scrollToArticleChatBox(boxIdx, viewH int) {
	vlines := m.buildArticleChatVLines()
	if len(vlines) == 0 {
		return
	}
	first, last := -1, -1
	for i, vl := range vlines {
		if vl.boxIdx == boxIdx {
			if first == -1 {
				first = i
			}
			last = i
		}
	}
	if first == -1 {
		return
	}
	if first >= m.achatScroll && last < m.achatScroll+viewH {
		return
	}
	if first < m.achatScroll {
		m.achatScroll = first
	} else {
		m.achatScroll = last - viewH + 1
		if m.achatScroll < 0 {
			m.achatScroll = 0
		}
	}
}

// achatAutoScrollToBottom scrolls article chat to the bottom if autoScroll is on.
func (m *Model) achatAutoScrollToBottom(viewH int) {
	if !m.achatAutoScroll {
		return
	}
	total := m.achatTotalLines()
	if total > viewH {
		m.achatScroll = total - viewH
	} else {
		m.achatScroll = 0
	}
}

// ── Box operations ─────────────────────────────────────────────────────────

// cmdArticleChatCollapseBox toggles collapse on a box.
func (m *Model) cmdArticleChatCollapseBox(boxIdx int) {
	if m.achatCollapsed == nil {
		m.achatCollapsed = map[int]bool{}
	}
	m.achatCollapsed[boxIdx] = !m.achatCollapsed[boxIdx]
}

// cmdArticleChatCommentBox toggles the commented flag on a box.
func (m *Model) cmdArticleChatCommentBox(boxIdx int) tea.Cmd {
	infos := m.achatBoxInfos()
	if boxIdx < 0 || boxIdx >= len(infos) {
		return nil
	}
	bi := infos[boxIdx]

	newState := !bi.commented
	toggle := func(msgs []chat.Message) {
		for i := bi.msgStart; i < bi.msgEnd && i < len(msgs); i++ {
			msgs[i].Commented = newState
		}
	}

	if m.achatEngine != nil {
		toggle(m.achatEngine.History().Msgs)
	} else {
		toggle(m.achatRawMsgs)
	}
	m.rebuildArticleChatLines(m.achatBuildWidth())

	// Save in background.
	articlesRoot := m.cfg.ArticlesRoot
	slug := m.achatSlug
	var toSave []chat.Message
	if m.achatEngine != nil {
		src := m.achatEngine.History().Msgs
		toSave = make([]chat.Message, len(src))
		copy(toSave, src)
	} else {
		toSave = make([]chat.Message, len(m.achatRawMsgs))
		copy(toSave, m.achatRawMsgs)
	}
	status := "✓ exchange commented out"
	if !newState {
		status = "✓ exchange uncommented"
	}
	return func() tea.Msg {
		st := chat.NewArticleChatStore(articlesRoot, slug)
		if err := st.SaveHistory(&chat.History{Msgs: toSave}); err != nil {
			return cmdDoneMsg{err: "comment: " + err.Error()}
		}
		return cmdDoneMsg{statusMsg: status}
	}
}

// cmdArticleChatDeleteBox deletes a box from history.
func (m *Model) cmdArticleChatDeleteBox(boxIdx int) tea.Cmd {
	infos := m.achatBoxInfos()
	if boxIdx < 0 || boxIdx >= len(infos) {
		return nil
	}
	bi := infos[boxIdx]

	deleteFromSlice := func(msgs []chat.Message) []chat.Message {
		start, end := bi.msgStart, bi.msgEnd
		if start < 0 || end > len(msgs) || start >= end {
			return msgs
		}
		out := make([]chat.Message, 0, len(msgs)-(end-start))
		out = append(out, msgs[:start]...)
		out = append(out, msgs[end:]...)
		return out
	}

	if m.achatEngine != nil {
		m.achatEngine.History().Msgs = deleteFromSlice(m.achatEngine.History().Msgs)
	} else {
		m.achatRawMsgs = deleteFromSlice(m.achatRawMsgs)
	}

	if m.achatCollapsed != nil {
		newCollapsed := make(map[int]bool)
		for k, v := range m.achatCollapsed {
			if k < boxIdx {
				newCollapsed[k] = v
			} else if k > boxIdx {
				newCollapsed[k-1] = v
			}
		}
		m.achatCollapsed = newCollapsed
	}

	m.rebuildArticleChatLines(m.achatBuildWidth())
	numBoxes := m.achatBoxCount()
	if numBoxes == 0 {
		m.achatBoxCursor = 0
	} else if m.achatBoxCursor >= numBoxes {
		m.achatBoxCursor = numBoxes - 1
	}

	articlesRoot := m.cfg.ArticlesRoot
	slug := m.achatSlug
	var toSave []chat.Message
	if m.achatEngine != nil {
		src := m.achatEngine.History().Msgs
		toSave = make([]chat.Message, len(src))
		copy(toSave, src)
	} else {
		toSave = make([]chat.Message, len(m.achatRawMsgs))
		copy(toSave, m.achatRawMsgs)
	}
	return func() tea.Msg {
		st := chat.NewArticleChatStore(articlesRoot, slug)
		if err := st.SaveHistory(&chat.History{Msgs: toSave}); err != nil {
			return cmdDoneMsg{err: "delete: " + err.Error()}
		}
		return cmdDoneMsg{statusMsg: "✓ exchange deleted"}
	}
}

// cmdArticleChatTTS speaks the selected box via TTS.
func (m *Model) cmdArticleChatTTS() tea.Cmd {
	if m.ttsPlayer.Playing() {
		m.stopTTS()
		m.statusMsg = ""
		return nil
	}
	infos := m.achatBoxInfos()
	if m.achatBoxCursor < 0 || m.achatBoxCursor >= len(infos) {
		m.statusMsg = "nothing to speak"
		return nil
	}
	bi := infos[m.achatBoxCursor]
	var msgs []chat.Message
	if m.achatEngine != nil {
		msgs = m.achatEngine.History().Msgs
	} else {
		msgs = m.achatRawMsgs
	}

	var parts []string
	for i := bi.msgStart; i < bi.msgEnd && i < len(msgs); i++ {
		if msgs[i].Content != "" {
			parts = append(parts, msgs[i].Content)
		}
	}
	if len(parts) == 0 {
		m.statusMsg = "nothing to speak"
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

// ── Status bar ────────────────────────────────────────────────────────────

// renderArticleChatStatusLine renders the three-section status bar for article chat,
// mirroring renderAskXStatusLine: left=streaming/status, center=per-turn stats, right=session totals.
func (m Model) renderArticleChatStatusLine() string {
	t := ActiveTheme
	w := m.width

	// Left: streaming indicator > status message.
	var left string
	if m.achatStreaming {
		profile := m.achatProfile
		if profile == "" {
			profile = m.cfg.ArticleChatProfileName()
		}
		label := "chat streaming · " + profile
		left = renderWaveIndicatorLeading(m.spinnerFrame, label, t.StreamingText, t.Dimmed)
	} else if m.statusMsg != "" {
		if m.statusErr || strings.HasPrefix(m.statusMsg, "✗") {
			left = fgBold(t.StatusError, " "+m.statusMsg)
		} else {
			left = fg(t.StatusText, " "+m.statusMsg)
		}
	}

	// Center: per-turn token stats (available after first response).
	var center string
	if m.achatLastUsage != nil {
		u := m.achatLastUsage
		center = fg(t.ContentDimmed, fmt.Sprintf("in:%d out:%d  %.1fs", u.InputTokens, u.OutputTokens, m.achatLastElapsed.Seconds()))
	}

	// Right: lifetime stats from events.jsonl (mirrors workspace chat).
	ws := m.achatWorkspaceStats
	var right string
	if ws.Turns > 0 {
		right = fg(t.ContentDimmed, fmt.Sprintf("%d turns · %dk in · %dk out · %s",
			ws.Turns,
			(ws.InputTokens+500)/1000,
			(ws.OutputTokens+500)/1000,
			formatUSD(ws.CostUSD)))
	}

	// Compose: left | center (padded) | right.
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

// ── Prompt helpers ─────────────────────────────────────────────────────────

// achatPromptPrefix returns the input prompt for article chat mode.
func (m *Model) achatPromptPrefix() string {
	profile := m.achatProfile
	if profile == "" {
		profile = m.cfg.ArticleChatProfileName()
	}
	return "chat(" + profile + ")> "
}

// achatBuildWidth returns the width for rebuilding article chat display lines.
func (m *Model) achatBuildWidth() int {
	w := m.width - m.navWidth() - 1
	if m.focus == paneContent {
		w -= 4
	}
	if w < 10 {
		w = 10
	}
	return w
}

// achatViewHeight returns the visible line count for the article chat split pane.
func (m *Model) achatViewHeight() int {
	mainH := m.mainAreaHeight()
	splitH := mainH / 2
	if splitH < 3 {
		splitH = 3
	}
	h := splitH - 1 // 1 for separator header
	if h < 1 {
		h = 1
	}
	return h
}

// ── Article chat command dispatch ──────────────────────────────────────────

// dispatchArticleChatCommand handles commands typed in article chat mode.
// Returns (handled bool, cmd tea.Cmd).
func (m *Model) dispatchArticleChatCommand(command, args string) (bool, tea.Cmd) {
	switch command {
	case "/clear":
		if m.achatEngine != nil {
			_ = m.achatEngine.ClearHistory()
		}
		m.achatRawMsgs = nil
		m.achatDisplayLines = nil
		m.achatScroll = 0
		m.achatBoxCursor = 0
		m.achatCollapsed = nil
		m.rebuildArticleChatLines(m.achatBuildWidth())
		m.statusMsg = "chat history cleared"
		return true, nil

	case "/profile", "/model":
		if args == "" {
			m.statusMsg = "profile: " + m.achatProfile
			return true, nil
		}
		prof, ok := m.cfg.Profiles[args]
		if !ok {
			m.statusMsg = "✗ unknown profile: " + args
			m.statusErr = true
			return true, nil
		}
		m.achatProfile = args
		// Persist sticky profile.
		_ = storefs.WriteArticleChatConfig(m.cfg.ArticlesRoot, m.achatSlug, storefs.ArticleChatConfig{Profile: args})
		// Update engine if running.
		if m.achatEngine != nil {
			if err := m.achatEngine.SetProfile(args, prof); err != nil {
				m.statusMsg = "✗ " + err.Error()
				m.statusErr = true
				return true, nil
			}
		}
		m.syncInputPrompt()
		m.statusMsg = "profile → " + args
		return true, nil

	case "/stats":
		var lines []string
		lines = append(lines, fmt.Sprintf("Article chat: %s", m.achatSlug))
		lines = append(lines, fmt.Sprintf("Profile: %s", m.achatProfile))
		lines = append(lines, fmt.Sprintf("Session tokens: %d in / %d out", m.achatSessionIn, m.achatSessionOut))
		if m.achatSessionCost > 0 {
			lines = append(lines, fmt.Sprintf("Session cost: $%.4f", m.achatSessionCost))
		}
		if m.achatLastUsage != nil {
			lines = append(lines, fmt.Sprintf("Last turn: %d in / %d out", m.achatLastUsage.InputTokens, m.achatLastUsage.OutputTokens))
		}
		m.setStatusLines(lines)
		return true, nil

	case "/system":
		if m.achatEngine != nil {
			// Engine doesn't expose system prompt directly; show config template.
			tmpl := m.cfg.ArticleChatSystemPrompt()
			m.setStatusLines(strings.Split(tmpl, "\n"))
		} else {
			m.statusMsg = "engine not initialized"
		}
		return true, nil

	case "/help":
		var lines []string
		lines = append(lines, "Article chat commands:")
		for _, c := range achatCommands {
			line := "  " + c.cmd
			if c.arg != "" {
				line += " " + c.arg
			}
			line += "  — " + c.desc
			lines = append(lines, line)
		}
		lines = append(lines, "")
		lines = append(lines, "@ markers (article-implicit):")
		lines = append(lines, "  @b  inject article body")
		lines = append(lines, "  @s  inject article summary")
		lines = append(lines, "  @f  inject article flash")
		lines = append(lines, "")
		lines = append(lines, "Content keys (when focused):")
		lines = append(lines, "  j/k       navigate boxes")
		lines = append(lines, "  v         collapse/expand box")
		lines = append(lines, "  #         toggle comment (exclude from LLM)")
		lines = append(lines, "  x         delete box")
		lines = append(lines, "  s         speak box via TTS")
		lines = append(lines, "  V         view in overlay")
		lines = append(lines, "  Esc       exit article chat")
		m.setStatusLines(lines)
		return true, nil
	}

	return false, nil
}

// collapseAllArticleChatBoxes marks every logical box as collapsed except the last one.
func (m *Model) collapseAllArticleChatBoxes() {
	n := m.achatBoxCount()
	m.achatCollapsed = make(map[int]bool, n)
	for i := 0; i < n-1; i++ {
		m.achatCollapsed[i] = true
	}
}

// ── Note insertion ─────────────────────────────────────────────────────────

// ── Rendering ─────────────────────────────────────────────────────────────

// renderArticleChatPane renders the article chat conversation in the content pane.
// Mirrors renderChatPane but uses achat* state.
func (m Model) renderArticleChatPane(height, width int) []string {
	t := ActiveTheme
	var lines []string

	// Separator header (matches askX split pane pattern).
	msgCount := len(m.achatRawMsgs)
	if m.achatEngine != nil {
		msgCount = len(m.achatEngine.History().Msgs)
	}
	label := " chat "
	if m.achatEngine != nil {
		label += "· " + m.achatEngine.Profile().Model + " "
	} else if m.achatProfile != "" {
		label += "· " + m.achatProfile + " "
	}
	label += fmt.Sprintf("· %d msgs ", msgCount)
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
	if m.achatFocused && m.focus == paneContent {
		headerColor = t.Accent
	}
	header := fg(headerColor, strings.Repeat("─", leftSep)+label+strings.Repeat("─", rightSep)+hint)
	lines = append(lines, header)

	// Chat content area.
	chatH := height - 1 // 1 for header
	if chatH < 1 {
		chatH = 1
	}

	if len(m.achatDisplayLines) == 0 && !m.achatStreaming {
		lines = append(lines, fg(t.ContentDimmed, "Type a message to start chatting."))
		for len(lines) < height {
			lines = append(lines, "")
		}
		return lines[:height]
	}

	// Boxed mode (focus == paneContent).
	if vlines := m.buildArticleChatVLines(); vlines != nil {
		innerW := width - 4
		if innerW < 4 {
			innerW = 4
		}
		topRule := fg(t.BoxBorder, "╭"+strings.Repeat("─", width-2)+"╮")
		botRule := fg(t.BoxBorder, "╰"+strings.Repeat("─", width-2)+"╯")
		bL := fg(t.BoxBorder, "│ ")
		bR := fg(t.BoxBorder, " │")
		topRuleC := fg(t.ContentDimmed, "╭"+strings.Repeat("─", width-2)+"╮")
		botRuleC := fg(t.ContentDimmed, "╰"+strings.Repeat("─", width-2)+"╯")
		bLC := fg(t.ContentDimmed, "│ ")
		bRC := fg(t.ContentDimmed, " │")

		total := len(vlines)
		start := m.achatScroll
		if start > total-chatH {
			start = total - chatH
		}
		if start < 0 {
			start = 0
		}
		end := start + chatH
		if end > total {
			end = total
		}

		selPlain := m.selectionMode && m.selectionMaxPane == paneContent

		for _, vl := range vlines[start:end] {
			switch {
			case vl.isBoxTop:
				if selPlain {
					lines = append(lines, "")
				} else if vl.isCommented {
					lines = append(lines, topRuleC)
				} else {
					lines = append(lines, topRule)
				}

			case vl.isBoxBottom:
				if selPlain {
					lines = append(lines, "")
				} else if vl.isCommented {
					lines = append(lines, botRuleC)
				} else {
					lines = append(lines, botRule)
				}

			case vl.isSep:
				lines = append(lines, "")

			case vl.isHeader:
				if selPlain {
					lines = append(lines, fg(t.ContentDimmed, vl.metaText))
				} else {
					expandHint := "v expand"
					if m.achatCollapsed != nil && m.achatCollapsed[vl.boxIdx] {
						expandHint = "v collapse"
					}
					var hintsStr string
					if vl.isCommented {
						hintsStr = "# uncomment · " + expandHint + " · s speak · x delete"
					} else {
						hintsStr = "# comment · " + expandHint + " · s speak · x delete"
					}
					left := vl.metaText
					if vl.isCommented {
						left = "📌 " + left
					}
					leftW := lipgloss.Width(left)
					hintsW := lipgloss.Width(hintsStr)
					pad := innerW - leftW - hintsW
					if pad < 1 {
						pad = 1
					}
					headerContent := fg(t.ContentDimmed, left) +
						strings.Repeat(" ", pad) +
						fg(t.ContentDimmed, hintsStr)
					totalW := leftW + pad + hintsW
					if totalW < innerW {
						headerContent += strings.Repeat(" ", innerW-totalW)
					}
					borderL, borderR := bL, bR
					if vl.isCommented {
						borderL, borderR = bLC, bRC
					}
					lines = append(lines, borderL+headerContent+borderR)
				}

			case vl.isEllipsis:
				if selPlain || !vl.isSelected {
					lines = append(lines, fg(t.ContentDimmed, vl.metaText))
				} else {
					text := fg(t.ContentDimmed, vl.metaText)
					visW := lipgloss.Width(vl.metaText)
					if visW < innerW {
						text += strings.Repeat(" ", innerW-visW)
					}
					borderL, borderR := bL, bR
					if vl.isCommented {
						borderL, borderR = bLC, bRC
					}
					lines = append(lines, borderL+text+borderR)
				}

			default:
				cl := m.achatDisplayLines[vl.contentIdx]
				if selPlain || !vl.isSelected {
					if vl.isCommented {
						lines = append(lines, fg(t.ContentDimmed, cl.text))
					} else {
						lines = append(lines, colorChatLine(cl, t))
					}
				} else {
					borderL, borderR := bL, bR
					if vl.isCommented {
						borderL, borderR = bLC, bRC
					}
					if cl.role == chatLineBlank || cl.text == "" {
						lines = append(lines, borderL+strings.Repeat(" ", innerW)+borderR)
						continue
					}
					isTTSLine := m.ttsPlayer.Playing() &&
						vl.boxIdx == m.chatTTSBoxIdx &&
						vl.contentIdx == m.chatTTSCursor
					budget := innerW
					if cl.role == chatLineQuote {
						budget = innerW - 2
						if budget < 2 {
							budget = 2
						}
					}
					if isTTSLine {
						budget -= 2
						if budget < 2 {
							budget = 2
						}
					}
					text := cl.text
					visW := lipgloss.Width(text)
					if visW > budget {
						runes := []rune(text)
						for len(runes) > 0 && lipgloss.Width(string(runes)) > budget-1 {
							runes = runes[:len(runes)-1]
						}
						text = string(runes) + "…"
					} else if visW < budget {
						text = text + strings.Repeat(" ", budget-visW)
					}
					var colored string
					if vl.isCommented {
						colored = fg(t.ContentDimmed, text)
					} else {
						colored = colorChatLine(chatLine{role: cl.role, text: text}, t)
					}
					if isTTSLine {
						colored = fgBold(t.InputPrompt, "▶ ") + colored
					}
					lines = append(lines, borderL+colored+borderR)
				}
			}
		}

		for len(lines) < height {
			lines = append(lines, "")
		}
		return lines[:height]
	}

	// Flat mode (no boxes): plain scroll over achatDisplayLines.
	totalLines := len(m.achatDisplayLines)
	start := m.achatScroll
	if start > totalLines-chatH {
		start = totalLines - chatH
	}
	if start < 0 {
		start = 0
	}
	end := start + chatH
	if end > totalLines {
		end = totalLines
	}

	for i := start; i < end; i++ {
		lines = append(lines, colorChatLine(m.achatDisplayLines[i], t))
	}

	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines[:height]
}

// ── Content key handling ──────────────────────────────────────────────────

// handleArticleChatContentKey handles keys in the content pane during article chat.
// Mirrors handleChatContentKey but uses achat* state.
func (m *Model) handleArticleChatContentKey(msg tea.KeyMsg) tea.Cmd {
	chatViewH := m.achatViewHeight()
	if chatViewH < 1 {
		chatViewH = 1
	}

	numBoxes := m.achatBoxCount()

	switch {
	case msg.Type == tea.KeyRunes:
		switch msg.String() {
		case "v":
			if numBoxes > 0 {
				m.cmdArticleChatCollapseBox(m.achatBoxCursor)
			}
			return nil
		case "#":
			if numBoxes > 0 {
				return m.cmdArticleChatCommentBox(m.achatBoxCursor)
			}
			return nil
		case "x":
			if numBoxes > 0 {
				return m.cmdArticleChatDeleteBox(m.achatBoxCursor)
			}
			return nil
		case "s":
			return m.cmdArticleChatTTS()
		case "V":
			return m.cmdArticleChatOverlay()
		case "[":
			return m.cmdChatTTSAdjustRate(-20)
		case "]":
			return m.cmdChatTTSAdjustRate(+20)
		}
	case key.Matches(msg, keys.NavUp):
		if m.achatBoxCursor > 0 {
			m.achatBoxCursor--
			m.achatAutoScroll = false
			m.scrollToArticleChatBox(m.achatBoxCursor, chatViewH)
		}
		return nil
	case key.Matches(msg, keys.NavDown):
		if m.achatBoxCursor < numBoxes-1 {
			m.achatBoxCursor++
			m.achatAutoScroll = m.achatBoxCursor >= numBoxes-1
			m.scrollToArticleChatBox(m.achatBoxCursor, chatViewH)
		}
		return nil
	}

	// Scroll operations.
	maxScroll := m.achatTotalLines() - chatViewH
	if maxScroll < 0 {
		maxScroll = 0
	}
	switch {
	case key.Matches(msg, keys.PageUp):
		m.achatScroll -= chatViewH
		if m.achatScroll < 0 {
			m.achatScroll = 0
		}
		m.achatAutoScroll = false
	case key.Matches(msg, keys.PageDown):
		m.achatScroll += chatViewH
		if m.achatScroll > maxScroll {
			m.achatScroll = maxScroll
		}
		if m.achatScroll >= maxScroll {
			m.achatAutoScroll = true
		}
	case key.Matches(msg, keys.Home):
		m.achatScroll = 0
		m.achatBoxCursor = 0
		m.achatAutoScroll = false
	case key.Matches(msg, keys.End):
		m.achatScroll = maxScroll
		if numBoxes > 0 {
			m.achatBoxCursor = numBoxes - 1
		}
		m.achatAutoScroll = true
	}
	return nil
}

// cmdArticleChatOverlay opens the selected box in the resource overlay.
func (m *Model) cmdArticleChatOverlay() tea.Cmd {
	infos := m.achatBoxInfos()
	if m.achatBoxCursor < 0 || m.achatBoxCursor >= len(infos) {
		m.setStatusError("nothing to view")
		return nil
	}
	bi := infos[m.achatBoxCursor]
	var msgs []chat.Message
	if m.achatEngine != nil {
		msgs = m.achatEngine.History().Msgs
	} else {
		msgs = m.achatRawMsgs
	}

	var parts []string
	for i := bi.msgStart; i < bi.msgEnd && i < len(msgs); i++ {
		if msgs[i].Content != "" {
			parts = append(parts, msgs[i].Content)
		}
	}
	if len(parts) == 0 {
		m.setStatusError("nothing to view")
		return nil
	}

	title := fmt.Sprintf("article chat #%d", m.achatBoxCursor+1)
	m.openResourceOverlay(title, strings.Join(parts, "\n\n"))
	return nil
}

// cmdArticleChatAddNote adds a note (comment) to the article chat history.
func (m *Model) cmdArticleChatAddNote(text string) tea.Cmd {
	if text == "" {
		m.setStatusError("comment cannot be empty — use //your note text")
		return nil
	}
	note := chat.Message{Role: chat.RoleNote, Content: text, Time: time.Now()}

	// Append to raw msgs for display (engine may not be initialised yet).
	m.achatRawMsgs = append(m.achatRawMsgs, note)
	// If engine is live, keep its history in sync too.
	if m.achatEngine != nil {
		m.achatEngine.History().Msgs = append(m.achatEngine.History().Msgs, note)
	}

	m.rebuildArticleChatLines(m.achatBuildWidth())
	m.achatAutoScroll = true
	viewH := m.achatViewHeight()
	m.achatAutoScrollToBottom(viewH)

	articlesRoot := m.cfg.ArticlesRoot
	slug := m.achatSlug
	var src []chat.Message
	if m.achatEngine != nil {
		src = m.achatEngine.History().Msgs
	} else {
		src = m.achatRawMsgs
	}
	toSave := make([]chat.Message, len(src))
	copy(toSave, src)
	return func() tea.Msg {
		st := chat.NewArticleChatStore(articlesRoot, slug)
		_ = st.SaveHistory(&chat.History{Msgs: toSave})
		return nil
	}
}

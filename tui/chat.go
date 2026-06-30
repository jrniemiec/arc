package tui

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/jrniemiec/arc/chat"
	chatengine "github.com/jrniemiec/arc/chat/engine"
	storefs "github.com/jrniemiec/arc/store/fs"
)

// Inline markdown patterns for stripping.
var (
	mdBold   = regexp.MustCompile(`\*\*(.+?)\*\*`)
	mdItalic = regexp.MustCompile(`(?:^|[^*])\*([^*\n]+?)\*(?:[^*]|$)`)
	mdCode   = regexp.MustCompile("`([^`]+)`")
)

// ── Async commands ──────────────────────────────────────────────────────────

// loadChatHistoryCmd reads chat history from disk without initializing the engine.
// focus=true switches the command pane focus (used for explicit Enter/click selection).
func (m *Model) loadChatHistoryCmd(workspaceName string, focus bool) tea.Cmd {
	cfg := m.cfg
	// Capture article count synchronously from workspaceItems before goroutine.
	articleCount := 0
	for _, ws := range m.workspaceItems {
		if ws.name == workspaceName {
			articleCount = ws.articleCount
			break
		}
	}
	return func() tea.Msg {
		st := chat.NewChatStore(cfg.DataRoot, workspaceName)
		history, err := st.LoadHistory()

		chatCfg, _ := storefs.ReadChatConfig(cfg.DataRoot, workspaceName)
		ragMode := chatCfg.RAGMode
		if ragMode == "" {
			ragMode = "open"
		}

		if err != nil {
			return chatHistoryLoadedMsg{workspace: workspaceName, err: err.Error(), focus: focus, articleCount: articleCount, ragMode: ragMode}
		}
		return chatHistoryLoadedMsg{workspace: workspaceName, msgs: history.Msgs, focus: focus, articleCount: articleCount, ragMode: ragMode}
	}
}

// startChatCmd constructs a chat engine asynchronously.
func (m *Model) startChatCmd(workspaceName string) tea.Cmd {
	cfg := m.cfg
	return func() tea.Msg {
		eng, err := chatengine.New(cfg, workspaceName, "")
		if err != nil {
			return chatReadyMsg{err: err.Error(), workspace: workspaceName}
		}
		return chatReadyMsg{engine: eng, workspace: workspaceName}
	}
}

// sendChatMsg sends the user prompt to the engine with streaming deltas.
func (m *Model) sendChatMsg(prompt string) tea.Cmd {
	eng := m.chatEngine
	send := m.programSend
	ctx, cancel := context.WithCancel(context.Background())
	m.chatCancelStream = cancel
	m.chatStreaming = true
	m.chatStreamBuf = ""

	return func() tea.Msg {
		result, err := eng.Chat(ctx, prompt, chatengine.ChatOptions{}, func(delta string) error {
			(*send)(chatStreamDeltaMsg(delta))
			return nil
		})
		if err != nil {
			return chatStreamDoneMsg{usage: result.Usage, elapsed: result.Elapsed, err: err.Error()}
		}
		return chatStreamDoneMsg{usage: result.Usage, elapsed: result.Elapsed}
	}
}

// ── Line rebuilding ─────────────────────────────────────────────────────────

// chatLineRole tags each display line so the renderer can color it correctly.
type chatLineRole uint8

const (
	chatLineBlank     chatLineRole = iota
	chatLineUser                   // ● prompt line
	chatLineAssistant              // indented assistant text
	chatLineNote                   // 📌 note
	chatLineHeader                 // ## markdown heading
	chatLineQuote                  // > blockquote
	chatLineCode                   // code block line
)

// chatLine is one rendered display line with its color role.
type chatLine struct {
	role chatLineRole
	text string
}

// mdStripInline strips **bold**, *italic*, and `code` span markers from a line of text.
func mdStripInline(s string) string {
	s = mdBold.ReplaceAllString(s, "$1")
	// Italic: careful to not mangle list bullets (* item) or un-paired stars.
	s = mdItalic.ReplaceAllStringFunc(s, func(m string) string {
		// Preserve any leading non-star char.
		inner := mdItalic.FindStringSubmatch(m)
		if len(inner) < 2 {
			return m
		}
		// Re-attach any leading char that was part of the lookbehind.
		prefix := ""
		if len(m) > 0 && m[0] != '*' {
			prefix = string(m[0])
		}
		return prefix + inner[1]
	})
	s = mdCode.ReplaceAllString(s, "$1")
	return s
}

// mdIsHR returns true if the line is a markdown horizontal rule.
func mdIsHR(line string) bool {
	if len(line) < 3 {
		return false
	}
	for _, c := range []byte{'-', '=', '*'} {
		if strings.TrimRight(line, string(c)) == "" {
			return true
		}
	}
	return false
}

// mdHeading returns (level, text) if line is a markdown heading, else (0, "").
func mdHeading(line string) (int, string) {
	for level := 6; level >= 1; level-- {
		prefix := strings.Repeat("#", level) + " "
		if strings.HasPrefix(line, prefix) {
			return level, strings.TrimPrefix(line, prefix)
		}
	}
	return 0, ""
}

// appendMarkdown parses assistant markdown content into chatLines with proper
// roles. Headings, HRs, blockquotes and code blocks are handled specially;
// inline markers (**bold**, *italic*, `code`) are stripped from plain text.
func (m *Model) appendMarkdown(content string, width int) []chatLine {
	const indent = "  "
	textW := width - len([]rune(indent))
	if textW < 10 {
		textW = 10
	}

	var lines []chatLine
	rawLines := strings.Split(content, "\n")
	n := len(rawLines)
	inCode := false

	// nextNonEmpty returns the next non-empty trimmed line after index i.
	nextNonEmpty := func(i int) string {
		for j := i + 1; j < n; j++ {
			l := strings.TrimRight(rawLines[j], " \t")
			if l != "" {
				return l
			}
		}
		return ""
	}

	for i, raw := range rawLines {
		line := strings.TrimRight(raw, " \t")

		// ── Code fence ───────────────────────────────────────────────────────
		if strings.HasPrefix(line, "```") {
			inCode = !inCode
			if inCode {
				lang := strings.TrimSpace(strings.TrimPrefix(line, "```"))
				if lang != "" {
					lines = append(lines, chatLine{chatLineCode, indent + "[" + lang + "]"})
				}
			}
			continue
		}
		if inCode {
			lines = append(lines, chatLine{chatLineCode, "    " + line})
			continue
		}

		// ── Blank line ───────────────────────────────────────────────────────
		if line == "" {
			// Only emit a blank line if the next non-empty line is a heading.
			// This separates sections visually without spacing every paragraph.
			next := nextNonEmpty(i)
			if next != "" && !mdIsHR(next) {
				if lvl, _ := mdHeading(next); lvl > 0 {
					// Avoid duplicate blank before heading.
					if len(lines) > 0 && lines[len(lines)-1].role != chatLineBlank {
						lines = append(lines, chatLine{chatLineBlank, ""})
					}
				}
			}
			continue
		}

		// ── Horizontal rule — skip entirely ──────────────────────────────────
		if mdIsHR(line) {
			continue
		}

		// ── Heading ──────────────────────────────────────────────────────────
		if level, text := mdHeading(line); level > 0 {
			text = mdStripInline(text)
			for _, wl := range wordWrap(text, width) {
				lines = append(lines, chatLine{chatLineHeader, wl})
			}
			continue
		}

		// ── Blockquote ───────────────────────────────────────────────────────
		if strings.HasPrefix(line, "> ") {
			text := mdStripInline(strings.TrimPrefix(line, "> "))
			for _, wl := range wordWrap(text, textW-2) {
				lines = append(lines, chatLine{chatLineQuote, indent + wl})
			}
			continue
		}

		// ── Regular text ─────────────────────────────────────────────────────
		text := mdStripInline(line)
		for _, wl := range wordWrap(text, textW) {
			lines = append(lines, chatLine{chatLineAssistant, indent + wl})
		}
	}
	return lines
}

// rebuildChatLines builds displayable lines from history + streaming buffer.
// Compact layout: ● user prompt, assistant lines indented directly beneath,
// blank line between exchanges.
func (m *Model) rebuildChatLines(width int) {
	if width < 10 {
		width = 10
	}

	// Use engine history if available, else fall back to stored raw messages.
	var msgs []chat.Message
	if m.chatEngine != nil {
		msgs = m.chatEngine.History().Msgs
	} else {
		msgs = m.chatRawMsgs
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
		// Blank line after user prompt to separate it from the answer.
		lines = append(lines, chatLine{chatLineBlank, ""})
		prevHadContent = true
	}

	appendNote := func(content string) {
		if prevHadContent {
			lines = append(lines, chatLine{chatLineBlank, ""})
		}
		for _, rl := range strings.Split(content, "\n") {
			rl = strings.TrimRight(rl, " \t")
			if rl == "" {
				continue
			}
			for _, wl := range wordWrap(rl, contW) {
				lines = append(lines, chatLine{chatLineNote, "📌 " + wl})
			}
		}
		prevHadContent = true
	}

	for _, msg := range msgs {
		switch msg.Role {
		case chat.RoleUser:
			appendUser(msg.Content)
		case chat.RoleAssistant:
			mdLines := m.appendMarkdown(msg.Content, width)
			// Trim leading/trailing blank lines from rendered markdown.
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

	// Streaming buffer: render in-progress markdown (may be partial).
	if m.chatStreaming && m.chatStreamBuf != "" {
		streamLines := m.appendMarkdown(m.chatStreamBuf, width)
		lines = append(lines, streamLines...)
	}

	// Collapse consecutive blank lines into at most one.
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
	m.chatDisplayLines = compacted
}

// ── Rendering ───────────────────────────────────────────────────────────────

// colorChatLine applies color/bold to a plain-text chatLine.
// text must already be truncated to the desired display width before calling.
func colorChatLine(cl chatLine, t Theme) string {
	switch cl.role {
	case chatLineUser:
		return fgBold(t.ChatUser, cl.text)
	case chatLineAssistant:
		return fg(t.ChatAssistant, cl.text)
	case chatLineHeader:
		return fgBold(t.ChatHeader, cl.text)
	case chatLineQuote:
		return fg(t.ChatQuote, "│ ") + fg(t.ChatAssistant, cl.text)
	case chatLineCode:
		return fg(t.ChatCode, cl.text)
	case chatLineNote:
		return fg(t.ContentDimmed, cl.text)
	default:
		return ""
	}
}

// chatVirtualLines builds the virtual display list when in boxed mode
// (focus == paneContent). Each exchange is wrapped in a rounded box.
// Returns nil when not in boxed mode.
type chatVLine struct {
	isBoxTop    bool
	isBoxBottom bool
	isSep       bool // blank line between boxes
	contentIdx  int  // index into chatDisplayLines; -1 for non-content lines
}

func (m Model) buildChatVLines() []chatVLine {
	if !(m.focus == paneContent) {
		return nil
	}
	dl := m.chatDisplayLines
	n := len(dl)
	if n == 0 {
		return nil
	}

	// Find the start index of each exchange (each run of chatLineUser).
	var starts []int
	for i, cl := range dl {
		if cl.role == chatLineUser && (i == 0 || dl[i-1].role != chatLineUser) {
			starts = append(starts, i)
		}
	}
	if len(starts) == 0 {
		return nil
	}

	var vlines []chatVLine
	for e, start := range starts {
		var end int
		if e+1 < len(starts) {
			end = starts[e+1]
		} else {
			end = n
		}
		// Trim trailing blank lines from this exchange's content.
		trimEnd := end
		for trimEnd > start && dl[trimEnd-1].role == chatLineBlank {
			trimEnd--
		}

		vlines = append(vlines, chatVLine{isBoxTop: true, contentIdx: -1})
		for i := start; i < trimEnd; i++ {
			vlines = append(vlines, chatVLine{contentIdx: i})
		}
		vlines = append(vlines, chatVLine{isBoxBottom: true, contentIdx: -1})
		// Blank separator between boxes (not after the last one).
		if e < len(starts)-1 {
			vlines = append(vlines, chatVLine{isSep: true, contentIdx: -1})
		}
	}
	return vlines
}

// renderChatPane renders the chat conversation in the right content pane.
func (m Model) renderChatPane(height, width int) []string {
	t := ActiveTheme
	var lines []string

	// One-line header: workspace · model (or hint) · N msgs · N articles · rag mode
	msgCount := len(m.chatRawMsgs)
	if m.chatEngine != nil {
		msgCount = len(m.chatEngine.History().Msgs)
	}
	header := fgBold(t.ContentTitle, m.chatWorkspace)
	if m.chatEngine != nil {
		header += fg(t.ContentDimmed, "  ·  "+m.chatEngine.Profile().Model)
	} else {
		header += fg(t.ContentDimmed, "  ·  type to start")
	}
	header += fg(t.ContentDimmed, fmt.Sprintf("  ·  %d msgs", msgCount))
	header += fg(t.ContentDimmed, fmt.Sprintf("  ·  %d articles", m.chatArticleCount))
	if m.chatRagMode != "" {
		header += fg(t.ContentDimmed, "  ·  "+m.chatRagMode)
	}
	lines = append(lines, truncate(header, width-1))
	lines = append(lines, fg(t.Dimmed, strings.Repeat("─", width)))

	// Chat content area.
	chatH := height - 2 // header + separator
	if chatH < 1 {
		chatH = 1
	}

	if len(m.chatDisplayLines) == 0 && !m.chatStreaming {
		lines = append(lines, fg(t.ContentDimmed, "Type a message to start chatting."))
		for len(lines) < height {
			lines = append(lines, "")
		}
		return lines[:height]
	}

	// Boxed mode (focus == paneContent): wrap each exchange in a rounded box.
	// Layout: │ {content padded to innerW} │
	// innerW = width - 4  ("│ " = 2 left, " │" = 2 right)
	if vlines := m.buildChatVLines(); vlines != nil {
		innerW := width - 4
		if innerW < 4 {
			innerW = 4
		}
		topRule := fg(t.BoxBorder, "╭"+strings.Repeat("─", width-2)+"╮")
		botRule := fg(t.BoxBorder, "╰"+strings.Repeat("─", width-2)+"╯")
		borderL := fg(t.BoxBorder, "│ ")
		borderR := fg(t.BoxBorder, " │")

		total := len(vlines)
		start := m.chatScroll
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

		for _, vl := range vlines[start:end] {
			switch {
			case vl.isBoxTop:
				lines = append(lines, topRule)
			case vl.isBoxBottom:
				lines = append(lines, botRule)
			case vl.isSep:
				lines = append(lines, "")
			default:
				cl := m.chatDisplayLines[vl.contentIdx]
				// For blank lines just emit an empty padded row.
				if cl.role == chatLineBlank || cl.text == "" {
					lines = append(lines, borderL+strings.Repeat(" ", innerW)+borderR)
					continue
				}
				// colorChatLine for chatLineQuote prepends "│ " (2 visual cols),
				// so the text budget is innerW-2.
				budget := innerW
				if cl.role == chatLineQuote {
					budget = innerW - 2
					if budget < 2 {
						budget = 2
					}
				}
				text := cl.text
				// Use visual width (handles wide chars, emoji) for truncation/padding.
				visW := lipgloss.Width(text)
				if visW > budget {
					// Trim runes until visual width fits.
					runes := []rune(text)
					for len(runes) > 0 && lipgloss.Width(string(runes)) > budget-1 {
						runes = runes[:len(runes)-1]
					}
					text = string(runes) + "…"
				} else if visW < budget {
					text = text + strings.Repeat(" ", budget-visW)
				}
				colored := colorChatLine(chatLine{role: cl.role, text: text}, t)
				lines = append(lines, borderL+colored+borderR)
			}
		}

		for len(lines) < height {
			lines = append(lines, "")
		}
		return lines[:height]
	}

	// Flat mode (no boxes): plain scroll over chatDisplayLines.
	totalLines := len(m.chatDisplayLines)
	start := m.chatScroll
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
		lines = append(lines, colorChatLine(m.chatDisplayLines[i], t))
	}

	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines[:height]
}

// renderChatStatusLine renders the chat-mode three-section status bar.
func (m Model) renderChatStatusLine() string {
	t := ActiveTheme
	w := m.width

	// Left: streaming indicator or status message.
	var left string
	if m.chatStreaming {
		left = renderWaveIndicator(m.spinnerFrame, "streaming", t.StreamingText, t.Dimmed)
	} else if m.statusMsg != "" {
		left = fg(t.StatusText, " "+m.statusMsg)
	}

	// Center: per-turn stats (if available).
	var center string
	if m.chatLastUsage != nil {
		u := m.chatLastUsage
		center = fg(t.ContentDimmed, fmt.Sprintf("in:%d out:%d  %.1fs", u.InputTokens, u.OutputTokens, m.chatLastElapsed.Seconds()))
	}

	// Right: session stats.
	var right string
	if m.chatEngine != nil {
		inTok, outTok, cost := m.chatEngine.SessionStats()
		if inTok > 0 || outTok > 0 {
			right = fg(t.ContentDimmed, fmt.Sprintf("session: %d/%d  %s", inTok, outTok, formatUSD(cost)))
		}
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

// renderWaveIndicator creates an animated "wave" streaming indicator.
// A brightness peak sweeps across the label + trailing dots.
func renderWaveIndicator(frame int, label string, bright, dim lipgloss.Color) string {
	full := []rune(" " + label + " ●●●●●")
	n := len(full)
	if n == 0 {
		return ""
	}
	peak := frame % n

	var sb strings.Builder
	for i, r := range full {
		// Distance with wrapping.
		dist := i - peak
		if dist < 0 {
			dist = -dist
		}
		if dist > n/2 {
			dist = n - dist
		}
		t := 1.0 - float64(dist)/float64(n/2+1)
		if t < 0 {
			t = 0
		}
		col := lerpColor(dim, bright, t)
		sb.WriteString(fg(col, string(r)))
	}
	return sb.String()
}

// lerpColor linearly interpolates between two hex colors.
func lerpColor(c1, c2 lipgloss.Color, t float64) lipgloss.Color {
	r1, g1, b1, ok1 := hexToRGB(string(c1))
	r2, g2, b2, ok2 := hexToRGB(string(c2))
	if !ok1 || !ok2 {
		if t > 0.5 {
			return c2
		}
		return c1
	}
	lerp := func(a, b int64) int64 {
		return a + int64(float64(b-a)*t)
	}
	return lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", lerp(r1, r2), lerp(g1, g2), lerp(b1, b2)))
}

// ── Chat command dispatch ───────────────────────────────────────────────────

// dispatchChatCommand handles commands in chat mode.
// Returns a tea.Cmd if an async operation is needed.
func (m *Model) dispatchChatCommand(val string) tea.Cmd {
	parts := strings.Fields(val)
	if len(parts) == 0 {
		return nil
	}
	cmd := strings.ToLower(parts[0])

	switch cmd {
	case "/clear":
		if m.chatEngine != nil {
			if err := m.chatEngine.ClearHistory(); err != nil {
				m.statusMsg = "✗ " + err.Error()
			} else {
				m.chatDisplayLines = nil
				m.chatRawMsgs = nil
				m.chatScroll = 0
				m.chatStreamBuf = ""
				m.chatLastUsage = nil
				m.chatLastElapsed = 0
				m.statusMsg = "✓ conversation cleared"
			}
		}
		return nil

	case "/stats":
		if m.chatEngine != nil {
			inTok, outTok, cost := m.chatEngine.SessionStats()
			m.setStatusLines([]string{
				fmt.Sprintf("Session tokens: %d in / %d out", inTok, outTok),
				fmt.Sprintf("Estimated cost: %s", formatUSD(cost)),
				fmt.Sprintf("Profile: %s  ·  Model: %s", m.chatEngine.ProfileName(), m.chatEngine.Profile().Model),
			})
		}
		return nil

	case "/system":
		if m.chatEngine != nil {
			sys := m.chatEngine.SystemPrompt()
			if sys == "" {
				m.statusMsg = "(no system prompt)"
			} else {
				m.setStatusLines(strings.Split(sys, "\n"))
			}
		}
		return nil

	case "/meta":
		return m.chatShowMeta()

	case "/save":
		arg := ""
		if len(parts) > 1 {
			arg = parts[1]
		}
		return m.chatSave(arg)

	case "/help":
		var lines []string
		lines = append(lines, "Chat commands:")
		for _, c := range chatCommands {
			line := fmt.Sprintf("  %-14s %s  %s", c.cmd, c.arg, c.desc)
			lines = append(lines, line)
		}
		lines = append(lines, "")
		lines = append(lines, "Keys: ↑/↓ scroll · Esc focus input · Tab switch pane · q quit")
		m.setStatusLines(lines)
		return nil

	default:
		m.statusMsg = "✗ unknown chat command: " + cmd
		return nil
	}
}

// chatShowMeta displays workspace details in the status pane.
func (m *Model) chatShowMeta() tea.Cmd {
	if m.chatEngine == nil {
		return nil
	}
	// Find workspace info from workspaceItems.
	var ws *workspaceItem
	for i := range m.workspaceItems {
		if m.workspaceItems[i].name == m.chatWorkspace {
			ws = &m.workspaceItems[i]
			break
		}
	}

	var lines []string
	lines = append(lines, "Workspace: "+m.chatWorkspace)
	if ws != nil {
		if ws.description != "" {
			lines = append(lines, "Description: "+ws.description)
		}
		lines = append(lines, fmt.Sprintf("Articles: %d  ·  Collections: %d", ws.articleCount, ws.collectionCount))
		lines = append(lines, fmt.Sprintf("Resources: %d  ·  Outcomes: %d", ws.resourceCount, ws.outcomeCount))
		lines = append(lines, fmt.Sprintf("Status: %s  ·  Created: %s", ws.status, ws.createdAt.Format("2006-01-02")))
	}
	lines = append(lines, fmt.Sprintf("Profile: %s  ·  Model: %s", m.chatEngine.ProfileName(), m.chatEngine.Profile().Model))
	inTok, outTok, cost := m.chatEngine.SessionStats()
	lines = append(lines, fmt.Sprintf("Session: %d in / %d out  ·  Cost: %s", inTok, outTok, formatUSD(cost)))

	m.setStatusLines(lines)
	return nil
}

// chatSave saves the chat conversation to the workspace outcomes directory.
func (m *Model) chatSave(filename string) tea.Cmd {
	if m.chatEngine == nil {
		m.statusMsg = "✗ no active chat"
		return nil
	}
	if filename == "" {
		filename = fmt.Sprintf("chat-%s", time.Now().Format("2006-01-02-150405"))
	}

	hist := m.chatEngine.History()
	var sb strings.Builder
	sb.WriteString("# Chat: " + m.chatWorkspace + "\n\n")
	sb.WriteString("Profile: " + m.chatEngine.ProfileName() + "  ·  Model: " + m.chatEngine.Profile().Model + "\n\n")
	sb.WriteString("---\n\n")
	for _, msg := range hist.Msgs {
		switch msg.Role {
		case chat.RoleUser:
			sb.WriteString("**You:** " + msg.Content + "\n\n")
		case chat.RoleAssistant:
			sb.WriteString("**Assistant:** " + msg.Content + "\n\n")
		}
	}

	dataRoot := m.cfg.DataRoot
	wsName := m.chatWorkspace
	content := sb.String()
	fname := filename + ".md"
	return func() tea.Msg {
		err := storefs.WriteWorkspaceOutcome(dataRoot, wsName, fname, []byte(content))
		if err != nil {
			return cmdDoneMsg{err: err.Error()}
		}
		return cmdDoneMsg{statusMsg: "✓ saved to outcomes/" + fname}
	}
}

// exitChatMode returns to the workspace nav view.
func (m *Model) exitChatMode() {
	if m.chatCancelStream != nil {
		m.chatCancelStream()
		m.chatCancelStream = nil
	}
	m.chatMode = false
	m.chatEngine = nil
	m.chatWorkspace = ""
	m.chatDisplayLines = nil
	m.chatRawMsgs = nil
	m.chatScroll = 0
	m.chatStreaming = false
	m.chatStreamBuf = ""
	m.chatLastUsage = nil
	m.chatLastElapsed = 0
	m.chatPendingPrompt = ""
	m.chatArticleCount = 0
	m.chatRagMode = ""
	m.focus = paneNav
	m.statusMsg = ""
	m.statusLines = nil
}

// chatAutoScrollToBottom scrolls chat to the bottom if autoScroll is on.
func (m *Model) chatAutoScrollToBottom(viewH int) {
	if !m.chatAutoScroll {
		return
	}
	total := len(m.chatDisplayLines)
	if vlines := m.buildChatVLines(); vlines != nil {
		total = len(vlines)
	}
	maxScroll := total - viewH
	if maxScroll < 0 {
		maxScroll = 0
	}
	m.chatScroll = maxScroll
}

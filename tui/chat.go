package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

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
	ctx, cancel := context.WithCancel(context.Background())
	m.chatCancelStream = cancel
	m.chatStreaming = true
	m.chatStreamBuf = ""
	shared := &streamBuf{}
	m.chatSharedBuf = shared

	return func() tea.Msg {
		result, err := eng.Chat(ctx, prompt, chatengine.ChatOptions{}, func(delta string) error {
			shared.Append(delta)
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

// chatVLine is one line in the virtual boxed display (focus == paneContent).
type chatVLine struct {
	isBoxTop    bool
	isBoxBottom bool
	isSep       bool   // blank line between boxes
	isHeader    bool   // time · model + hints line, first line inside the selected box
	isEllipsis  bool   // collapsed indicator (… N more lines)
	metaText    string // left side of header: "time  model"
	contentIdx  int    // index into chatDisplayLines; -1 for non-content lines
	boxIdx      int    // which logical box (0-based) this line belongs to
	isSelected  bool   // true when boxIdx == chatBoxCursor
}

// chatBoxInfo holds per-box metadata derived from message history.
type chatBoxInfo struct {
	role     chatLineRole
	ts       string
	profile  string
	msgStart int // inclusive index into msgs
	msgEnd   int // exclusive index into msgs
}

// chatBoxInfos walks message history and returns one entry per logical box.
// A box is either a user→assistant exchange or a standalone note.
func (m *Model) chatBoxInfos() []chatBoxInfo {
	msgs := m.chatRawMsgs
	if m.chatEngine != nil {
		msgs = m.chatEngine.History().Msgs
	}
	var infos []chatBoxInfo
	for i, msg := range msgs {
		switch msg.Role {
		case chat.RoleUser:
			ts := ""
			if !msg.Time.IsZero() {
				ts = msg.Time.Format("Jan 2, 2006 · 15:04")
			}
			infos = append(infos, chatBoxInfo{role: chatLineUser, ts: ts, msgStart: i, msgEnd: i + 1})
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
		}
	}
	return infos
}

// chatBoxCount returns the number of logical boxes in the current display.
func (m *Model) chatBoxCount() int {
	dl := m.chatDisplayLines
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

// scrollToChatBox adjusts chatScroll so that box boxIdx is visible.
func (m *Model) scrollToChatBox(boxIdx, viewH int) {
	vlines := m.buildChatVLines()
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
	// Already fully visible.
	if first >= m.chatScroll && last < m.chatScroll+viewH {
		return
	}
	// Scroll to show the top of the box.
	m.chatScroll = first
	maxScroll := len(vlines) - viewH
	if maxScroll < 0 {
		maxScroll = 0
	}
	if m.chatScroll > maxScroll {
		m.chatScroll = maxScroll
	}
}

// buildChatVLines builds the virtual display list when in boxed mode
// (focus == paneContent). Only the selected box is wrapped in a border;
// all other exchanges render as plain text. This matches c2's behaviour.
// Returns nil when not in boxed mode.
func (m Model) buildChatVLines() []chatVLine {
	dl := m.chatDisplayLines
	n := len(dl)
	if n == 0 {
		return nil
	}

	// Detect box boundaries from display lines.
	type boxStart struct {
		idx  int
		role chatLineRole
	}
	var boxes []boxStart
	for i, cl := range dl {
		if cl.role == chatLineUser && (i == 0 || dl[i-1].role != chatLineUser) {
			boxes = append(boxes, boxStart{i, chatLineUser})
		} else if cl.role == chatLineNote && (i == 0 || dl[i-1].role != chatLineNote) {
			boxes = append(boxes, boxStart{i, chatLineNote})
		}
	}
	if len(boxes) == 0 {
		return nil
	}

	// Per-box metadata from message history.
	infos := m.chatBoxInfos()

	var vlines []chatVLine
	for e, box := range boxes {
		selected := e == m.chatBoxCursor && m.focus == paneContent && !m.selectionMode
		collapsed := m.chatCollapsed != nil && m.chatCollapsed[e]

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

		// Total content line count — used for the ellipsis "N more lines" label.
		totalContent := trimEnd - box.idx

		if selected {
			// Focused box: full border + header line with hints.
			vlines = append(vlines, chatVLine{isBoxTop: true, contentIdx: -1, boxIdx: e, isSelected: true})

			// Header line: "time  model" on left, hints on right.
			var leftParts []string
			if e < len(infos) {
				info := infos[e]
				if info.ts != "" {
					leftParts = append(leftParts, info.ts)
				}
				if info.profile != "" {
					leftParts = append(leftParts, info.profile)
				}
			}
			metaLeft := strings.Join(leftParts, "  ")
			vlines = append(vlines, chatVLine{isHeader: true, metaText: metaLeft, contentIdx: -1, boxIdx: e, isSelected: true})

			// Content lines (possibly collapsed).
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
			// Non-focused: plain content, no border.
			// Collapse state still applies — show only first 3 lines + ellipsis.
			if collapsed {
				limit := box.idx + 3
				if limit > trimEnd {
					limit = trimEnd
				}
				for i := box.idx; i < limit; i++ {
					vlines = append(vlines, chatVLine{contentIdx: i, boxIdx: e, isSelected: false})
				}
				if limit < trimEnd {
					remaining := totalContent - (limit - box.idx)
					vlines = append(vlines, chatVLine{
						isEllipsis: true,
						metaText:   fmt.Sprintf("... (%d more lines)", remaining),
						contentIdx: -1, boxIdx: e, isSelected: false,
					})
				}
			} else {
				for i := box.idx; i < trimEnd; i++ {
					vlines = append(vlines, chatVLine{contentIdx: i, boxIdx: e, isSelected: false})
				}
			}
		}

		if e < len(boxes)-1 {
			vlines = append(vlines, chatVLine{isSep: true, contentIdx: -1, boxIdx: e})
		}
	}
	return vlines
}

// collapseAllBoxes marks every logical box as collapsed except the last one.
func (m *Model) collapseAllBoxes() {
	n := m.chatBoxCount()
	m.chatCollapsed = make(map[int]bool, n)
	for i := 0; i < n-1; i++ {
		m.chatCollapsed[i] = true
	}
}

// cmdChatCollapseBox toggles the collapsed state of box at boxIdx.
func (m *Model) cmdChatCollapseBox(boxIdx int) {
	if m.chatCollapsed == nil {
		m.chatCollapsed = make(map[int]bool)
	}
	m.chatCollapsed[boxIdx] = !m.chatCollapsed[boxIdx]
}

// cmdChatDeleteBox removes the exchange at boxIdx from memory and persists.
func (m *Model) cmdChatDeleteBox(boxIdx int) tea.Cmd {
	infos := m.chatBoxInfos()
	if boxIdx < 0 || boxIdx >= len(infos) {
		m.setStatusError("nothing to delete")
		return nil
	}
	info := infos[boxIdx]

	// Remove from the active message source.
	deleteFromSlice := func(msgs []chat.Message) []chat.Message {
		start, end := info.msgStart, info.msgEnd
		if start < 0 || end > len(msgs) || start >= end {
			return msgs
		}
		out := make([]chat.Message, 0, len(msgs)-(end-start))
		out = append(out, msgs[:start]...)
		out = append(out, msgs[end:]...)
		return out
	}

	if m.chatEngine != nil {
		m.chatEngine.History().Msgs = deleteFromSlice(m.chatEngine.History().Msgs)
	} else {
		m.chatRawMsgs = deleteFromSlice(m.chatRawMsgs)
	}

	// Shift collapsed keys: entries above boxIdx stay, those below shift down by 1.
	if m.chatCollapsed != nil {
		newCollapsed := make(map[int]bool)
		for k, v := range m.chatCollapsed {
			if k < boxIdx {
				newCollapsed[k] = v
			} else if k > boxIdx {
				newCollapsed[k-1] = v
			}
		}
		m.chatCollapsed = newCollapsed
	}

	// Rebuild display.
	m.rebuildChatLines(m.chatBuildWidth())

	// Clamp cursor.
	numBoxes := m.chatBoxCount()
	if numBoxes == 0 {
		m.chatBoxCursor = 0
	} else if m.chatBoxCursor >= numBoxes {
		m.chatBoxCursor = numBoxes - 1
	}

	// Collect messages to save.
	cfg := m.cfg
	ws := m.chatWorkspace
	var toSave []chat.Message
	if m.chatEngine != nil {
		src := m.chatEngine.History().Msgs
		toSave = make([]chat.Message, len(src))
		copy(toSave, src)
	} else {
		toSave = make([]chat.Message, len(m.chatRawMsgs))
		copy(toSave, m.chatRawMsgs)
	}

	return func() tea.Msg {
		st := chat.NewChatStore(cfg.DataRoot, ws)
		h := &chat.History{Msgs: toSave}
		if err := st.SaveHistory(h); err != nil {
			return cmdDoneMsg{err: "delete: " + err.Error()}
		}
		return cmdDoneMsg{statusMsg: "✓ exchange deleted"}
	}
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
		if m.chatRagMode != "" {
			header += fg(t.ContentDimmed, "  ·  mode: "+m.chatRagMode)
		}
	}
	header += fg(t.ContentDimmed, fmt.Sprintf("  ·  %d msgs", msgCount))
	header += fg(t.ContentDimmed, fmt.Sprintf("  ·  %d articles", m.chatArticleCount))
	lines = append(lines, truncate(header, width-1))
	// Show workspace description if available.
	ws := m.selectedWorkspace()
	if ws != nil && ws.description != "" {
		lines = append(lines, fg(t.ContentDimmed, truncate(ws.description, width-1)))
	}
	lines = append(lines, fg(t.Dimmed, strings.Repeat("─", width)))

	// Chat content area.
	headerLines := len(lines) // header + optional description + separator
	chatH := height - headerLines
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

	// Boxed mode (focus == paneContent).
	// Only the selected box gets a border; all others render as plain text.
	// This matches c2's renderConversation behaviour.
	if vlines := m.buildChatVLines(); vlines != nil {
		// innerW = content width inside a box border ("│ " + content + " │").
		innerW := width - 4
		if innerW < 4 {
			innerW = 4
		}
		topRule := fg(t.BoxBorder, "╭"+strings.Repeat("─", width-2)+"╮")
		botRule := fg(t.BoxBorder, "╰"+strings.Repeat("─", width-2)+"╯")
		bL := fg(t.BoxBorder, "│ ")
		bR := fg(t.BoxBorder, " │")

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

		selPlain := m.selectionMode && m.selectionMaxPane == paneContent

		for _, vl := range vlines[start:end] {
			switch {
			case vl.isBoxTop:
				if selPlain {
					lines = append(lines, "")
				} else {
					lines = append(lines, topRule)
				}

			case vl.isBoxBottom:
				if selPlain {
					lines = append(lines, "")
				} else {
					lines = append(lines, botRule)
				}

			case vl.isSep:
				lines = append(lines, "")

			case vl.isHeader:
				if selPlain {
					// Plain header: just the metadata text, no border or hints.
					lines = append(lines, fg(t.ContentDimmed, vl.metaText))
				} else {
					// Header line inside the selected box:
					// left = "time  model", right = "v expand/collapse · s speak · x delete"
					// All in t.Dimmed — identical to c2.
					expandHint := "v expand"
					if m.chatCollapsed != nil && m.chatCollapsed[vl.boxIdx] {
						expandHint = "v collapse"
					}
					hintsStr := expandHint + " · s speak · x delete"
					left := vl.metaText
					leftW := lipgloss.Width(left)
					hintsW := lipgloss.Width(hintsStr)
					pad := innerW - leftW - hintsW
					if pad < 1 {
						pad = 1
					}
					headerContent := fg(t.ContentDimmed, left) +
						strings.Repeat(" ", pad) +
						fg(t.ContentDimmed, hintsStr)
					// Pad to full innerW if total is short.
					total := leftW + pad + hintsW
					if total < innerW {
						headerContent += strings.Repeat(" ", innerW-total)
					}
					lines = append(lines, bL+headerContent+bR)
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
					lines = append(lines, bL+text+bR)
				}

			default:
				cl := m.chatDisplayLines[vl.contentIdx]
				if selPlain || !vl.isSelected {
					// Plain text — no box border.
					lines = append(lines, colorChatLine(cl, t))
				} else {
					// Inside the box border: pad to innerW.
					if cl.role == chatLineBlank || cl.text == "" {
						lines = append(lines, bL+strings.Repeat(" ", innerW)+bR)
						continue
					}
					budget := innerW
					if cl.role == chatLineQuote {
						budget = innerW - 2
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
					colored := colorChatLine(chatLine{role: cl.role, text: text}, t)
					lines = append(lines, bL+colored+bR)
				}
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

	// Left: TTS indicator > streaming indicator > status message.
	var left string
	if m.ttsPlayer.Playing() {
		rate := m.cfg.TTSRate
		if rate <= 0 {
			rate = 200
		}
		label := fmt.Sprintf("♪ #%d  say  %d wpm  [ slower  ] faster", m.chatBoxCursor+1, rate)
		left = renderWaveIndicator(m.spinnerFrame, label, t.StreamingText, t.Dimmed)
	} else if m.chatStreaming {
		streamLabel := "streaming"
		if m.chatEngine != nil {
			streamLabel += " · " + m.chatEngine.Profile().Model
		}
		left = renderWaveIndicator(m.spinnerFrame, streamLabel, t.StreamingText, t.Dimmed)
	} else if m.statusMsg != "" {
		if m.statusErr {
			left = fgBold(t.StatusError, " "+m.statusMsg)
		} else {
			left = fg(t.StatusText, " "+m.statusMsg)
		}
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
	m.statusErr = false
	parts := strings.Fields(val)
	if len(parts) == 0 {
		return nil
	}
	cmd := strings.ToLower(parts[0])

	// Preserve original formatting (newlines, whitespace) in fullArg
	// by stripping the command prefix instead of re-joining Fields.
	fullArg := ""
	if idx := strings.Index(val, parts[0]); idx >= 0 {
		rest := val[idx+len(parts[0]):]
		if trimmed := strings.TrimLeft(rest, " "); trimmed != "" {
			fullArg = trimmed
		}
	}

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

	case "/reload":
		m.chatEngine = nil
		m.statusMsg = "✓ engine reset — will reinitialise on next message"
		return nil

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

	case "/resource-list":
		m.cmdResourceList()
		return nil

	case "/resource-add":
		return m.cmdResourceAdd(fullArg)

	case "/resource-mkdir":
		return m.cmdResourceMkdir(fullArg)

	case "/resource-remove", "/resource-delete":
		arg := ""
		if len(parts) > 1 {
			arg = parts[1]
		}
		m.cmdResourceRemove(arg)
		return nil

	case "/resource-view":
		arg := ""
		if len(parts) > 1 {
			arg = parts[1]
		}
		m.cmdResourceView(arg)
		return nil

	case "/resource-edit":
		arg := ""
		if len(parts) > 1 {
			arg = parts[1]
		}
		return m.cmdResourceEdit(arg)

	case "/resource-new":
		arg := ""
		if len(parts) > 1 {
			arg = parts[1]
		}
		return m.cmdResourceNew(arg)

	case "/resource-save":
		return m.chatSaveResource(fullArg)

	case "/scratch":
		global := parts[0] == "/Scratch"
		return m.cmdScratch(fullArg, global)

	case "/askx":
		global := parts[0] == "/AskX"
		return m.cmdAskX(fullArg, global)

	default:
		// Fall through to global command dispatcher so /log, /stats, /help etc. work.
		return m.dispatchCommand(val)
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
	msgs := m.chatMessages()
	if len(msgs) == 0 {
		m.setStatusError("no chat messages to save")
		return nil
	}
	if filename == "" {
		filename = fmt.Sprintf("chat-%s", time.Now().Format("2006-01-02-150405"))
	}

	dataRoot := m.cfg.DataRoot
	wsName := m.chatWorkspace
	content := m.formatChatMarkdown(msgs)
	fname := filename + ".md"
	return func() tea.Msg {
		err := storefs.WriteWorkspaceOutcome(dataRoot, wsName, fname, []byte(content))
		if err != nil {
			return cmdDoneMsg{err: err.Error()}
		}
		return cmdDoneMsg{statusMsg: "✓ saved to outcomes/" + fname, reloadWorkspaces: true}
	}
}

// chatSaveResource saves the current chat session as a resource file.
func (m *Model) chatSaveResource(filename string) tea.Cmd {
	msgs := m.chatMessages()
	if len(msgs) == 0 {
		m.setStatusError("no chat messages to save")
		return nil
	}
	if filename == "" {
		filename = fmt.Sprintf("chat-%s", time.Now().Format("2006-01-02-150405"))
	}

	dataRoot := m.cfg.DataRoot
	wsName := m.chatWorkspace
	content := m.formatChatMarkdown(msgs)
	fname := filename + ".md"
	return func() tea.Msg {
		err := storefs.WriteWorkspaceResource(dataRoot, wsName, fname, []byte(content))
		if err != nil {
			return cmdDoneMsg{err: err.Error()}
		}
		return cmdDoneMsg{statusMsg: "✓ saved to resources/" + fname, reloadWorkspaces: true}
	}
}

// chatMessages returns the current chat messages, preferring the engine history
// if available, falling back to chatRawMsgs.
func (m *Model) chatMessages() []chat.Message {
	if m.chatEngine != nil {
		return m.chatEngine.History().Msgs
	}
	return m.chatRawMsgs
}

// formatChatMarkdown renders chat messages as a markdown document.
func (m *Model) formatChatMarkdown(msgs []chat.Message) string {
	var sb strings.Builder
	sb.WriteString("# Chat: " + m.chatWorkspace + "\n\n")
	if m.chatEngine != nil {
		sb.WriteString("Profile: " + m.chatEngine.ProfileName() + "  ·  Model: " + m.chatEngine.Profile().Model + "\n\n")
	}
	sb.WriteString("---\n\n")
	for _, msg := range msgs {
		switch msg.Role {
		case chat.RoleUser:
			sb.WriteString("**You:** " + msg.Content + "\n\n")
		case chat.RoleAssistant:
			sb.WriteString("**Assistant:** " + msg.Content + "\n\n")
		}
	}
	return sb.String()
}

// addChatNote stores text as a RoleNote message — visible in chat history
// but never sent to the LLM. Triggered by the "//" prefix in the input.
func (m *Model) addChatNote(text string) tea.Cmd {
	if text == "" {
		m.setStatusError("comment cannot be empty — use //your note text")
		return nil
	}
	ws := m.chatWorkspace
	cfg := m.cfg

	// Append to in-memory raw msgs for display (engine may not be init yet).
	m.chatRawMsgs = append(m.chatRawMsgs, chat.Message{
		Role:    chat.RoleNote,
		Content: text,
		Time:    time.Now(),
	})
	// If engine is live, keep its history in sync too.
	if m.chatEngine != nil {
		m.chatEngine.History().Append(chat.RoleNote, text)
	}
	rightW := m.chatBuildWidth()
	m.rebuildChatLines(rightW)
	chatViewH := m.height - 6 - m.completionCount() - 2
	m.chatAutoScrollToBottom(chatViewH)

	// Persist to history.json asynchronously.
	// Load current history from disk and append the note, so we don't
	// overwrite turns that were added by the engine after it was initialised.
	noteText := text
	return func() tea.Msg {
		st := chat.NewChatStore(cfg.DataRoot, ws)
		h, err := st.LoadHistory()
		if err != nil {
			return cmdDoneMsg{err: "note: " + err.Error()}
		}
		h.Append(chat.RoleNote, noteText)
		if err := st.SaveHistory(h); err != nil {
			return cmdDoneMsg{err: "note: " + err.Error()}
		}
		return cmdDoneMsg{}
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
	m.chatSharedBuf = nil
	m.chatLastUsage = nil
	m.chatLastElapsed = 0
	m.chatPendingPrompt = ""
	m.chatArticleCount = 0
	m.chatRagMode = ""
	m.chatBoxCursor = 0
	m.chatCollapsed = nil
	m.closeScratch()
	m.closeAskX()
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

// ── Resource overlay ────────────────────────────────────────────────────────

// resourceReloadMsg fires after an external editor exits for a resource file.
type resourceReloadMsg struct{ name string }

// openResourceOverlay opens the resource file overlay for the named file.
func (m *Model) openResourceOverlay(name, text string) {
	m.resourcePreFocus = m.focus
	m.resourceLines = strings.Split(text, "\n")
	m.resourceName = name
	m.resourceCursor = 0
	m.resourceScroll = 0
	m.focus = paneResource
}

// closeResourceOverlay tears down the resource overlay and restores previous focus.
func (m *Model) closeResourceOverlay() {
	m.stopTTS()
	m.focus = m.resourcePreFocus
	m.resourceLines = nil
	m.resourceName = ""
	m.resourceCursor = 0
	m.resourceScroll = 0
}

// scrollResourceToCursor adjusts resourceScroll so the cursor line is visible.
func (m *Model) scrollResourceToCursor(viewH int) {
	if m.resourceCursor < m.resourceScroll {
		m.resourceScroll = m.resourceCursor
	} else if m.resourceCursor >= m.resourceScroll+viewH {
		m.resourceScroll = m.resourceCursor - viewH + 1
	}
}

// cmdResourceList lists resources for the current workspace.
func (m *Model) cmdResourceList() {
	resources, err := storefs.ListWorkspaceResources(m.cfg.DataRoot, m.chatWorkspace)
	if err != nil {
		m.setStatusError("resource-list: " + err.Error())
		return
	}
	if len(resources) == 0 {
		m.setStatusLines([]string{fmt.Sprintf("(no resources for workspace %q)", m.chatWorkspace)})
		return
	}
	lines := []string{fmt.Sprintf("resources for workspace %q:", m.chatWorkspace)}
	for _, r := range resources {
		if r.IsDir {
			lines = append(lines, fmt.Sprintf("  %-32s  %8s", r.Name+"/", "dir"))
			continue
		}
		var sizeStr string
		switch {
		case r.Size >= 1024*1024:
			sizeStr = fmt.Sprintf("%.1f MB", float64(r.Size)/1024/1024)
		case r.Size >= 1024:
			sizeStr = fmt.Sprintf("%.1f KB", float64(r.Size)/1024)
		default:
			sizeStr = fmt.Sprintf("%d B", r.Size)
		}
		lines = append(lines, fmt.Sprintf("  %-32s  %8s", r.Name, sizeStr))
	}
	m.setStatusLines(lines)
}

// cmdResourceAdd copies a local file or directory into workspace/resources/.
// Supports --into <dir> to place the resource inside a subdirectory.
func (m *Model) cmdResourceAdd(rawArgs string) tea.Cmd {
	if rawArgs == "" {
		m.setStatusError("usage: /resource-add <file-or-dir> [--into <dir>]")
		return nil
	}
	// Parse --into flag from args.
	path, into := parseIntoFlag(rawArgs)
	if path == "" {
		m.setStatusError("usage: /resource-add <file-or-dir> [--into <dir>]")
		return nil
	}
	ws := m.chatWorkspace
	cfg := m.cfg
	return func() tea.Msg {
		// Expand ~ before stat.
		expanded := path
		if strings.HasPrefix(expanded, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				expanded = filepath.Join(home, expanded[2:])
			}
		}

		// Glob expansion: if path contains wildcard characters, expand and add each match.
		if strings.ContainsAny(expanded, "*?[") {
			matches, err := filepath.Glob(expanded)
			if err != nil {
				return cmdDoneMsg{err: "resource-add: invalid glob pattern: " + err.Error()}
			}
			if len(matches) == 0 {
				return cmdDoneMsg{err: "resource-add: no files match " + path}
			}
			var names []string
			for _, match := range matches {
				info, err := os.Stat(match)
				if err != nil {
					return cmdDoneMsg{err: "resource-add: " + err.Error()}
				}
				var name string
				if info.IsDir() {
					name, err = storefs.AddDirResource(cfg.DataRoot, ws, match, into)
				} else {
					name, err = storefs.AddFileResource(cfg.DataRoot, ws, match, into)
				}
				if err != nil {
					return cmdDoneMsg{err: "resource-add: " + err.Error()}
				}
				names = append(names, name)
			}
			return cmdDoneMsg{statusMsg: fmt.Sprintf("✓ %d resources added to workspace %q: %s", len(names), ws, strings.Join(names, ", ")), reloadWorkspaces: true}
		}

		info, err := os.Stat(expanded)
		if err != nil {
			return cmdDoneMsg{err: "resource-add: " + err.Error()}
		}
		var name string
		if info.IsDir() {
			name, err = storefs.AddDirResource(cfg.DataRoot, ws, path, into)
		} else {
			name, err = storefs.AddFileResource(cfg.DataRoot, ws, path, into)
		}
		if err != nil {
			return cmdDoneMsg{err: "resource-add: " + err.Error()}
		}
		return cmdDoneMsg{statusMsg: fmt.Sprintf("✓ resource %q added to workspace %q", name, ws), reloadWorkspaces: true}
	}
}

// parseIntoFlag extracts --into <dir> from a raw argument string.
// Returns the remaining path and the into value (empty if not specified).
func parseIntoFlag(raw string) (path, into string) {
	parts := strings.Fields(raw)
	var pathParts []string
	for i := 0; i < len(parts); i++ {
		if parts[i] == "--into" && i+1 < len(parts) {
			into = parts[i+1]
			i++ // skip value
		} else {
			pathParts = append(pathParts, parts[i])
		}
	}
	return strings.Join(pathParts, " "), into
}

// cmdResourceMkdir creates a directory inside workspace/resources/.
func (m *Model) cmdResourceMkdir(name string) tea.Cmd {
	if name == "" {
		m.setStatusError("usage: /resource-mkdir <name>")
		return nil
	}
	ws := m.chatWorkspace
	cfg := m.cfg
	return func() tea.Msg {
		if err := storefs.MkdirWorkspaceResource(cfg.DataRoot, ws, name); err != nil {
			return cmdDoneMsg{err: "resource-mkdir: " + err.Error()}
		}
		return cmdDoneMsg{statusMsg: fmt.Sprintf("✓ created resource folder %q in workspace %q", name, ws), reloadWorkspaces: true}
	}
}

// cmdResourceRemove removes a resource file or directory from workspace/resources/ with confirmation.
func (m *Model) cmdResourceRemove(name string) {
	if name == "" {
		m.setStatusError("usage: /resource-remove <name>")
		return
	}
	ws := m.chatWorkspace
	cfg := m.cfg
	// Check if it's a directory for a clearer confirmation prompt.
	resPath := filepath.Join(storefs.WorkspaceDir(cfg.DataRoot, ws), "resources", name)
	prompt := fmt.Sprintf("delete resource %q? (yes/N)", name)
	if info, err := os.Stat(resPath); err == nil && info.IsDir() {
		prompt = fmt.Sprintf("delete resource folder %q and all its contents? (yes/N)", name)
	}
	m.askConfirm(prompt, func() tea.Cmd {
		return func() tea.Msg {
			if err := storefs.RemoveWorkspaceResource(cfg.DataRoot, ws, name); err != nil {
				return cmdDoneMsg{err: "resource-remove: " + err.Error()}
			}
			return cmdDoneMsg{statusMsg: fmt.Sprintf("✓ resource %q removed from workspace %q", name, ws), reloadWorkspaces: true}
		}
	})
}

// cmdUnlinkArticle removes an article from a workspace collection or the workspace itself.
func (m *Model) cmdUnlinkArticle(row *wsRow) {
	ws := m.workspaceItems[row.wsIdx]
	cfg := m.cfg
	slug := row.slug
	title := row.title
	if title == "" {
		title = slug
	}

	if row.colSlug != "" {
		col := row.colSlug
		m.askConfirm(fmt.Sprintf("unlink %q from collection %q? (yes/N)", title, col), func() tea.Cmd {
			return func() tea.Msg {
				if err := storefs.RemoveArticleFromCollection(cfg.DataRoot, col, slug); err != nil {
					return cmdDoneMsg{err: "unlink: " + err.Error()}
				}
				return cmdDoneMsg{statusMsg: fmt.Sprintf("✓ unlinked %q from collection %q", title, col), reloadWorkspaces: true}
			}
		})
	} else {
		wsName := ws.name
		m.askConfirm(fmt.Sprintf("unlink %q from workspace %q? (yes/N)", title, wsName), func() tea.Cmd {
			return func() tea.Msg {
				if err := storefs.RemoveArticleFromWorkspace(cfg.DataRoot, wsName, slug); err != nil {
					return cmdDoneMsg{err: "unlink: " + err.Error()}
				}
				return cmdDoneMsg{statusMsg: fmt.Sprintf("✓ unlinked %q from workspace %q", title, wsName), reloadWorkspaces: true}
			}
		})
	}
}

// cmdUnlinkCollection removes a collection from a workspace.
func (m *Model) cmdUnlinkCollection(row *wsRow) {
	ws := m.workspaceItems[row.wsIdx]
	cfg := m.cfg
	colSlug := row.colSlug
	wsName := ws.name

	m.askConfirm(fmt.Sprintf("unlink collection %q from workspace %q? (yes/N)", colSlug, wsName), func() tea.Cmd {
		return func() tea.Msg {
			if err := storefs.RemoveCollectionFromWorkspace(cfg.DataRoot, wsName, colSlug); err != nil {
				return cmdDoneMsg{err: "unlink: " + err.Error()}
			}
			return cmdDoneMsg{statusMsg: fmt.Sprintf("✓ unlinked collection %q from workspace %q", colSlug, wsName), reloadWorkspaces: true}
		}
	})
}

// cmdOutcomeRemove removes an outcome from workspace/outcomes/ with confirmation.
func (m *Model) cmdOutcomeRemove(name string) {
	if name == "" {
		m.setStatusError("usage: delete outcome — select an outcome first")
		return
	}
	ws := m.chatWorkspace
	cfg := m.cfg
	m.askConfirm(fmt.Sprintf("delete outcome %q? (yes/N)", name), func() tea.Cmd {
		return func() tea.Msg {
			if err := storefs.RemoveWorkspaceOutcome(cfg.DataRoot, ws, name); err != nil {
				return cmdDoneMsg{err: "outcome-remove: " + err.Error()}
			}
			return cmdDoneMsg{statusMsg: fmt.Sprintf("✓ outcome %q removed from workspace %q", name, ws), reloadWorkspaces: true}
		}
	})
}

// cmdResourceView opens a resource file in the text overlay.
func (m *Model) cmdResourceView(name string) {
	if name == "" {
		m.setStatusError("usage: /resource-view <name>")
		return
	}
	dir := filepath.Join(storefs.WorkspaceDir(m.cfg.DataRoot, m.chatWorkspace), "resources")
	filePath := filepath.Join(dir, name)
	data, err := os.ReadFile(filePath)
	if err != nil {
		m.setStatusError(fmt.Sprintf("resource %q not found", name))
		return
	}
	// Binary check.
	check := data
	if len(check) > 512 {
		check = check[:512]
	}
	if !utf8.Valid(check) {
		m.setStatusError(fmt.Sprintf("%q is not a text file", name))
		return
	}
	const maxBytes = 200 * 1024
	if len(data) > maxBytes {
		data = append(data[:maxBytes], []byte("\n[file truncated at 200 KB]")...)
	}
	m.openResourceOverlay(name, string(data))
}

// cmdResourceEdit opens a resource file in $EDITOR in a separate terminal window.
func (m *Model) cmdResourceEdit(name string) tea.Cmd {
	if name == "" {
		m.setStatusError("usage: /resource-edit <name>")
		return nil
	}
	editor := os.Getenv("EDITOR")
	if editor == "" {
		m.setStatusError("$EDITOR is not set — add 'export EDITOR=<path>' to your shell config")
		return nil
	}
	dir := filepath.Join(storefs.WorkspaceDir(m.cfg.DataRoot, m.chatWorkspace), "resources")
	filePath := filepath.Join(dir, name)
	if _, err := os.Stat(filePath); err != nil {
		m.setStatusError(fmt.Sprintf("resource %q not found", name))
		return nil
	}
	m.openEditorInTerminal(editor, filePath, name)
	return nil
}

// cmdResourceNew creates a new resource file and opens it in $EDITOR in a separate terminal window.
func (m *Model) cmdResourceNew(name string) tea.Cmd {
	if name == "" {
		m.setStatusError("usage: /resource-new <name>")
		return nil
	}
	editor := os.Getenv("EDITOR")
	if editor == "" {
		m.setStatusError("$EDITOR is not set — add 'export EDITOR=<path>' to your shell config")
		return nil
	}
	dir := filepath.Join(storefs.WorkspaceDir(m.cfg.DataRoot, m.chatWorkspace), "resources")
	filePath := filepath.Join(dir, name)
	if _, err := os.Stat(filePath); err == nil {
		m.setStatusError(fmt.Sprintf("resource %q already exists — use /resource-edit", name))
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		m.setStatusError("resource-new: " + err.Error())
		return nil
	}
	if err := os.WriteFile(filePath, nil, 0o644); err != nil {
		m.setStatusError("resource-new: " + err.Error())
		return nil
	}
	m.openEditorInTerminal(editor, filePath, name)
	return nil
}

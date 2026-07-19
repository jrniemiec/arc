package tui

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	storefs "github.com/jrniemiec/arc/store/fs"
)

// ── ANSI helpers ──────────────────────────────────────────────────────────────

// fg wraps text in a truecolor (or ANSI-256) foreground sequence and resets.
func fg(col lipgloss.Color, text string) string {
	s := string(col)
	if r, g, b, ok := hexToRGB(s); ok {
		return fmt.Sprintf("\033[38;2;%d;%d;%dm%s\033[0m", r, g, b, text)
	}
	if n, err := strconv.Atoi(s); err == nil {
		return fmt.Sprintf("\033[38;5;%dm%s\033[0m", n, text)
	}
	return text
}

// fgBold wraps text in bold + truecolor foreground.
func fgBold(col lipgloss.Color, text string) string {
	s := string(col)
	if r, g, b, ok := hexToRGB(s); ok {
		return fmt.Sprintf("\033[1;38;2;%d;%d;%dm%s\033[0m", r, g, b, text)
	}
	if n, err := strconv.Atoi(s); err == nil {
		return fmt.Sprintf("\033[1;38;5;%dm%s\033[0m", n, text)
	}
	return "\033[1m" + text + "\033[0m"
}

// fgFaint wraps text in faint + color.
func fgFaint(col lipgloss.Color, text string) string {
	s := string(col)
	if r, g, b, ok := hexToRGB(s); ok {
		return fmt.Sprintf("\033[2;38;2;%d;%d;%dm%s\033[0m", r, g, b, text)
	}
	if n, err := strconv.Atoi(s); err == nil {
		return fmt.Sprintf("\033[2;38;5;%dm%s\033[0m", n, text)
	}
	return "\033[2m" + text + "\033[0m"
}

// fgLines applies fg() to each line individually so viewport line-splitting
// does not break multi-line colored strings.
func fgLines(col lipgloss.Color, text string) string {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = fg(col, l)
	}
	return strings.Join(lines, "\n")
}

// reverse wraps text in reverse-video (swaps fg/bg). Used for selected items.
func reverse(text string) string {
	return "\033[7m" + text + "\033[0m"
}

// hexToRGB parses a "#RRGGBB" color string.
func hexToRGB(hex string) (r, g, b int64, ok bool) {
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) != 6 {
		return 0, 0, 0, false
	}
	rv, err1 := strconv.ParseInt(hex[0:2], 16, 64)
	gv, err2 := strconv.ParseInt(hex[2:4], 16, 64)
	bv, err3 := strconv.ParseInt(hex[4:6], 16, 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, 0, 0, false
	}
	return rv, gv, bv, true
}

// sep renders a full-width horizontal separator line.
func sep(width int) string {
	return fg(ActiveTheme.Dimmed, strings.Repeat("─", width))
}

// renderSplitSep renders a horizontal separator split at the nav/content divider.
// isTop=true uses ┬, isTop=false uses ┴. The active pane's portion is accent-colored.
func (m Model) renderSplitSep(width int, isTop bool) string {
	t := ActiveTheme

	// Shell mode: bottom separator (above input) goes bright red.
	if !isTop && strings.HasPrefix(m.input.Value(), "!") {
		return shellBorderColor + strings.Repeat("─", width) + "\033[0m"
	}

	// Selection mode with maximized pane: plain full-width separator, no junction.
	if m.selectionMode && m.selectionMaxPane != 0 {
		color := t.Accent
		return fg(color, strings.Repeat("─", width))
	}

	navW := m.navWidth()
	rightW := width - navW - 1
	if rightW < 0 {
		rightW = 0
	}

	junction := "┬"
	if !isTop {
		junction = "┴"
	}

	var navColor, contentColor, junctionColor lipgloss.Color
	switch m.focus {
	case paneTabBar:
		if isTop {
			// Top separator is the border below the tab bar — highlight full line.
			navColor = t.Accent
			contentColor = t.Accent
			junctionColor = t.Accent
		} else {
			navColor = t.Dimmed
			contentColor = t.Dimmed
			junctionColor = t.Dimmed
		}
	case paneNav:
		navColor = t.Accent
		contentColor = t.Dimmed
		junctionColor = t.Accent
	case paneContent:
		navColor = t.Dimmed
		contentColor = t.Accent
		junctionColor = t.Accent
	case paneCommand:
		if !isTop {
			// bottom sep = top border of the command pane
			navColor = t.Accent
			contentColor = t.Accent
			junctionColor = t.Accent
		} else {
			navColor = t.Dimmed
			contentColor = t.Dimmed
			junctionColor = t.Dimmed
		}
	default:
		navColor = t.Dimmed
		contentColor = t.Dimmed
		junctionColor = t.Dimmed
	}

	return fg(navColor, strings.Repeat("─", navW)) +
		fg(junctionColor, junction) +
		fg(contentColor, strings.Repeat("─", rightW))
}

// oneLine collapses all whitespace/control characters to spaces and trims.
// Prevents embedded newlines from breaking the fixed-line layout.
func oneLine(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' {
			b.WriteRune(' ')
		} else if r < 32 || r == 127 {
			// skip other control characters
		} else {
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

// truncate cuts s to maxWidth visible chars (no ANSI in s assumed).
func truncate(s string, maxWidth int) string {
	if lipgloss.Width(s) <= maxWidth {
		return s
	}
	runes := []rune(s)
	for len(runes) > 0 && lipgloss.Width(string(runes)) > maxWidth-1 {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "…"
}

// ── Layout constants ──────────────────────────────────────────────────────────

// topBarHeight is used by update.go for navPaneHeight calculation.
const topBarHeight = 2   // tab bar + separator
const hintBarHeight = 1  // status bar (bottom line)
const statusSepHeight = 1 // separator between command input and status bar
const cmdBarHeight = 1   // command input line

// navWidth returns the left nav pane width.
// Uses user-set override when dragged; otherwise ~28% of terminal, clamped to [20, 60].
func (m Model) navWidth() int {
	if m.navWidthOverride > 0 {
		return m.navWidthOverride
	}
	w := m.width / 4
	if w < 20 {
		w = 20
	}
	return w
}

// dividerCol returns the terminal column index (0-based) of the │ divider.
func (m Model) dividerCol() int {
	return m.navWidth()
}

// ── View ──────────────────────────────────────────────────────────────────────

// View implements tea.Model.
// Builds an explicit slice of exactly m.height lines and joins with "\n".
// This guarantees bubbletea never receives more lines than the terminal height,
// which would cause scrolling and make the top bar disappear.
func (m Model) View() string {
	if m.width == 0 || m.height < 6 {
		return ""
	}

	// Resource overlay takes over the full screen.
	if m.focus == paneResource {
		return m.renderResourceOverlay()
	}

	// Fixed rows: top bar (2) + split sep (1) + detail lines (N) + cmd (N) + status sep (1) + completions (N) + status bar (1) = 5+inputH+N
	compLines := m.renderCompletionLines()
	editDetailLines := m.reviewDetailLines()
	inputH := m.inputVisualHeight()
	fixedRows := 5 + len(editDetailLines) + inputH + len(compLines)
	mainHeight := m.height - fixedRows
	if mainHeight < 1 {
		mainHeight = 1
	}

	// Build each section into a []string of exactly the right line count.
	topLines := []string{m.renderTabBar(), m.renderSplitSep(m.width, true)}
	mainLines := strings.Split(m.renderMainArea(mainHeight), "\n")
	cmdInput := m.renderCommandInput()
	botLines := make([]string, 0, 4+len(editDetailLines)+inputH+len(compLines))
	botLines = append(botLines, m.renderSplitSep(m.width, false))
	botLines = append(botLines, editDetailLines...)
	botLines = append(botLines, strings.Split(cmdInput, "\n")...)
	botLines = append(botLines, m.renderStatusSep())
	botLines = append(botLines, compLines...)
	botLines = append(botLines, m.renderStatusLine())

	// Assemble exactly m.height lines — clamp/pad each section defensively.
	out := make([]string, 0, m.height)
	out = append(out, topLines...)
	for i := 0; i < mainHeight; i++ {
		if i < len(mainLines) {
			out = append(out, mainLines[i])
		} else {
			out = append(out, "")
		}
	}
	out = append(out, botLines...)

	// Safety: clamp to exactly m.height so a buggy sub-renderer can't overflow.
	if len(out) > m.height {
		out = out[:m.height]
	}
	for len(out) < m.height {
		out = append(out, "")
	}

	return strings.Join(out, "\n")
}

// renderTabBar renders the top tab bar line with cost summary right-aligned.
func (m Model) renderTabBar() string {
	t := ActiveTheme
	var parts []string
	for i := tab(0); i < tabCount; i++ {
		label := i.String()
		if i == m.activeTab {
			if m.focus == paneTabBar {
				parts = append(parts, fgBold(t.Accent, "["+label+"]"))
			} else {
				parts = append(parts, fgBold(t.TabActive, "["+label+"]"))
			}
		} else {
			parts = append(parts, fg(t.TabInactive, " "+label+" "))
		}
		if int(i) < int(tabCount)-1 {
			parts = append(parts, fg(t.Dimmed, "  "))
		}
	}
	left := strings.Join(parts, "")
	leftW := lipgloss.Width(left)

	// Cost summary — only shown when there's been spend.
	if m.statsLoaded && m.stats.CostTotal > 0 {
		costStr := fmt.Sprintf("Cost: today %s · 7d %s · 30d %s · ∑ %s ",
			formatUSD(m.stats.CostToday),
			formatUSD(m.stats.CostThisWeek),
			formatUSD(m.stats.CostThisMonth),
			formatUSD(m.stats.CostTotal),
		)
		costRendered := fg(t.ContentDimmed, costStr)
		costW := lipgloss.Width(costStr)
		pad := m.width - leftW - costW
		if pad > 0 {
			return left + strings.Repeat(" ", pad) + costRendered
		}
	}
	return left
}

// tabBarHitTest returns the tab index at column x in the tab bar, or -1 if none.
// Layout mirrors renderTabBar: each tab is "[Label]" or " Label ", separated by "  ".
func tabBarHitTest(x int) tab {
	col := 0
	for i := tab(0); i < tabCount; i++ {
		label := i.String()
		width := len(label) + 2 // " Label " or "[Label]"
		if x >= col && x < col+width {
			return i
		}
		col += width
		if int(i) < int(tabCount)-1 {
			col += 2 // separator "  "
		}
	}
	return -1
}

// renderMainArea renders the split left/right pane for the current tab.
func (m Model) renderMainArea(height int) string {
	// Selection mode with maximized pane: render only the focused pane at full width.
	if m.selectionMode && m.selectionMaxPane != 0 {
		var lines []string
		switch m.selectionMaxPane {
		case paneNav:
			lines = m.renderNavPane(height)
		case paneContent:
			lines = m.renderContentPane(height, m.width)
		}
		var sb strings.Builder
		for i := 0; i < height; i++ {
			if i < len(lines) {
				sb.WriteString(lines[i])
			}
			if i < height-1 {
				sb.WriteByte('\n')
			}
		}
		return sb.String()
	}

	t := ActiveTheme
	navW := m.navWidth()
	rightWidth := m.width - navW - 1 // 1 for the vertical divider
	if rightWidth < 10 {
		rightWidth = 10
	}

	leftLines := m.renderNavPane(height)
	rightLines := m.renderContentPane(height, rightWidth)

	// Divider color reflects active pane: accent when nav or content is focused.
	var dividerColor lipgloss.Color
	switch m.focus {
	case paneNav, paneContent:
		dividerColor = t.Accent
	default:
		dividerColor = t.Dimmed
	}
	divider := fg(dividerColor, "│")

	var sb strings.Builder
	for i := 0; i < height; i++ {
		var l, r string
		if i < len(leftLines) {
			l = leftLines[i]
		}
		if i < len(rightLines) {
			r = rightLines[i]
		}
		// Pad left pane to fixed width
		l = padRight(l, navW)
		sb.WriteString(l)
		sb.WriteString(divider)
		sb.WriteString(r)
		if i < height-1 {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

// renderNavPane returns lines for the left navigator pane.
func (m Model) renderNavPane(height int) []string {
	t := ActiveTheme

	var lines []string

	switch m.activeTab {
	case tabLibrary:
		// Sub-tab bar + blank separator, then list content.
		lines = append(lines, m.renderNavSubTabBar())
		lines = append(lines, "")
		switch m.navSubTab {
		case navSubTabArticles:
			lines = append(lines, m.renderNavLibrary(height-2)...)
		case navSubTabCollections:
			lines = append(lines, m.renderNavCollections(height-2)...)
		case navSubTabWorkspaces:
			lines = append(lines, m.renderNavWorkspaces(height-2)...)
		}
	case tabAgent:
		// Sub-tab bar + blank separator, then per-sub-tab list.
		lines = append(lines, m.renderAgentNavSubTabBar())
		lines = append(lines, "")
		switch m.agentSubTab {
		case agentSubTabRuns, agentSubTabDecisions:
			lines = append(lines, m.renderNavAgentRuns(height-2)...)
		case agentSubTabFeeds:
			lines = append(lines, fg(t.NavDimmed, "  (no feeds)"))
		}
	default:
		// Other tabs keep a single label header.
		var headerLabel string
		switch m.activeTab {
		case tabStats:
			headerLabel = "Overview"
		default:
			headerLabel = m.activeTab.String()
		}
		if m.focus == paneNav {
			lines = append(lines, fgBold(t.NavGroup, headerLabel))
		} else {
			lines = append(lines, fg(t.NavDimmed, headerLabel))
		}
		if m.activeTab == tabStats {
			lines = append(lines, fg(t.NavDimmed, "(stats)"))
		}
	}

	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines[:height]
}

// renderScratchPane renders the scratch split pane content.
func (m Model) renderScratchPane(height, width int) []string {
	t := ActiveTheme
	var lines []string

	// Header separator with label.
	label := " Scratch "
	ws := m.scratchWorkspace()
	if ws != "" {
		label = " Scratch [" + ws + "] "
	}
	hints := " V view · E edit "
	sepLen := width - len([]rune(label)) - len([]rune(hints))
	if sepLen < 0 {
		sepLen = 0
	}
	leftSep := sepLen / 2
	rightSep := sepLen - leftSep
	headerColor := t.Dimmed
	if m.scratchFocused && m.focus == paneContent {
		headerColor = t.Accent
	}
	header := fg(headerColor, strings.Repeat("─", leftSep)+label+strings.Repeat("─", rightSep)+hints)
	lines = append(lines, header)

	viewH := height - 1 // minus header
	if viewH < 1 {
		viewH = 1
	}

	// Boxed mode: when scratch is focused, render with box borders around selected block.
	if vlines := m.buildScratchVLines(); vlines != nil {
		innerW := width - 4
		if innerW < 4 {
			innerW = 4
		}
		topRule := fg(t.BoxBorder, "╭"+strings.Repeat("─", width-2)+"╮")
		botRule := fg(t.BoxBorder, "╰"+strings.Repeat("─", width-2)+"╯")
		bL := fg(t.BoxBorder, "│ ")
		bR := fg(t.BoxBorder, " │")

		total := len(vlines)
		start := m.scratchScroll
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
				// Date separator rendered in dimmed.
				if vl.lineIdx >= 0 && vl.lineIdx < len(m.scratchLines) {
					lines = append(lines, fg(t.Dimmed, m.scratchLines[vl.lineIdx]))
				} else {
					lines = append(lines, "")
				}
			case vl.isHeader:
				// Header inside selected box: hints right-aligned.
				hintsW := lipgloss.Width(vl.metaText)
				pad := innerW - hintsW
				if pad < 0 {
					pad = 0
				}
				headerContent := strings.Repeat(" ", pad) + fg(t.ContentDimmed, vl.metaText)
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
				// Content line.
				if vl.lineIdx < 0 || vl.lineIdx >= len(m.scratchLines) {
					lines = append(lines, "")
					continue
				}
				text := m.scratchLines[vl.lineIdx]
				if vl.isSelected {
					visW := lipgloss.Width(text)
					if visW < innerW {
						text = text + strings.Repeat(" ", innerW-visW)
					} else if visW > innerW {
						text = truncate(text, innerW)
					}
					lines = append(lines, bL+fg(t.ContentText, text)+bR)
				} else {
					lines = append(lines, fg(t.NavDimmed, text))
				}
			}
		}

		for len(lines) < height {
			lines = append(lines, "")
		}
		return lines[:height]
	}

	// Flat mode: plain scroll (scratch not focused).
	end := m.scratchScroll + viewH
	if end > len(m.scratchLines) {
		end = len(m.scratchLines)
	}
	for i := m.scratchScroll; i < end; i++ {
		lines = append(lines, fg(t.NavDimmed, m.scratchLines[i]))
	}

	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines[:height]
}

// renderNavSubTabBar renders the Articles / Collections / Workspaces sub-tab row,
// truncated to the nav pane width.
func (m Model) renderNavSubTabBar() string {
	t := ActiveTheme
	w := m.navWidth()
	var parts []string
	visibleWidth := 0
	for i := navSubTab(0); i < navSubTabCount; i++ {
		label := i.String()
		text := " " + label + " "
		if i == m.navSubTab {
			text = "[" + label + "]"
		}
		textWidth := len([]rune(text))
		if visibleWidth+textWidth > w {
			break
		}
		if i == m.navSubTab {
			if m.focus == paneNavSubTab {
				parts = append(parts, fgBold(t.Accent, text))
			} else {
				parts = append(parts, fgBold(t.TabActive, text))
			}
		} else {
			parts = append(parts, fg(t.TabInactive, text))
		}
		visibleWidth += textWidth
		if int(i) < int(navSubTabCount)-1 {
			sep := "  "
			if visibleWidth+len(sep) > w {
				break
			}
			parts = append(parts, fg(t.Dimmed, sep))
			visibleWidth += len(sep)
		}
	}
	return strings.Join(parts, "")
}

// navSubTabHitTest returns the navSubTab at column x, or -1 if none.
func navSubTabHitTest(x int) navSubTab {
	col := 0
	for i := navSubTab(0); i < navSubTabCount; i++ {
		label := i.String()
		width := len(label) + 2 // "[Label]" or " Label "
		if x >= col && x < col+width {
			return i
		}
		col += width
		if int(i) < int(navSubTabCount)-1 {
			col += 2 // separator "  "
		}
	}
	return -1
}

// renderNavCollections renders the collection tree into the nav pane.
func (m Model) renderNavCollections(maxLines int) []string {
	t := ActiveTheme

	if !m.collectionsLoaded {
		return []string{fg(t.NavDimmed, "  loading…")}
	}
	if m.collectionsErr != "" {
		return []string{fg(t.NavDimmed, "  error: "+truncate(m.collectionsErr, m.navWidth()-4))}
	}
	if len(m.navRows) == 0 {
		return []string{fg(t.NavDimmed, "  (no collections)")}
	}

	var lines []string
	end := m.navRowScroll + maxLines
	if end > len(m.navRows) {
		end = len(m.navRows)
	}
	for i := m.navRowScroll; i < end; i++ {
		row := m.navRows[i]
		selected := i == m.navRowCursor
		var line string

		switch row.kind {
		case rowCollection:
			arrow := "▶ "
			if row.expanded {
				arrow = "▼ "
			}
			label := arrow + row.colSlug
			if row.colCount > 0 {
				label += fmt.Sprintf("  (%d)", row.colCount)
			}
			label = truncate(label, m.navWidth()-1)
			if selected {
				line = reverse(label)
			} else {
				line = fgBold(t.NavGroup, label)
			}

		case rowArticle:
			prefix := "    " // indented under collection
			dot := "• "
			if row.item.favorite {
				dot = "★ "
			} else if row.item.read {
				dot = "  "
			}
			idTag := ""
			idTagLen := 0
			if row.item.numID > 0 {
				idTag = fmt.Sprintf("%d ", row.item.numID)
				idTagLen = len(idTag)
			}
			title := truncate(oneLine(row.item.title), m.navWidth()-len(prefix)-len(dot)-idTagLen)
			if selected {
				line = reverse(prefix + idTag + dot + title)
			} else if row.item.favorite {
				idPart := ""
				if idTag != "" {
					idPart = fg(t.NavDimmed, idTag)
				}
				line = fg(t.NavDimmed, prefix) + idPart + fg(t.Favorite, "★") + " " + fg(t.NavText, title)
			} else {
				idPart := ""
				if idTag != "" {
					idPart = fg(t.NavDimmed, idTag)
				}
				line = fg(t.NavDimmed, prefix) + idPart + fg(t.NavMark, dot[:1]) + " " + fg(t.NavText, title)
			}
		}
		lines = append(lines, line)
	}

	// Scroll indicator
	if len(m.navRows) > maxLines {
		pct := 0
		if len(m.navRows) > 1 {
			pct = m.navRowScroll * 100 / (len(m.navRows) - maxLines)
		}
		lines = append(lines, fg(t.NavDimmed, fmt.Sprintf(" ↕ %d/%d (%d%%)", m.navRowCursor+1, len(m.navRows), pct)))
	}
	return lines
}

// renderNavWorkspaces renders the workspace foldable tree into the nav pane.
func (m Model) renderNavWorkspaces(maxLines int) []string {
	t := ActiveTheme

	// When a workspace-scoped article search is active, show navItems (search
	// results) using the standard article list renderer instead of the workspace tree.
	if m.navFilter != "" && len(m.navItems) > 0 {
		return m.renderNavLibrary(maxLines)
	}

	if !m.workspacesLoaded {
		return []string{fg(t.NavDimmed, "  loading…")}
	}
	if m.workspacesErr != "" {
		return []string{fg(t.NavDimmed, "  error: "+truncate(m.workspacesErr, m.navWidth()-4))}
	}
	if len(m.workspaceItems) == 0 {
		return []string{fg(t.NavDimmed, "  (no workspaces — use  arc workspace new)")}
	}

	w := m.navWidth()
	var lines []string
	end := m.wsScroll + maxLines
	if end > len(m.wsRows) {
		end = len(m.wsRows)
	}
	for i := m.wsScroll; i < end; i++ {
		row := m.wsRows[i]
		selected := i == m.wsCursor
		wsIdx := row.wsIdx
		var ws workspaceItem
		if wsIdx >= 0 && wsIdx < len(m.workspaceItems) {
			ws = m.workspaceItems[wsIdx]
		}

		var label string
		switch row.kind {
		case wsRowWorkspace:
			arrow := "▶ "
			if ws.expanded {
				arrow = "▼ "
			}
			flags := ""
			if ws.hasSystem {
				flags += " ✎"
			}
			if ws.hasHistory {
				flags += " 💬"
			}
			atticCount := len(ws.atticArticles) + len(ws.atticCollections)
			counts := fmt.Sprintf(" (%da %dc %dr)", ws.articleCount, ws.collectionCount, ws.resourceCount)
			if atticCount > 0 {
				counts = fmt.Sprintf(" (%da %dc %dr %d⌂)", ws.articleCount, ws.collectionCount, ws.resourceCount, atticCount)
			}
			if selected {
				pin := ""
				if ws.pinned {
					pin = "★ "
				}
				label = reverse(truncate(arrow+pin+ws.name+counts+flags, w-1))
			} else if ws.pinned {
				label = fgBold(t.NavGroup, truncate(arrow, w-1)) +
					fgBold(t.Pinned, "★ ") +
					fgBold(t.NavGroup, truncate(ws.name+counts+flags, w-1))
			} else {
				label = fgBold(t.NavGroup, truncate(arrow+ws.name+counts+flags, w-1))
			}

		case wsRowScratch:
			prefix := "  "
			dot := "✎ "
			name := storefs.ScratchName(ws.name)
			label = prefix + dot + name
			if selected {
				label = reverse(label)
			} else {
				label = fg(t.NavDimmed, prefix) + fg(t.Accent, "✎") + " " + fg(t.NavText, name)
			}

		case wsRowCollection:
			arrow := "  ▶ "
			if ws.expandedCols[row.colSlug] {
				arrow = "  ▼ "
			}
			label = truncate(arrow+row.colSlug+fmt.Sprintf(" (%d)", row.count), w-1)
			if selected {
				label = reverse(label)
			} else {
				label = fg(t.NavText, label)
			}

		case wsRowArticle:
			prefix := "  " // 2 spaces indent (direct child)
			if row.colSlug != "" {
				prefix = "    " // 4 spaces under collection
			}
			dot := "• "
			idTag := ""
			idTagLen := 0
			if row.numID > 0 {
				idTag = fmt.Sprintf("%d ", row.numID)
				idTagLen = len(idTag)
			}
			title := truncate(oneLine(row.title), w-len(prefix)-len(dot)-idTagLen)
			label = prefix + idTag + dot + title
			if selected {
				label = reverse(label)
			} else {
				idPart := ""
				if idTag != "" {
					idPart = fg(t.NavDimmed, idTag)
				}
				label = fg(t.NavDimmed, prefix) + idPart + fg(t.NavMark, "•") + " " + fg(t.NavText, title)
			}

		case wsRowResourceGroup:
			arrow := "  ▶ "
			if ws.resourcesExpanded {
				arrow = "  ▼ "
			}
			label = truncate(arrow+"resources"+fmt.Sprintf(" (%d)", row.count), w-1)
			if selected {
				label = reverse(label)
			} else {
				label = fg(t.NavText, label)
			}

		case wsRowResourceDir:
			// Compute indent based on nesting depth.
			depth := strings.Count(row.resourceName, string(filepath.Separator))
			indent := strings.Repeat("  ", depth+2) // 2 base + depth
			arrow := "▶ "
			if ws.expandedResourceDirs[row.resourceName] {
				arrow = "▼ "
			}
			dirName := filepath.Base(row.resourceName)
			label = truncate(indent+arrow+dirName, w-1)
			if selected {
				label = reverse(label)
			} else {
				label = fg(t.NavDimmed, indent) + fg(t.NavText, arrow+dirName)
			}

		case wsRowResource:
			depth := strings.Count(row.resourceName, string(filepath.Separator))
			prefix := strings.Repeat("  ", depth+2) // 2 base + depth
			dot := "◦ "
			name := filepath.Base(row.resourceName)
			name = truncate(name, w-len(prefix)-len(dot))
			label = prefix + dot + name
			if selected {
				label = reverse(label)
			} else {
				label = fg(t.NavDimmed, prefix) + fg(t.NavDimmed, "◦") + " " + fg(t.NavText, name)
			}

		case wsRowOutcomeGroup:
			arrow := "  ▶ "
			if ws.outcomesExpanded {
				arrow = "  ▼ "
			}
			label = truncate(arrow+"outcomes"+fmt.Sprintf(" (%d)", row.count), w-1)
			if selected {
				label = reverse(label)
			} else {
				label = fg(t.NavText, label)
			}

		case wsRowOutcome:
			prefix := "    "
			dot := "◦ "
			name := truncate(row.outcomeName, w-len(prefix)-len(dot))
			label = prefix + dot + name
			if selected {
				label = reverse(label)
			} else {
				label = fg(t.NavDimmed, prefix) + fg(t.NavDimmed, "◦") + " " + fg(t.NavText, name)
			}

		case wsRowAtticGroup:
			arrow := "  ▶ "
			if ws.atticExpanded {
				arrow = "  ▼ "
			}
			label = truncate(arrow+"attic"+fmt.Sprintf(" (%d)", row.count), w-1)
			if selected {
				label = reverse(label)
			} else {
				label = fg(t.NavDimmed, label)
			}

		case wsRowAtticArticle:
			prefix := "    "
			dot := "◦ "
			title := truncate(oneLine(row.title), w-len(prefix)-len(dot))
			label = prefix + dot + title
			if selected {
				label = reverse(label)
			} else {
				label = fg(t.NavDimmed, prefix+dot+title)
			}

		case wsRowAtticCollection:
			prefix := "    "
			dot := "◦ "
			name := truncate(row.colSlug, w-len(prefix)-len(dot))
			label = prefix + dot + name
			if selected {
				label = reverse(label)
			} else {
				label = fg(t.NavDimmed, prefix+dot+name)
			}
		}
		lines = append(lines, label)
	}

	// Scroll indicator
	if len(m.wsRows) > maxLines {
		pct := 0
		if len(m.wsRows) > 1 {
			pct = m.wsScroll * 100 / (len(m.wsRows) - maxLines)
		}
		lines = append(lines, fg(t.NavDimmed, fmt.Sprintf(" ↕ %d/%d (%d%%)", m.wsCursor+1, len(m.wsRows), pct)))
	}
	return lines
}

// renderNavLibrary renders the article list into the nav pane.
func (m Model) renderNavLibrary(maxLines int) []string {
	t := ActiveTheme

	if !m.navLoaded {
		return []string{fg(t.NavDimmed, "loading…")}
	}
	if m.navErr != "" {
		return []string{fg(t.NavDimmed, "error: "+truncate(m.navErr, m.navWidth()-2))}
	}
	if len(m.navItems) == 0 {
		return []string{fg(t.NavDimmed, "(empty)")}
	}

	numbered := m.navFilter != ""
	// Width of the widest number prefix, e.g. "12. " = 4 chars for 10-99 items.
	numWidth := 0
	if numbered {
		numWidth = len(fmt.Sprintf("%d. ", len(m.navItems)))
	}

	var lines []string
	end := m.navScroll + maxLines
	if end > len(m.navItems) {
		end = len(m.navItems)
	}
	// Compute width of numeric ID column (e.g. 3 digits for IDs up to 999).
	maxNumID := 0
	for _, it := range m.navItems {
		if it.numID > maxNumID {
			maxNumID = it.numID
		}
	}
	idWidth := len(fmt.Sprintf("%d", maxNumID))
	if idWidth < 2 {
		idWidth = 2
	}

	for i := m.navScroll; i < end; i++ {
		item := m.navItems[i]

		// Numeric ID prefix (dimmed)
		idStr := fmt.Sprintf("%*d ", idWidth, item.numID)
		if item.numID == 0 {
			idStr = strings.Repeat(" ", idWidth+1)
		}

		var prefix string
		if numbered {
			prefix = fmt.Sprintf("%*d. ", numWidth-2, i+1) // right-align number
		} else {
			if item.favorite {
				prefix = "★ "
			} else if item.read {
				prefix = "  "
			} else {
				prefix = "• "
			}
		}
		title := truncate(oneLine(item.title), m.navWidth()-len(prefix)-idWidth-1)
		var line string
		if i == m.navCursor {
			line = reverse(idStr + prefix + title)
		} else {
			idPart := fg(t.NavDimmed, idStr)
			if numbered {
				if item.favorite {
					line = idPart + fg(t.Favorite, "★") + " " + fg(t.NavText, title)
				} else {
					line = idPart + fg(t.NavDimmed, prefix) + fg(t.NavText, title)
				}
			} else {
				if item.favorite {
					line = idPart + fg(t.Favorite, "★") + " " + fg(t.NavText, title)
				} else {
					dotChar := prefix[:len(prefix)-1] // strip trailing space for coloring
					line = idPart + fg(t.NavMark, dotChar) + " " + fg(t.NavText, title)
				}
			}
		}
		lines = append(lines, line)
	}
	// scroll indicator
	if len(m.navItems) > maxLines {
		pct := 0
		if len(m.navItems) > 1 {
			pct = m.navScroll * 100 / (len(m.navItems) - maxLines)
		}
		lines = append(lines, fg(t.NavDimmed, fmt.Sprintf(" ↕ %d/%d (%d%%)", m.navCursor+1, len(m.navItems), pct)))
	}
	return lines
}

// renderContentPane returns lines for the right content pane.
func (m Model) renderContentPane(height, width int) []string {
	// Calculate scratch/askX split if open (mutually exclusive).
	splitH := 0
	contentH := height
	if m.scratchOpen || m.askxOpen || m.previewOpen {
		splitH = height / 3
		if splitH < 3 {
			splitH = 3
		}
		contentH = height - splitH
		if contentH < 3 {
			contentH = 3
		}
	}

	var lines []string
	if m.chatMode {
		lines = m.renderChatPane(contentH, width)
	} else {
		switch m.activeTab {
		case tabLibrary:
			lines = m.renderContentLibrary(contentH, width)
		case tabAgent:
			lines = m.renderContentAgent(contentH, width)
		case tabStats:
			lines = m.renderContentStats(contentH, width)
		default:
			lines = m.renderContentPlaceholder(contentH, width)
		}
	}

	// Pad content to contentH.
	for len(lines) < contentH {
		lines = append(lines, "")
	}
	lines = lines[:contentH]

	// Append scratch or askX pane if open.
	if m.scratchOpen && splitH > 0 {
		lines = append(lines, m.renderScratchPane(splitH, width)...)
	} else if m.askxOpen && splitH > 0 {
		lines = append(lines, m.renderAskXPane(splitH, width)...)
	} else if m.previewOpen && splitH > 0 {
		lines = append(lines, m.renderPreviewPane(splitH, width)...)
	}

	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines[:height]
}

// contentHeaderLines returns the number of lines above the scrollable content.
// Base: title + slug + meta1 + meta2 + [collections] + [author/feed] + [agent] + meta3 + sep + tabs + sep = 9
func contentHeaderLines(item *navItem) int {
	n := 9 // base fixed lines
	if item != nil {
		if len(item.collections) > 0 {
			n++
		}
		if item.author != "" || item.publishedAt != "" || item.feed != "" {
			n++
		}
		if item.agentReason != "" || item.qualityScore > 0 {
			n++
		}
	}
	return n
}

// selectedCollection returns the navRow when the cursor is on a collection header, or nil.
func (m Model) selectedCollection() *navRow {
	if m.navSubTab != navSubTabCollections {
		return nil
	}
	if m.navRowCursor >= 0 && m.navRowCursor < len(m.navRows) {
		r := &m.navRows[m.navRowCursor]
		if r.kind == rowCollection {
			return r
		}
	}
	return nil
}

// selectedNavItem returns the navItem currently under the cursor, or nil.
// wsSearchActive reports whether a workspace-scoped article search is currently
// showing results in the nav pane (instead of the workspace tree).
func (m Model) wsSearchActive() bool {
	return m.navSubTab == navSubTabWorkspaces && m.navFilter != "" && len(m.navItems) > 0
}

func (m Model) selectedNavItem() *navItem {
	switch m.navSubTab {
	case navSubTabArticles:
		if m.navCursor >= 0 && m.navCursor < len(m.navItems) {
			return &m.navItems[m.navCursor]
		}
	case navSubTabCollections:
		if m.navRowCursor >= 0 && m.navRowCursor < len(m.navRows) {
			r := m.navRows[m.navRowCursor]
			if r.kind == rowArticle && r.item != nil {
				return r.item
			}
		}
	case navSubTabWorkspaces:
		if m.wsSearchActive() {
			// Search results mode: use navCursor over navItems directly.
			if m.navCursor >= 0 && m.navCursor < len(m.navItems) {
				slog.Debug("selectedNavItem: ws search mode", "navCursor", m.navCursor, "id", m.navItems[m.navCursor].id)
				return &m.navItems[m.navCursor]
			}
			return nil
		}
		if m.wsCursor >= 0 && m.wsCursor < len(m.wsRows) {
			row := m.wsRows[m.wsCursor]
			if row.kind == wsRowArticle && row.slug != "" {
				for i := range m.navItemsAll {
					if m.navItemsAll[i].id == row.slug {
						return &m.navItemsAll[i]
					}
				}
			}
		}
	}
	return nil
}

func (m Model) renderContentLibrary(height, width int) []string {
	t := ActiveTheme
	var lines []string

	if m.navSubTab == navSubTabWorkspaces && !m.wsSearchActive() {
		return m.renderContentWorkspace(height, width)
	}

	if col := m.selectedCollection(); col != nil {
		return m.renderContentCollection(height, width, col)
	}

	item := m.selectedNavItem()
	if item == nil {
		lines = append(lines, fgBold(t.ContentTitle, "arc knowledge base"))
		lines = append(lines, "")
		if !m.navLoaded {
			lines = append(lines, fg(t.ContentDimmed, "Loading articles…"))
		} else {
			lines = append(lines, fg(t.ContentDimmed, "No articles. Use  arc ingest <url>  to add one."))
		}
		for len(lines) < height {
			lines = append(lines, "")
		}
		return lines[:height]
	}
	titleColor := t.ContentText
	if m.focus == paneContent {
		titleColor = t.ContentTitle
	}

	// ── Header ────────────────────────────────────────────────────────────────
	// #numID · title · slug on same line
	sep := fg(t.ContentDimmed, "  ·  ")
	idPrefix := ""
	idPrefixLen := 0
	if item.numID > 0 {
		idPrefix = fmt.Sprintf("ID: %d", item.numID)
		idPrefixLen = len(idPrefix) + 5 // "  ·  "
	}
	slugStr := fg(t.ContentDimmed, item.id)
	titleMaxW := width - 1 - lipgloss.Width("  ·  "+item.id) - idPrefixLen
	if titleMaxW < 10 {
		titleMaxW = 10
	}
	headerLine := ""
	if idPrefix != "" {
		headerLine = fg(t.ContentDimmed, idPrefix) + sep
	}
	headerLine += fgBold(titleColor, truncate(oneLine(item.title), titleMaxW)) + sep + slugStr
	lines = append(lines, headerLine)

	// meta line 1: ingest date · source type · url · read status · favorite
	readMark := fg(t.ContentDimmed, "unread")
	if item.read {
		readMark = fg(t.NavMark, "✓ read")
	}
	if item.favorite {
		readMark += fg(t.ContentDimmed, "  ·  ") + fg(t.Favorite, "★ favorite")
	}
	meta1 := fg(t.ContentDimmed, item.date.Format("2006-01-02"))
	if item.sourceType != "" {
		meta1 += fg(t.ContentDimmed, "  ·  "+item.sourceType)
	}
	if item.url != "" {
		meta1 += fg(t.ContentDimmed, "  ·  "+truncate(item.url, 60))
	}
	meta1 += fg(t.ContentDimmed, "  ·  ") + readMark
	lines = append(lines, meta1)

	// meta line 2: author · published at · feed
	var provParts []string
	if item.author != "" {
		provParts = append(provParts, item.author)
	}
	if item.publishedAt != "" {
		provParts = append(provParts, "published: "+item.publishedAt)
	}
	if item.feed != "" {
		provParts = append(provParts, "feed: "+item.feed)
	}
	if len(provParts) > 0 {
		lines = append(lines, fg(t.ContentDimmed, truncate(strings.Join(provParts, "  ·  "), width-1)))
	} else {
		lines = append(lines, "")
	}

	// meta line 3: tags
	if len(item.tags) > 0 {
		lines = append(lines, fg(t.ContentDimmed, truncate("tags: "+strings.Join(item.tags, ", "), width-1)))
	} else {
		lines = append(lines, "")
	}

	// meta line 3b: collections (own line, may be long)
	if len(item.collections) > 0 {
		lines = append(lines, fg(t.ContentDimmed, truncate("collections: "+strings.Join(item.collections, ", "), width-1)))
	}

	// meta line 4: agent reason · quality score
	var agentParts []string
	if item.agentReason != "" {
		agentParts = append(agentParts, item.agentReason)
	}
	if item.qualityScore > 0 {
		agentParts = append(agentParts, fmt.Sprintf("quality: %.2f", item.qualityScore))
	}
	if len(agentParts) > 0 {
		lines = append(lines, fg(t.ContentDimmed, truncate(strings.Join(agentParts, "  ·  "), width-1)))
	}

	// meta line 5: available variants
	var variantParts []string
	if item.summary != "" {
		variantParts = append(variantParts, "summary: "+item.summary)
	}
	if item.flashModel != "" {
		variantParts = append(variantParts, "flash: "+item.flashModel)
	}
	if len(variantParts) > 0 {
		lines = append(lines, fg(t.ContentDimmed, strings.Join(variantParts, "  ")))
	} else {
		lines = append(lines, "")
	}

	// ── Separator ─────────────────────────────────────────────────────────────
	lines = append(lines, fg(t.Dimmed, strings.Repeat("─", width)))

	// ── Sub-tab strip ─────────────────────────────────────────────────────────
	lines = append(lines, m.renderContentTabs(width))

	// ── Separator ─────────────────────────────────────────────────────────────
	lines = append(lines, fg(t.Dimmed, strings.Repeat("─", width)))

	// ── Scrollable content ────────────────────────────────────────────────────
	viewH := height - contentHeaderLines(item)
	if viewH < 1 {
		viewH = 1
	}

	if m.contentLoading {
		lines = append(lines, fg(t.ContentDimmed, "loading…"))
	} else if len(m.contentLines) == 0 {
		lines = append(lines, fg(t.ContentDimmed, "(not available — use  arc reprocess  to generate)"))
	} else {
		// Render from contentScroll (logical lines), wrapping each line to width.
		// All lines get a 2-char prefix: "▶ " for the cursor line, "  " otherwise.
		// Stop when viewH visual rows are consumed.
		visual := 0
		for i := m.contentScroll; i < len(m.contentLines) && visual < viewH; i++ {
			wrapped := wordWrap(m.contentLines[i], width-3)
			if len(wrapped) == 0 {
				wrapped = []string{""}
			}
			isCursor := i == m.contentLineCursor
			for wi, wl := range wrapped {
				if visual >= viewH {
					break
				}
				if isCursor && wi == 0 {
					lines = append(lines, fgBold(t.InputPrompt, "▶ ")+fg(t.TopBarText, wl))
				} else if isCursor {
					lines = append(lines, "  "+fg(t.TopBarText, wl))
				} else {
					lines = append(lines, fg(t.Dimmed, "  ")+fg(t.ContentText, wl))
				}
				visual++
			}
		}
	}

	// Pad to full height first.
	for len(lines) < height {
		lines = append(lines, "")
	}
	lines = lines[:height]

	// Scroll indicator — bottom-right corner, overlaid on the last line.
	if len(m.contentLines) > 0 && (m.contentScroll > 0 || m.contentScroll+viewH < len(m.contentLines)) {
		pct := 0
		maxScroll := len(m.contentLines) - 1
		if maxScroll > 0 {
			pct = m.contentLineCursor * 100 / maxScroll
		}
		indicator := fmt.Sprintf("line %d/%d (%d%%) ", m.contentLineCursor+1, len(m.contentLines), pct)
		contentW := width
		lastIdx := height - 1
		base := lines[lastIdx]
		baseVisible := lipgloss.Width(base)
		indW := len([]rune(indicator))
		if indW <= contentW {
			pad := contentW - baseVisible - indW
			if pad < 0 {
				pad = 0
			}
			lines[lastIdx] = base + strings.Repeat(" ", pad) + fg(t.ContentDimmed, indicator)
		}
	}
	return lines
}

// renderContentTabs renders the [Flash] [Summary] [Body] [Cards] tab strip
// with a right-aligned "s speak" hint when the content pane is focused.
func (m Model) renderContentTabs(width int) string {
	t := ActiveTheme
	var parts []string
	active := m.activeSection()
	tabs := []contentTab{ctFlash, ctSummary, ctBody, ctCards}
	for _, ct := range tabs {
		label := "[" + ct.String() + "]"
		if ct == active && m.contentHas[ct] {
			parts = append(parts, fgBold(t.ContentTabActive, label))
		} else if m.contentHas[ct] {
			parts = append(parts, fg(t.ContentTabInactive, label))
		} else {
			parts = append(parts, fg(t.Dimmed, label))
		}
	}
	tabStr := strings.Join(parts, " ")
	if m.focus == paneContent && !m.chatMode && len(m.contentLines) > 0 {
		hint := fg(t.Dimmed, " s speak ")
		gap := width - lipgloss.Width(tabStr) - lipgloss.Width(hint)
		if gap > 0 {
			tabStr += strings.Repeat(" ", gap) + hint
		}
	}
	return tabStr
}

// renderContentCollection renders collection metadata in the content pane.
func (m Model) renderContentCollection(height, width int, col *navRow) []string {
	t := ActiveTheme
	var lines []string

	titleColor := t.ContentText
	if m.focus == paneContent {
		titleColor = t.ContentTitle
	}

	headerLine := ""
	if col.colNumID > 0 {
		headerLine = fg(t.ContentDimmed, fmt.Sprintf("ID: %d", col.colNumID)) + fg(t.ContentDimmed, "  ·  ")
	}
	headerLine += fgBold(titleColor, truncate(col.colSlug, width-1))
	lines = append(lines, headerLine)

	// meta line 1: article count · created at
	meta1 := fg(t.ContentDimmed, fmt.Sprintf("%d articles", col.colCount))
	if !col.colCreatedAt.IsZero() {
		meta1 += fg(t.ContentDimmed, "  ·  created "+col.colCreatedAt.Format("2006-01-02"))
	}
	lines = append(lines, meta1)

	// meta line 2: name (if different from slug)
	if col.colName != "" && col.colName != col.colSlug {
		lines = append(lines, fg(t.ContentDimmed, "name: "+truncate(col.colName, width-6)))
	} else {
		lines = append(lines, "")
	}

	// meta line 3: flags
	var flags []string
	if col.colHasSummary {
		flags = append(flags, "meta-summary")
	}
	if col.colHasSystem {
		flags = append(flags, "system prompt")
	}
	if len(flags) > 0 {
		lines = append(lines, fg(t.ContentDimmed, strings.Join(flags, "  ·  ")))
	} else {
		lines = append(lines, "")
	}

	lines = append(lines, fg(t.Dimmed, strings.Repeat("─", width)))
	lines = append(lines, "")
	lines = append(lines, fg(t.Dimmed, strings.Repeat("─", width)))

	// Description body
	if col.colDesc != "" {
		for _, l := range wordWrap(col.colDesc, width-1) {
			lines = append(lines, fg(t.ContentText, l))
		}
	} else {
		lines = append(lines, fg(t.ContentDimmed, "(no description)"))
	}

	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines[:height]
}

// renderContentWorkspace renders workspace details in the content pane.
func (m Model) renderContentWorkspace(height, width int) []string {
	t := ActiveTheme
	var lines []string

	titleColor := t.ContentText
	if m.focus == paneContent {
		titleColor = t.ContentTitle
	}

	if !m.workspacesLoaded {
		lines = append(lines, fg(t.ContentDimmed, "loading…"))
		for len(lines) < height {
			lines = append(lines, "")
		}
		return lines[:height]
	}

	if len(m.workspaceItems) == 0 {
		lines = append(lines, fgBold(t.ContentTitle, "Workspaces"))
		lines = append(lines, fg(t.ContentDimmed, "No workspaces yet. Use  arc workspace new <name> <title>"))
		for len(lines) < height {
			lines = append(lines, "")
		}
		return lines[:height]
	}

	// Find the workspace for the current cursor row.
	wsIdx := 0
	if m.wsCursor >= 0 && m.wsCursor < len(m.wsRows) {
		wsIdx = m.wsRows[m.wsCursor].wsIdx
	}
	if wsIdx < 0 || wsIdx >= len(m.workspaceItems) {
		wsIdx = 0
	}
	ws := m.workspaceItems[wsIdx]

	// Title · slug
	sep := fg(t.ContentDimmed, "  ·  ")
	slugStr := fg(t.ContentDimmed, ws.name)
	titleMaxW := width - 1 - lipgloss.Width("  ·  "+ws.name)
	if titleMaxW < 10 {
		titleMaxW = 10
	}
	titleStr := ws.name
	if ws.description != "" {
		titleStr = ws.description
	}
	lines = append(lines, fgBold(titleColor, truncate(titleStr, titleMaxW))+sep+slugStr)

	// meta line 1: status · created · articles · collections
	meta1Raw := ws.status
	if !ws.createdAt.IsZero() {
		meta1Raw += "  ·  created " + ws.createdAt.Format("2006-01-02")
	}
	meta1Raw += fmt.Sprintf("  ·  %d articles  ·  %d collections", ws.articleCount, ws.collectionCount)
	lines = append(lines, fg(t.ContentDimmed, truncate(meta1Raw, width-1)))

	// meta line 2: resources · outcomes
	meta2Raw := fmt.Sprintf("%d resources  ·  %d outcomes", ws.resourceCount, ws.outcomeCount)
	lines = append(lines, fg(t.ContentDimmed, truncate(meta2Raw, width-1)))

	// meta line 3: chat config
	chatParts := []string{}
	if ws.chatProfile != "" {
		chatParts = append(chatParts, "profile: "+ws.chatProfile)
	}
	if ws.chatStrategy != "" {
		chatParts = append(chatParts, "strategy: "+ws.chatStrategy)
	}
	if ws.hasSystem {
		chatParts = append(chatParts, "✎ system prompt")
	}
	if ws.hasHistory {
		chatParts = append(chatParts, "💬 chat history")
	}
	if len(chatParts) > 0 {
		lines = append(lines, fg(t.ContentDimmed, truncate(strings.Join(chatParts, "  ·  "), width-1)))
	} else {
		lines = append(lines, "")
	}

	lines = append(lines, fg(t.Dimmed, strings.Repeat("─", width)))
	lines = append(lines, fg(t.ContentDimmed, "arc workspace chat "+ws.name))
	lines = append(lines, fg(t.Dimmed, strings.Repeat("─", width)))

	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines[:height]
}

// renderAgentNavSubTabBar renders the Runs / Decisions / Feeds sub-tab row for the Agent nav pane.
func (m Model) renderAgentNavSubTabBar() string {
	t := ActiveTheme
	w := m.navWidth()
	var parts []string
	visibleWidth := 0
	for i := agentSubTab(0); i < agentSubTabCount; i++ {
		label := i.String()
		text := " " + label + " "
		if i == m.agentSubTab {
			text = "[" + label + "]"
		}
		textWidth := len([]rune(text))
		if visibleWidth+textWidth > w {
			break
		}
		if i == m.agentSubTab {
			if m.focus == paneNavSubTab {
				parts = append(parts, fgBold(t.Accent, text))
			} else {
				parts = append(parts, fgBold(t.TabActive, text))
			}
		} else {
			parts = append(parts, fg(t.TabInactive, text))
		}
		visibleWidth += textWidth
		if int(i) < int(agentSubTabCount)-1 {
			sep := "  "
			if visibleWidth+len(sep) > w {
				break
			}
			parts = append(parts, fg(t.Dimmed, sep))
			visibleWidth += len(sep)
		}
	}
	return strings.Join(parts, "")
}

// agentNavSubTabHitTest returns the agentSubTab at column x, or -1 if none.
func agentNavSubTabHitTest(x int) agentSubTab {
	col := 0
	for i := agentSubTab(0); i < agentSubTabCount; i++ {
		label := i.String()
		width := len(label) + 2
		if x >= col && x < col+width {
			return i
		}
		col += width
		if int(i) < int(agentSubTabCount)-1 {
			col += 2
		}
	}
	return -1
}

// renderNavAgentRuns renders the runs list in the Agent nav pane.
func (m Model) renderNavAgentRuns(maxLines int) []string {
	t := ActiveTheme
	if !m.agentRunsLoaded {
		return []string{fg(t.NavDimmed, "  loading…")}
	}
	if m.agentRunsErr != "" {
		return []string{fg(t.StatusError, "  "+m.agentRunsErr)}
	}
	if len(m.agentRuns) == 0 {
		return []string{
			fg(t.NavDimmed, "  No runs yet."),
			fg(t.NavDimmed, "  Run /agent run to start."),
		}
	}

	var lines []string
	for i := m.agentRunsScroll; i < len(m.agentRuns) && len(lines) < maxLines; i++ {
		rec := m.agentRuns[i]
		selected := i == m.agentRunsCursor

		date := rec.StartedAt.Local().Format("01/02 15:04")
		ingested := fmt.Sprintf("+%d", rec.TotalIngest)
		cost := ""
		if rec.TotalCostUSD > 0 {
			cost = fmt.Sprintf("  $%.2f", rec.TotalCostUSD)
		}
		label := fmt.Sprintf("  %s  %s%s", date, ingested, cost)

		if selected && m.focus == paneNav {
			lines = append(lines, fgBold(t.Accent, label))
		} else if selected {
			lines = append(lines, fg(t.Accent, label))
		} else {
			lines = append(lines, fg(t.NavText, label))
		}
	}
	return lines
}

// renderContentAgent renders the Agent tab content pane.
func (m Model) renderContentAgent(height, width int) []string {
	t := ActiveTheme
	var lines []string

	switch m.agentSubTab {
	case agentSubTabRuns:
		if !m.agentRunsLoaded {
			lines = append(lines, fg(t.ContentDimmed, "Loading…"))
		} else if m.agentRunsErr != "" {
			lines = append(lines, fg(t.StatusError, m.agentRunsErr))
		} else if len(m.agentRuns) == 0 {
			lines = append(lines, fgBold(t.ContentTitle, "Agent Runs"))
			lines = append(lines, "")
			lines = append(lines, fg(t.ContentDimmed, "  No runs recorded yet."))
			lines = append(lines, "")
			lines = append(lines, fg(t.ContentDimmed, "  Run /agent run to start a feed scan."))
		} else if m.agentRunsCursor >= 0 && m.agentRunsCursor < len(m.agentRuns) {
			return m.renderAgentRunDetail(height, width)
		}
	case agentSubTabDecisions:
		if m.agentRunsCursor >= 0 && m.agentRunsCursor < len(m.agentRuns) {
			return m.renderAgentDecisionsContent(height, width)
		}
		lines = append(lines, fgBold(t.ContentTitle, "Decisions"))
		lines = append(lines, "")
		lines = append(lines, fg(t.ContentDimmed, "  No run selected."))
	case agentSubTabFeeds:
		lines = append(lines, fgBold(t.ContentTitle, "Feeds"))
		lines = append(lines, "")
		lines = append(lines, fg(t.ContentDimmed, "  No feeds configured."))
		lines = append(lines, "")
		lines = append(lines, fg(t.ContentDimmed, "  Use /agent feed add <url> to add a feed."))
	}

	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines[:height]
}

// renderAgentRunDetail renders the run detail content pane using the flat row model.
func (m Model) renderAgentRunDetail(height, width int) []string {
	t := ActiveTheme
	rec := m.agentRuns[m.agentRunsCursor]

	runType := rec.RunType
	if runType == "" {
		runType = "daily"
	}
	duration := rec.FinishedAt.Sub(rec.StartedAt).Round(time.Second)

	// Build the fixed header block (always visible at top, not scrolled).
	statRow := func(label, val string) string {
		return fg(t.ContentDimmed, fmt.Sprintf("  %-12s", label)) + fg(t.ContentText, val)
	}
	header := []string{
		fgBold(t.ContentTitle, fmt.Sprintf("%s  [%s]  %s", rec.RunID, runType, duration)),
		"",
		statRow("Started", rec.StartedAt.Local().Format("2006-01-02 15:04:05")),
		statRow("Feeds", fmt.Sprintf("%d", len(rec.Feeds))),
		statRow("Ingested", fmt.Sprintf("%d", rec.TotalIngest)),
		statRow("Maybe", fmt.Sprintf("%d", rec.TotalMaybe)),
		statRow("Skipped", fmt.Sprintf("%d", rec.TotalSkip)),
	}
	if rec.TotalCostUSD > 0 {
		header = append(header, statRow("Cost", formatUSD(rec.TotalCostUSD)))
	}
	if rec.Error != "" {
		header = append(header, "", fg(t.StatusError, "  Error: "+rec.Error))
	}
	header = append(header, "", fg(t.ContentDimmed, "  Feeds  (Space to expand)"))

	// Build the navigable row list.
	detailRows := m.buildAgentDetailRows()

	// Compute which navIdx the cursor is on (for highlighting).
	// navIdx maps agentContentCursor → detailRows index.
	var navIdx []int
	for i, r := range detailRows {
		if r.kind == agentRowFeed || r.kind == agentRowArticle {
			navIdx = append(navIdx, i)
		}
	}

	// Render navigable rows into display lines, tracking which line each navIdx maps to.
	type rowLine struct {
		navPos int // index into navIdx, or -1
		text   string
	}
	var rowLines []rowLine

	navPos := 0
	for i, r := range detailRows {
		if r.kind == agentRowHeader {
			continue // header handled above
		}
		isNav := false
		curNavPos := -1
		for _, ni := range navIdx {
			if ni == i {
				isNav = true
				curNavPos = navPos
				navPos++
				break
			}
		}

		selected := isNav && curNavPos == m.agentContentCursor && m.focus == paneContent

		switch r.kind {
		case agentRowFeed:
			expanded := m.agentFeedExpanded[r.feedIdx]
			arrow := "▶"
			if expanded {
				arrow = "▼"
			}
			feedErr := ""
			if r.feedIdx < len(rec.Feeds) && rec.Feeds[r.feedIdx].Error != "" {
				feedErr = "  ✗"
			}
			text := fmt.Sprintf("  %s %s%s", arrow, r.feedName, feedErr)
			statsText := "    " + r.feedStats
			if selected {
				rowLines = append(rowLines, rowLine{curNavPos, fgBold(t.Accent, text)})
				rowLines = append(rowLines, rowLine{-1, fg(t.ContentDimmed, statsText)})
			} else {
				rowLines = append(rowLines, rowLine{curNavPos, fg(t.ContentText, text)})
				rowLines = append(rowLines, rowLine{-1, fg(t.ContentDimmed, statsText)})
			}
		case agentRowArticle:
			icon := "✗"
			iconCol := t.StatusError
			switch r.verdict {
			case "ingest":
				icon = "✓"
				iconCol = t.Accent
			case "maybe":
				icon = "?"
				iconCol = t.Favorite
			}
			iconPart := fg(iconCol, "    "+icon+" ")
			titlePart := fg(t.ContentText, r.title)
			if selected {
				iconPart = fgBold(iconCol, "    "+icon+" ")
				titlePart = fgBold(t.ContentText, r.title)
			}
			rowLines = append(rowLines, rowLine{curNavPos, iconPart + titlePart})
			if r.reason != "" {
				reason := strings.ReplaceAll(r.reason, "\n", " ")
				rowLines = append(rowLines, rowLine{-1, fg(t.ContentDimmed, "      "+reason)})
			}
		}
	}

	// Scroll rowLines so cursor is visible.
	contentH := height - len(header)
	if contentH < 1 {
		contentH = 1
	}

	// Find scroll offset: ensure cursor row is in view.
	// agentContentScroll refers to the navIdx position; translate to rowLine index.
	scrollStart := 0
	if m.agentContentCursor > 0 {
		// Find first rowLine whose navPos >= agentContentScroll.
		for i, rl := range rowLines {
			if rl.navPos >= m.agentContentScroll {
				scrollStart = i
				break
			}
		}
	}

	visibleRows := rowLines
	if scrollStart < len(rowLines) {
		visibleRows = rowLines[scrollStart:]
	}

	var lines []string
	lines = append(lines, header...)
	for i := 0; i < contentH && i < len(visibleRows); i++ {
		lines = append(lines, visibleRows[i].text)
	}

	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines[:height]
}

// renderAgentDecisionsContent renders the Decisions sub-tab content pane.
// Shows items from the selected run's decisions file grouped by foldable feed sections.
// Article rows show action state: ✓ done, + approved, – skipped, ? pending maybe.
func (m Model) renderAgentDecisionsContent(height, width int) []string {
	t := ActiveTheme
	rec := m.agentRuns[m.agentRunsCursor]

	// Count pending items (skip/maybe not yet approved).
	pendingCount := 0
	approvedCount := 0
	for _, df := range m.agentRunDecisions.Feeds {
		for _, item := range df.Items {
			if item.Status == "done" {
				continue
			}
			if item.Action == "+" {
				approvedCount++
			} else if item.Verdict == "skip" || item.Verdict == "maybe" {
				pendingCount++
			}
		}
	}

	statRow := func(label, val string) string {
		return fg(t.ContentDimmed, fmt.Sprintf("  %-12s", label)) + fg(t.ContentText, val)
	}
	header := []string{
		fgBold(t.ContentTitle, fmt.Sprintf("%s  [%s]", rec.RunID, rec.RunType)),
		"",
		statRow("Started", rec.StartedAt.Local().Format("2006-01-02 15:04:05")),
		statRow("Pending", fmt.Sprintf("%d", pendingCount)),
		statRow("Approved", fmt.Sprintf("%d", approvedCount)),
		"",
		fg(t.ContentDimmed, "  Feeds  (Space to expand · a=approve · s=skip)"),
	}

	detailRows := m.buildAgentDecisionRows()

	var navIdx []int
	for i, r := range detailRows {
		if r.kind == agentRowFeed || r.kind == agentRowArticle {
			navIdx = append(navIdx, i)
		}
	}

	type rowLine struct {
		navPos int
		text   string
	}
	var rowLines []rowLine

	navPos := 0
	for i, r := range detailRows {
		if r.kind == agentRowHeader {
			continue
		}
		isNav := false
		curNavPos := -1
		for _, ni := range navIdx {
			if ni == i {
				isNav = true
				curNavPos = navPos
				navPos++
				break
			}
		}
		selected := isNav && curNavPos == m.agentContentCursor && m.focus == paneContent

		switch r.kind {
		case agentRowFeed:
			expanded := m.agentFeedExpanded[r.feedIdx]
			arrow := "▶"
			if expanded {
				arrow = "▼"
			}
			text := fmt.Sprintf("  %s %s", arrow, r.feedName)
			statsText := "    " + r.feedStats
			if selected {
				rowLines = append(rowLines, rowLine{curNavPos, fgBold(t.Accent, text)})
				rowLines = append(rowLines, rowLine{-1, fg(t.ContentDimmed, statsText)})
			} else {
				rowLines = append(rowLines, rowLine{curNavPos, fg(t.ContentText, text)})
				rowLines = append(rowLines, rowLine{-1, fg(t.ContentDimmed, statsText)})
			}
		case agentRowArticle:
			var icon string
			var iconCol lipgloss.Color
			switch {
			case r.status == "done":
				icon = "✓"
				iconCol = t.Accent
			case r.action == "+":
				icon = "+"
				iconCol = t.Accent
			case r.verdict == "maybe" && r.action == "":
				icon = "?"
				iconCol = t.Favorite
			default:
				icon = "✗"
				iconCol = t.StatusError
			}
			iconPart := fg(iconCol, "    "+icon+" ")
			titlePart := fg(t.ContentText, r.title)
			if selected {
				iconPart = fgBold(iconCol, "    "+icon+" ")
				titlePart = fgBold(t.ContentText, r.title)
			}
			rowLines = append(rowLines, rowLine{curNavPos, iconPart + titlePart})
			if r.reason != "" {
				reason := strings.ReplaceAll(r.reason, "\n", " ")
				rowLines = append(rowLines, rowLine{-1, fg(t.ContentDimmed, "      "+reason)})
			}
		}
	}

	contentH := height - len(header)
	if contentH < 1 {
		contentH = 1
	}

	scrollStart := 0
	if m.agentContentCursor > 0 {
		for i, rl := range rowLines {
			if rl.navPos >= m.agentContentScroll {
				scrollStart = i
				break
			}
		}
	}

	visibleRows := rowLines
	if scrollStart < len(rowLines) {
		visibleRows = rowLines[scrollStart:]
	}

	var lines []string
	lines = append(lines, header...)
	for i := 0; i < contentH && i < len(visibleRows); i++ {
		lines = append(lines, visibleRows[i].text)
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines[:height]
}

func (m Model) renderContentStats(height, width int) []string {
	t := ActiveTheme
	var lines []string

	lines = append(lines, fgBold(t.ContentTitle, "Knowledge Base Stats"))
	lines = append(lines, "")

	if !m.statsLoaded {
		lines = append(lines, fg(t.ContentDimmed, "Loading…"))
	} else {
		s := m.stats
		row := func(label string, val string) string {
			return fg(t.ContentDimmed, fmt.Sprintf("  %-20s", label)) + fg(t.ContentText, val)
		}
		lines = append(lines, row("Articles", fmt.Sprintf("%d", s.TotalArticles)))
		lines = append(lines, row("Unread", fmt.Sprintf("%d", s.Unread)))
		lines = append(lines, row("Collections", fmt.Sprintf("%d", s.TotalCollections)))
		lines = append(lines, row("With embedding", fmt.Sprintf("%d", s.EmbedCoverage)))
		lines = append(lines, "")
		lines = append(lines, row("Cost today", formatUSD(s.CostToday)))
		lines = append(lines, row("Cost 7d", formatUSD(s.CostThisWeek)))
		lines = append(lines, row("Cost 30d", formatUSD(s.CostThisMonth)))
		lines = append(lines, row("Cost total", formatUSD(s.CostTotal)))

		// Per-model spend, sorted descending, skipping zero.
		type modelCost struct {
			model string
			usd   float64
		}
		var mc []modelCost
		for model, usd := range s.CostByModel {
			if usd > 0 {
				mc = append(mc, modelCost{model, usd})
			}
		}
		sort.Slice(mc, func(i, j int) bool { return mc[i].usd > mc[j].usd })
		if len(mc) > 0 {
			lines = append(lines, "")
			lines = append(lines, fg(t.ContentDimmed, "  Spend by model"))
			// Compute column width from longest model name.
			maxModelW := 0
			for _, entry := range mc {
				if len(entry.model) > maxModelW {
					maxModelW = len(entry.model)
				}
			}
			modelRow := func(model, val string) string {
				return fg(t.ContentDimmed, fmt.Sprintf("    %-*s", maxModelW+2, model)) + fg(t.ContentText, val)
			}
			for _, entry := range mc {
				lines = append(lines, modelRow(entry.model, formatUSD(entry.usd)))
			}
		}
	}

	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines[:height]
}

func (m Model) renderContentPlaceholder(height, width int) []string {
	t := ActiveTheme
	var lines []string
	lines = append(lines, fgBold(t.ContentTitle, m.activeTab.String()))
	lines = append(lines, "")
	lines = append(lines, fg(t.ContentDimmed, "(coming soon)"))
	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines[:height]
}

// formatUSD renders a dollar amount concisely.
func formatUSD(v float64) string {
	if v == 0 {
		return "$0.00"
	}
	if v < 0.01 {
		return fmt.Sprintf("$%.4f", v)
	}
	return fmt.Sprintf("$%.2f", v)
}

// wordWrap splits text into lines of at most maxWidth visible characters.
func wordWrap(text string, maxWidth int) []string {
	if maxWidth < 10 {
		maxWidth = 10
	}
	// Normalize embedded newlines so they don't bypass width measurement.
	text = strings.ReplaceAll(text, "\r\n", " ")
	text = strings.ReplaceAll(text, "\n", " ")
	if lipgloss.Width(text) <= maxWidth {
		return []string{text}
	}
	var lines []string
	words := strings.Fields(text)
	cur := ""
	for _, w := range words {
		candidate := w
		if cur != "" {
			candidate = cur + " " + w
		}
		if lipgloss.Width(candidate) > maxWidth {
			if cur != "" {
				lines = append(lines, cur)
			}
			cur = w
		} else {
			cur = candidate
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}


// shellBorderColor is bright bold red ANSI, used to tint input and separators in shell mode.
const shellBorderColor = "\033[1;91m"

// fgShellInput renders text in bright bold red when shell mode is active,
// otherwise falls back to the given theme color.
func fgShellInput(shellMode bool, col lipgloss.Color, text string) string {
	if shellMode {
		return shellBorderColor + text + "\033[0m"
	}
	return fg(col, text)
}

// renderCommandInput renders the command input with multi-line support.
// Ported from c2: uses textarea.Model for editing state but renders manually
// with raw ANSI to avoid background color issues.
func (m Model) renderCommandInput() string {
	t := ActiveTheme
	shellMode := strings.HasPrefix(m.input.Value(), "!")

	promptStr := m.inputPrompt()
	promptRunes := []rune(promptStr)
	const padW = 1

	var line0W, contW int
	if shellMode {
		line0W = m.width
		contW = m.width
	} else {
		line0W = m.width - padW - len(promptRunes)
		contW = m.width - padW
	}
	if line0W < 1 {
		line0W = 1
	}
	if contW < 1 {
		contW = 1
	}

	// Unfocused: show placeholder or truncated value.
	if m.focus != paneCommand {
		prompt := fg(t.InputPrompt, promptStr)
		if shellMode {
			prompt = ""
		}
		availW := m.width - len(promptRunes)
		if shellMode {
			availW = m.width
		}
		if availW < 4 {
			availW = 4
		}
		if m.input.Value() == "" {
			return strings.Repeat(" ", padW) + prompt + fg(t.Dimmed, "esc · type a command")
		}
		return strings.Repeat(" ", padW) + prompt + fgShellInput(shellMode, t.InputText, truncate(m.input.Value(), availW))
	}

	// Focused: render multi-line with cursor, word-wrapping logical lines.
	curLogLine := m.input.Line()
	curLineInfo := m.input.LineInfo()
	curLogCol := curLineInfo.StartColumn + curLineInfo.ColumnOffset

	logicalLines := strings.Split(m.input.Value(), "\n")
	if len(logicalLines) == 0 {
		logicalLines = []string{""}
	}

	var rendered []string
	firstVisualLine := true

	for li, line := range logicalLines {
		runes := []rune(line)
		wW := contW
		if li == 0 {
			wW = line0W
		}

		// Split logical line into visual chunks (word-wrap).
		type chunk struct {
			runes    []rune
			logStart int // column offset within logical line
		}
		var chunks []chunk
		if len(runes) == 0 {
			chunks = []chunk{{runes: []rune{}, logStart: 0}}
		} else {
			for start := 0; start < len(runes); start += wW {
				end := start + wW
				if end > len(runes) {
					end = len(runes)
				}
				chunks = append(chunks, chunk{runes: runes[start:end], logStart: start})
			}
		}

		for ci, ch := range chunks {
			// Build prefix.
			var prefix string
			if firstVisualLine {
				if shellMode {
					prefix = ""
				} else {
					prefix = strings.Repeat(" ", padW) + fg(t.InputPrompt, promptStr)
				}
				firstVisualLine = false
			} else {
				if shellMode {
					prefix = ""
				} else {
					prefix = strings.Repeat(" ", padW)
				}
			}

			// Is cursor in this chunk?
			if li == curLogLine && m.focus == paneCommand {
				chunkEnd := ch.logStart + len(ch.runes)
				isLast := ci == len(chunks)-1
				if curLogCol >= ch.logStart && (curLogCol < chunkEnd || isLast) {
					colInChunk := curLogCol - ch.logStart
					if colInChunk > len(ch.runes) {
						colInChunk = len(ch.runes)
					}
					before := string(ch.runes[:colInChunk])
					var curChar, after string
					if colInChunk < len(ch.runes) {
						curChar = string(ch.runes[colInChunk])
						after = string(ch.runes[colInChunk+1:])
					} else {
						curChar = " "
					}
					var cursorSeq string
					if m.cursorVisible {
						cursorSeq = "\033[7m" + curChar + "\033[27m"
					} else {
						cursorSeq = fg(t.InputText, curChar)
						if curChar == " " {
							cursorSeq = " "
						}
					}
					rendered = append(rendered,
						prefix+fgShellInput(shellMode, t.InputText, before)+cursorSeq+fgShellInput(shellMode, t.InputText, after))
					continue
				}
			}
			rendered = append(rendered, prefix+fgShellInput(shellMode, t.InputText, string(ch.runes)))
		}
	}

	return strings.Join(rendered, "\n")
}

// renderStatusSep renders the separator between the command input and the status bar.
// Accent-colored when the command pane is focused; bright red in shell mode.
func (m Model) renderStatusSep() string {
	t := ActiveTheme
	if strings.HasPrefix(m.input.Value(), "!") {
		return shellBorderColor + strings.Repeat("─", m.width) + "\033[0m"
	}
	if m.focus == paneCommand || m.focus == paneStatus {
		return fg(t.Accent, strings.Repeat("─", m.width))
	}
	return fg(t.Dimmed, strings.Repeat("─", m.width))
}

// renderCompletionLines renders the expanded status area content.
// Priority: completions > statusLines. Returns nil when neither is active.
func (m Model) renderCompletionLines() []string {
	t := ActiveTheme

	// Completion popup
	if len(m.cmdComplete) > 0 {
		maxCmd, maxArg := 0, 0
		for _, c := range m.cmdComplete {
			if len(c.cmd) > maxCmd {
				maxCmd = len(c.cmd)
			}
			if len(c.arg) > maxArg {
				maxArg = len(c.arg)
			}
		}
		lines := make([]string, len(m.cmdComplete))
		for i, c := range m.cmdComplete {
			cmdPart := fmt.Sprintf(" %-*s  ", maxCmd, c.cmd)
			argPart := fmt.Sprintf("%-*s  ", maxArg, c.arg)
			if i == m.cmdCompleteIdx {
				lines[i] = fgBold(t.Accent, cmdPart) + fg(t.ContentDimmed, argPart) + fg(t.ContentText, c.desc)
			} else {
				lines[i] = fg(t.NavText, cmdPart) + fg(t.ContentDimmed, argPart+c.desc)
			}
		}
		return lines
	}

	// Param picker (second level: /cmd <partial arg>)
	if len(m.paramItems) > 0 {
		lines := make([]string, len(m.paramItems))
		for i, p := range m.paramItems {
			var display string
			if p.desc != "" {
				display = fmt.Sprintf("%-18s  %s", p.cmd, p.desc)
			} else {
				display = p.cmd
			}
			display = truncate(display, m.width-2)
			if i == m.paramIdx {
				lines[i] = fgBold(t.Accent, " "+display)
			} else {
				lines[i] = fg(t.NavText, " "+display) + fg(t.ContentDimmed, "")
			}
		}
		return lines
	}

	// Multi-line status content (/help, /tags, /collections, command output)
	if len(m.statusLines) > 0 {
		// Determine max visible lines — cap at 30% of terminal height.
		maxVisible := m.height * 30 / 100
		if maxVisible < 3 {
			maxVisible = 3
		}
		start := m.statusScroll
		if start > len(m.statusLines)-1 {
			start = len(m.statusLines) - 1
		}
		end := start + maxVisible
		if end > len(m.statusLines) {
			end = len(m.statusLines)
		}
		visible := m.statusLines[start:end]
		lines := make([]string, len(visible))
		for i, l := range visible {
			if m.statusErr {
				lines[i] = fg(lipgloss.Color("#FF6B6B"), " "+truncate(l, m.width-1))
			} else {
				lines[i] = fg(t.ContentText, " "+truncate(l, m.width-1))
			}
		}
		return lines
	}

	return nil
}

// renderStatusLine renders the bottom status bar line.
// Priority: selectionMode > chatMode > pendingConfirmMsg > navFilter > statusMsg > empty.
func (m Model) renderStatusLine() string {
	t := ActiveTheme
	if m.populateRunning && !m.selectionMode {
		return renderWaveIndicator(m.spinnerFrame, m.populateLabel, t.StreamingText, t.Dimmed)
	}
	if m.askxStreaming && !m.selectionMode {
		label := "askX streaming · " + m.askxResolvedProfile
		return renderWaveIndicator(m.spinnerFrame, label, t.StreamingText, t.Dimmed)
	}
	if m.chatMode && !m.selectionMode && m.pendingConfirmMsg == "" {
		return m.renderChatStatusLine()
	}
	if m.ttsPlayer.Playing() && m.contentTTSText != "" && !m.selectionMode {
		rate := m.cfg.TTSRate
		if rate <= 0 {
			rate = 200
		}
		label := fmt.Sprintf("♪ article  say  %d wpm  [ slower  ] faster", rate)
		return renderWaveIndicator(m.spinnerFrame, label, t.StreamingText, t.Dimmed)
	}
	if m.selectionMode {
		return fgBold(t.Accent, truncate(" selection mode — drag to select · Cmd+C to copy · Ctrl+S or Esc to exit", m.width))
	}
	if m.navFilter != "" {
		return fg(t.Accent, truncate(" "+m.navFilter, m.width))
	}
	if m.statusMsg != "" {
		if m.statusErr || strings.HasPrefix(m.statusMsg, "✗") {
			return fgBold(t.StatusError, truncate(" "+m.statusMsg, m.width))
		}
		return fg(t.StatusText, truncate(" "+m.statusMsg, m.width))
	}
	// Idle: show context stats for the active tab/sub-tab.
	if m.activeTab == tabLibrary {
		switch m.navSubTab {
		case navSubTabArticles:
			if m.navLoaded {
				unread := 0
				for _, item := range m.navItemsAll {
					if !item.read {
						unread++
					}
				}
				return fg(t.Dimmed, fmt.Sprintf(" Articles · %d total · %d unread", len(m.navItemsAll), unread))
			}
		case navSubTabCollections:
			if m.collectionsLoaded {
				n := 0
				for _, r := range m.navRows {
					if r.kind == rowCollection {
						n++
					}
				}
				return fg(t.Dimmed, fmt.Sprintf(" Collections · %d total", n))
			}
		case navSubTabWorkspaces:
			if m.workspacesLoaded {
				return fg(t.Dimmed, fmt.Sprintf(" Workspaces · %d total", len(m.workspaceItems)))
			}
		}
	}
	return ""
}

// hintsFor returns context-sensitive key hints for the status bar.
func (m Model) hintsFor() string {
	switch m.activeTab {
	case tabLibrary:
		return " j/k navigate · Tab pane · s speak · / command · 1·2·3 tabs · q quit · ? help"
	case tabAgent:
		return " j/k navigate · R run · D dry-run · 1·2·3 tabs · q quit · ? help"
	case tabStats:
		return " j/k navigate · r refresh · 1·2·3 tabs · q quit · ? help"
	default:
		return " 1·2·3 tabs · q quit · ? help"
	}
}

// padRight pads a string (which may contain ANSI codes) to width visible chars.
func padRight(s string, width int) string {
	visible := lipgloss.Width(s)
	if visible >= width {
		return s
	}
	return s + strings.Repeat(" ", width-visible)
}

// ── Resource overlay ──────────────────────────────────────────────────────────

// renderResourceOverlay renders the full-screen resource file viewer.
func (m Model) renderResourceOverlay() string {
	t := ActiveTheme
	w := m.width
	h := m.height

	// Layout: top bar (2 lines) + content + hint bar (2 lines)
	contentH := h - 4
	if contentH < 1 {
		contentH = 1
	}

	var out []string

	// Top bar: "arc │ resource: <name>"
	left := fgBold(t.TopBarText, "arc") + fg(t.Dimmed, " │ ") + fg(t.TopBarText, "resource: "+m.resourceName)
	out = append(out, " "+left)
	out = append(out, fg(t.BoxBorder, strings.Repeat("─", w)))

	// Scrollable content.
	start := m.resourceScroll
	end := start + contentH
	if end > len(m.resourceLines) {
		end = len(m.resourceLines)
	}
	for i := start; i < end; i++ {
		line := m.resourceLines[i]
		if i == m.resourceCursor {
			out = append(out, fgBold(t.InputPrompt, "▶ ")+fg(t.TopBarText, line))
		} else {
			out = append(out, fg(t.Dimmed, "  ")+fg(t.ChatAssistant, line))
		}
	}
	// Pad remaining content lines.
	for len(out) < h-2 {
		out = append(out, "")
	}

	// Position indicator — bottom-right corner of content area.
	total := len(m.resourceLines)
	if total > 0 {
		pct := 0
		if total > 1 {
			pct = m.resourceCursor * 100 / (total - 1)
		}
		indicator := fmt.Sprintf("line %d/%d (%d%%) ", m.resourceCursor+1, total, pct)
		lastIdx := len(out) - 1
		base := out[lastIdx]
		baseW := lipgloss.Width(base)
		indW := len([]rune(indicator))
		pad := w - baseW - indW
		if pad < 0 {
			pad = 0
		}
		out[lastIdx] = base + strings.Repeat(" ", pad) + fg(t.ContentDimmed, indicator)
	}

	// Hint bar: separator + status/hints line.
	out = append(out, fg(t.BoxBorder, strings.Repeat("─", w)))
	if m.ttsPlayer.Playing() {
		rate := m.cfg.TTSRate
		if rate <= 0 {
			rate = 200
		}
		label := fmt.Sprintf("♪ %s  say  %d wpm  [ slower  ] faster", m.resourceName, rate)
		out = append(out, renderWaveIndicator(m.spinnerFrame, label, t.StreamingText, t.Dimmed))
	} else {
		out = append(out, " "+fg(t.Dimmed, "↑↓ / PgUp PgDn  move  ·  g/G  top/bottom  ·  s  speak  ·  e  edit  ·  Ctrl+X  close"))
	}

	// Safety clamp.
	if len(out) > h {
		out = out[:h]
	}
	for len(out) < h {
		out = append(out, "")
	}
	return strings.Join(out, "\n")
}

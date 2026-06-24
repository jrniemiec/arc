package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
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

const (
	topBarHeight  = 2 // tab bar line + separator
	hintBarHeight = 1 // bottom hints line
	cmdBarHeight  = 1 // command input line + separator = 2, but for scaffold just 1
	leftPaneWidth = 26
)

// ── View ──────────────────────────────────────────────────────────────────────

// View implements tea.Model.
func (m Model) View() string {
	if m.width == 0 {
		return ""
	}
	t := ActiveTheme

	var sb strings.Builder

	// 1. Top tab bar
	sb.WriteString(m.renderTabBar())
	sb.WriteByte('\n')
	sb.WriteString(sep(m.width))
	sb.WriteByte('\n')

	// 2. Main area (left nav + right content)
	mainHeight := m.height - topBarHeight - cmdBarHeight - 1 - hintBarHeight
	if mainHeight < 1 {
		mainHeight = 1
	}
	sb.WriteString(m.renderMainArea(mainHeight))

	// 3. Separator + command input
	sb.WriteByte('\n')
	sb.WriteString(sep(m.width))
	sb.WriteByte('\n')
	sb.WriteString(m.renderCommandInput())

	// 4. Hints bar
	sb.WriteByte('\n')
	sb.WriteString(fg(t.StatusText, m.hintsFor()))

	return sb.String()
}

// renderTabBar renders the top tab bar line.
func (m Model) renderTabBar() string {
	t := ActiveTheme
	var parts []string
	for i := tab(0); i < tabCount; i++ {
		label := fmt.Sprintf(" %s ", i.String())
		if i == m.activeTab {
			parts = append(parts, fgBold(t.TabActive, "["+strings.TrimSpace(label)+"]"))
		} else {
			parts = append(parts, fg(t.TabInactive, " "+strings.TrimSpace(label)+" "))
		}
	}
	bar := strings.Join(parts, fg(t.Dimmed, "  "))
	// right-align tab number hints
	hint := fg(t.Dimmed, "1·2·3 tabs")
	gap := m.width - lipgloss.Width(bar) - lipgloss.Width(hint)
	if gap < 1 {
		gap = 1
	}
	return bar + strings.Repeat(" ", gap) + hint
}

// renderMainArea renders the split left/right pane for the current tab.
func (m Model) renderMainArea(height int) string {
	t := ActiveTheme
	rightWidth := m.width - leftPaneWidth - 1 // 1 for the vertical divider
	if rightWidth < 10 {
		rightWidth = 10
	}

	leftLines := m.renderNavPane(height)
	rightLines := m.renderContentPane(height, rightWidth)

	divider := fg(t.Dimmed, "│")

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
		l = padRight(l, leftPaneWidth)
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

	header := fgBold(t.NavGroup, m.activeTab.String())
	lines = append(lines, header)
	lines = append(lines, "")

	switch m.activeTab {
	case tabLibrary:
		lines = append(lines, m.renderNavLibrary(height-2)...)
	case tabAgent:
		lines = append(lines, fg(t.NavDimmed, "(coming soon)"))
	case tabStats:
		lines = append(lines, fg(t.NavDimmed, "(stats)"))
	}

	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines[:height]
}

// renderNavLibrary renders the article list into the nav pane.
func (m Model) renderNavLibrary(maxLines int) []string {
	t := ActiveTheme

	if !m.navLoaded {
		return []string{fg(t.NavDimmed, "loading…")}
	}
	if m.navErr != "" {
		return []string{fg(t.NavDimmed, "error: "+truncate(m.navErr, leftPaneWidth-2))}
	}
	if len(m.navItems) == 0 {
		return []string{fg(t.NavDimmed, "(empty)")}
	}

	var lines []string
	end := m.navScroll + maxLines
	if end > len(m.navItems) {
		end = len(m.navItems)
	}
	for i := m.navScroll; i < end; i++ {
		item := m.navItems[i]
		var dot string
		if !item.read {
			dot = fg(t.NavMark, "•")
		} else {
			dot = " "
		}
		title := truncate(item.title, leftPaneWidth-3) // 1 dot + 1 space + title
		line := dot + " " + fg(t.NavText, title)
		if i == m.navCursor {
			line = reverse(dot + " " + title)
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
	switch m.activeTab {
	case tabLibrary:
		return m.renderContentLibrary(height, width)
	case tabStats:
		return m.renderContentStats(height, width)
	default:
		return m.renderContentPlaceholder(height, width)
	}
}

func (m Model) renderContentLibrary(height, width int) []string {
	t := ActiveTheme
	var lines []string

	if len(m.navItems) == 0 || m.navCursor < 0 || m.navCursor >= len(m.navItems) {
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

	item := m.navItems[m.navCursor]
	lines = append(lines, fgBold(t.ContentTitle, truncate(item.title, width-1)))
	lines = append(lines, fg(t.ContentDimmed, item.date.Format("2006-01-02")))
	lines = append(lines, "")

	flash := m.flashContent()
	if flash != "" {
		// Wrap flash text to content width and emit lines.
		for _, para := range strings.Split(strings.TrimSpace(flash), "\n") {
			wrapped := wordWrap(para, width-2)
			for _, wl := range wrapped {
				lines = append(lines, fg(t.ContentText, wl))
			}
		}
	} else {
		lines = append(lines, fg(t.ContentDimmed, "(no flash summary — press  r  to generate)"))
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
		lines = append(lines, row("Cost this month", formatUSD(s.CostThisMonth)))
		lines = append(lines, row("Cost total", formatUSD(s.CostTotal)))
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

// wordWrap splits text into lines of at most maxWidth runes.
func wordWrap(text string, maxWidth int) []string {
	if maxWidth < 10 {
		maxWidth = 10
	}
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


// renderCommandInput renders the command input line.
func (m Model) renderCommandInput() string {
	t := ActiveTheme
	prompt := fg(t.InputPrompt, "> ")
	if m.focus == paneCommand {
		var cursor string
		if m.cursorVisible {
			cursor = reverse(" ")
		} else {
			cursor = " "
		}
		return prompt + cursor + " " + fg(t.InputText, "type a command, / to search")
	}
	return prompt + fg(t.Dimmed, "_")
}

// hintsFor returns context-sensitive key hints for the status bar.
func (m Model) hintsFor() string {
	switch m.activeTab {
	case tabLibrary:
		return " j/k navigate · Tab pane · / command · 1·2·3 tabs · q quit · ? help"
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

package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	storefs "github.com/jrniemiec/arc/store/fs"
)

// ── Toggle & lifecycle ──────────────────────────────────────────────────────

// togglePreview toggles the preview pane (Ctrl+O). Workspace-only.
func (m *Model) togglePreview() {
	if m.previewOpen {
		m.closePreview()
		return
	}
	if m.navSubTab != navSubTabWorkspaces {
		m.setStatusError("preview: only available in workspace tab")
		return
	}
	// Mutual exclusion: close scratch and askX.
	if m.scratchOpen {
		m.closeScratch()
	}
	if m.askxOpen {
		m.closeAskX()
		m.clearAskXInput()
	}
	m.previewOpen = true
	m.updatePreviewContent()
}

// closePreview tears down the preview pane state.
func (m *Model) closePreview() {
	m.previewOpen = false
	m.previewFocused = false
	m.previewScroll = 0
	m.previewLines = nil
	m.previewTitle = ""
	m.previewLastSlug = ""
	m.previewLastResource = ""
}

// maybeUpdatePreview updates the preview pane content if it is open.
// Called from nav cursor change paths in the workspace tab.
func (m *Model) maybeUpdatePreview() {
	if !m.previewOpen {
		return
	}
	m.updatePreviewContent()
}

// ── Content loading ─────────────────────────────────────────────────────────

// updatePreviewContent loads content for the currently selected workspace row.
// Non-previewable rows are a no-op (sticky: last content remains).
func (m *Model) updatePreviewContent() {
	row := m.selectedWsRow()
	if row == nil {
		return
	}

	switch row.kind {
	case wsRowArticle:
		if row.slug == m.previewLastSlug {
			return // already loaded
		}
		m.loadPreviewArticle(row)

	case wsRowResource, wsRowOutcome:
		name := row.resourceName
		if row.kind == wsRowOutcome {
			name = row.outcomeName
		}
		if name == m.previewLastResource {
			return // already loaded
		}
		m.loadPreviewFile(row)

	default:
		// Non-previewable row: keep last content (sticky).
	}
}

// loadPreviewArticle loads article content (flash/summary/body) into the preview pane.
// Same format as openArticleOverlay.
func (m *Model) loadPreviewArticle(row *wsRow) {
	if row.wsIdx < 0 || row.wsIdx >= len(m.workspaceItems) {
		return
	}

	// Resolve article root from slug.
	root := filepath.Join(m.cfg.DataRoot, "articles", row.slug)
	files := storefs.ProbeFiles(root)
	files.Summary = storefs.ResolveSummary(root, m.cfg.PreferredStyles, m.cfg.PreferredModels)
	files.Flash = storefs.ResolveFlash(root, m.cfg.PreferredModels)

	type part struct {
		label string
		path  string
	}
	var parts []part
	if files.Flash != "" {
		parts = append(parts, part{"Flash", files.Flash})
	}
	if files.Summary != "" {
		parts = append(parts, part{"Summary", files.Summary})
	}
	if files.Body != "" {
		parts = append(parts, part{"Body", files.Body})
	}
	if len(parts) == 0 {
		m.previewLines = []string{"(no content files available)"}
		m.previewTitle = row.title
		m.previewLastSlug = row.slug
		m.previewLastResource = ""
		m.previewScroll = 0
		return
	}

	var sb strings.Builder
	for i, p := range parts {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		pad := 60 - 4 - len(p.label) - 1
		if pad < 3 {
			pad = 3
		}
		sb.WriteString(fmt.Sprintf("── %s %s\n\n", p.label, strings.Repeat("─", pad)))
		data, err := os.ReadFile(p.path)
		if err != nil {
			sb.WriteString(fmt.Sprintf("[error reading %s: %v]", p.label, err))
		} else {
			sb.WriteString(strings.TrimSpace(string(data)))
		}
	}

	title := row.title
	if title == "" {
		title = row.slug
	}
	m.previewTitle = title
	m.previewLines = splitLines(sb.String())
	m.previewLastSlug = row.slug
	m.previewLastResource = ""
	m.previewScroll = 0
}

// loadPreviewFile loads a workspace resource or outcome file into the preview pane.
// Same logic as openWorkspaceFile.
func (m *Model) loadPreviewFile(row *wsRow) {
	if row.wsIdx < 0 || row.wsIdx >= len(m.workspaceItems) {
		return
	}
	ws := m.workspaceItems[row.wsIdx]

	var subdir, filename string
	switch row.kind {
	case wsRowResource:
		subdir = "resources"
		filename = row.resourceName
	case wsRowOutcome:
		subdir = "outcomes"
		filename = row.outcomeName
	default:
		return
	}

	filePath := filepath.Join(storefs.WorkspaceDir(m.cfg.DataRoot, ws.name), subdir, filename)
	data, err := os.ReadFile(filePath)
	if err != nil {
		m.previewLines = []string{fmt.Sprintf("[error: %v]", err)}
		m.previewTitle = filename
		m.previewLastResource = filename
		m.previewLastSlug = ""
		m.previewScroll = 0
		return
	}

	// Binary check.
	check := data
	if len(check) > 512 {
		check = check[:512]
	}
	if !utf8.Valid(check) {
		m.previewLines = []string{fmt.Sprintf("%q is not a text file", filename)}
		m.previewTitle = filename
		m.previewLastResource = filename
		m.previewLastSlug = ""
		m.previewScroll = 0
		return
	}

	const maxBytes = 200 * 1024
	if len(data) > maxBytes {
		data = append(data[:maxBytes], []byte("\n[file truncated at 200 KB]")...)
	}

	m.previewTitle = filename
	m.previewLines = splitLines(string(data))
	m.previewLastResource = filename
	m.previewLastSlug = ""
	m.previewScroll = 0
}

// ── Rendering ───────────────────────────────────────────────────────────────

// renderPreviewPane renders the preview split pane content.
func (m Model) renderPreviewPane(height, width int) []string {
	t := ActiveTheme
	var lines []string

	// Header: title on the left, hints on the right.
	title := m.previewTitle
	if title == "" {
		title = "Preview"
	}
	label := " " + title + " "
	hints := " V view "
	sepLen := width - len([]rune(label)) - len([]rune(hints))
	if sepLen < 0 {
		sepLen = 0
	}
	leftSep := sepLen / 2
	rightSep := sepLen - leftSep
	headerColor := t.Dimmed
	if m.previewFocused && m.focus == paneContent {
		headerColor = t.Accent
	}
	header := fg(headerColor, strings.Repeat("─", leftSep)+label+strings.Repeat("─", rightSep)+hints)
	lines = append(lines, header)

	viewH := height - 1
	if viewH < 1 {
		viewH = 1
	}

	dl := m.previewLines
	if len(dl) == 0 {
		dl = []string{"(no preview content)"}
	}

	total := len(dl)
	start := m.previewScroll
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
		lines = append(lines, dl[i])
	}

	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines[:height]
}

// previewViewH returns the usable height for preview content (excluding header).
func (m *Model) previewViewH() int {
	mainH := m.height - 6 - m.completionCount()
	h := mainH / 3
	if h < 3 {
		h = 3
	}
	return h - 1
}

// ── Key handling ────────────────────────────────────────────────────────────

// handlePreviewKey handles keys when the preview pane is focused.
func (m *Model) handlePreviewKey(msg tea.KeyMsg) tea.Cmd {
	viewH := m.previewViewH()
	total := len(m.previewLines)

	switch {
	case key.Matches(msg, keys.NavUp):
		if m.previewScroll > 0 {
			m.previewScroll--
		}
	case key.Matches(msg, keys.NavDown):
		maxScroll := total - viewH
		if maxScroll < 0 {
			maxScroll = 0
		}
		if m.previewScroll < maxScroll {
			m.previewScroll++
		}
	case key.Matches(msg, keys.PageUp):
		m.previewScroll -= viewH
		if m.previewScroll < 0 {
			m.previewScroll = 0
		}
	case key.Matches(msg, keys.PageDown):
		m.previewScroll += viewH
		maxScroll := total - viewH
		if maxScroll < 0 {
			maxScroll = 0
		}
		if m.previewScroll > maxScroll {
			m.previewScroll = maxScroll
		}
	case key.Matches(msg, keys.Home):
		m.previewScroll = 0
	case key.Matches(msg, keys.End):
		maxScroll := total - viewH
		if maxScroll < 0 {
			maxScroll = 0
		}
		m.previewScroll = maxScroll
	case key.Matches(msg, keys.Back):
		m.previewFocused = false
	}
	return nil
}

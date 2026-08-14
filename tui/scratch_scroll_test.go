package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textarea"
)

// scratchModel builds a Model with the scratch pane open and focused, holding a
// single selectable block of n lines under a date separator.
func scratchModel(termH, n int) Model {
	var lines []string
	lines = append(lines, "---------- Fri, August 14, 2026 10:03 ----------")
	blockStart := len(lines)
	lines = append(lines, "• Before Poland - deal with:")
	for i := 1; i < n; i++ {
		lines = append(lines, "- item")
	}

	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.SetHeight(1)

	return Model{
		width:          120,
		height:         termH,
		focus:          paneContent,
		activeTab:      tabLibrary,
		navSubTab:      navSubTabWorkspaces,
		input:          ta,
		scratchOpen:    true,
		scratchFocused: true,
		scratchLines:   lines,
		scratchBlocks: []scratchBlock{
			{startLine: 0, endLine: 0, isSep: true},
			{startLine: blockStart, endLine: len(lines) - 1, text: strings.Join(lines[blockStart:], "\n")},
		},
		scratchBlockCursor: 1,
	}
}

// renderScreen renders the full frame the way View() assembles it.
func renderScreen(m Model) []string {
	bot := m.renderBottomLines()
	mainH := m.height - topBarHeight - len(bot)
	if mainH < 1 {
		mainH = 1
	}
	out := []string{m.renderTabBar(), m.renderSplitSep(m.width, true)}
	out = append(out, strings.Split(m.renderMainArea(mainH), "\n")...)
	return append(out, bot...)
}

// The bottom border of the selected block must be visible once the pane is
// scrolled to the bottom: the pane height used by the scroll math has to match
// the one the renderer actually uses.
func TestScratchScrollToBottomShowsBoxBottom(t *testing.T) {
	// A leftover multi-line input value is the case that regressed: the unfocused
	// input renders as a single row, but the old layout math reserved up to three.
	inputs := []string{"", "one line", "line one\nline two\nline three\nline four"}

	for _, in := range inputs {
		for termH := 20; termH <= 60; termH++ {
			for n := 1; n <= 12; n++ {
				m := scratchModel(termH, n)
				m.input.SetValue(in)
				m.scratchScrollToBottom()

				screen := renderScreen(m)
				if !strings.Contains(strings.Join(screen, "\n"), "╰") {
					t.Fatalf("input=%q termH=%d block=%d lines: box bottom border missing\n%s",
						in, termH, n, strings.Join(screen, "\n"))
				}
				if len(screen) != termH {
					t.Fatalf("input=%q termH=%d block=%d lines: rendered %d rows, want %d",
						in, termH, n, len(screen), termH)
				}
			}
		}
	}
}

// mainAreaHeight is what Update-side scroll math reads; it must equal the height
// View() actually hands to renderMainArea.
func TestMainAreaHeightMatchesRenderedLayout(t *testing.T) {
	for termH := 12; termH <= 60; termH++ {
		m := scratchModel(termH, 5)
		m.input.SetValue("line one\nline two\nline three")

		want := termH - topBarHeight - len(m.renderBottomLines())
		if want < 1 {
			want = 1
		}
		if got := m.mainAreaHeight(); got != want {
			t.Fatalf("termH=%d: mainAreaHeight=%d, rendered main area=%d", termH, got, want)
		}
	}
}

// The cheap row count used by layout math must equal what the renderer emits.
func TestBottomChromeRowsMatchesRender(t *testing.T) {
	cases := []struct {
		name  string
		mutir func(*Model)
	}{
		{"idle", func(m *Model) {}},
		{"focused input", func(m *Model) { m.focus = paneCommand; m.input.Focus() }},
		{"multi-line input, unfocused", func(m *Model) { m.input.SetValue("a\nb\nc\nd") }},
		{"multi-line input, focused", func(m *Model) {
			m.focus = paneCommand
			m.input.Focus()
			m.input.SetValue("a\nb\nc\nd")
		}},
		{"completions", func(m *Model) {
			m.cmdComplete = []cmdCompletion{{cmd: "/scratch", desc: "notes"}, {cmd: "/search", desc: "find"}}
		}},
		{"param hint only", func(m *Model) { m.paramHint = "acts on: workspace foo" }},
		{"status lines", func(m *Model) { m.statusLines = []string{"one", "two", "three", "four"} }},
		{"agent confirm", func(m *Model) { m.agentConfirmLines = []string{"confirm?", "y/n"} }},
		{"ingest running", func(m *Model) { m.ingestRunning = true; m.ingestLog = []string{"a", "b"} }},
		{"ingest with cost", func(m *Model) {
			m.ingestRunning = true
			m.ingestCostEstimate = "$0.01"
		}},
	}
	for _, tc := range cases {
		m := scratchModel(40, 5)
		tc.mutir(&m)
		if got, want := m.bottomChromeRows(), len(m.renderBottomLines()); got != want {
			t.Errorf("%s: bottomChromeRows=%d, rendered=%d", tc.name, got, want)
		}
	}
}

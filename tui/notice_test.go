package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestNoticeShowsOnceAndRestoresFocus(t *testing.T) {
	m := &Model{focus: paneContent}

	m.showNotice("sync-diverged", "Sync stopped", []string{"body"})
	if m.focus != paneNotice {
		t.Fatalf("focus = %v, want paneNotice", m.focus)
	}

	m.handleNoticeKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if m.focus != paneContent {
		t.Errorf("focus = %v after dismiss, want the pane it interrupted", m.focus)
	}

	// Divergence is re-read on every status refresh. A box that came back each
	// time would train the user to dismiss it unread.
	m.showNotice("sync-diverged", "Sync stopped", []string{"body"})
	if m.focus == paneNotice {
		t.Error("notice raised a second time for the same key")
	}
}

func TestNoticeRendersTitleAndBody(t *testing.T) {
	m := &Model{focus: paneContent, width: 100, height: 30}
	m.showNotice("k", "Sync stopped — this tree has diverged", divergedNoticeLines())

	out := m.View()
	for _, want := range []string{
		"Sync stopped",
		"arc sync repair",
		"press any key to continue",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("View() missing %q", want)
		}
	}
}

// A narrow terminal must degrade rather than tear the border.
func TestNoticeSurvivesNarrowTerminal(t *testing.T) {
	m := &Model{focus: paneContent, width: 24, height: 12}
	m.showNotice("k", "Sync stopped", divergedNoticeLines())

	out := m.View()
	if lines := strings.Split(out, "\n"); len(lines) != 12 {
		t.Errorf("rendered %d lines, want exactly height (12)", len(lines))
	}
}

// A command must never be truncated to fit. Padding silently cut
// "…&& git push" down to "…&& git pus" at 64 columns — still copy-pasteable,
// and broken. Narrow terminals wrap it instead.
func TestNoticeNeverTruncatesACommand(t *testing.T) {
	// A synthetic command rather than the live message: this guards the renderer,
	// so it must keep testing wrapping even when the wording gets shorter.
	const want = "git fetch origin && git merge origin/main && git push"
	body := []string{"To repair, in the arc data directory:", "    " + want}

	for _, width := range []int{100, 80, 64, 50, 40} {
		m := &Model{focus: paneContent, width: width, height: 30}
		m.showNotice("k", "Sync stopped", body)

		// Collapse the wrap: the command may span lines, but every token must
		// survive and stay in order. Border columns sit between those lines, so
		// they come out before the join.
		text := strings.ReplaceAll(stripANSI(m.View()), "│", " ")
		flat := strings.Join(strings.Fields(text), " ")
		if !strings.Contains(flat, want) {
			t.Errorf("width %d: command not intact in output", width)
		}
	}
}

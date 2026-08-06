package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/jrniemiec/arc/service"
	"github.com/jrniemiec/arc/tts"
)

func TestDegradedBadge(t *testing.T) {
	cases := map[string]string{
		service.DegradedSemantic: "⚠ keyword only",
		service.DegradedKeyword:  "⚠ semantic only",
		"":                       "",
	}
	for degraded, want := range cases {
		if got := degradedBadge(degraded); got != want {
			t.Errorf("degradedBadge(%q) = %q, want %q", degraded, got, want)
		}
	}
}

// The badge lives in navFilter rather than statusMsg because renderStatusLine
// gives navFilter priority. A search that returns results always sets
// navFilter, so a degradation left only in statusMsg is never drawn — which is
// exactly how a broken semantic half stays invisible.
func TestNavFilterOutranksStatusMsgInStatusLine(t *testing.T) {
	m := Model{width: 120, ttsPlayer: tts.NewPlayer("", 0)}
	m.navFilter = `search: "q" · 3 results · ⚠ keyword only  ·  /clear to reset`
	m.statusMsg = "⚠ semantic search unavailable"

	line := m.renderStatusLine()
	if !strings.Contains(line, "⚠ keyword only") {
		t.Fatalf("nav filter badge missing from status line: %q", line)
	}
	if strings.Contains(line, "semantic search unavailable") {
		t.Fatalf("statusMsg unexpectedly won over navFilter — badge no longer needed in navFilter: %q", line)
	}
}

func TestSourceCounts(t *testing.T) {
	mixed := []service.SearchResult{
		{Source: "both"}, {Source: "both"}, {Source: "fts"}, {Source: "fts"},
	}
	if got, want := sourceCounts(mixed), " (2 both, 2 fts)"; got != want {
		t.Errorf("sourceCounts(mixed) = %q, want %q", got, want)
	}
	// Single-source sets name the source without a count. They must not be
	// silent: "which search found it" is the point, and most sets are uniform.
	uniform := []service.SearchResult{{Source: "vector"}, {Source: "vector"}}
	if got, want := sourceCounts(uniform), " (vector)"; got != want {
		t.Errorf("sourceCounts(uniform) = %q, want %q", got, want)
	}
	single := []service.SearchResult{{Source: "fts"}}
	if got, want := sourceCounts(single), " (fts)"; got != want {
		t.Errorf("sourceCounts(single hit) = %q, want %q", got, want)
	}
	if got := sourceCounts(nil); got != "" {
		t.Errorf("sourceCounts(nil) = %q, want \"\"", got)
	}
}

// The badge is appended after the title is truncated, so its width has to be
// reserved up front. If it is not, long titles push the row past the pane.
func TestSearchBadgeNeverOverflowsNavPane(t *testing.T) {
	const width = 40
	// navLoaded must be set or the renderer short-circuits to "loading…" and
	// the test measures nothing. navFilter is what search results actually set,
	// and it switches rows to numbered prefixes — so this exercises the real
	// search-result layout, not the browsing one.
	m := Model{
		width:            200,
		navWidthOverride: width,
		navLoaded:        true,
		navFilter:        `search: "q" · 3 results`,
		ttsPlayer:        tts.NewPlayer("", 0),
	}
	m.navItems = []navItem{
		{numID: 1, title: strings.Repeat("very long article title ", 6), searchSource: "vector"},
		{numID: 22, title: "Short", searchSource: "both"},
		{numID: 333, title: "Medium length title here", searchSource: "fts"},
	}

	lines := m.renderNavLibrary(len(m.navItems))
	if len(lines) != len(m.navItems) {
		t.Fatalf("expected %d rendered rows, got %d: %q", len(m.navItems), len(lines), lines)
	}
	for i, line := range lines {
		if w := lipgloss.Width(line); w > width {
			t.Errorf("row overflows nav pane: width %d > %d\n  %q", w, width, line)
		}
		want := "[" + m.navItems[i].searchSource + "]"
		if !strings.Contains(line, want) {
			t.Errorf("row %d missing badge %s: %q", i, want, line)
		}
	}
	// The long title must actually have been truncated, or the row never
	// competed with the badge for width and the test proves nothing.
	if !strings.Contains(lines[0], "…") {
		t.Errorf("long title was not truncated, badge width may not be reserved: %q", lines[0])
	}
}

// Badges are for search results only; browsing rows must stay clean.
func TestNoBadgeWithoutSearchSource(t *testing.T) {
	m := Model{width: 200, navWidthOverride: 40, navLoaded: true, ttsPlayer: tts.NewPlayer("", 0)}
	m.navItems = []navItem{{numID: 1, title: "Ordinary browsing row"}}

	lines := m.renderNavLibrary(1)
	if len(lines) != 1 {
		t.Fatalf("expected 1 rendered row, got %q", lines)
	}
	// Match the badge shapes, not a bare "[" — ANSI colour codes contain one.
	for _, src := range []string{"[fts]", "[vector]", "[both]"} {
		if strings.Contains(lines[0], src) {
			t.Errorf("browsing row carries badge %s: %q", src, lines[0])
		}
	}
}

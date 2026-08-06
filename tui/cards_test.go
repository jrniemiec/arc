package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/jrniemiec/arc/flashcards"
)

var sampleCards = []byte(`[
  {"type":"concept","front":"What is the fundamental difference between standard RAG and agentic RAG?","back":"Standard RAG is a linear pipeline. Agentic RAG inserts a decision step.","tags":["rag","architecture"]},
  {"type":"fact","front":"How many core capabilities?","back":"Three.","tags":["rag"]}
]`)

// ids returns the stable card IDs for sampleCards under the given slug.
func ids(t *testing.T, slug string) []string {
	t.Helper()
	cards, err := flashcards.Parse(slug, sampleCards)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out := make([]string, len(cards))
	for i, c := range cards {
		out[i] = c.ID
	}
	return out
}

func TestRenderCardsNothingRevealed(t *testing.T) {
	lines, _ := renderCards(sampleCards, "slug", nil, 80)
	joined := strings.Join(lines, "\n")

	if !strings.Contains(joined, "fundamental difference") {
		t.Error("question is missing")
	}
	if strings.Contains(joined, "linear pipeline") {
		t.Error("an answer leaked while nothing was revealed")
	}
	if strings.Contains(joined, "#rag") {
		t.Error("tags should stay hidden until reveal")
	}
}

func TestRenderCardsRevealsOnlyTheChosenCard(t *testing.T) {
	id := ids(t, "slug")
	lines, _ := renderCards(sampleCards, "slug", map[string]bool{id[1]: true}, 80)
	joined := strings.Join(lines, "\n")

	if !strings.Contains(joined, "Three.") {
		t.Error("revealed card is missing its answer")
	}
	if strings.Contains(joined, "linear pipeline") {
		t.Error("revealing one card leaked another card's answer")
	}
	// The untouched card keeps its collapsed one-line form.
	if !strings.Contains(joined, "fundamental difference") {
		t.Error("collapsed card lost its question")
	}
}

func TestRenderCardsAllRevealed(t *testing.T) {
	id := ids(t, "slug")
	lines, _ := renderCards(sampleCards, "slug", map[string]bool{id[0]: true, id[1]: true}, 80)
	joined := strings.Join(lines, "\n")

	for _, want := range []string{"linear pipeline", "Three.", "#rag", "#architecture", "Q  ", "A  "} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q", want)
		}
	}
}

// The line→card mapping is what space acts on: every rendered line must name
// the card it belongs to, in both collapsed and revealed form.
func TestRenderCardsLineMapping(t *testing.T) {
	id := ids(t, "slug")

	for _, revealed := range []map[string]bool{nil, {id[0]: true}, {id[0]: true, id[1]: true}} {
		lines, cardIDs := renderCards(sampleCards, "slug", revealed, 80)
		if len(lines) != len(cardIDs) {
			t.Fatalf("mapping length %d != lines %d", len(cardIDs), len(lines))
		}
		seen := map[string]bool{}
		for i, cid := range cardIDs {
			if cid == "" && strings.TrimSpace(lines[i]) != "" {
				t.Errorf("non-blank line %q maps to no card", lines[i])
			}
			if cid != "" {
				seen[cid] = true
			}
		}
		for _, want := range id {
			if !seen[want] {
				t.Errorf("card %s never appears in the mapping", want)
			}
		}
	}
}

func TestRenderCardsRespectsWidth(t *testing.T) {
	// Includes multibyte characters: real card text is full of → and —, which
	// are 3 bytes but one display column.
	wide := []byte(`[{"type":"concept","front":"Does query → embedding → retrieval — the linear path — still hold?","back":"No — the loop replaces it, as → shows.","tags":["a"]}]`)

	for _, data := range [][]byte{sampleCards, wide} {
		cards, err := flashcards.Parse("slug", data)
		if err != nil {
			t.Fatal(err)
		}
		all := map[string]bool{}
		for _, c := range cards {
			all[c.ID] = true
		}

		for _, w := range []int{40, 60, 100} {
			for _, revealed := range []map[string]bool{nil, all} {
				lines, _ := renderCards(data, "slug", revealed, w)
				for _, l := range lines {
					if got := lipgloss.Width(l); got > w {
						t.Errorf("width=%d: line is %d columns: %q", w, got, l)
					}
				}
			}
		}
	}
}

func TestRenderCardsBadJSONDoesNotPanic(t *testing.T) {
	lines, cardIDs := renderCards([]byte(`{not json`), "slug", nil, 80)
	if len(lines) != 1 || !strings.Contains(lines[0], "unreadable") {
		t.Errorf("want a single 'unreadable' line, got %v", lines)
	}
	if len(cardIDs) != len(lines) {
		t.Errorf("mapping length %d != lines %d", len(cardIDs), len(lines))
	}
}

func TestCardsHeaderReflectsRevealState(t *testing.T) {
	id := ids(t, "slug")

	tests := []struct {
		name     string
		revealed map[string]bool
		want     string
	}{
		{"none", nil, "[space] reveal"},
		{"some", map[string]bool{id[0]: true}, "1 revealed"},
		{"all", map[string]bool{id[0]: true, id[1]: true}, "all revealed"},
	}
	for _, tt := range tests {
		got := cardsHeader(sampleCards, "slug", tt.revealed)
		if !strings.Contains(got, tt.want) {
			t.Errorf("%s: header %q missing %q", tt.name, got, tt.want)
		}
		if !strings.Contains(got, "2") {
			t.Errorf("%s: header %q missing the card count", tt.name, got)
		}
		// Every state must advertise delete — it is otherwise undiscoverable.
		if !strings.Contains(got, "[D] delete") {
			t.Errorf("%s: header %q missing the delete hint", tt.name, got)
		}
	}
}

func TestCardIDsIn(t *testing.T) {
	got := cardIDsIn(sampleCards, "slug")
	if len(got) != 2 {
		t.Fatalf("got %d ids, want 2", len(got))
	}
	if got[0] == got[1] {
		t.Error("ids are not distinct")
	}
	if cardIDsIn([]byte("garbage"), "slug") != nil {
		t.Error("want nil for unparseable input")
	}
}

func TestWrapIndentHangingIndent(t *testing.T) {
	lines := wrapIndent("one two three four five six seven eight nine ten", 30, ">>> ", "    ")
	if len(lines) < 2 {
		t.Fatalf("expected wrapping, got %v", lines)
	}
	if !strings.HasPrefix(lines[0], ">>> ") {
		t.Errorf("first line prefix wrong: %q", lines[0])
	}
	for _, l := range lines[1:] {
		if !strings.HasPrefix(l, "    ") {
			t.Errorf("continuation line prefix wrong: %q", l)
		}
	}
}

// /flashcards and /flashcards-delete used to be refused outside the Articles
// sub-tab. selectedNavItem resolves an article row inside a collection and
// inside a workspace too, so the gate only forced a trip back to the Articles
// tab to act on the article already under the cursor. Both commands still act
// on exactly one article — no batch cost is implied by this.
func TestFlashcardsNotGatedOnSubTab(t *testing.T) {
	for _, tc := range []struct {
		name string
		sub  navSubTab
	}{
		{"articles", navSubTabArticles},
		{"collections", navSubTabCollections},
		{"workspaces", navSubTabWorkspaces},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, cmd := range []string{"/flashcards", "/cards", "/flashcards-delete", "/cards-delete"} {
				m := &Model{activeTab: tabLibrary, navSubTab: tc.sub}
				m.dispatchCommand(cmd)
				if strings.Contains(m.statusMsg, "only available in Articles context") {
					t.Errorf("%s refused on the %s sub-tab: %q", cmd, tc.name, m.statusMsg)
				}
			}
		})
	}
}

// The completion and /help lists have to agree with dispatch, or the command
// works but nothing advertises it.
func TestFlashcardsRegisteredInCollectionAndWorkspaceLists(t *testing.T) {
	has := func(cmds []cmdCompletion, name string) bool {
		for _, c := range cmds {
			if c.cmd == name {
				return true
			}
		}
		return false
	}

	for _, tc := range []struct {
		name string
		cmds []cmdCompletion
	}{
		{"collectionCommands", collectionCommands},
		{"workspaceCommands", workspaceCommands},
	} {
		for _, want := range []string{"/flashcards", "/flashcards-delete"} {
			if !has(tc.cmds, want) {
				t.Errorf("%s missing %s", tc.name, want)
			}
		}
	}

	m := &Model{activeTab: tabLibrary}
	for _, group := range []string{"collection", "workspace"} {
		out := strings.Join(m.helpLines(group), "\n")
		if !strings.Contains(out, "/flashcards") {
			t.Errorf("/help %s missing /flashcards: %q", group, out)
		}
	}
}

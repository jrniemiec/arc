package flashcards

import "testing"

func TestParseBareArray(t *testing.T) {
	// The shape every real file on disk uses.
	data := []byte(`[
		{"type":"concept","front":"What is X?","back":"X is Y.","tags":["a","b"]},
		{"type":"fact","front":"How many?","back":"Three."}
	]`)

	cards, err := Parse("my-article", data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(cards) != 2 {
		t.Fatalf("got %d cards, want 2", len(cards))
	}
	if cards[0].Front != "What is X?" || cards[0].Back != "X is Y." {
		t.Errorf("card 0 = %+v", cards[0])
	}
	if len(cards[0].Tags) != 2 {
		t.Errorf("tags = %v, want 2", cards[0].Tags)
	}
	if cards[0].ID == "" || cards[0].ID == cards[1].ID {
		t.Errorf("ids not distinct: %q %q", cards[0].ID, cards[1].ID)
	}
}

func TestParseWrapperObject(t *testing.T) {
	// The shape in DESIGN_LOG.md and testdata/, using q/a keys.
	data := []byte(`{
		"article_id":"sparse-attention",
		"model":"claude-sonnet-4-6",
		"cards":[{"id":"1","type":"concept","q":"What problem?","a":"Quadratic scaling.","tags":["attention"]}]
	}`)

	cards, err := Parse("sparse-attention", data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(cards) != 1 {
		t.Fatalf("got %d cards, want 1", len(cards))
	}
	if cards[0].Front != "What problem?" || cards[0].Back != "Quadratic scaling." {
		t.Errorf("card = %+v", cards[0])
	}
}

func TestParseRejectsNonDecks(t *testing.T) {
	// Valid JSON that is not a deck. The old json.Valid check let all of these
	// through and wrote them to disk.
	for _, in := range []string{
		`"just a string"`,
		`{}`,
		`[]`,
		`[{"type":"concept"}]`,             // no front, no back
		`[{"front":"Q only"}]`,             // no back
		`{"cards":[{"q":"","a":"orphan"}]}`, // empty front
	} {
		if _, err := Parse("slug", []byte(in)); err == nil {
			t.Errorf("Parse(%s) = nil error, want error", in)
		}
	}
}

func TestParseSkipsIncompleteCardsButKeepsGoodOnes(t *testing.T) {
	data := []byte(`[
		{"front":"Good?","back":"Yes."},
		{"front":"Incomplete?"},
		{"front":"Also good?","back":"Also yes."}
	]`)

	cards, err := Parse("slug", data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(cards) != 2 {
		t.Fatalf("got %d cards, want 2 (incomplete one dropped)", len(cards))
	}
}

func TestParseInvalidJSON(t *testing.T) {
	if _, err := Parse("slug", []byte(`{not json`)); err == nil {
		t.Error("want error for malformed JSON")
	}
}

func TestCardIDStability(t *testing.T) {
	// Reformatting the question must not orphan its review history.
	base := CardID("slug", "What is sparse attention?")
	same := []string{
		"what is sparse attention?",
		"  What   is sparse   attention?  ",
		"What is sparse attention?\n",
	}
	for _, s := range same {
		if got := CardID("slug", s); got != base {
			t.Errorf("CardID(%q) = %s, want %s", s, got, base)
		}
	}

	// Different article, same question — different card.
	if CardID("other-slug", "What is sparse attention?") == base {
		t.Error("ids collide across articles")
	}
	// Different question — different card.
	if CardID("slug", "What is dense attention?") == base {
		t.Error("ids collide across questions")
	}
}

func TestFronts(t *testing.T) {
	cards := []Card{{Front: "A?"}, {Front: "B?"}}
	got := Fronts(cards)
	if len(got) != 2 || got[0] != "A?" || got[1] != "B?" {
		t.Errorf("Fronts = %v", got)
	}
}

package config

import "testing"

func TestCardCountForWordsDefaults(t *testing.T) {
	var c Config // no FlashcardCounts set — builtins apply

	tests := []struct {
		words int
		want  int
	}{
		{0, 5},
		{107, 5},    // shortest article in the corpus
		{499, 5},
		{500, 8},    // bucket boundary is exclusive on the lower rule
		{1500, 12},
		{1600, 12},  // median article
		{2999, 12},
		{3000, 15},
		{5320, 15},  // longest article in the corpus
	}
	for _, tt := range tests {
		if got := c.CardCountForWords(tt.words); got != tt.want {
			t.Errorf("CardCountForWords(%d) = %d, want %d", tt.words, got, tt.want)
		}
	}
}

func TestCardCountForWordsRespectsConfig(t *testing.T) {
	c := Config{}
	c.Ingest.FlashcardCounts = []FlashcardCountRule{
		{MaxWords: 1000, Cards: 3},
		{MaxWords: 0, Cards: 7},
	}

	if got := c.CardCountForWords(500); got != 3 {
		t.Errorf("short article = %d, want 3", got)
	}
	if got := c.CardCountForWords(50000); got != 7 {
		t.Errorf("long article = %d, want 7", got)
	}
}

func TestCardCountForWordsNoCatchAll(t *testing.T) {
	// Without a MaxWords:0 rule, articles past the last bucket get 0, which
	// means "let the model decide" rather than a silent clamp.
	c := Config{}
	c.Ingest.FlashcardCounts = []FlashcardCountRule{{MaxWords: 100, Cards: 4}}

	if got := c.CardCountForWords(50); got != 4 {
		t.Errorf("in-range = %d, want 4", got)
	}
	if got := c.CardCountForWords(5000); got != 0 {
		t.Errorf("out-of-range = %d, want 0", got)
	}
}

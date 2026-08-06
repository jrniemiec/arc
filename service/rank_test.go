package service

import (
	"math"
	"sort"
	"testing"
)

// The merge step scaled scores with 1-|score|/maxAbs, handing the best hit 0
// and the worst ~1, then sorted descending — so combined search returned every
// result in reverse. It stayed hidden because a reversed list still looks like
// a ranked list. These tests pin the direction on both halves.

func TestRankNormPutsStrongestHitAtOne(t *testing.T) {
	// SQLite bm25(): negative, more negative = stronger match.
	bm25 := []float64{-8.4, -3.1, -0.7}
	maxAbs := 8.4

	best := rankNorm(bm25[0], maxAbs)
	worst := rankNorm(bm25[2], maxAbs)

	if math.Abs(best-1.0) > 1e-9 {
		t.Errorf("strongest BM25 hit should normalize to 1, got %.4f", best)
	}
	if best <= worst {
		t.Errorf("BM25 ranking inverted: strongest %.4f <= weakest %.4f", best, worst)
	}

	// Cosine similarity: positive, higher = stronger match.
	cos := []float64{0.3430, 0.3054}
	if a, b := rankNorm(cos[0], 0.3430), rankNorm(cos[1], 0.3430); a <= b {
		t.Errorf("cosine ranking inverted: stronger %.4f <= weaker %.4f", a, b)
	}
}

// Sorting normalized scores descending must reproduce the original ranking.
func TestRankNormSurvivesTheSort(t *testing.T) {
	// Ranked best-first, as the FTS half delivers them.
	ranked := []struct {
		id    string
		score float64
	}{
		{"illustrated-transformer", -8.4},
		{"annotated-transformer", -3.1},
		{"attention-is-all-you-need", -0.7},
	}
	maxAbs := 8.4

	type scored struct {
		id   string
		norm float64
	}
	out := make([]scored, 0, len(ranked))
	for _, r := range ranked {
		out = append(out, scored{r.id, rankNorm(r.score, maxAbs)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].norm > out[j].norm })

	for i, want := range ranked {
		if out[i].id != want.id {
			t.Fatalf("rank %d: got %s, want %s — merge reorders the surviving half",
				i, out[i].id, want.id)
		}
	}
}

func TestRankNormHandlesAllZeroScores(t *testing.T) {
	if got := rankNorm(0, 0); got != 0 {
		t.Errorf("rankNorm(0,0) = %v, want 0 (no divide by zero)", got)
	}
}

package library_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jrniemiec/arc/config"
	"github.com/jrniemiec/arc/library"
	"github.com/jrniemiec/arc/store"
)

// testdataArticles returns the path to the test article fixtures.
func testdataArticles(t *testing.T) string {
	t.Helper()
	// Walk up from library/ to find testdata/
	root, err := filepath.Abs("../testdata/articles")
	if err != nil {
		t.Fatalf("resolve testdata path: %v", err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("testdata not found at %s: %v", root, err)
	}
	return root
}

// openTestLibrary creates a Library backed by a temp SQLite db and the testdata fixtures.
func openTestLibrary(t *testing.T) *library.Library {
	t.Helper()
	ctx := context.Background()

	tmpDir := t.TempDir()
	cfg := config.Default()
	cfg.ArticlesRoot = testdataArticles(t)
	cfg.DBPath = filepath.Join(tmpDir, "arc.db")
	cfg.VectorPath = filepath.Join(tmpDir, "index")
	cfg.EventsPath = filepath.Join(tmpDir, "events.jsonl")

	lib, err := library.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("open library: %v", err)
	}
	t.Cleanup(func() { lib.Close() })
	return lib
}

func TestReindex(t *testing.T) {
	ctx := context.Background()
	lib := openTestLibrary(t)

	var indexed, total int
	_, err := lib.Reindex(ctx, func(i, tot int) {
		indexed = i
		total = tot
	})
	if err != nil {
		t.Fatalf("reindex: %v", err)
	}
	if indexed == 0 {
		t.Fatal("expected at least one article indexed")
	}
	if indexed != total {
		t.Fatalf("indexed %d but total %d", indexed, total)
	}
	t.Logf("reindexed %d/%d articles", indexed, total)
}

func TestGet(t *testing.T) {
	ctx := context.Background()
	lib := openTestLibrary(t)

	if _, err := lib.Reindex(ctx, nil); err != nil {
		t.Fatalf("reindex: %v", err)
	}

	a, err := lib.Get(ctx, "20260521-sparse-attention-survey")
	if err != nil {
		t.Fatalf("get article: %v", err)
	}

	if a.ID != "20260521-sparse-attention-survey" {
		t.Errorf("got ID %q, want 20260521-sparse-attention-survey", a.ID)
	}
	if a.Title != "Sparse Attention Mechanisms: A Survey" {
		t.Errorf("got title %q", a.Title)
	}
	if a.QualityScore != 0.85 {
		t.Errorf("got quality_score %f, want 0.85", a.QualityScore)
	}
	if len(a.Tags) == 0 {
		t.Error("expected tags to be populated")
	}
	if len(a.Collections) == 0 {
		t.Error("expected collections to be populated")
	}
}

func TestFileResolution(t *testing.T) {
	ctx := context.Background()
	lib := openTestLibrary(t)

	if _, err := lib.Reindex(ctx, nil); err != nil {
		t.Fatalf("reindex: %v", err)
	}

	a, err := lib.Get(ctx, "20260521-sparse-attention-survey")
	if err != nil {
		t.Fatalf("get article: %v", err)
	}

	if a.Files.Body == "" {
		t.Error("expected body file to be resolved")
	}
	if a.Files.Summary == "" {
		t.Error("expected summary file to be resolved")
	}
	if a.Files.Flash == "" {
		t.Error("expected flash file to be resolved")
	}
	if a.Files.Flashcards == "" {
		t.Error("expected flashcards file to be resolved")
	}

	t.Logf("body:       %s", a.Files.Body)
	t.Logf("summary:    %s", a.Files.Summary)
	t.Logf("flash:      %s", a.Files.Flash)
	t.Logf("flashcards: %s", a.Files.Flashcards)
}

func TestReadContent(t *testing.T) {
	ctx := context.Background()
	lib := openTestLibrary(t)

	if _, err := lib.Reindex(ctx, nil); err != nil {
		t.Fatalf("reindex: %v", err)
	}

	a, err := lib.Get(ctx, "20260521-sparse-attention-survey")
	if err != nil {
		t.Fatalf("get article: %v", err)
	}

	body, err := lib.ReadBody(a)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if len(body) == 0 {
		t.Error("body is empty")
	}

	summary, err := lib.ReadSummary(a)
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	if len(summary) == 0 {
		t.Error("summary is empty")
	}

	flash, err := lib.ReadFlash(a)
	if err != nil {
		t.Fatalf("read flash: %v", err)
	}
	if len(flash) == 0 {
		t.Error("flash is empty")
	}

	flashcards, err := lib.ReadFlashcards(a)
	if err != nil {
		t.Fatalf("read flashcards: %v", err)
	}
	if len(flashcards) == 0 {
		t.Error("flashcards is empty")
	}
}

func TestSearch(t *testing.T) {
	ctx := context.Background()
	lib := openTestLibrary(t)

	if _, err := lib.Reindex(ctx, nil); err != nil {
		t.Fatalf("reindex: %v", err)
	}

	results, err := lib.Search(ctx, store.Query{
		Text: "sparse attention",
		Mode: store.QueryKeyword,
		TopK: 10,
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected search results for 'sparse attention'")
	}

	found := false
	for _, r := range results {
		if r.Article.ID == "20260521-sparse-attention-survey" {
			found = true
			t.Logf("found article with score %f, excerpt: %s", r.Score, r.Excerpt)
		}
	}
	if !found {
		t.Error("sparse-attention-survey not in search results")
	}
}

func TestListByCollection(t *testing.T) {
	ctx := context.Background()
	lib := openTestLibrary(t)

	if _, err := lib.Reindex(ctx, nil); err != nil {
		t.Fatalf("reindex: %v", err)
	}

	articles, err := lib.List(ctx, store.Filter{Collection: "ml-papers"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(articles) == 0 {
		t.Fatal("expected articles in ml-papers collection")
	}

	for _, a := range articles {
		t.Logf("  %s — %s", a.ID, a.Title)
	}
}

func TestCostCalc(t *testing.T) {
	cfg := config.Default()

	cases := []struct {
		model    string
		in       int
		out      int
		wantUSD  float64
	}{
		{"claude-opus-4-6", 1_000_000, 0, 15.00},
		{"claude-haiku-4-5", 1_000_000, 1_000_000, 4.80},
		{"nomic-embed-text", 1_000_000, 0, 0.00},
		{"claude-sonnet-4-6", 10_000, 1_000, 0.045},
	}

	for _, tc := range cases {
		got := cfg.CalcCost(tc.model, tc.in, tc.out)
		if abs(got-tc.wantUSD) > 0.001 {
			t.Errorf("CalcCost(%s, %d, %d) = %.4f, want %.4f",
				tc.model, tc.in, tc.out, got, tc.wantUSD)
		}
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

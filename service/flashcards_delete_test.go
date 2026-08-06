package service_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jrniemiec/arc/config"
	"github.com/jrniemiec/arc/library"
	"github.com/jrniemiec/arc/service"
	"github.com/jrniemiec/arc/store/fs"
)

const fixtureSlug = "20260521-sparse-attention-survey"

// newTestService copies the article fixtures into a temp dir and opens a
// Service over them. The copy matters: these tests delete files, and the
// fixtures are checked in.
func newTestService(t *testing.T) (*service.Service, string) {
	t.Helper()

	src, err := filepath.Abs("../testdata/articles")
	if err != nil {
		t.Fatalf("resolve testdata: %v", err)
	}
	tmp := t.TempDir()
	articles := filepath.Join(tmp, "articles")
	if err := os.CopyFS(articles, os.DirFS(src)); err != nil {
		t.Fatalf("copy testdata: %v", err)
	}

	cfg := config.Default()
	cfg.DataRoot = tmp
	cfg.ArticlesRoot = articles
	cfg.VectorPath = filepath.Join(tmp, "vector")
	cfg.DBPath = filepath.Join(tmp, "test.db")

	lib, err := library.Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open library: %v", err)
	}
	t.Cleanup(func() { lib.Close() })

	if err := lib.IndexSlugs(context.Background(), []string{fixtureSlug}); err != nil {
		t.Fatalf("index: %v", err)
	}
	return service.New(lib, cfg), filepath.Join(articles, fixtureSlug)
}

func TestDeleteFlashcardsDryRunTouchesNothing(t *testing.T) {
	svc, dir := newTestService(t)
	before := len(fs.ListFlashcards(dir))

	res, err := svc.DeleteFlashcards(context.Background(), service.DeleteFlashcardsRequest{
		Slug: fixtureSlug, DryRun: true,
	})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if len(res.Deleted) != before {
		t.Errorf("matched %d files, want %d", len(res.Deleted), before)
	}
	if res.Cards == 0 {
		t.Error("dry run reported 0 cards")
	}
	if got := len(fs.ListFlashcards(dir)); got != before {
		t.Errorf("dry run removed files: %d left, want %d", got, before)
	}
}

func TestDeleteFlashcardsClearsMetaOnLastVariant(t *testing.T) {
	svc, dir := newTestService(t)

	res, err := svc.DeleteFlashcards(context.Background(), service.DeleteFlashcardsRequest{Slug: fixtureSlug})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if res.Remaining != 0 {
		t.Errorf("Remaining = %d, want 0", res.Remaining)
	}
	if got := len(fs.ListFlashcards(dir)); got != 0 {
		t.Errorf("%d files left, want 0", got)
	}

	// meta.json must stop advertising cards, or `arc list` reports a variant
	// that is gone.
	m, err := fs.ReadMeta(filepath.Join(dir, "meta.json"))
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	if m.FlashcardModel != "" || m.FlashcardStyle != "" {
		t.Errorf("meta still advertises cards: model=%q style=%q", m.FlashcardModel, m.FlashcardStyle)
	}
	// Unrelated fields must survive.
	if m.SummaryModel == "" || m.Title == "" {
		t.Error("delete clobbered unrelated meta fields")
	}
}

func TestDeleteFlashcardsKeepsUnmatchedVariants(t *testing.T) {
	svc, dir := newTestService(t)

	// Add a second variant so filtering has something to discriminate.
	orig := filepath.Join(dir, "flashcards.socratic.claude-sonnet-4-6.json")
	data, err := os.ReadFile(orig)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "flashcards.cloze.gpt-4o-mini.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	res, err := svc.DeleteFlashcards(context.Background(), service.DeleteFlashcardsRequest{
		Slug: fixtureSlug, Model: "gpt-4o-mini",
	})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(res.Deleted) != 1 || res.Remaining != 1 {
		t.Fatalf("deleted=%d remaining=%d, want 1/1", len(res.Deleted), res.Remaining)
	}
	if _, err := os.Stat(orig); err != nil {
		t.Errorf("deleted the wrong variant: %v", err)
	}

	// A surviving variant means meta must NOT be cleared.
	m, err := fs.ReadMeta(filepath.Join(dir, "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	if m.FlashcardModel == "" {
		t.Error("meta cleared while a variant is still on disk")
	}
}

func TestDeleteFlashcardsErrorsWhenNothingMatches(t *testing.T) {
	svc, _ := newTestService(t)

	// Filter that matches no variant.
	if _, err := svc.DeleteFlashcards(context.Background(), service.DeleteFlashcardsRequest{
		Slug: fixtureSlug, Model: "no-such-model",
	}); err == nil {
		t.Error("want an error when the filter matches nothing")
	}

	// Deleting twice must report the second attempt, not silently succeed.
	if _, err := svc.DeleteFlashcards(context.Background(), service.DeleteFlashcardsRequest{Slug: fixtureSlug}); err != nil {
		t.Fatalf("first delete: %v", err)
	}
	if _, err := svc.DeleteFlashcards(context.Background(), service.DeleteFlashcardsRequest{Slug: fixtureSlug}); err == nil {
		t.Error("want an error when the article has no flashcards left")
	}
}

func TestDeleteFlashcardsRemovesCardTextFromSearch(t *testing.T) {
	svc, _ := newTestService(t)

	// A phrase from a card question in the fixture, absent from its summary.
	const q = "window-based attention complexity"

	hit := func() bool {
		res, err := svc.Search(context.Background(), service.SearchRequest{Query: q, Limit: 10})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		for _, r := range res.Hits {
			if r.Article.ID == fixtureSlug {
				return true
			}
		}
		return false
	}

	if !hit() {
		t.Skip("fixture card text is not distinctive enough to test the index drop")
	}

	if _, err := svc.DeleteFlashcards(context.Background(), service.DeleteFlashcardsRequest{Slug: fixtureSlug}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if hit() {
		t.Error("card question still matches after delete — FTS was not updated")
	}
}

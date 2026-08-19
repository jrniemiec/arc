package fs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeArticle(t *testing.T, root, slug string, numID int) {
	t.Helper()
	dir := filepath.Join(root, slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"id":"` + slug + `","num_id":` + itoa(numID) + `,"title":"t","ingested_at":"2026-08-11T00:00:00Z"}`
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// A counter can be perfectly consistent while IDs are duplicated. That is what
// happened in practice: next_id matched max+1, the old check passed, and the
// problem surfaced only as a UNIQUE constraint failure part-way through a
// reindex, with the index already half torn down.
func TestValidateNumIDCounterCatchesDuplicates(t *testing.T) {
	root := t.TempDir()
	articles := filepath.Join(root, "articles")

	writeArticle(t, articles, "a-one", 1)
	writeArticle(t, articles, "a-two", 2)
	writeArticle(t, articles, "a-three", 2) // duplicate
	if err := os.WriteFile(filepath.Join(root, "next_id"), []byte("3\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := ValidateNumIDCounter(root, articles)
	if err == nil {
		t.Fatal("expected a duplicate to be reported, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate numeric IDs") {
		t.Errorf("error does not name the problem: %v", err)
	}
	// The message must name the offenders, or repair means hunting by hand.
	if !strings.Contains(err.Error(), "a-two") || !strings.Contains(err.Error(), "a-three") {
		t.Errorf("error does not identify the holders: %v", err)
	}
}

func TestValidateNumIDCounterPassesWhenUnique(t *testing.T) {
	root := t.TempDir()
	articles := filepath.Join(root, "articles")

	writeArticle(t, articles, "a-one", 1)
	writeArticle(t, articles, "a-two", 2)
	if err := os.WriteFile(filepath.Join(root, "next_id"), []byte("3\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := ValidateNumIDCounter(root, articles); err != nil {
		t.Errorf("unique IDs should pass, got %v", err)
	}
}

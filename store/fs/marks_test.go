package fs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newArticle(t *testing.T, root, slug string) {
	t.Helper()
	dir := filepath.Join(root, slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"id":"` + slug + `","num_id":1,"title":"t","ingested_at":"2026-08-11T00:00:00Z"}`
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Marks must round-trip through meta.json. They used to live only in SQLite,
// so rebuilding the database destroyed them.
func TestSetAndGetMark(t *testing.T) {
	root := t.TempDir()
	newArticle(t, root, "a-one")
	when := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)

	for _, mark := range []Mark{MarkRead, MarkPlayed, MarkFavorite} {
		if err := SetMark(root, "a-one", mark, &when); err != nil {
			t.Fatalf("SetMark(%s): %v", mark, err)
		}
		got, err := GetMark(root, "a-one", mark)
		if err != nil {
			t.Fatalf("GetMark(%s): %v", mark, err)
		}
		if got == nil || !got.Equal(when) {
			t.Errorf("%s = %v, want %v", mark, got, when)
		}
	}
}

// Clearing must remove the key, not leave a stale timestamp behind.
func TestClearMark(t *testing.T) {
	root := t.TempDir()
	newArticle(t, root, "a-one")
	when := time.Now().UTC()

	if err := SetMark(root, "a-one", MarkRead, &when); err != nil {
		t.Fatal(err)
	}
	if err := SetMark(root, "a-one", MarkRead, nil); err != nil {
		t.Fatal(err)
	}

	got, err := GetMark(root, "a-one", MarkRead)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("read_at = %v after clearing, want nil", got)
	}

	// omitempty means the key should be gone from the file entirely.
	raw, err := os.ReadFile(filepath.Join(root, "a-one", "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if _, present := m["read_at"]; present {
		t.Errorf("read_at key still present in meta.json after clearing")
	}
}

// Setting one mark must not disturb another, or marking read would clear a
// favourite.
func TestMarksAreIndependent(t *testing.T) {
	root := t.TempDir()
	newArticle(t, root, "a-one")
	when := time.Now().UTC()

	if err := SetMark(root, "a-one", MarkFavorite, &when); err != nil {
		t.Fatal(err)
	}
	if err := SetMark(root, "a-one", MarkRead, &when); err != nil {
		t.Fatal(err)
	}
	if err := SetMark(root, "a-one", MarkRead, nil); err != nil {
		t.Fatal(err)
	}

	fav, err := GetMark(root, "a-one", MarkFavorite)
	if err != nil {
		t.Fatal(err)
	}
	if fav == nil {
		t.Error("favorite was lost when read was set and cleared")
	}
}

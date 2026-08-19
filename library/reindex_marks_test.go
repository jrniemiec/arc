package library

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jrniemiec/arc/config"
	"github.com/jrniemiec/arc/store/fs"
)

// A database written before marks were persisted holds them nowhere else. The
// clear at the start of a rebuild would destroy them, so Reindex writes them to
// disk first. This is the case that actually lost data.
func TestReindexRescuesDatabaseOnlyMarks(t *testing.T) {
	root := t.TempDir()
	articles := filepath.Join(root, "articles")
	slug := "a-one"
	dir := filepath.Join(articles, slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := `{"id":"` + slug + `","num_id":1,"title":"t","ingested_at":"2026-08-11T00:00:00Z"}`
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
	// Walk only visits directories that have a body.
	if err := os.WriteFile(filepath.Join(dir, "body.txt"), []byte("body"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "next_id"), []byte("2\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	lib, err := Open(ctx, config.Config{
		DataRoot:     root,
		ArticlesRoot: articles,
		DBPath:       filepath.Join(root, "arc.db"),
		VectorPath:   filepath.Join(root, "index"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer lib.Close()

	if _, err := lib.Reindex(ctx, nil); err != nil {
		t.Fatal(err)
	}

	// Simulate the old world: a mark that reached only the database.
	when := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	if err := lib.db.MarkFavorite(ctx, slug, when); err != nil {
		t.Fatal(err)
	}
	if onDisk, _ := fs.GetMark(articles, slug, fs.MarkFavorite); onDisk != nil {
		t.Fatal("precondition failed: mark should not be on disk yet")
	}

	if _, err := lib.Reindex(ctx, nil); err != nil {
		t.Fatal(err)
	}

	onDisk, err := fs.GetMark(articles, slug, fs.MarkFavorite)
	if err != nil {
		t.Fatal(err)
	}
	if onDisk == nil {
		t.Fatal("favourite was destroyed by the rebuild instead of rescued to disk")
	}
	if !onDisk.Equal(when) {
		t.Errorf("rescued mark = %v, want %v", onDisk, when)
	}

	// And it must be back in the rebuilt index, not merely on disk.
	a, err := lib.Get(ctx, slug)
	if err != nil {
		t.Fatal(err)
	}
	if a.FavoritedAt == nil {
		t.Error("mark on disk but missing from the rebuilt index")
	}
}

// Reindex must not resurrect an article deleted from disk.
func TestReindexDropsStaleRows(t *testing.T) {
	root := t.TempDir()
	articles := filepath.Join(root, "articles")
	for _, slug := range []string{"a-one", "a-two"} {
		dir := filepath.Join(articles, slug)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		n := "1"
		if slug == "a-two" {
			n = "2"
		}
		meta := `{"id":"` + slug + `","num_id":` + n + `,"title":"t","ingested_at":"2026-08-11T00:00:00Z"}`
		if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(meta), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "body.txt"), []byte("body"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "next_id"), []byte("3\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	lib, err := Open(ctx, config.Config{
		DataRoot:     root,
		ArticlesRoot: articles,
		DBPath:       filepath.Join(root, "arc.db"),
		VectorPath:   filepath.Join(root, "index"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer lib.Close()

	if _, err := lib.Reindex(ctx, nil); err != nil {
		t.Fatal(err)
	}

	// Remove one article behind arc's back, as a hand-deletion or an external
	// tool would.
	if err := os.RemoveAll(filepath.Join(articles, "a-two")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "next_id"), []byte("2\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := lib.Reindex(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Articles != 1 {
		t.Errorf("indexed %d articles, want 1", res.Articles)
	}
	if _, err := lib.Get(ctx, "a-two"); err == nil {
		t.Error("deleted article still in the index after reindex")
	}
}

// A num_id that moved between articles used to collide with the row still
// holding it, failing part-way through and leaving the index half written.
func TestReindexHandlesSwappedNumIDs(t *testing.T) {
	root := t.TempDir()
	articles := filepath.Join(root, "articles")
	write := func(slug string, numID int) {
		dir := filepath.Join(articles, slug)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		m := map[string]any{"id": slug, "num_id": numID, "title": "t",
			"ingested_at": "2026-08-11T00:00:00Z"}
		b, _ := json.Marshal(m)
		if err := os.WriteFile(filepath.Join(dir, "meta.json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "body.txt"), []byte("body"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a-one", 1)
	write("a-two", 2)
	if err := os.WriteFile(filepath.Join(root, "next_id"), []byte("3\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	lib, err := Open(ctx, config.Config{
		DataRoot:     root,
		ArticlesRoot: articles,
		DBPath:       filepath.Join(root, "arc.db"),
		VectorPath:   filepath.Join(root, "index"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer lib.Close()

	if _, err := lib.Reindex(ctx, nil); err != nil {
		t.Fatal(err)
	}

	write("a-one", 2) // swap
	write("a-two", 1)

	if _, err := lib.Reindex(ctx, nil); err != nil {
		t.Fatalf("reindex failed on swapped num_ids: %v", err)
	}
	a, err := lib.Get(ctx, "a-one")
	if err != nil {
		t.Fatal(err)
	}
	if a.NumID != 2 {
		t.Errorf("a-one num_id = %d, want 2", a.NumID)
	}
}

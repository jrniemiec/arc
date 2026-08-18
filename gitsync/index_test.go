package gitsync

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// newArticles builds an articles root containing the given slugs, each with a
// meta.json, and returns its path.
func newArticles(t *testing.T, slugs ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, s := range slugs {
		dir := filepath.Join(root, s)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestClassify(t *testing.T) {
	root := newArticles(t, "present-one", "present-two")

	got := Classify([]string{
		"articles/present-one/summary.brief.opus.txt",
		"articles/present-one/meta.json", // same slug twice — must dedupe
		"articles/present-two/meta.json",
		"articles/gone-away/meta.json", // no longer on disk
		"collections/reading/some-slug",
		"next_id",
		"README.md", // unrelated, ignored
		"",          // blank, ignored
	}, root)

	wantIndex := []string{"present-one", "present-two"}
	if !reflect.DeepEqual(got.Index, wantIndex) {
		t.Errorf("Index = %v, want %v", got.Index, wantIndex)
	}
	wantDeindex := []string{"gone-away"}
	if !reflect.DeepEqual(got.Deindex, wantDeindex) {
		t.Errorf("Deindex = %v, want %v", got.Deindex, wantDeindex)
	}
	if !got.Collections {
		t.Error("Collections should be true when collections/ changed")
	}
	if !got.NextID {
		t.Error("NextID should be true when next_id changed")
	}
}

// Classification is decided by whether meta.json exists now, not by git's A/M/D
// status. A delete followed by a re-add in the same range must index, not
// deindex.
func TestClassifyPrefersDiskState(t *testing.T) {
	root := newArticles(t, "recreated")

	got := Classify([]string{"articles/recreated/meta.json"}, root)

	if len(got.Deindex) != 0 {
		t.Errorf("Deindex = %v, want empty", got.Deindex)
	}
	if !reflect.DeepEqual(got.Index, []string{"recreated"}) {
		t.Errorf("Index = %v, want [recreated]", got.Index)
	}
}

func TestClassifyEmpty(t *testing.T) {
	root := newArticles(t)

	if got := Classify(nil, root); !got.Empty() {
		t.Errorf("no paths should classify as empty, got %+v", got)
	}
	if got := Classify([]string{"README.md", "docs/x.md"}, root); !got.Empty() {
		t.Errorf("unrelated paths should classify as empty, got %+v", got)
	}
}

func TestArticleSlug(t *testing.T) {
	tests := []struct{ in, want string }{
		{"articles/my-slug/meta.json", "my-slug"},
		{"articles/my-slug/nested/deep.txt", "my-slug"},
		{"articles/my-slug", ""}, // no file component
		{"articles/", ""},
	}
	for _, tt := range tests {
		if got := articleSlug(tt.in); got != tt.want {
			t.Errorf("articleSlug(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

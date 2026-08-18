package gitsync

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Changes is the classification of a set of git-reported paths into the actions
// the local database needs.
type Changes struct {
	// Index are slugs whose article files changed and still exist on disk.
	Index []string
	// Deindex are slugs whose article directory is gone.
	Deindex []string
	// Collections is true when anything under collections/ changed. Membership
	// lives in symlinks, so a membership change touches no article file and
	// would be missed by slug indexing alone.
	Collections bool
	// NextID is true when the shared id counter changed, prompting a validation
	// pass. A mismatch is reported, never fatal.
	NextID bool
}

// Empty reports whether there is nothing for the database to do.
func (c Changes) Empty() bool {
	return len(c.Index) == 0 && len(c.Deindex) == 0 && !c.Collections && !c.NextID
}

// Indexer is the subset of library.Library the pull path needs. Declared here so
// gitsync does not depend on the library package.
type Indexer interface {
	IndexSlugs(ctx context.Context, slugs []string) error
	DeindexSlugs(ctx context.Context, slugs []string) error
	SyncCollections(ctx context.Context) error
}

// Classify maps git-reported paths onto database actions.
//
// A slug is classified by testing whether its meta.json exists *now*, rather
// than by reading git's A/M/D status. Renames, partial deletes, and a delete
// followed by a re-add in the same range then need no special cases: the
// filesystem is the source of truth and it is already in its final state.
//
// articlesRoot is the absolute path to the articles directory; paths are
// repository-relative as git reports them.
func Classify(paths []string, articlesRoot string) Changes {
	var ch Changes
	index := map[string]bool{}
	deindex := map[string]bool{}

	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		switch {
		case strings.HasPrefix(p, "articles/"):
			slug := articleSlug(p)
			if slug == "" {
				continue
			}
			if _, err := os.Stat(filepath.Join(articlesRoot, slug, "meta.json")); err == nil {
				index[slug] = true
			} else {
				deindex[slug] = true
			}
		case strings.HasPrefix(p, "collections/"):
			ch.Collections = true
		case p == "next_id":
			ch.NextID = true
		}
	}

	// A slug cannot be in both sets — the stat above is decisive — but guard
	// anyway so an unexpected state cannot produce a delete-then-insert.
	for slug := range index {
		delete(deindex, slug)
	}

	ch.Index = sortedKeys(index)
	ch.Deindex = sortedKeys(deindex)
	return ch
}

// articleSlug extracts the slug from a repository-relative article path.
// Returns "" when the path has no slug component.
func articleSlug(p string) string {
	rest := strings.TrimPrefix(p, "articles/")
	if i := strings.IndexByte(rest, '/'); i > 0 {
		return rest[:i]
	}
	return ""
}

func sortedKeys(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Apply brings the local database in line with the changes.
//
// It deliberately does not abort on a num_id counter mismatch the way a full
// Reindex does. A mismatch after a forced takeover would otherwise block every
// subsequent pull; the pull must still land, with the problem reported for the
// user to repair.
func (c Changes) Apply(ctx context.Context, idx Indexer, validateNumIDs func() error) error {
	if c.Empty() {
		slog.Debug("gitsync: no indexable changes in pull")
		return nil
	}
	slog.Info("gitsync applying pull changes",
		"index", len(c.Index), "deindex", len(c.Deindex),
		"collections", c.Collections, "next_id", c.NextID)

	if c.Collections {
		if err := idx.SyncCollections(ctx); err != nil {
			return err
		}
	}
	if len(c.Deindex) > 0 {
		if err := idx.DeindexSlugs(ctx, c.Deindex); err != nil {
			return err
		}
	}
	if len(c.Index) > 0 {
		if err := idx.IndexSlugs(ctx, c.Index); err != nil {
			return err
		}
	}
	if c.NextID && validateNumIDs != nil {
		if err := validateNumIDs(); err != nil {
			// Reported, never fatal — see the doc comment above.
			slog.Warn("gitsync: num_id counter mismatch after pull; repair needed", "err", err)
			return &NumIDMismatch{Err: err}
		}
	}
	return nil
}

// NumIDMismatch reports that the shared id counter disagrees with the articles
// on disk. The pull succeeded; the counter needs manual repair.
type NumIDMismatch struct{ Err error }

func (e *NumIDMismatch) Error() string {
	return "num_id counter mismatch after pull (indexing completed): " + e.Err.Error()
}

func (e *NumIDMismatch) Unwrap() error { return e.Err }

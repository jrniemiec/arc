package agent

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jrniemiec/arc/ingest/feed"
	"github.com/jrniemiec/arc/store"
	"github.com/jrniemiec/arc/store/fs"
	"github.com/jrniemiec/arc/store/sqlite"
)

const (
	recentTitlesN = 30 // how many recent article titles to include
	topTagsN      = 25 // how many top tags to include
)

// libraryStore is the subset of the sqlite store used to build library context.
// Defined as an interface to allow testing without a real database.
type libraryStore interface {
	List(ctx context.Context, f store.Filter) ([]store.Article, error)
	TopTags(ctx context.Context, n int) ([]string, error)
}

// BuildLibraryContext queries the arc database to produce a LibraryContext
// for use in the feed filter prompt. It pulls recent titles, top tags, and
// the existing collections the filter is asked to suggest from.
//
// dataRoot supplies the collections, which live on disk rather than in the
// database — an empty dataRoot simply omits them.
func BuildLibraryContext(ctx context.Context, db *sqlite.Store, dataRoot string) (*feed.LibraryContext, error) {
	lib, err := buildLibraryContext(ctx, db)
	if err != nil {
		return nil, err
	}
	lib.Collections = listCollections(dataRoot)
	return lib, nil
}

// listCollections returns "slug: description" entries, the format the filter
// prompt expects. Failure is not fatal: a filter without the collection list
// still returns verdicts, it just cannot suggest existing slugs.
func listCollections(dataRoot string) []string {
	if dataRoot == "" {
		return nil
	}
	metas, err := fs.ListCollections(dataRoot)
	if err != nil {
		slog.Warn("could not list collections for filter context", "err", err)
		return nil
	}
	if len(metas) == 0 {
		return nil
	}
	out := make([]string, 0, len(metas))
	for _, m := range metas {
		if m.Description != "" {
			out = append(out, m.Slug+": "+m.Description)
			continue
		}
		out = append(out, m.Slug)
	}
	return out
}

func buildLibraryContext(ctx context.Context, db libraryStore) (*feed.LibraryContext, error) {
	// Recent article titles — newest first.
	recent, err := db.List(ctx, store.Filter{
		Limit: recentTitlesN,
	})
	if err != nil {
		return nil, fmt.Errorf("list recent articles: %w", err)
	}
	titles := make([]string, 0, len(recent))
	for _, a := range recent {
		if a.Title != "" {
			titles = append(titles, a.Title)
		}
	}

	// Top tags by frequency.
	tags, err := db.TopTags(ctx, topTagsN)
	if err != nil {
		return nil, fmt.Errorf("top tags: %w", err)
	}

	return &feed.LibraryContext{
		RecentTitles: titles,
		TopTags:      tags,
	}, nil
}

package agent

import (
	"context"
	"fmt"

	"github.com/jrniemiec/arc/ingest/feed"
	"github.com/jrniemiec/arc/store"
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
// for use in the feed filter prompt. It pulls recent titles and top tags.
func BuildLibraryContext(ctx context.Context, db *sqlite.Store) (*feed.LibraryContext, error) {
	return buildLibraryContext(ctx, db)
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

// Package feed parses RSS and Atom feeds into a normalized representation.
// It has no dependencies on the rest of arc and can be used standalone.
package feed

import (
	"context"
	"fmt"
	"time"

	"github.com/mmcdole/gofeed"
)

// Feed is a normalized representation of an RSS or Atom feed.
type Feed struct {
	Title       string
	Link        string
	Description string
	Items       []Item
}

// Item is a single entry in a feed.
type Item struct {
	GUID      string
	Title     string
	Link      string
	Author    string
	Published time.Time
	Summary   string // description or content snippet
	Tags      []string
}

// Parse fetches and parses an RSS or Atom feed from the given URL.
func Parse(ctx context.Context, url string) (*Feed, error) {
	fp := gofeed.NewParser()
	raw, err := fp.ParseURLWithContext(url, ctx)
	if err != nil {
		return nil, fmt.Errorf("parse feed %s: %w", url, err)
	}
	return normalize(raw), nil
}

func normalize(raw *gofeed.Feed) *Feed {
	f := &Feed{
		Title:       raw.Title,
		Link:        raw.Link,
		Description: raw.Description,
		Items:       make([]Item, 0, len(raw.Items)),
	}
	for _, ri := range raw.Items {
		f.Items = append(f.Items, normalizeItem(ri))
	}
	return f
}

func normalizeItem(ri *gofeed.Item) Item {
	item := Item{
		GUID:  ri.GUID,
		Title: ri.Title,
		Link:  ri.Link,
	}

	// GUID fallback: some feeds omit GUID, use link instead.
	if item.GUID == "" {
		item.GUID = ri.Link
	}

	if ri.PublishedParsed != nil {
		item.Published = *ri.PublishedParsed
	} else if ri.UpdatedParsed != nil {
		item.Published = *ri.UpdatedParsed
	}

	if ri.Author != nil {
		item.Author = ri.Author.Name
	}

	// Prefer description as summary — it's usually the excerpt.
	// content:encoded is often the full article HTML, too large for a summary field.
	item.Summary = ri.Description

	for _, cat := range ri.Categories {
		item.Tags = append(item.Tags, cat)
	}

	return item
}

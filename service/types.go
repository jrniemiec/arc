package service

import (
	"github.com/jrniemiec/arc/store"
)

// Part identifies which part of an article to read.
type Part int

const (
	PartBody       Part = iota // body.txt (default)
	PartSummary                // summary.<style>.<model>.txt
	PartFlash                  // flash.<model>.txt
	PartFlashcards             // flashcards.<style>.<model>.json
)

// ReadRequest specifies what to read and which variant to prefer.
type ReadRequest struct {
	ID    string
	Part  Part
	Model string // override preferred model
	Style string // override preferred style
}

// SearchRequest drives a search operation.
type SearchRequest struct {
	Query      string
	Collection string
	Tags       []string
	Mode       store.QueryMode
	Limit      int
}

// SearchResult is one hit from a search.
type SearchResult struct {
	Article store.Article
	Score   float64
	Excerpt string
	Source  string // "fts" | "vector" | "both"
}

// IngestRequest describes an article to ingest.
type IngestRequest struct {
	// Input — exactly one must be set
	URL  string
	File string

	// Metadata
	Collection string
	Title      string // if empty, LLM generates it

	// Generation profile/style overrides (empty = use config defaults)
	SummaryStyle     string // overrides config ingest.summary_style
	SummaryProfile   string // profile name, overrides config ingest.summary_profile
	FlashProfile     string // profile name, overrides config ingest.flash_profile
	FlashcardProfile string // profile name, overrides config ingest.flashcard_profile
	FlashcardStyle   string // overrides config ingest.flashcard_style

	// Flags
	NoFlashcards bool
	DryRun       bool

	// Progress is called with a human-readable status at each pipeline step.
	Progress func(msg string)
}

// IngestResult is returned after a successful ingest.
type IngestResult struct {
	ArticleID string
	Slug      string
	Cost      store.CostRecord
	DryRun    bool // true if no files were written
}

// Stats is a snapshot of the knowledge base.
type Stats struct {
	TotalArticles  int
	TotalCollections int
	TotalTags      int
	Unread         int
	Unplayed       int
	CostThisMonth  float64
	CostTotal      float64
}

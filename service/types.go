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
	ArticleID       string
	Slug            string
	Cost            store.CostRecord
	DryRun          bool   // true if no files were written
	ExtractionStats string // human-readable extraction stats line
}

// SummarizeRequest describes a standalone summarize operation.
type SummarizeRequest struct {
	// Exactly one of Slug or Text must be set. Slug="-" reads from stdin.
	Slug string // existing article ID; reads body from disk
	Text string // raw text to summarize directly

	Style   string // overrides config ingest.summary_style
	Profile string // profile name, overrides config ingest.summary_profile
	Write   bool   // if true and Slug is set, write variant file alongside existing files

	Progress func(string)
}

// SummarizeResult holds the output of a summarize operation.
type SummarizeResult struct {
	Text      string
	Model     string
	Style     string
	CostUSD   float64
	Written   bool   // true if a variant file was written
	WritePath string // path of written file, if any
}

// FlashRequest describes a standalone flash generation operation.
type FlashRequest struct {
	Slug     string // existing article ID; reads summary (or body if --from-body)
	Text     string // raw text to flash directly (set when piping)
	Profile  string // profile name override
	FromBody bool   // read body instead of summary (slug mode only)
	Write    bool   // write flash file into the article directory (slug mode only)
	Progress func(string)
}

// FlashResult holds the output of a flash operation.
type FlashResult struct {
	Text      string
	Model     string
	CostUSD   float64
	Written   bool
	WritePath string
}

// FlashcardsRequest describes a standalone flashcard generation operation.
type FlashcardsRequest struct {
	Slug     string // existing article ID; reads summary by default
	Text     string // raw text (set when piping)
	Style    string // "socratic" | "cloze"
	Profile  string // profile name override
	FromBody bool   // use body instead of summary (slug mode only)
	Write    bool   // write flashcard file into the article directory (slug mode only)
	Progress func(string)
}

// FlashcardsResult holds the output of a flashcard operation.
type FlashcardsResult struct {
	JSON      []byte
	Style     string
	Model     string
	CostUSD   float64
	Written   bool
	WritePath string
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

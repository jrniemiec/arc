package service

import (
	"time"

	"github.com/jrniemiec/arc/config"
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

// BatchIngestRequest describes a batch ingest operation from a file or stdin.
type BatchIngestRequest struct {
	// Source — exactly one must be set
	File string // path to file with one URL/file per line; "-" reads stdin

	// Shared options applied to every item
	Collection       string
	SummaryStyle     string
	SummaryProfile   string
	FlashProfile     string
	FlashcardProfile string
	Flashcards       bool // force-enable flashcards regardless of config default
	NoFlashcards     bool // force-disable flashcards regardless of config default
	NoEmbed          bool
	DryRun           bool
	Force            bool

	Progress func(msg string)
}

// BatchIngestResult summarizes a batch ingest run.
type BatchIngestResult struct {
	Ingested   int
	Teasers    int      // articles tagged as teaser (subset of Ingested)
	Skipped    int      // duplicates
	Errors     int
	Slugs      []string // successfully ingested slugs
	CostUSD    float64
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
	Flashcards   bool // force-enable flashcards regardless of config default
	NoFlashcards bool // force-disable flashcards regardless of config default
	NoEmbed      bool
	DryRun       bool
	Force        bool // skip URL duplicate check

	// Progress is called with a human-readable status at each pipeline step.
	Progress func(msg string)
}

// IngestResult is returned after a successful ingest.
type IngestResult struct {
	ArticleID       string
	Slug            string
	Cost            store.CostRecord
	DryRun          bool   // true if no files were written
	Teaser          bool   // true if article was below min_words threshold
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

// CollectionSuggestion is a proposed new collection from library-wide analysis.
type CollectionSuggestion struct {
	Slug        string
	Description string
	Articles    []string // article slugs
}

// CollectionMatch is a ranked suggestion for which collection an article fits.
// If Slug is empty, NewSlug/NewDescription propose a new collection to create.
type CollectionMatch struct {
	Slug           string
	Reason         string
	NewSlug        string // set when LLM proposes a new collection
	NewDescription string // description for the proposed new collection
}

// WorkspaceInfo describes a workspace with counts.
type WorkspaceInfo struct {
	Name            string
	Description     string
	Status          string
	CreatedAt       time.Time
	ArticleCount    int
	CollectionCount int
	ResourceCount   int
	OutcomeCount    int
	HasSystem       bool
	HasHistory      bool
	ChatConfig      config.ChatConfig
	Articles        []string // article slugs
	CollectionSlugs []string // collection slugs
}

// CollectionInfo describes a collection with article count.
type CollectionInfo struct {
	Slug         string
	Name         string
	Description  string
	CreatedAt    time.Time
	ArticleCount int
	HasSummary   bool // meta-summary file exists
	HasSystem    bool // system.txt exists
}

// Stats is a snapshot of the knowledge base.
type Stats struct {
	TotalArticles    int
	TotalCollections int
	TotalTags        int
	Unread           int
	Unplayed         int
	EmbedCoverage    int // articles with an embedding
	CostThisMonth    float64
	CostTotal        float64

	// Breakdowns
	CostByModel           map[string]float64 // total USD spent per model (all operations)
	ArticlesByModel       map[string]int     // article count by summary model
	ArticlesByStyle       map[string]int     // article count by summary style
	ArticlesByCollection  map[string]int     // article count by collection (+ "(uncollected)")
	EmbedByCollection     map[string]int     // embedded article count by collection
}

// ReprocessRequest describes which articles to reprocess and how.
type ReprocessRequest struct {
	Slug         string // article ID or fuzzy slug; ignored when All=true or Collection is set
	All          bool   // process all articles
	Collection   string // process all articles in this collection slug
	Clean        bool   // delete existing variant files before regenerating
	Refetch      bool   // re-fetch body from source URL or PDF
	BodyFile     string // replace body.txt from this file ("-" = stdin)
	NoSummary    bool
	NoFlash      bool
	Flashcards   bool // force-enable flashcards regardless of config default
	NoFlashcards bool // force-disable flashcards regardless of config default
	NoEmbed      bool
	Missing      bool // skip articles that already have all requested variants
	Progress     func(msg string)
}

// ReprocessResult summarizes a reprocess run.
type ReprocessResult struct {
	Processed int
	Skipped   int
	CostUSD   float64
}

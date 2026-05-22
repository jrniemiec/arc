package store

import "time"

// Article is the core domain type — one entry in the knowledge base.
type Article struct {
	ID             string     // "20260521-sparse-attention-survey"
	Title          string
	URL            string
	SourceType     string // url | pdf | text | rss | gmail
	Feed           string // RSS feed or Gmail label name
	Author         string
	PublishedAt    string // ISO date, extracted from content
	Language       string
	IngestedAt     time.Time
	ReadAt         *time.Time
	PlayedAt       *time.Time
	Collections    []string // collection IDs
	Tags           []Tag
	SummaryModel   string
	SummaryStyle   string
	FlashModel     string
	FlashcardModel string
	FlashcardStyle string
	EmbedModel     string
	QualityScore   float64
	Relations      []Relation
	Files          Files
}

// Files resolves which physical files back this article.
// All paths are absolute. Empty string means the file does not exist.
type Files struct {
	Root             string // absolute path to article dir
	Body             string // body.txt
	Summary          string // best summary variant per config preferences
	Flash            string // best flash variant per config preferences
	Flashcards       string // best flashcards variant per config preferences
	SourceURL        string // source.url
	SourcePDF        string // source.pdf
	SourceHTML       string // source.html
	Meta             string // meta.json
}

// Tag is a keyword attached to an article with its origin.
type Tag struct {
	Value  string
	Source TagSource
}

type TagSource string

const (
	TagSourceLLM  TagSource = "llm"
	TagSourceUser TagSource = "user"
)

// Relation is a directed link from this article to another.
type Relation struct {
	ToID        string
	Type        RelationType
	DetectedBy  string // "user" | "agent"
	DetectedAt  time.Time
}

type RelationType string

const (
	RelationExpandsOn   RelationType = "expands-on"
	RelationContradicts RelationType = "contradicts"
	RelationSeeAlso     RelationType = "see-also"
	RelationSupersedes  RelationType = "supersedes"
	RelationCitedBy     RelationType = "cited-by"
)

// Collection is a named grouping of articles.
type Collection struct {
	ID          string
	Name        string
	Description string
	CreatedAt   time.Time
}

// Filter constrains List results.
type Filter struct {
	Collection string
	Tags       []string
	SourceType string
	After      *time.Time
	Before     *time.Time
	Unread     bool
	Unplayed   bool
	Limit      int
	Offset     int
}

// Query drives Search.
type Query struct {
	Text   string
	Filter Filter
	Mode   QueryMode
	TopK   int
}

type QueryMode int

const (
	QueryKeyword  QueryMode = iota // FTS5 only
	QuerySemantic                  // vector only
	QueryCombined                  // FTS5 + vector, merged
)

// Result is a single search hit.
type Result struct {
	Article Article
	Score   float64
	Excerpt string // matched snippet (keyword) or chunk text (semantic)
	Source  string // "fts" | "vector" | "both"
}

// CostRecord tracks token usage and USD cost for one ingest operation.
type CostRecord struct {
	Summary    CostEntry
	Flash      CostEntry
	Flashcards CostEntry
	Embed      EmbedCostEntry
	TotalUSD   float64
}

type CostEntry struct {
	Model        string
	InputTokens  int
	OutputTokens int
	USD          float64
}

type EmbedCostEntry struct {
	Model  string
	Tokens int
	USD    float64
}

// Event is one entry in the append-only event log.
type Event struct {
	TS        time.Time   `json:"ts"`
	Type      string      `json:"type"`
	ArticleID string      `json:"article_id,omitempty"`
	Query     string      `json:"query,omitempty"`
	Hits      int         `json:"hits,omitempty"`
	Cost      *CostRecord `json:"cost,omitempty"`
	Summary   string      `json:"summary,omitempty"`
}

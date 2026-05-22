package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config is the top-level arc configuration.
type Config struct {
	// Root directory for the arc data store.
	// Default: ~/.arc
	DataRoot string `json:"data_root"`

	// ArticlesRoot is the directory containing article subdirectories.
	// Default: <DataRoot>/articles
	ArticlesRoot string `json:"articles_root"`

	// DBPath is the SQLite database file path.
	// Default: <DataRoot>/arc.db
	DBPath string `json:"db_path"`

	// VectorPath is the chromem-go vector index directory.
	// Default: <DataRoot>/index
	VectorPath string `json:"vector_path"`

	// EventsPath is the append-only JSONL event log.
	// Default: <DataRoot>/events.jsonl
	EventsPath string `json:"events_path"`

	// Ingest controls which models are used during article ingestion.
	Ingest IngestConfig `json:"ingest"`

	// PreferredModels controls variant selection when reading articles.
	// First match wins.
	PreferredModels []string `json:"preferred_models"`

	// PreferredStyles controls variant selection when reading summaries/flashcards.
	// First match wins.
	PreferredStyles []string `json:"preferred_styles"`

	// Pricing overrides the built-in pricing table.
	// Map of model name → {input_per_mtok, output_per_mtok}.
	Pricing map[string]ModelPrice `json:"pricing,omitempty"`
}

// IngestConfig specifies which models to use for each ingest step.
type IngestConfig struct {
	SummaryModel   string `json:"summary_model"`
	SummaryStyle   string `json:"summary_style"`
	FlashModel     string `json:"flash_model"`
	FlashcardModel string `json:"flashcard_model"`
	FlashcardStyle string `json:"flashcard_style"`
	EmbedModel     string `json:"embed_model"`
	EmbedBackend   string `json:"embed_backend"` // "ollama" | "openai"
	ChunkSize      int    `json:"chunk_size"`     // tokens per chunk, default 500
	ChunkOverlap   int    `json:"chunk_overlap"`  // token overlap, default 50
}

// ModelPrice holds per-million-token pricing for a model.
type ModelPrice struct {
	InputPerMTok  float64 `json:"input_per_mtok"`
	OutputPerMTok float64 `json:"output_per_mtok"`
}

// builtinPricing is the default pricing table (USD per million tokens).
var builtinPricing = map[string]ModelPrice{
	"claude-opus-4-6":          {InputPerMTok: 15.00, OutputPerMTok: 75.00},
	"claude-sonnet-4-6":        {InputPerMTok: 3.00, OutputPerMTok: 15.00},
	"claude-haiku-4-5":         {InputPerMTok: 0.80, OutputPerMTok: 4.00},
	"gpt-4.1":                  {InputPerMTok: 2.00, OutputPerMTok: 8.00},
	"gpt-4o-mini":              {InputPerMTok: 0.15, OutputPerMTok: 0.60},
	"nomic-embed-text":         {InputPerMTok: 0.00, OutputPerMTok: 0.00},
	"text-embedding-3-small":   {InputPerMTok: 0.02, OutputPerMTok: 0.00},
	"text-embedding-3-large":   {InputPerMTok: 0.13, OutputPerMTok: 0.00},
}

// Default returns a Config with sensible defaults.
func Default() Config {
	home, _ := os.UserHomeDir()
	dataRoot := filepath.Join(home, ".arc")

	return Config{
		DataRoot:     dataRoot,
		ArticlesRoot: filepath.Join(dataRoot, "articles"),
		DBPath:       filepath.Join(dataRoot, "arc.db"),
		VectorPath:   filepath.Join(dataRoot, "index"),
		EventsPath:   filepath.Join(dataRoot, "events.jsonl"),
		Ingest: IngestConfig{
			SummaryModel:   "claude-opus-4-6",
			SummaryStyle:   "study-notes",
			FlashModel:     "claude-haiku-4-5",
			FlashcardModel: "claude-sonnet-4-6",
			FlashcardStyle: "socratic",
			EmbedModel:     "nomic-embed-text",
			EmbedBackend:   "ollama",
			ChunkSize:      500,
			ChunkOverlap:   50,
		},
		PreferredModels: []string{"claude-opus-4-6", "claude-sonnet-4-6", "claude-haiku-4-5", "gpt-4.1", "gpt-4o-mini"},
		PreferredStyles: []string{"study-notes", "bullets", "technical"},
	}
}

// Load reads a config file, falling back to defaults for missing fields.
func Load(path string) (Config, error) {
	cfg := Default()

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("open config: %w", err)
	}
	defer f.Close()

	if err := json.NewDecoder(f).Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("decode config: %w", err)
	}

	cfg.applyDefaults()
	return cfg, nil
}

func (c *Config) applyDefaults() {
	home, _ := os.UserHomeDir()
	if c.DataRoot == "" {
		c.DataRoot = filepath.Join(home, ".arc")
	}
	if c.ArticlesRoot == "" {
		c.ArticlesRoot = filepath.Join(c.DataRoot, "articles")
	}
	if c.DBPath == "" {
		c.DBPath = filepath.Join(c.DataRoot, "arc.db")
	}
	if c.VectorPath == "" {
		c.VectorPath = filepath.Join(c.DataRoot, "index")
	}
	if c.EventsPath == "" {
		c.EventsPath = filepath.Join(c.DataRoot, "events.jsonl")
	}
	if c.Ingest.SummaryModel == "" {
		c.Ingest.SummaryModel = "claude-opus-4-6"
	}
	if c.Ingest.SummaryStyle == "" {
		c.Ingest.SummaryStyle = "study-notes"
	}
	if c.Ingest.FlashModel == "" {
		c.Ingest.FlashModel = "claude-haiku-4-5"
	}
	if c.Ingest.FlashcardModel == "" {
		c.Ingest.FlashcardModel = "claude-sonnet-4-6"
	}
	if c.Ingest.FlashcardStyle == "" {
		c.Ingest.FlashcardStyle = "socratic"
	}
	if c.Ingest.EmbedModel == "" {
		c.Ingest.EmbedModel = "nomic-embed-text"
	}
	if c.Ingest.EmbedBackend == "" {
		c.Ingest.EmbedBackend = "ollama"
	}
	if c.Ingest.ChunkSize == 0 {
		c.Ingest.ChunkSize = 500
	}
	if c.Ingest.ChunkOverlap == 0 {
		c.Ingest.ChunkOverlap = 50
	}
	if len(c.PreferredModels) == 0 {
		c.PreferredModels = []string{"claude-opus-4-6", "claude-sonnet-4-6", "claude-haiku-4-5", "gpt-4.1", "gpt-4o-mini"}
	}
	if len(c.PreferredStyles) == 0 {
		c.PreferredStyles = []string{"study-notes", "bullets", "technical"}
	}
}

// PriceFor returns the pricing for a model, checking user overrides first.
func (c *Config) PriceFor(model string) (ModelPrice, bool) {
	if c.Pricing != nil {
		if p, ok := c.Pricing[model]; ok {
			return p, true
		}
	}
	p, ok := builtinPricing[model]
	return p, ok
}

// CalcCost returns the USD cost for a given number of input and output tokens.
func (c *Config) CalcCost(model string, inputTokens, outputTokens int) float64 {
	p, ok := c.PriceFor(model)
	if !ok {
		return 0
	}
	return (float64(inputTokens)*p.InputPerMTok + float64(outputTokens)*p.OutputPerMTok) / 1_000_000
}

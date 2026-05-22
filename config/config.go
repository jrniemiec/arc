package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config is the top-level arc configuration.
type Config struct {
	DataRoot     string `json:"data_root"`     // default: ~/.arc
	ArticlesRoot string `json:"articles_root"` // default: <DataRoot>/articles
	DBPath       string `json:"db_path"`       // default: <DataRoot>/arc.db
	VectorPath   string `json:"vector_path"`   // default: <DataRoot>/index
	EventsPath   string `json:"events_path"`   // default: <DataRoot>/events.jsonl

	// Profiles defines available LLM provider+model combinations.
	// Keyed by short name (e.g. "oai-mini", "opus"). Built-in profiles
	// are always available; user config may add or override entries.
	Profiles map[string]Profile `json:"profiles"`

	// Ingest controls which profiles and styles are used during ingestion.
	Ingest IngestConfig `json:"ingest"`

	// PreferredModels controls variant file selection when reading articles.
	// First match wins. Used by arc read / arc list.
	PreferredModels []string `json:"preferred_models"`

	// PreferredStyles controls variant file selection for summaries/flashcards.
	PreferredStyles []string `json:"preferred_styles"`
}

// Profile describes one LLM provider+model combination.
type Profile struct {
	Provider string `json:"provider"` // "anthropic" | "openai" | "ollama"
	Model    string `json:"model"`
	Host     string `json:"host,omitempty"` // Ollama only, default http://localhost:11434

	Info ProfileInfo `json:"info"`
}

// ProfileInfo holds human-readable metadata about a profile.
// Displayed by `arc profiles` and useful for new users choosing a model.
type ProfileInfo struct {
	CostTier   string             `json:"cost_tier"`             // "local" | "very_low" | "low" | "medium" | "high" | "premium"
	CostVsValue string            `json:"cost_vs_value"`         // one-line tradeoff summary
	Pricing    *ProfilePricing    `json:"pricing_usd_per_1m_tokens,omitempty"`
}

// ProfilePricing holds per-million-token pricing.
type ProfilePricing struct {
	Input       float64 `json:"input"`
	Output      float64 `json:"output"`
	CachedInput float64 `json:"cached_input,omitempty"`
}

// IngestConfig specifies which profiles and styles to use for each ingest step.
type IngestConfig struct {
	SummaryProfile   string `json:"summary_profile"`   // profile name for summarization
	FlashProfile     string `json:"flash_profile"`     // profile name for flash generation
	FlashcardProfile string `json:"flashcard_profile"` // profile name for flashcard generation
	SummaryStyle     string `json:"summary_style"`     // "study-notes" | "bullets" | "technical"
	FlashcardStyle   string `json:"flashcard_style"`   // "socratic" | "cloze"
	EmbedProfile     string `json:"embed_profile"`     // profile name for embeddings
	ChunkSize        int    `json:"chunk_size"`        // words per summarization chunk
}

// builtinProfiles ships with the binary. Users can add/override in config.json.
var builtinProfiles = map[string]Profile{
	"oai-mini": {
		Provider: "openai",
		Model:    "gpt-4o-mini",
		Info: ProfileInfo{
			CostTier:    "very_low",
			CostVsValue: "Best for bulk summarization, flash, and flashcard generation. Lowest cost. Weaker on nuanced analysis of dense academic content.",
			Pricing:     &ProfilePricing{Input: 0.15, Output: 0.60, CachedInput: 0.075},
		},
	},
	"oai-4.1": {
		Provider: "openai",
		Model:    "gpt-4.1",
		Info: ProfileInfo{
			CostTier:    "high",
			CostVsValue: "Excellent for summaries of technical and academic content. Strong instruction following, large context. Good cost-per-quality for serious reading lists.",
			Pricing:     &ProfilePricing{Input: 2.00, Output: 8.00, CachedInput: 0.50},
		},
	},
	"oai-4o": {
		Provider: "openai",
		Model:    "gpt-4o",
		Info: ProfileInfo{
			CostTier:    "medium",
			CostVsValue: "Balanced choice. Better than oai-mini for nuanced summarization, but not as cost-effective as oai-4.1 at the high end.",
			Pricing:     &ProfilePricing{Input: 2.50, Output: 10.00, CachedInput: 1.25},
		},
	},
	"oai-5-mini": {
		Provider: "openai",
		Model:    "gpt-5-mini",
		Info: ProfileInfo{
			CostTier:    "low",
			CostVsValue: "Attractive middle tier. Significantly stronger than gpt-4o-mini for reasoning-heavy summarization, still much cheaper than gpt-4.1.",
			Pricing:     &ProfilePricing{Input: 0.25, Output: 2.00, CachedInput: 0.025},
		},
	},
	"oai-5": {
		Provider: "openai",
		Model:    "gpt-5",
		Info: ProfileInfo{
			CostTier:    "premium",
			CostVsValue: "Best quality for deeply complex or long-form content. Use when summary quality is critical and cost is secondary.",
			Pricing:     &ProfilePricing{Input: 1.25, Output: 10.00, CachedInput: 0.125},
		},
	},
	"opus": {
		Provider: "anthropic",
		Model:    "claude-opus-4-6",
		Info: ProfileInfo{
			CostTier:    "premium",
			CostVsValue: "Recommended for production summarization. Best coherence and reduction quality on long articles. Quality compounds in map-reduce — worth the cost.",
			Pricing:     &ProfilePricing{Input: 15.00, Output: 75.00},
		},
	},
	"sonnet": {
		Provider: "anthropic",
		Model:    "claude-sonnet-4-6",
		Info: ProfileInfo{
			CostTier:    "medium",
			CostVsValue: "Recommended for production flashcard generation. Strong structured JSON output, good instruction following. Right balance for single-call tasks.",
			Pricing:     &ProfilePricing{Input: 3.00, Output: 15.00},
		},
	},
	"haiku": {
		Provider: "anthropic",
		Model:    "claude-haiku-4-5-20251001",
		Info: ProfileInfo{
			CostTier:    "very_low",
			CostVsValue: "Recommended for production flash generation. Trivial single-call task — Haiku is fast, cheap, and more than capable for 3-5 sentence audio summaries.",
			Pricing:     &ProfilePricing{Input: 0.80, Output: 4.00},
		},
	},
	"llama": {
		Provider: "ollama",
		Host:     "http://localhost:11434",
		Model:    "llama3.1:8b",
		Info: ProfileInfo{
			CostTier:    "local",
			CostVsValue: "Free if you run Ollama locally. Good for experimentation and offline use. Lower quality ceiling than cloud models for dense academic content.",
		},
	},
	"qwen": {
		Provider: "ollama",
		Host:     "http://localhost:11434",
		Model:    "qwen2.5-coder:7b",
		Info: ProfileInfo{
			CostTier:    "local",
			CostVsValue: "Free local option, stronger than llama on technical content. Good for code-heavy articles.",
		},
	},
}

// Default returns a Config with sensible defaults.
// All ingest steps default to oai-mini for low-cost development/testing.
// Switch summary_profile to "opus", flash_profile to "haiku", flashcard_profile
// to "sonnet" for production quality — see local/MODEL_PRICING.md.
func Default() Config {
	home, _ := os.UserHomeDir()
	dataRoot := filepath.Join(home, ".arc")

	return Config{
		DataRoot:     dataRoot,
		ArticlesRoot: filepath.Join(dataRoot, "articles"),
		DBPath:       filepath.Join(dataRoot, "arc.db"),
		VectorPath:   filepath.Join(dataRoot, "index"),
		EventsPath:   filepath.Join(dataRoot, "events.jsonl"),
		Profiles:     builtinProfiles,
		Ingest: IngestConfig{
			SummaryProfile:   "oai-mini",
			FlashProfile:     "oai-mini",
			FlashcardProfile: "oai-mini",
			SummaryStyle:     "study-notes",
			FlashcardStyle:   "socratic",
			EmbedProfile:     "llama",
			ChunkSize:        3000,
		},
		PreferredModels: []string{
			"claude-opus-4-6", "claude-sonnet-4-6", "claude-haiku-4-5-20251001",
			"gpt-4.1", "gpt-4o-mini",
		},
		PreferredStyles: []string{"study-notes", "bullets", "technical"},
	}
}

// Load reads a config file, falling back to defaults for missing fields.
// Built-in profiles are always available; user-defined profiles are merged in.
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

	// Decode into a temporary struct so we can merge profiles rather than replace.
	var overlay struct {
		DataRoot        string             `json:"data_root"`
		ArticlesRoot    string             `json:"articles_root"`
		DBPath          string             `json:"db_path"`
		VectorPath      string             `json:"vector_path"`
		EventsPath      string             `json:"events_path"`
		Profiles        map[string]Profile `json:"profiles"`
		Ingest          IngestConfig       `json:"ingest"`
		PreferredModels []string           `json:"preferred_models"`
		PreferredStyles []string           `json:"preferred_styles"`
	}
	if err := json.NewDecoder(f).Decode(&overlay); err != nil {
		return cfg, fmt.Errorf("decode config: %w", err)
	}

	if overlay.DataRoot != "" {
		cfg.DataRoot = overlay.DataRoot
	}
	if overlay.ArticlesRoot != "" {
		cfg.ArticlesRoot = overlay.ArticlesRoot
	}
	if overlay.DBPath != "" {
		cfg.DBPath = overlay.DBPath
	}
	if overlay.VectorPath != "" {
		cfg.VectorPath = overlay.VectorPath
	}
	if overlay.EventsPath != "" {
		cfg.EventsPath = overlay.EventsPath
	}
	// Merge user profiles on top of builtins
	for k, v := range overlay.Profiles {
		cfg.Profiles[k] = v
	}
	if overlay.Ingest.SummaryProfile != "" {
		cfg.Ingest.SummaryProfile = overlay.Ingest.SummaryProfile
	}
	if overlay.Ingest.FlashProfile != "" {
		cfg.Ingest.FlashProfile = overlay.Ingest.FlashProfile
	}
	if overlay.Ingest.FlashcardProfile != "" {
		cfg.Ingest.FlashcardProfile = overlay.Ingest.FlashcardProfile
	}
	if overlay.Ingest.SummaryStyle != "" {
		cfg.Ingest.SummaryStyle = overlay.Ingest.SummaryStyle
	}
	if overlay.Ingest.FlashcardStyle != "" {
		cfg.Ingest.FlashcardStyle = overlay.Ingest.FlashcardStyle
	}
	if overlay.Ingest.EmbedProfile != "" {
		cfg.Ingest.EmbedProfile = overlay.Ingest.EmbedProfile
	}
	if overlay.Ingest.ChunkSize != 0 {
		cfg.Ingest.ChunkSize = overlay.Ingest.ChunkSize
	}
	if len(overlay.PreferredModels) > 0 {
		cfg.PreferredModels = overlay.PreferredModels
	}
	if len(overlay.PreferredStyles) > 0 {
		cfg.PreferredStyles = overlay.PreferredStyles
	}

	return cfg, nil
}

// DefaultConfigJSON returns the full default config serialized as indented JSON.
// Used by `arc init` to write the initial config file.
func DefaultConfigJSON() ([]byte, error) {
	return json.MarshalIndent(Default(), "", "  ")
}

// Validate checks that the config is minimally usable.
func (c *Config) Validate() error {
	if len(c.Profiles) == 0 {
		return fmt.Errorf("no profiles defined")
	}
	for _, name := range []string{c.Ingest.SummaryProfile, c.Ingest.FlashProfile, c.Ingest.FlashcardProfile} {
		if name == "" {
			continue
		}
		if _, ok := c.Profiles[name]; !ok {
			return fmt.Errorf("ingest profile %q not found in profiles", name)
		}
	}
	return nil
}

// Profile returns a named profile, checking user-defined profiles first.
// Returns the profile and true if found, zero value and false if not.
func (c *Config) Profile(name string) (Profile, bool) {
	p, ok := c.Profiles[name]
	return p, ok
}

// CalcCost returns the USD cost for a model given token counts.
// Looks up pricing from the matching profile (by model name).
func (c *Config) CalcCost(model string, inputTokens, outputTokens int) float64 {
	for _, p := range c.Profiles {
		if p.Model == model && p.Info.Pricing != nil {
			input := float64(inputTokens) * p.Info.Pricing.Input / 1_000_000
			output := float64(outputTokens) * p.Info.Pricing.Output / 1_000_000
			return input + output
		}
	}
	return 0
}

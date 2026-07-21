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

	// CookieJars maps domain suffixes to Netscape cookie jar file paths.
	// e.g. {"medium.com": "~/.arc/cookies/medium.txt"}
	// Used automatically when fetching URLs whose host matches a key.
	CookieJars map[string]string `json:"cookie_jars,omitempty"`

	// Chat holds default settings for arc workspace chat.
	// These are copied verbatim into chat/chat.json when a new workspace is created.
	// After creation the workspace file is the sole source of truth — this section
	// is never consulted at runtime.
	Chat ChatConfig `json:"chat"`

	// Input correction (Ctrl+G in TUI)
	// CorrectionProfile selects which profile to use for Ctrl+G corrections.
	// Defaults to "oai-mini" if empty.
	CorrectionProfile string `json:"correction_profile,omitempty"`
	// CorrectionPrompt overrides the built-in system prompt used for corrections.
	// Leave empty to use the default.
	CorrectionPrompt string `json:"correction_prompt,omitempty"`

	// TTS (macOS say(1))
	TTSVoice string `json:"tts_voice,omitempty"` // say(1) voice name; empty = system default
	TTSRate  int    `json:"tts_rate,omitempty"`   // words per minute; 0 = 200

	// AskX (single-shot LLM query pane in TUI)
	AskX AskXConfig `json:"askx"`

	// WorkspacePopulate controls LLM-assisted workspace population.
	WorkspacePopulate WorkspacePopulateConfig `json:"workspace_populate"`

	// Agent
	AgentPath string `json:"agent_path,omitempty"` // default: <DataRoot>/agent

	// Logging
	LogPath  string `json:"log_path,omitempty"`  // default: <DataRoot>/arc.log
	LogLevel string `json:"log_level,omitempty"` // debug|info|warn|error; default: info
}

// ChatConfig holds the configuration for a workspace chat session.
// It maps 1:1 to workspaces/<name>/chat/chat.json.
// The global config.Chat section serves as a template — copied into each new workspace.
// All fields are written to the workspace file (no omitempty) so users can see and
// edit every option.
type ChatConfig struct {
	// Profile is the arc profile name (provider + model) used for chat.
	// Empty falls back to ingest.flash_profile, then the first available profile.
	Profile string `json:"profile"`

	// Strategy controls how conversation history is trimmed to fit the context window.
	// Options: "tail" (last N user turns), "token-budget" (fit within token ceiling),
	// "summarize" (compress old history via LLM).
	Strategy string `json:"strategy"`

	// ContextLimit is the token budget for token-budget and summarize strategies.
	// 0 means no explicit limit (provider default context window is used).
	ContextLimit int `json:"context_limit"`

	// MaxOutputTokens caps the response length. 0 uses the provider default (4096).
	MaxOutputTokens int `json:"max_output_tokens"`

	// MaxUserMessages is the number of past user turns kept by the tail strategy.
	// Default: 50.
	MaxUserMessages int `json:"max_user_messages"`

	// SummarizerProfile is the arc profile used to run history compaction in the
	// summarize strategy. Empty falls back to the main chat Profile.
	SummarizerProfile string `json:"summarizer_profile"`

	// VerbatimRatio is the fraction of the token budget kept as verbatim recent
	// messages in the summarize strategy. The remainder is covered by the summary.
	// Default: 0.4 (40% verbatim, 60% summary).
	VerbatimRatio float64 `json:"verbatim_ratio"`

	// GroundingMode controls how the chat assistant sources its answers.
	// Options: "corpus-only", "corpus-first" (default), "open".
	GroundingMode string `json:"grounding_mode"`

	// RAGMode controls how the LLM uses the injected knowledge base.
	// Deprecated: replaced by GroundingMode. Kept for backward compatibility
	// with existing chat.json files until the RAG system is fully removed.
	RAGMode string `json:"rag_mode"`

	// RAGInstruction overrides the default instruction text for the selected RAGMode.
	// Deprecated: replaced by grounding mode prompt blocks.
	RAGInstruction string `json:"rag_instruction,omitempty"`
}

// Profile describes one LLM provider+model combination.
type Profile struct {
	Provider string `json:"provider"` // "anthropic" | "openai" | "ollama"
	Model    string `json:"model"`
	Host     string `json:"host,omitempty"`  // Ollama only, default http://localhost:11434
	Think    bool   `json:"think,omitempty"` // Ollama only: enable thinking/reasoning mode

	Info ProfileInfo `json:"info"`
}

// ProfileInfo holds human-readable metadata about a profile.
// Displayed by `arc profiles` and useful for new users choosing a model.
type ProfileInfo struct {
	CostTier      string          `json:"cost_tier"`                         // "local" | "very_low" | "low" | "medium" | "high" | "premium"
	CostVsValue   string          `json:"cost_vs_value"`                     // one-line tradeoff summary
	ContextWindow int             `json:"context_window,omitempty"`          // model context window in tokens (0 = unknown)
	Pricing       *ProfilePricing `json:"pricing_usd_per_1m_tokens,omitempty"`
}

// ProfilePricing holds per-million-token pricing.
type ProfilePricing struct {
	Input       float64 `json:"input"`
	Output      float64 `json:"output"`
	CachedInput float64 `json:"cached_input,omitempty"`
}

// SummaryStyleConfig holds the system prompt for one summary style.
// Users can override built-in prompts or add new styles in config.json.
type SummaryStyleConfig struct {
	SystemPrompt string `json:"system_prompt"`
}

// IngestConfig specifies which profiles and styles to use for each ingest step.
type IngestConfig struct {
	SummaryProfile   string `json:"summary_profile"`   // profile name for summarization
	FlashProfile     string `json:"flash_profile"`     // profile name for flash generation
	FlashcardProfile string `json:"flashcard_profile"` // profile name for flashcard generation
	SummaryStyle     string `json:"summary_style"`     // "study-notes" | "bullets" | "technical"
	FlashcardStyle   string `json:"flashcard_style"`   // "socratic" | "cloze"
	EmbedProfile     string `json:"embed_profile"`     // profile name for embeddings

	// Summarization tuning
	ChunkTokens      int                           `json:"chunk_tokens"`       // tokens per chunk; default 900
	SummaryMaxTokens int                           `json:"summary_max_tokens"` // max output tokens per LLM call; default 2048
	SummaryStyles    map[string]SummaryStyleConfig `json:"summary_styles"`     // per-style system prompts; user may override or add styles

	// Flash tuning
	FlashSystemPrompt string `json:"flash_system_prompt"` // system prompt for flash generation
	FlashMaxTokens    int    `json:"flash_max_tokens"`    // max output tokens; default 256

	// Flashcard tuning
	FlashcardMaxTokens int                              `json:"flashcard_max_tokens"` // max output tokens; default 2048
	FlashcardStyles    map[string]FlashcardStyleConfig  `json:"flashcard_styles"`     // per-style system prompts

	// Flashcard generation.
	// When false (the default), flashcards are skipped unless --flashcards is passed explicitly.
	// Set to true to generate flashcards for every ingest by default.
	Flashcards bool `json:"flashcards"`

	// Teaser detection
	MinWords int `json:"min_words"` // articles below this word count are tagged "teaser" and skip LLM steps; default 300

	// Collection suggestion
	CollectionSuggestProfile  string `json:"collection_suggest_profile"`  // profile for arc collections suggest; default: flash_profile
	CollectionSuggestPrompt   string `json:"collection_suggest_prompt"`   // system prompt override for collection suggestion
	CollectionDescribePrompt  string `json:"collection_describe_prompt"`  // system prompt override for collection description generation
}

// FlashcardStyleConfig holds the system prompt for one flashcard style.
type FlashcardStyleConfig struct {
	SystemPrompt string `json:"system_prompt"`
}

// AskXConfig holds configuration for the /askX single-shot LLM query pane.
type AskXConfig struct {
	// Profile is the arc profile name used for askX queries.
	// Default: "haiku".
	Profile string `json:"profile"`

	// SystemPrompt is the system prompt sent with every askX query.
	SystemPrompt string `json:"system_prompt"`

	// MaxOutputTokens caps the response length. 0 uses the provider default.
	MaxOutputTokens int `json:"max_output_tokens"`
}

// WorkspacePopulateConfig controls LLM-assisted workspace population.
type WorkspacePopulateConfig struct {
	// Profile is the arc profile name used for workspace population.
	// Default: "haiku" (classification task, cheap model suffices).
	Profile string `json:"profile"`

	// Pass1Prompt is the system prompt for pass 1 (articles-only shortlisting).
	Pass1Prompt string `json:"pass1_prompt"`

	// Pass1WithCollectionsPrompt is the system prompt for pass 1 when --include-collections is set.
	Pass1WithCollectionsPrompt string `json:"pass1_with_collections_prompt"`

	// Pass2Prompt is the system prompt for pass 2 (final selection with flash summaries).
	Pass2Prompt string `json:"pass2_prompt"`

	// CostNote holds human-readable cost/budget information for this operation.
	CostNote string `json:"cost_note"`
}

// DefaultWorkspacePopulateCostNote is the built-in cost note for workspace populate.
const DefaultWorkspacePopulateCostNote = `Two-pass LLM selection: ~32K input tokens, ~1K output tokens.
Assumes ~500 articles, ~80 collections in library.
Pass 1 (shortlist): ~20K tokens — all titles + collection members.
Pass 2 (refine):    ~12-15K tokens — ~60 candidate flash summaries.
Estimated cost per run:
  Haiku 3.5:  ~$0.03
  Sonnet 4:   ~$0.11
  Opus 4:     ~$0.56
Haiku is the recommended default — this is classification, not generation.`

// DefaultAskXSystemPrompt is the built-in system prompt for askX queries.
const DefaultAskXSystemPrompt = `You are a concise, knowledgeable assistant. Answer directly and precisely. No preamble, no filler. Use plain text — no markdown formatting.`

// builtinFlashcardStyles are the default system prompts for each flashcard style.
var builtinFlashcardStyles = map[string]FlashcardStyleConfig{
	"socratic": {SystemPrompt: `You generate flashcards as a JSON array. Each card: {"type":"concept|fact|insight","front":"question","back":"answer","tags":["tag1"]}. Written for the ear — no markdown, natural language. Use probing questions that test deep understanding. Return only the JSON array.`},
	"cloze":    {SystemPrompt: `You generate flashcards as a JSON array. Each card: {"type":"concept|fact|insight","front":"question","back":"answer","tags":["tag1"]}. Written for the ear — no markdown, natural language. Use fill-in-the-blank style fronts. Return only the JSON array.`},
}

// FlashcardStylePrompt returns the system prompt for the given flashcard style.
func (c *Config) FlashcardStylePrompt(style string) string {
	if sc, ok := c.Ingest.FlashcardStyles[style]; ok && sc.SystemPrompt != "" {
		return sc.SystemPrompt
	}
	if sc, ok := builtinFlashcardStyles[style]; ok {
		return sc.SystemPrompt
	}
	return builtinFlashcardStyles["socratic"].SystemPrompt
}

// DefaultFlashSystemPrompt is the built-in system prompt for flash generation.
// Optimised for TTS: natural sentences, no markdown, spoken-word rhythm.
const DefaultFlashSystemPrompt = `You are generating a flash summary for audio playback.

Goal: 4-5 sentences, each 20 words or fewer. Capture what the article is about, the key finding or mechanism, and why it matters.

Rules:
- Each sentence on its own line, separated by a blank line
- Concrete nouns, active verbs — write for the ear, not the page
- No markdown of any kind: no #, no **, no *, no -
- No title, no header, no preamble, no closing remark
- No generic openers ("This article discusses...", "The author explores...")
- Preserve specific numbers, names, and facts where they add meaning
- Use only information from the provided text`

// DefaultCollectionSuggestPrompt is the built-in system prompt for library-wide
// collection suggestion. Given a list of article titles, the LLM proposes a set
// of collection slugs with descriptions and article assignments.
const DefaultCollectionSuggestPrompt = `You are organizing a personal knowledge base into collections.

Given a list of articles (slug + title), suggest 5-10 collection slugs that would
meaningfully group them. A collection should represent a coherent topic or theme.

Rules:
- Collection slugs: lowercase, hyphens only, no spaces (e.g. "machine-learning", "go-performance")
- Each collection should have 2+ articles — do not create single-article collections
- An article can belong to multiple collections
- Prefer specific over generic (e.g. "transformer-architectures" over "ai")
- Do not suggest collections that already exist (listed under "Existing collections")
- Return JSON only, no prose

Return a JSON array:
[
  {
    "slug": "machine-learning",
    "description": "ML papers, architectures, and research",
    "articles": ["slug-1", "slug-2"]
  }
]`

// DefaultCollectionArticleSuggestPrompt is the built-in system prompt for
// per-article collection suggestion. Given an article and existing collections,
// the LLM ranks which collections the article fits.
const DefaultCollectionArticleSuggestPrompt = `You are organizing a personal knowledge base.

Given an article (title + flash summary) and a list of existing collections
(slug + description), suggest which collections this article belongs to.
If no existing collection is a good fit, propose a new one instead.

Rules:
- Only suggest existing collections that are a genuine fit — do not force matches
- Rank by confidence (highest first)
- If none of the existing collections fit well, include one entry with "slug": null
  and a "new_collection" object proposing a new collection slug and description
- Return JSON only, no prose

Return a JSON array:
[
  {
    "slug": "machine-learning",
    "reason": "transformer architecture paper — direct match"
  },
  {
    "slug": null,
    "new_collection": {
      "slug": "ai-model-releases",
      "description": "Announcements and analysis of new AI model releases"
    },
    "reason": "no existing collection covers model release announcements"
  }
]`

// CollectionSuggestPrompt returns the effective system prompt for library-wide
// collection suggestion, preferring user config over built-in default.
func (c *Config) CollectionSuggestPrompt() string {
	if c.Ingest.CollectionSuggestPrompt != "" {
		return c.Ingest.CollectionSuggestPrompt
	}
	return DefaultCollectionSuggestPrompt
}

// DefaultCollectionDescribePrompt is the system prompt for generating a
// one-sentence collection description from its slug and member article titles.
const DefaultCollectionDescribePrompt = `You are organizing a personal knowledge base.

Given a collection slug and a list of its member article titles, write a single
sentence describing what this collection covers.

Rules:
- One sentence only, no more
- Be specific — mention the actual topics, not generic filler
- Do not start with "This collection..." or "A collection of..."
- Return only the description text, no JSON, no quotes, no commentary`

// CollectionDescribePrompt returns the effective system prompt for collection
// description generation, preferring user config over built-in default.
func (c *Config) CollectionDescribePrompt() string {
	if c.Ingest.CollectionDescribePrompt != "" {
		return c.Ingest.CollectionDescribePrompt
	}
	return DefaultCollectionDescribePrompt
}

// CollectionSuggestProfileName returns the effective profile name for collection
// suggestion, falling back to flash_profile if not explicitly set.
func (c *Config) CollectionSuggestProfileName() string {
	if c.Ingest.CollectionSuggestProfile != "" {
		return c.Ingest.CollectionSuggestProfile
	}
	return c.Ingest.FlashProfile
}

// WorkspacePopulateProfileName returns the effective profile name for workspace populate.
func (c *Config) WorkspacePopulateProfileName() string {
	if c.WorkspacePopulate.Profile != "" {
		return c.WorkspacePopulate.Profile
	}
	return "haiku"
}

// WorkspacePopulatePass1Prompt returns the system prompt for pass 1 (articles-only).
func (c *Config) WorkspacePopulatePass1Prompt() string {
	if c.WorkspacePopulate.Pass1Prompt != "" {
		return c.WorkspacePopulate.Pass1Prompt
	}
	return DefaultPopulatePass1Prompt
}

// WorkspacePopulatePass1WithCollectionsPrompt returns the system prompt for pass 1 with collections.
func (c *Config) WorkspacePopulatePass1WithCollectionsPrompt() string {
	if c.WorkspacePopulate.Pass1WithCollectionsPrompt != "" {
		return c.WorkspacePopulate.Pass1WithCollectionsPrompt
	}
	return DefaultPopulatePass1WithCollectionsPrompt
}

// WorkspacePopulatePass2Prompt returns the system prompt for pass 2.
func (c *Config) WorkspacePopulatePass2Prompt() string {
	if c.WorkspacePopulate.Pass2Prompt != "" {
		return c.WorkspacePopulate.Pass2Prompt
	}
	return DefaultPopulatePass2Prompt
}

// DefaultPopulatePass1Prompt is the built-in system prompt for pass 1 (articles-only).
const DefaultPopulatePass1Prompt = `You are a strict research librarian. Given a workspace purpose and a list of article titles, shortlist ONLY articles with a strong, direct connection to the workspace topic.

Instructions:
- Infer scope from the workspace name and purpose. A narrow/introductory workspace (e.g. "building my first X") needs at most 15-20 candidates. A broad research workspace may go up to 40.
- Be aggressive about excluding loosely related articles. An article about a tangentially related topic is NOT a candidate.
- This is a shortlist for a second pass, but over-inclusion wastes budget and degrades quality.
- If the user provides guidance, treat it as a hard constraint.
- Return ONLY valid JSON, no commentary.

Output format:
{"articles": ["slug1", "slug2"]}`

// DefaultPopulatePass1WithCollectionsPrompt is the built-in system prompt for pass 1 with collections.
const DefaultPopulatePass1WithCollectionsPrompt = `You are a strict research librarian. Given a workspace purpose, collections, and articles, select ONLY items with a strong, direct connection.

Instructions:
- Infer scope from the workspace name and purpose. A narrow workspace needs fewer items than a broad one.
- Select collections only when their entire topic aligns with the workspace purpose.
- Select article CANDIDATES only if they are directly relevant AND not covered by a selected collection.
- Be aggressive about excluding loosely related items.
- If the user provides guidance, treat it as a hard constraint.
- Return ONLY valid JSON, no commentary.

Output format:
{"collections": ["slug1", "slug2"], "articles": ["slug1", "slug2"]}`

// DefaultPopulatePass2Prompt is the built-in system prompt for pass 2.
const DefaultPopulatePass2Prompt = `You are a strict research librarian making a final selection for a workspace.

Instructions:
- Review each candidate article and its flash summary.
- Select ONLY articles that are clearly and directly relevant to the workspace purpose.
- For a narrow/introductory workspace: select 5-10 articles maximum. For a broad research workspace: up to 20-30.
- When in doubt, EXCLUDE. A focused workspace with 5 high-relevance articles is far better than 15 loosely related ones.
- If the user provides guidance, treat it as a hard constraint that overrides the defaults above.
- Return ONLY valid JSON, no commentary.

Output format:
{"articles": ["slug1", "slug2"]}`

// builtinSummaryStyles are the default system prompts for each summary style.
// Merged with user-defined styles from config (user overrides win).
var builtinSummaryStyles = map[string]SummaryStyleConfig{
	"study-notes": {SystemPrompt: "You are a knowledge curator building a personal reading archive. Write structured study notes using markdown. Sections: ## Key Concepts (define clearly), ## Insights (non-obvious or surprising takeaways), ## Key Facts (specific numbers, names, dates, examples worth remembering). Preserve specifics — vague summaries have no recall value. Use only information from the provided text."},
	"bullets":     {SystemPrompt: "You are a precise knowledge curator. Write 8–15 bullet points grouped by theme. Lead with the single most important point. Preserve specific numbers and names. No filler, no preamble. Use only information from the provided text."},
	"technical":   {SystemPrompt: "You are a technical writer. Summarize: what the system or approach does, how it works (architecture, methods, key decisions), results and benchmarks, and practical implications or limitations. Preserve version numbers, metrics, and technical terms exactly. Use markdown headers. Use only information from the provided text."},
	"executive":   {SystemPrompt: "You are a senior analyst. Write 3–5 sentences: the core claim or problem, the key evidence or approach, and the most important implication or recommendation. Be direct. Use only information from the provided text."},
}

// StylePrompt returns the system prompt for the given summary style.
// User-defined styles in config override built-in defaults.
func (c *Config) StylePrompt(style string) string {
	if sc, ok := c.Ingest.SummaryStyles[style]; ok && sc.SystemPrompt != "" {
		return sc.SystemPrompt
	}
	if sc, ok := builtinSummaryStyles[style]; ok {
		return sc.SystemPrompt
	}
	return builtinSummaryStyles["study-notes"].SystemPrompt
}

// builtinProfiles ships with the binary. Users can add/override in config.json.
var builtinProfiles = map[string]Profile{
	"oai-mini": {
		Provider: "openai",
		Model:    "gpt-4o-mini",
		Info: ProfileInfo{
			CostTier:      "very_low",
			CostVsValue:   "Best for bulk summarization, flash, and flashcard generation. Lowest cost. Weaker on nuanced analysis of dense academic content.",
			ContextWindow: 128_000,
			Pricing:       &ProfilePricing{Input: 0.15, Output: 0.60, CachedInput: 0.075},
		},
	},
	"oai-4.1": {
		Provider: "openai",
		Model:    "gpt-4.1",
		Info: ProfileInfo{
			CostTier:      "high",
			CostVsValue:   "Excellent for summaries of technical and academic content. Strong instruction following, large context. Good cost-per-quality for serious reading lists.",
			ContextWindow: 1_047_576,
			Pricing:       &ProfilePricing{Input: 2.00, Output: 8.00, CachedInput: 0.50},
		},
	},
	"oai-4o": {
		Provider: "openai",
		Model:    "gpt-4o",
		Info: ProfileInfo{
			CostTier:      "medium",
			CostVsValue:   "Balanced choice. Better than oai-mini for nuanced summarization, but not as cost-effective as oai-4.1 at the high end.",
			ContextWindow: 128_000,
			Pricing:       &ProfilePricing{Input: 2.50, Output: 10.00, CachedInput: 1.25},
		},
	},
	"oai-5-mini": {
		Provider: "openai",
		Model:    "gpt-5-mini",
		Info: ProfileInfo{
			CostTier:      "low",
			CostVsValue:   "Attractive middle tier. Significantly stronger than gpt-4o-mini for reasoning-heavy summarization, still much cheaper than gpt-4.1.",
			ContextWindow: 1_047_576,
			Pricing:       &ProfilePricing{Input: 0.25, Output: 2.00, CachedInput: 0.025},
		},
	},
	"oai-5": {
		Provider: "openai",
		Model:    "gpt-5",
		Info: ProfileInfo{
			CostTier:      "premium",
			CostVsValue:   "Best quality for deeply complex or long-form content. Use when summary quality is critical and cost is secondary.",
			ContextWindow: 1_047_576,
			Pricing:       &ProfilePricing{Input: 1.25, Output: 10.00, CachedInput: 0.125},
		},
	},
	"opus": {
		Provider: "anthropic",
		Model:    "claude-opus-4-6",
		Info: ProfileInfo{
			CostTier:      "premium",
			CostVsValue:   "Recommended for production summarization. Best coherence and reduction quality on long articles. Quality compounds in map-reduce — worth the cost.",
			ContextWindow: 200_000,
			Pricing:       &ProfilePricing{Input: 15.00, Output: 75.00},
		},
	},
	"sonnet": {
		Provider: "anthropic",
		Model:    "claude-sonnet-4-6",
		Info: ProfileInfo{
			CostTier:      "medium",
			CostVsValue:   "Recommended for production flashcard generation. Strong structured JSON output, good instruction following. Right balance for single-call tasks.",
			ContextWindow: 200_000,
			Pricing:       &ProfilePricing{Input: 3.00, Output: 15.00},
		},
	},
	"haiku": {
		Provider: "anthropic",
		Model:    "claude-haiku-4-5-20251001",
		Info: ProfileInfo{
			CostTier:      "very_low",
			CostVsValue:   "Recommended for production flash generation. Trivial single-call task — Haiku is fast, cheap, and more than capable for 3-5 sentence audio summaries.",
			ContextWindow: 200_000,
			Pricing:       &ProfilePricing{Input: 0.80, Output: 4.00},
		},
	},
	"oai-embed": {
		Provider: "openai",
		Model:    "text-embedding-3-small",
		Info: ProfileInfo{
			CostTier:      "very_low",
			CostVsValue:   "OpenAI text-embedding-3-small. Used for semantic search vector index. 1536 dims, ~$0.02/million tokens.",
			ContextWindow: 8_191,
			Pricing:       &ProfilePricing{Input: 0.02},
		},
	},
	"llama": {
		Provider: "ollama",
		Host:     "http://localhost:11434",
		Model:    "llama3.1:8b",
		Info: ProfileInfo{
			CostTier:      "local",
			CostVsValue:   "Free if you run Ollama locally. Good for experimentation and offline use. Lower quality ceiling than cloud models for dense academic content.",
			ContextWindow: 128_000,
		},
	},
	"qwen": {
		Provider: "ollama",
		Host:     "http://localhost:11434",
		Model:    "qwen2.5-coder:7b",
		Info: ProfileInfo{
			CostTier:      "local",
			CostVsValue:   "Free local option, stronger than llama on technical content. Good for code-heavy articles.",
			ContextWindow: 32_768,
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
			SummaryProfile:    "oai-mini",
			FlashProfile:      "oai-mini",
			FlashcardProfile:  "oai-mini",
			SummaryStyle:      "study-notes",
			FlashcardStyle:    "socratic",
			EmbedProfile:      "oai-embed",
			ChunkTokens:       900,
			SummaryMaxTokens:  2048,
			SummaryStyles:     builtinSummaryStyles,
			FlashSystemPrompt:  DefaultFlashSystemPrompt,
			FlashMaxTokens:     256,
			FlashcardMaxTokens: 2048,
			FlashcardStyles:    builtinFlashcardStyles,
			MinWords:           300,
		},
		PreferredModels: []string{
			"claude-opus-4-6", "claude-sonnet-4-6", "claude-haiku-4-5-20251001",
			"gpt-4.1", "gpt-4o-mini",
		},
		PreferredStyles: []string{"study-notes", "bullets", "technical"},
		Chat: ChatConfig{
			Profile:         "oai-mini",
			Strategy:        "tail",
			ContextLimit:    0,
			MaxOutputTokens: 0,
			MaxUserMessages: 50,
			VerbatimRatio:   0.4,
			GroundingMode:   "corpus-first",
			RAGMode:         "open",
		},
		AskX: AskXConfig{
			Profile:         "haiku",
			SystemPrompt:    DefaultAskXSystemPrompt,
			MaxOutputTokens: 4096,
		},
		WorkspacePopulate: WorkspacePopulateConfig{
			Profile:                    "haiku",
			Pass1Prompt:                DefaultPopulatePass1Prompt,
			Pass1WithCollectionsPrompt: DefaultPopulatePass1WithCollectionsPrompt,
			Pass2Prompt:                DefaultPopulatePass2Prompt,
			CostNote:                   DefaultWorkspacePopulateCostNote,
		},
		AgentPath: filepath.Join(dataRoot, "agent"),
		LogPath:   filepath.Join(dataRoot, "arc.log"),
		LogLevel:  "info",
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
		AgentPath       string             `json:"agent_path"`
		Profiles        map[string]Profile `json:"profiles"`
		Ingest          IngestConfig       `json:"ingest"`
		PreferredModels []string           `json:"preferred_models"`
		PreferredStyles []string           `json:"preferred_styles"`
		CookieJars      map[string]string  `json:"cookie_jars"`
		Chat               ChatConfig               `json:"chat"`
		AskX               AskXConfig               `json:"askx"`
		WorkspacePopulate  WorkspacePopulateConfig   `json:"workspace_populate"`
		LogPath            string                    `json:"log_path"`
		LogLevel           string                    `json:"log_level"`
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
	if overlay.AgentPath != "" {
		cfg.AgentPath = overlay.AgentPath
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
	if overlay.Ingest.ChunkTokens != 0 {
		cfg.Ingest.ChunkTokens = overlay.Ingest.ChunkTokens
	}
	if overlay.Ingest.SummaryMaxTokens != 0 {
		cfg.Ingest.SummaryMaxTokens = overlay.Ingest.SummaryMaxTokens
	}
	// Merge user summary styles on top of builtins
	for k, v := range overlay.Ingest.SummaryStyles {
		cfg.Ingest.SummaryStyles[k] = v
	}
	if overlay.Ingest.FlashSystemPrompt != "" {
		cfg.Ingest.FlashSystemPrompt = overlay.Ingest.FlashSystemPrompt
	}
	if overlay.Ingest.FlashMaxTokens != 0 {
		cfg.Ingest.FlashMaxTokens = overlay.Ingest.FlashMaxTokens
	}
	if overlay.Ingest.FlashcardMaxTokens != 0 {
		cfg.Ingest.FlashcardMaxTokens = overlay.Ingest.FlashcardMaxTokens
	}
	if overlay.Ingest.MinWords != 0 {
		cfg.Ingest.MinWords = overlay.Ingest.MinWords
	}
	if overlay.Ingest.CollectionSuggestProfile != "" {
		cfg.Ingest.CollectionSuggestProfile = overlay.Ingest.CollectionSuggestProfile
	}
	if overlay.Ingest.CollectionSuggestPrompt != "" {
		cfg.Ingest.CollectionSuggestPrompt = overlay.Ingest.CollectionSuggestPrompt
	}
	if overlay.Ingest.CollectionDescribePrompt != "" {
		cfg.Ingest.CollectionDescribePrompt = overlay.Ingest.CollectionDescribePrompt
	}
	for k, v := range overlay.Ingest.FlashcardStyles {
		cfg.Ingest.FlashcardStyles[k] = v
	}
	if len(overlay.PreferredModels) > 0 {
		cfg.PreferredModels = overlay.PreferredModels
	}
	if len(overlay.PreferredStyles) > 0 {
		cfg.PreferredStyles = overlay.PreferredStyles
	}
	if len(overlay.CookieJars) > 0 {
		cfg.CookieJars = overlay.CookieJars
	}
	if overlay.Chat.Profile != "" {
		cfg.Chat.Profile = overlay.Chat.Profile
	}
	if overlay.Chat.Strategy != "" {
		cfg.Chat.Strategy = overlay.Chat.Strategy
	}
	if overlay.Chat.ContextLimit != 0 {
		cfg.Chat.ContextLimit = overlay.Chat.ContextLimit
	}
	if overlay.Chat.MaxOutputTokens != 0 {
		cfg.Chat.MaxOutputTokens = overlay.Chat.MaxOutputTokens
	}
	if overlay.Chat.MaxUserMessages != 0 {
		cfg.Chat.MaxUserMessages = overlay.Chat.MaxUserMessages
	}
	if overlay.Chat.SummarizerProfile != "" {
		cfg.Chat.SummarizerProfile = overlay.Chat.SummarizerProfile
	}
	if overlay.Chat.VerbatimRatio != 0 {
		cfg.Chat.VerbatimRatio = overlay.Chat.VerbatimRatio
	}
	if overlay.Chat.GroundingMode != "" {
		cfg.Chat.GroundingMode = overlay.Chat.GroundingMode
	}
	if overlay.Chat.RAGMode != "" {
		cfg.Chat.RAGMode = overlay.Chat.RAGMode
	}
	if overlay.Chat.RAGInstruction != "" {
		cfg.Chat.RAGInstruction = overlay.Chat.RAGInstruction
	}
	if overlay.AskX.Profile != "" {
		cfg.AskX.Profile = overlay.AskX.Profile
	}
	if overlay.AskX.SystemPrompt != "" {
		cfg.AskX.SystemPrompt = overlay.AskX.SystemPrompt
	}
	if overlay.AskX.MaxOutputTokens != 0 {
		cfg.AskX.MaxOutputTokens = overlay.AskX.MaxOutputTokens
	}
	if overlay.WorkspacePopulate.Profile != "" {
		cfg.WorkspacePopulate.Profile = overlay.WorkspacePopulate.Profile
	}
	if overlay.WorkspacePopulate.Pass1Prompt != "" {
		cfg.WorkspacePopulate.Pass1Prompt = overlay.WorkspacePopulate.Pass1Prompt
	}
	if overlay.WorkspacePopulate.Pass1WithCollectionsPrompt != "" {
		cfg.WorkspacePopulate.Pass1WithCollectionsPrompt = overlay.WorkspacePopulate.Pass1WithCollectionsPrompt
	}
	if overlay.WorkspacePopulate.Pass2Prompt != "" {
		cfg.WorkspacePopulate.Pass2Prompt = overlay.WorkspacePopulate.Pass2Prompt
	}
	if overlay.WorkspacePopulate.CostNote != "" {
		cfg.WorkspacePopulate.CostNote = overlay.WorkspacePopulate.CostNote
	}
	if overlay.LogPath != "" {
		cfg.LogPath = overlay.LogPath
	}
	if overlay.LogLevel != "" {
		cfg.LogLevel = overlay.LogLevel
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

package agent

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jrniemiec/arc/ingest/feed"
	"github.com/jrniemiec/arc/internal/jsonc"
)

// ConfigTemplate is the commented starter config written when no agent config
// exists yet. `arc init` writes it during setup, and the feed writers below
// scaffold it on demand so a config created by /feed-add is documented too.
//
//go:embed config_template.jsonc
var ConfigTemplate []byte

// AgentConfig is the agent's own configuration file (~/.arc/agent/config.json).
// It is separate from the main arc config to keep agent-specific settings isolated
// and allow the agent to evolve independently.
type AgentConfig struct {
	// InterestProfile is a free-text description of the user's interests.
	// Used as the primary input to the relevance filter LLM.
	InterestProfile string `json:"interest_profile"`

	// Focus is an optional temporary emphasis — overrides or supplements
	// the interest profile for the current agent run. Corresponds to
	// `arc agent focus "..."` or the --focus flag.
	Focus string `json:"focus,omitempty"`

	// Notes are ad-hoc guidance messages from the user, set via
	// `arc agent note "..."`. Each note is a single sentence.
	Notes []string `json:"notes,omitempty"`

	// LearningGoals describes topics at different depth levels.
	LearningGoals []LearningGoal `json:"learning_goals,omitempty"`

	// FilterProfile is the LLM profile name used for relevance filtering.
	// Defaults to "haiku" (cheap, fast — filter runs per article).
	FilterProfile string `json:"filter_profile,omitempty"`

	// FilterPrompt is the system prompt template for the relevance filter.
	// Empty means feed.DefaultFilterPrompt. Placeholders — {interest_profile},
	// {feed_filter}, {recent_titles}, {top_tags}, {collections} — are filled
	// per feed; {response_format} carries the JSON contract the parser needs
	// and is appended automatically when the template omits it.
	FilterPrompt string `json:"filter_prompt,omitempty"`

	// FilterSummaryMaxChars caps how much of a feed item's summary reaches the
	// filter. Zero means feed.DefaultSummaryMaxChars. Raising it costs input
	// tokens on every item in every feed.
	FilterSummaryMaxChars int `json:"filter_summary_max_chars,omitempty"`

	// SummaryProfile is the LLM profile name used for summarizing ingested articles.
	// Defaults to "haiku" — agent runs ingest many articles so speed matters more than
	// summary quality. Override to "sonnet" for higher-quality summaries.
	SummaryProfile string `json:"summary_profile,omitempty"`

	// FlashProfile is the LLM profile name used for flash (audio) summary generation.
	// Defaults to the global ingest.flash_profile if empty.
	// Flash generation is a single short call — a cheaper model than summary is fine.
	FlashProfile string `json:"flash_profile,omitempty"`

	// Languages is an optional list of ISO 639-1 language codes to accept (e.g. ["en"]).
	// Articles whose detected language is not in the list are skipped before ingest.
	// Empty means accept all languages.
	Languages []string `json:"languages,omitempty"`

	// Feeds is the list of RSS/Atom feeds to poll.
	Feeds []FeedConfig `json:"feeds"`
}

// LearningGoal describes one topic at a specified depth level.
type LearningGoal struct {
	Topic string `json:"topic"`
	// Depth is one of: "building" (hands-on, building from scratch),
	// "exploring" (broad survey, not building), "awareness" (just keep up).
	Depth string `json:"depth"`
}

// FeedConfig describes one RSS/Atom feed and how to filter it.
type FeedConfig struct {
	// URL is the RSS/Atom feed endpoint.
	URL string `json:"url"`

	// Name is a human-readable label shown in logs. Inferred from feed if empty.
	Name string `json:"name,omitempty"`

	// Filter is an optional per-feed narrowing instruction passed to the LLM.
	// Example: "only Kubernetes and distributed systems posts"
	Filter string `json:"filter,omitempty"`

	// Tags is an optional list of feed-native tags/categories to pre-filter
	// at the RSS level before calling the LLM. Empty means accept all.
	Tags []string `json:"tags,omitempty"`

	// BlockDomains is an optional list of hostnames to reject before any LLM
	// call. Items whose URL host matches any entry (exact or eTLD+1) are
	// dropped silently. Example: ["pagedout.institute", "example.com"]
	BlockDomains []string `json:"block_domains,omitempty"`

	// Disabled skips this feed without removing it from the config.
	Disabled bool `json:"disabled,omitempty"`
}

// FilterProfileName returns the effective filter profile name.
func (c *AgentConfig) FilterProfileName() string {
	if c.FilterProfile != "" {
		return c.FilterProfile
	}
	return "haiku"
}

// FilterPromptTemplate returns the effective filter prompt template.
func (c *AgentConfig) FilterPromptTemplate() string {
	if strings.TrimSpace(c.FilterPrompt) != "" {
		return c.FilterPrompt
	}
	return feed.DefaultFilterPrompt
}

// FilterSummaryMaxCharsOrDefault returns the effective summary cap for filter input.
func (c *AgentConfig) FilterSummaryMaxCharsOrDefault() int {
	if c.FilterSummaryMaxChars > 0 {
		return c.FilterSummaryMaxChars
	}
	return feed.DefaultSummaryMaxChars
}

// SummaryProfileName returns the effective summary profile name for agent-ingested articles.
func (c *AgentConfig) SummaryProfileName() string {
	if c.SummaryProfile != "" {
		return c.SummaryProfile
	}
	return "haiku"
}

// FlashProfileName returns the effective flash profile name for agent-ingested articles.
// Returns empty string when not set, so the pipeline falls back to the global ingest.flash_profile.
func (c *AgentConfig) FlashProfileName() string {
	return c.FlashProfile
}

// LoadAgentConfig reads the agent config from path.
// Accepts both .jsonc (preferred) and .json — if path ends in .json and a
// .jsonc sibling exists, the .jsonc file is used instead.
// Returns a minimal default config (empty feeds, no profile) if the file does not exist.
func LoadAgentConfig(path string) (AgentConfig, error) {
	path = resolvePath(path)

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return AgentConfig{}, nil
		}
		return AgentConfig{}, fmt.Errorf("open agent config: %w", err)
	}

	var cfg AgentConfig
	if err := jsonc.Unmarshal(data, &cfg); err != nil {
		return AgentConfig{}, fmt.Errorf("decode agent config: %w", err)
	}
	return cfg, nil
}

// AddFeed appends f to the feeds list in the config at path and saves.
func AddFeed(path string, f FeedConfig) error {
	cfg, err := LoadAgentConfig(path)
	if err != nil {
		return err
	}
	cfg.Feeds = append(cfg.Feeds, f)
	return saveFeeds(path, cfg.Feeds)
}

// UpdateFeed replaces the feed at idx in the config at path and saves.
func UpdateFeed(path string, idx int, f FeedConfig) error {
	cfg, err := LoadAgentConfig(path)
	if err != nil {
		return err
	}
	if idx < 0 || idx >= len(cfg.Feeds) {
		return fmt.Errorf("feed index %d out of range", idx)
	}
	cfg.Feeds[idx] = f
	return saveFeeds(path, cfg.Feeds)
}

// DeleteFeed removes the feed at idx from the config at path and saves.
func DeleteFeed(path string, idx int) error {
	cfg, err := LoadAgentConfig(path)
	if err != nil {
		return err
	}
	if idx < 0 || idx >= len(cfg.Feeds) {
		return fmt.Errorf("feed index %d out of range", idx)
	}
	cfg.Feeds = append(cfg.Feeds[:idx], cfg.Feeds[idx+1:]...)
	return saveFeeds(path, cfg.Feeds)
}

// ToggleFeed flips the Disabled field of the feed at idx in the config at path and saves.
func ToggleFeed(path string, idx int) error {
	cfg, err := LoadAgentConfig(path)
	if err != nil {
		return err
	}
	if idx < 0 || idx >= len(cfg.Feeds) {
		return fmt.Errorf("feed index %d out of range", idx)
	}
	cfg.Feeds[idx].Disabled = !cfg.Feeds[idx].Disabled
	return saveFeeds(path, cfg.Feeds)
}

// resolvePath prefers the .jsonc sibling when the caller passes the legacy
// .json path. Every reader and writer goes through it so they agree on which
// file is authoritative.
func resolvePath(path string) string {
	if !strings.HasSuffix(path, ".json") {
		return path
	}
	jsoncPath := filepath.Join(filepath.Dir(path), "config.jsonc")
	if _, err := os.Stat(jsoncPath); err == nil {
		return jsoncPath
	}
	return path
}

// saveFeeds writes feeds into the config at path, replacing only the
// "feeds": [...] span and leaving every other byte — comments, unset fields,
// formatting — untouched. Re-marshalling the whole struct would drop all
// JSONC comments and every field carrying omitempty, so feed edits must not
// round-trip through SaveAgentConfig.
//
// When the config does not exist yet it is scaffolded from ConfigTemplate
// first, so a config created by the TUI is as documented as one from arc init.
func saveFeeds(path string, feeds []FeedConfig) error {
	path = resolvePath(path)

	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("read agent config: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("create agent dir: %w", err)
		}
		data = ConfigTemplate
	}

	// Indent one level in: the "feeds" key sits at the root, two spaces deep.
	encoded, err := json.MarshalIndent(feeds, "  ", "  ")
	if err != nil {
		return fmt.Errorf("marshal feeds: %w", err)
	}
	if len(feeds) == 0 {
		encoded = []byte("[]")
	}

	out, err := replaceFeedsSpan(data, encoded)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return fmt.Errorf("write agent config: %w", err)
	}
	return nil
}

// replaceFeedsSpan swaps the value of the root "feeds" key for encoded.
// If the key is absent the pair is injected before the final closing brace.
func replaceFeedsSpan(data, encoded []byte) ([]byte, error) {
	keyAt := findKey(data, "feeds")
	if keyAt < 0 {
		idx := bytes.LastIndexByte(data, '}')
		if idx < 0 {
			return nil, fmt.Errorf("malformed agent config: no closing brace found")
		}
		inner := bytes.TrimSpace(data[:idx])
		sep := ",\n  "
		if bytes.HasSuffix(inner, []byte("{")) {
			sep = "\n  "
		}
		inject := append([]byte(sep+`"feeds": `), encoded...)
		inject = append(inject, '\n')
		out := make([]byte, 0, len(data)+len(inject))
		out = append(out, data[:idx]...)
		out = append(out, inject...)
		return append(out, data[idx:]...), nil
	}

	openAt := indexOfValue(data, keyAt, '[')
	if openAt < 0 {
		return nil, fmt.Errorf("malformed agent config: %q is not an array", "feeds")
	}
	closeAt := matchBracket(data, openAt)
	if closeAt < 0 {
		return nil, fmt.Errorf("malformed agent config: unterminated feeds array")
	}

	out := make([]byte, 0, len(data)+len(encoded))
	out = append(out, data[:openAt]...)
	out = append(out, encoded...)
	return append(out, data[closeAt+1:]...), nil
}

// The scan helpers below walk JSONC as text rather than parsing it, so a
// rewrite can touch one span and leave the rest of the file byte-identical.
// All of them step over strings and // and /* */ comments, so a URL containing
// a bracket or a commented-out example feed cannot be mistaken for structure.

// findKey returns the index of the opening quote of the root-level key name,
// or -1 if the file has no such key.
func findKey(data []byte, name string) int {
	quoted := `"` + name + `"`
	depth := 0
	for i := 0; i < len(data); {
		switch c := data[i]; {
		case c == '/' && i+1 < len(data) && (data[i+1] == '/' || data[i+1] == '*'):
			i = skipComment(data, i)
		case c == '"':
			end := skipString(data, i)
			if depth == 1 && string(data[i:end]) == quoted {
				if j := skipNonCode(data, end); j < len(data) && data[j] == ':' {
					return i
				}
			}
			i = end
		case c == '{' || c == '[':
			depth++
			i++
		case c == '}' || c == ']':
			depth--
			i++
		default:
			i++
		}
	}
	return -1
}

// indexOfValue returns the index of want — the byte opening the value — for
// the key whose opening quote is at keyAt, or -1 if the value is a different
// kind than expected.
func indexOfValue(data []byte, keyAt int, want byte) int {
	i := skipNonCode(data, skipString(data, keyAt))
	if i >= len(data) || data[i] != ':' {
		return -1
	}
	i = skipNonCode(data, i+1)
	if i >= len(data) || data[i] != want {
		return -1
	}
	return i
}

// matchBracket returns the index of the bracket closing the one at openAt,
// or -1 when it is never closed.
func matchBracket(data []byte, openAt int) int {
	depth := 0
	for i := openAt; i < len(data); {
		switch c := data[i]; {
		case c == '/' && i+1 < len(data) && (data[i+1] == '/' || data[i+1] == '*'):
			i = skipComment(data, i)
		case c == '"':
			i = skipString(data, i)
		case c == '{' || c == '[':
			depth++
			i++
		case c == '}' || c == ']':
			depth--
			if depth == 0 {
				return i
			}
			i++
		default:
			i++
		}
	}
	return -1
}

// skipString returns the index just past the string literal starting at i.
func skipString(data []byte, i int) int {
	for j := i + 1; j < len(data); j++ {
		switch data[j] {
		case '\\':
			j++ // escaped byte is part of the literal
		case '"':
			return j + 1
		}
	}
	return len(data)
}

// skipComment returns the index just past the comment starting at i.
func skipComment(data []byte, i int) int {
	if i+1 >= len(data) {
		return i + 1
	}
	if data[i+1] == '/' {
		if end := bytes.IndexByte(data[i:], '\n'); end >= 0 {
			return i + end
		}
		return len(data)
	}
	if end := bytes.Index(data[i+2:], []byte("*/")); end >= 0 {
		return i + 2 + end + 2
	}
	return len(data)
}

// skipNonCode returns the index of the next byte at or after i that is neither
// whitespace nor part of a comment.
func skipNonCode(data []byte, i int) int {
	for i < len(data) {
		c := data[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			i++
			continue
		}
		if c == '/' && i+1 < len(data) && (data[i+1] == '/' || data[i+1] == '*') {
			i = skipComment(data, i)
			continue
		}
		return i
	}
	return len(data)
}

// SaveAgentConfig writes cfg to path as indented JSON.
//
// This rewrites the entire file, dropping comments and unset optional fields.
// Feed edits use saveFeeds instead; reach for this only when the whole config
// is genuinely being replaced.
func SaveAgentConfig(path string, cfg AgentConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal agent config: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write agent config: %w", err)
	}
	return nil
}

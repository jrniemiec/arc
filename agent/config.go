package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jrniemiec/arc/internal/jsonc"
)

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

	// SummaryProfile is the LLM profile name used for summarizing ingested articles.
	// Defaults to "haiku" — agent runs ingest many articles so speed matters more than
	// summary quality. Override to "sonnet" for higher-quality summaries.
	SummaryProfile string `json:"summary_profile,omitempty"`

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

// SummaryProfileName returns the effective summary profile name for agent-ingested articles.
func (c *AgentConfig) SummaryProfileName() string {
	if c.SummaryProfile != "" {
		return c.SummaryProfile
	}
	return "haiku"
}

// LoadAgentConfig reads the agent config from path.
// Accepts both .jsonc (preferred) and .json — if path ends in .json and a
// .jsonc sibling exists, the .jsonc file is used instead.
// Returns a minimal default config (empty feeds, no profile) if the file does not exist.
func LoadAgentConfig(path string) (AgentConfig, error) {
	// Prefer .jsonc sibling when caller passes the legacy .json path.
	if strings.HasSuffix(path, ".json") {
		jsoncPath := filepath.Join(filepath.Dir(path), "config.jsonc")
		if _, err := os.Stat(jsoncPath); err == nil {
			path = jsoncPath
		}
	}

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
	return SaveAgentConfig(path, cfg)
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
	return SaveAgentConfig(path, cfg)
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
	return SaveAgentConfig(path, cfg)
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
	return SaveAgentConfig(path, cfg)
}

// SaveAgentConfig writes cfg to path as indented JSON.
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

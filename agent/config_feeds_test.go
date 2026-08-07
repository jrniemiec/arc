package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jrniemiec/arc/ingest/feed"
	"github.com/jrniemiec/arc/internal/jsonc"
)

// writeCfg writes contents to a config.jsonc in a temp dir and returns its path.
func writeCfg(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.jsonc")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func readCfg(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	return string(data)
}

// The regression this whole change exists for: a feed edit used to re-marshal
// the struct, erasing every comment and every unset optional field.
func TestAddFeedPreservesCommentsAndUnsetFields(t *testing.T) {
	path := writeCfg(t, `{
  // What I care about.
  "interest_profile": "transformer internals",

  /* Block comments survive too. */
  "learning_goals": [
    { "topic": "attention", "depth": "building" }
  ],

  "feeds": [],

  "filter_profile": "haiku",
  "languages": ["en"]
}
`)

	if err := AddFeed(path, FeedConfig{URL: "https://example.com/feed.xml", Name: "Example"}); err != nil {
		t.Fatalf("AddFeed: %v", err)
	}

	got := readCfg(t, path)
	for _, want := range []string{
		"// What I care about.",
		"/* Block comments survive too. */",
		`"filter_profile": "haiku"`,
		`"languages": ["en"]`,
		`"topic": "attention"`,
		"https://example.com/feed.xml",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("lost %q after AddFeed:\n%s", want, got)
		}
	}

	cfg, err := LoadAgentConfig(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(cfg.Feeds) != 1 || cfg.Feeds[0].Name != "Example" {
		t.Fatalf("feeds = %+v, want one Example feed", cfg.Feeds)
	}
	if cfg.InterestProfile != "transformer internals" || cfg.FilterProfile != "haiku" {
		t.Errorf("scalar fields lost: %+v", cfg)
	}
	if len(cfg.LearningGoals) != 1 || len(cfg.Languages) != 1 {
		t.Errorf("list fields lost: %+v", cfg)
	}
}

// A config created by the TUI should be as documented as one from arc init.
func TestAddFeedScaffoldsTemplateWhenMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent", "config.jsonc")

	if err := AddFeed(path, FeedConfig{URL: "https://example.com/feed.xml"}); err != nil {
		t.Fatalf("AddFeed: %v", err)
	}

	got := readCfg(t, path)
	if !strings.Contains(got, "Interest profile") || !strings.Contains(got, "filter_profile") {
		t.Errorf("missing config was not scaffolded from the template:\n%s", got)
	}

	cfg, err := LoadAgentConfig(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(cfg.Feeds) != 1 || cfg.Feeds[0].URL != "https://example.com/feed.xml" {
		t.Fatalf("feeds = %+v, want the added feed", cfg.Feeds)
	}
	if cfg.FilterProfileName() != "haiku" {
		t.Errorf("FilterProfileName() = %q, want haiku from template", cfg.FilterProfileName())
	}
}

// The template ships with commented-out example feeds inside the array. Those
// comments must not end the span early or survive as junk.
func TestAddFeedIntoTemplateWithCommentedExamples(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.jsonc")
	if err := os.WriteFile(path, ConfigTemplate, 0o644); err != nil {
		t.Fatalf("write template: %v", err)
	}

	if err := AddFeed(path, FeedConfig{URL: "https://example.com/a.xml"}); err != nil {
		t.Fatalf("AddFeed: %v", err)
	}
	if err := AddFeed(path, FeedConfig{URL: "https://example.com/b.xml"}); err != nil {
		t.Fatalf("second AddFeed: %v", err)
	}

	cfg, err := LoadAgentConfig(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(cfg.Feeds) != 2 {
		t.Fatalf("feeds = %+v, want 2", cfg.Feeds)
	}
	if strings.Contains(readCfg(t, path), "news.ycombinator.com") {
		t.Error("commented-out example feed leaked into the written array")
	}
}

func TestUpdateToggleDeleteFeedPreserveComments(t *testing.T) {
	const header = "// keep me\n"
	path := writeCfg(t, header+`{
  "interest_profile": "x",
  "feeds": [
    { "url": "https://a.example/feed", "name": "A" },
    { "url": "https://b.example/feed", "name": "B" }
  ],
  "summary_profile": "sonnet"
}
`)

	if err := UpdateFeed(path, "https://a.example/feed", FeedConfig{URL: "https://a.example/feed", Name: "A2"}); err != nil {
		t.Fatalf("UpdateFeed: %v", err)
	}
	if err := ToggleFeed(path, "https://b.example/feed"); err != nil {
		t.Fatalf("ToggleFeed: %v", err)
	}

	cfg, err := LoadAgentConfig(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if cfg.Feeds[0].Name != "A2" {
		t.Errorf("feed 0 name = %q, want A2", cfg.Feeds[0].Name)
	}
	if !cfg.Feeds[1].Disabled {
		t.Error("feed 1 should be disabled after ToggleFeed")
	}
	if cfg.SummaryProfile != "sonnet" {
		t.Errorf("summary_profile lost: %q", cfg.SummaryProfile)
	}

	if err := DeleteFeed(path, "https://a.example/feed"); err != nil {
		t.Fatalf("DeleteFeed: %v", err)
	}
	got := readCfg(t, path)
	if !strings.HasPrefix(got, header) {
		t.Errorf("leading comment lost:\n%s", got)
	}
	if strings.Contains(got, "a.example") {
		t.Error("deleted feed still present")
	}

	cfg, err = LoadAgentConfig(path)
	if err != nil {
		t.Fatalf("reload after delete: %v", err)
	}
	if len(cfg.Feeds) != 1 || cfg.Feeds[0].Name != "B" {
		t.Fatalf("feeds = %+v, want only B", cfg.Feeds)
	}
}

// Deleting the last feed leaves an empty array, not a broken file.
func TestDeleteLastFeedLeavesEmptyArray(t *testing.T) {
	path := writeCfg(t, `{
  "interest_profile": "x",
  "feeds": [{ "url": "https://a.example/feed" }]
}
`)

	if err := DeleteFeed(path, "https://a.example/feed"); err != nil {
		t.Fatalf("DeleteFeed: %v", err)
	}
	cfg, err := LoadAgentConfig(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(cfg.Feeds) != 0 {
		t.Fatalf("feeds = %+v, want none", cfg.Feeds)
	}
	if got := readCfg(t, path); !strings.Contains(got, `"feeds": []`) {
		t.Errorf("want an empty array literal:\n%s", got)
	}
}

// A config with no feeds key at all gets one injected.
func TestAddFeedInjectsMissingFeedsKey(t *testing.T) {
	path := writeCfg(t, `{
  // no feeds key here
  "interest_profile": "x"
}
`)

	if err := AddFeed(path, FeedConfig{URL: "https://example.com/feed.xml"}); err != nil {
		t.Fatalf("AddFeed: %v", err)
	}
	cfg, err := LoadAgentConfig(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(cfg.Feeds) != 1 {
		t.Fatalf("feeds = %+v, want 1", cfg.Feeds)
	}
	if cfg.InterestProfile != "x" {
		t.Errorf("interest_profile lost: %q", cfg.InterestProfile)
	}
	if !strings.Contains(readCfg(t, path), "// no feeds key here") {
		t.Error("comment lost")
	}
}

func TestAddFeedInjectsIntoEmptyObject(t *testing.T) {
	path := writeCfg(t, "{}\n")

	if err := AddFeed(path, FeedConfig{URL: "https://example.com/feed.xml"}); err != nil {
		t.Fatalf("AddFeed: %v", err)
	}
	cfg, err := LoadAgentConfig(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(cfg.Feeds) != 1 {
		t.Fatalf("feeds = %+v, want 1", cfg.Feeds)
	}
}

// A bracket inside a string must not be mistaken for the end of the array.
func TestFeedURLContainingBracket(t *testing.T) {
	path := writeCfg(t, `{
  "feeds": [
    { "url": "https://example.com/feed?q=a]b", "name": "Tricky" }
  ],
  "filter_profile": "haiku"
}
`)

	if err := AddFeed(path, FeedConfig{URL: "https://example.com/second"}); err != nil {
		t.Fatalf("AddFeed: %v", err)
	}
	cfg, err := LoadAgentConfig(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(cfg.Feeds) != 2 {
		t.Fatalf("feeds = %+v, want 2", cfg.Feeds)
	}
	if cfg.FilterProfile != "haiku" {
		t.Errorf("filter_profile lost — array span overran: %q", cfg.FilterProfile)
	}
}

// A nested "feeds" key belonging to some other object must not be targeted.
func TestFindKeyIgnoresNestedAndQuotedMatches(t *testing.T) {
	data := []byte(`{
  "interest_profile": "I like \"feeds\" a lot",
  "nested": { "feeds": [1, 2, 3] },
  "feeds": []
}`)

	at := findKey(data, "feeds")
	if at < 0 {
		t.Fatal("findKey returned -1")
	}
	if got := string(data[at:]); !strings.HasPrefix(got, `"feeds": []`) {
		t.Errorf("matched the wrong key: %q", got)
	}
}

func TestReplaceFeedsSpanRejectsUnterminatedArray(t *testing.T) {
	_, err := replaceFeedsSpan([]byte(`{"feeds": [`), []byte("[]"))
	if err == nil {
		t.Fatal("want an error for an unterminated array, got nil")
	}
	if !strings.Contains(err.Error(), "unterminated") {
		t.Errorf("err = %v, want it to name the problem", err)
	}
}

func TestReplaceFeedsSpanRejectsNonArrayFeeds(t *testing.T) {
	_, err := replaceFeedsSpan([]byte(`{"feeds": "nope"}`), []byte("[]"))
	if err == nil {
		t.Fatal("want an error when feeds is not an array, got nil")
	}
}

// The template ships the prompt verbatim; if the Go default changes without
// the template following, users editing config.jsonc see stale text.
func TestTemplateFilterPromptMatchesDefault(t *testing.T) {
	var cfg AgentConfig
	if err := jsonc.Unmarshal(ConfigTemplate, &cfg); err != nil {
		t.Fatalf("template does not parse: %v", err)
	}
	if cfg.FilterPrompt != feed.DefaultFilterPrompt {
		t.Errorf("template filter_prompt has drifted from feed.DefaultFilterPrompt:\n--- template ---\n%s\n--- default ---\n%s",
			cfg.FilterPrompt, feed.DefaultFilterPrompt)
	}
	if cfg.FilterSummaryMaxChars != feed.DefaultSummaryMaxChars {
		t.Errorf("template filter_summary_max_chars = %d, want %d",
			cfg.FilterSummaryMaxChars, feed.DefaultSummaryMaxChars)
	}
}

func TestFilterPromptAccessors(t *testing.T) {
	var empty AgentConfig
	if empty.FilterPromptTemplate() != feed.DefaultFilterPrompt {
		t.Error("unset filter_prompt should fall back to the built-in default")
	}
	if empty.FilterSummaryMaxCharsOrDefault() != feed.DefaultSummaryMaxChars {
		t.Error("unset filter_summary_max_chars should fall back to the built-in default")
	}

	custom := AgentConfig{FilterPrompt: "mine", FilterSummaryMaxChars: 120}
	if custom.FilterPromptTemplate() != "mine" {
		t.Error("configured filter_prompt ignored")
	}
	if custom.FilterSummaryMaxCharsOrDefault() != 120 {
		t.Error("configured filter_summary_max_chars ignored")
	}

	blank := AgentConfig{FilterPrompt: "  \n "}
	if blank.FilterPromptTemplate() != feed.DefaultFilterPrompt {
		t.Error("whitespace-only filter_prompt should fall back to the default")
	}
}

// Feed edits must not disturb the prompt sitting next to them in the file.
func TestSaveFeedsPreservesFilterPrompt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.jsonc")
	if err := os.WriteFile(path, ConfigTemplate, 0o644); err != nil {
		t.Fatalf("write template: %v", err)
	}
	if err := AddFeed(path, FeedConfig{URL: "https://example.com/feed.xml"}); err != nil {
		t.Fatalf("AddFeed: %v", err)
	}
	cfg, err := LoadAgentConfig(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if cfg.FilterPrompt != feed.DefaultFilterPrompt {
		t.Error("filter_prompt changed after a feed edit")
	}
}

// The template quotes the response contract in its comments so users can see
// what {response_format} expands to. That copy must not drift from the real
// one — a stale example is worse than none, since it invites hand-pasting.
func TestTemplateDocumentsResponseFormatVerbatim(t *testing.T) {
	tmpl := string(ConfigTemplate)
	for _, line := range strings.Split(feed.ResponseFormat, "\n") {
		if !strings.Contains(tmpl, "//   "+line) {
			t.Errorf("template comment is missing this response-format line:\n%s", line)
		}
	}
	if !strings.Contains(tmpl, "DO NOT uncomment") {
		t.Error("the warning against pasting the contract into the prompt is gone")
	}
}

func TestBuildLibraryContextIncludesCollections(t *testing.T) {
	root := t.TempDir()
	for slug, desc := range map[string]string{
		"transformers": "circuit analysis and attention internals",
		"go-systems":   "",
	} {
		dir := filepath.Join(root, "collections", slug)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		meta := fmt.Sprintf(`{"slug":%q,"name":%q,"description":%q}`, slug, slug, desc)
		if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(meta), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got := listCollections(root)
	want := map[string]bool{
		"transformers: circuit analysis and attention internals": true,
		"go-systems": true, // no description — slug alone, not a dangling colon
	}
	if len(got) != len(want) {
		t.Fatalf("collections = %v, want %d entries", got, len(want))
	}
	for _, c := range got {
		if !want[c] {
			t.Errorf("unexpected entry %q", c)
		}
	}

	if listCollections("") != nil {
		t.Error("empty dataRoot should yield no collections")
	}
	if listCollections(filepath.Join(root, "nope")) != nil {
		t.Error("missing dataRoot should degrade to no collections, not panic")
	}
}

// The hazard URL addressing exists to prevent: the file changed under a
// loaded list, so the position the user picked now names a different feed.
func TestEditsFollowTheFeedNotThePosition(t *testing.T) {
	path := writeCfg(t, `{
  "feeds": [
    { "url": "https://a.example/feed", "name": "A" },
    { "url": "https://b.example/feed", "name": "B" },
    { "url": "https://c.example/feed", "name": "C" }
  ]
}
`)
	// Someone edits the config in $EDITOR and drops the first feed; a TUI
	// loaded before that still thinks B is at index 1 and C at index 2.
	if err := DeleteFeed(path, "https://a.example/feed"); err != nil {
		t.Fatalf("DeleteFeed: %v", err)
	}

	// Toggling "C" by its URL must hit C, though C now sits where B was.
	if err := ToggleFeed(path, "https://c.example/feed"); err != nil {
		t.Fatalf("ToggleFeed: %v", err)
	}

	cfg, err := LoadAgentConfig(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	for _, f := range cfg.Feeds {
		switch f.URL {
		case "https://c.example/feed":
			if !f.Disabled {
				t.Error("C should be disabled")
			}
		case "https://b.example/feed":
			if f.Disabled {
				t.Error("B was toggled — the edit followed the position, not the feed")
			}
		}
	}
}

func TestEditsFailOnUnknownURL(t *testing.T) {
	path := writeCfg(t, `{"feeds": [{ "url": "https://a.example/feed" }]}`)

	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{"update", func() error { return UpdateFeed(path, "https://gone.example/feed", FeedConfig{URL: "x"}) }},
		{"delete", func() error { return DeleteFeed(path, "https://gone.example/feed") }},
		{"toggle", func() error { return ToggleFeed(path, "https://gone.example/feed") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			if err == nil {
				t.Fatal("want an error for a feed that is no longer there")
			}
			if !strings.Contains(err.Error(), "changed on disk") {
				t.Errorf("err = %v, want it to explain why", err)
			}
		})
	}

	// The failed edits must not have touched the file.
	cfg, err := LoadAgentConfig(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(cfg.Feeds) != 1 || cfg.Feeds[0].URL != "https://a.example/feed" {
		t.Fatalf("feeds = %+v, want the original untouched", cfg.Feeds)
	}
}

// Duplicate URLs are ambiguous; guessing would reintroduce the silent
// wrong-feed edit that URL addressing is meant to remove.
func TestDuplicateURLIsRejected(t *testing.T) {
	path := writeCfg(t, `{
  "feeds": [
    { "url": "https://dup.example/feed", "name": "one" },
    { "url": "https://dup.example/feed", "name": "two" }
  ]
}
`)
	err := ToggleFeed(path, "https://dup.example/feed")
	if err == nil {
		t.Fatal("want an error for a duplicated url")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("err = %v, want it to name the fix", err)
	}
}

// Changing the URL is a legitimate edit — that is how a typo gets fixed.
func TestUpdateFeedCanChangeTheURL(t *testing.T) {
	path := writeCfg(t, `{"feeds": [{ "url": "https://tpyo.example/feed", "name": "A" }]}`)

	if err := UpdateFeed(path, "https://tpyo.example/feed",
		FeedConfig{URL: "https://typo.example/feed", Name: "A"}); err != nil {
		t.Fatalf("UpdateFeed: %v", err)
	}
	cfg, err := LoadAgentConfig(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(cfg.Feeds) != 1 || cfg.Feeds[0].URL != "https://typo.example/feed" {
		t.Fatalf("feeds = %+v, want the corrected url", cfg.Feeds)
	}
}

// A write must never leave a half-written config behind: the replacement is
// a rename, so the file is either the old one or the new one.
func TestWritesAreAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.jsonc")
	if err := os.WriteFile(path, ConfigTemplate, 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := AddFeed(path, FeedConfig{URL: "https://example.com/feed.xml"}); err != nil {
		t.Fatalf("AddFeed: %v", err)
	}

	// No temp files left in the directory.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "config.jsonc" {
			t.Errorf("stray file left behind: %s", e.Name())
		}
	}

	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Mode().Perm() != before.Mode().Perm() {
		t.Errorf("permissions changed: %v → %v", before.Mode().Perm(), after.Mode().Perm())
	}
	if _, err := LoadAgentConfig(path); err != nil {
		t.Errorf("config unreadable after write: %v", err)
	}
}

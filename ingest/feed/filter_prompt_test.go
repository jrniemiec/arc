package feed

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

// legacyPrompt is the prompt as it was built before the template existed.
// The default template must reproduce it byte for byte, so externalizing the
// text changes nothing for anyone who does not edit it.
func legacyPrompt(cfg FilterConfig) string {
	var sb strings.Builder
	sb.WriteString("You are a content relevance filter for a personal knowledge library.\n\n")
	sb.WriteString("## User interests\n")
	sb.WriteString(cfg.InterestProfile)
	sb.WriteString("\n\n")

	if cfg.FeedFilter != "" {
		sb.WriteString("## Feed-specific instructions\n")
		sb.WriteString(cfg.FeedFilter)
		sb.WriteString("\n\n")
	}

	if cfg.Library != nil {
		lib := cfg.Library
		if len(lib.RecentTitles) > 0 {
			sb.WriteString("## Recently ingested articles (newest first)\n")
			for _, t := range lib.RecentTitles {
				sb.WriteString("- ")
				sb.WriteString(t)
				sb.WriteString("\n")
			}
			sb.WriteString("\n")
		}
		if len(lib.TopTags) > 0 {
			sb.WriteString("## Most frequent topics in the library\n")
			sb.WriteString(strings.Join(lib.TopTags, ", "))
			sb.WriteString("\n\n")
		}
		if len(lib.Collections) > 0 {
			sb.WriteString("## Existing collections\n")
			for _, c := range lib.Collections {
				sb.WriteString("- ")
				sb.WriteString(c)
				sb.WriteString("\n")
			}
			sb.WriteString("\n")
		}
	} else if len(cfg.Collections) > 0 {
		sb.WriteString("## Existing collections\n")
		for _, c := range cfg.Collections {
			sb.WriteString("- ")
			sb.WriteString(c)
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	sb.WriteString(`Evaluate the article below. Respond with JSON only, no markdown fences:
{"verdict": "ingest|skip|maybe", "reason": "one sentence why", "collections": ["suggested-collection-slugs"]}

Rules:
- "ingest" = clearly relevant to the user's interests
- "skip" = not relevant or too shallow/clickbait
- "maybe" = borderline, might be interesting
- Use the recently ingested list to avoid re-ingesting already-covered topics.
- Be selective. When in doubt, skip.
- Non-English articles: skip unless the topic is exceptionally relevant.`)

	return sb.String()
}

func TestDefaultTemplateMatchesLegacyPrompt(t *testing.T) {
	cases := []struct {
		name string
		cfg  FilterConfig
	}{
		{"everything set", FilterConfig{
			InterestProfile: "transformer internals",
			FeedFilter:      "only mechanistic interpretability",
			Library: &LibraryContext{
				RecentTitles: []string{"Attention Is All You Need", "Induction Heads"},
				TopTags:      []string{"transformers", "attention"},
				Collections:  []string{"transformers: circuit analysis"},
			},
		}},
		{"no feed filter", FilterConfig{
			InterestProfile: "transformer internals",
			Library:         &LibraryContext{TopTags: []string{"go"}},
		}},
		{"no library at all", FilterConfig{InterestProfile: "transformer internals"}},
		{"standalone collections", FilterConfig{
			InterestProfile: "transformer internals",
			Collections:     []string{"go: systems programming"},
		}},
		{"empty library fields", FilterConfig{
			InterestProfile: "transformer internals",
			Library:         &LibraryContext{},
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := legacyPrompt(tc.cfg)
			got := RenderSystemPrompt(tc.cfg)
			if got != want {
				t.Errorf("prompt drifted from the legacy output:\n--- got ---\n%s\n--- want ---\n%s", got, want)
			}
		})
	}
}

func TestCustomPromptReplacesEverything(t *testing.T) {
	cfg := FilterConfig{
		InterestProfile: "kernels",
		FeedFilter:      "eBPF only",
		Prompt:          "Judge this.\n\nInterests: {interest_profile}\nFeed: {feed_filter}\n\n{response_format}",
	}

	got := RenderSystemPrompt(cfg)
	if strings.Contains(got, "content relevance filter") {
		t.Error("built-in wording leaked into a custom prompt")
	}
	for _, want := range []string{"Judge this.", "Interests: kernels", "Feed: eBPF only", `"verdict"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// The parser contract must survive a template that forgets it.
func TestResponseFormatAppendedWhenPlaceholderMissing(t *testing.T) {
	got := RenderSystemPrompt(FilterConfig{
		InterestProfile: "x",
		Prompt:          "Just decide: {interest_profile}",
	})
	if !strings.HasSuffix(got, ResponseFormat) {
		t.Errorf("response format not appended:\n%s", got)
	}
	if strings.Count(got, `"verdict": "ingest|skip|maybe"`) != 1 {
		t.Errorf("response format should appear exactly once:\n%s", got)
	}
}

func TestEmptyParagraphsDropped(t *testing.T) {
	got := RenderSystemPrompt(FilterConfig{
		InterestProfile: "x",
		Prompt: "Head\n\n## Interests\n{interest_profile}\n\n## Feed\n{feed_filter}\n\n" +
			"## Tags\n{top_tags}\n\n{response_format}",
	})
	if strings.Contains(got, "## Feed") {
		t.Errorf("heading survived its empty placeholder:\n%s", got)
	}
	if strings.Contains(got, "## Tags") {
		t.Errorf("heading survived its empty placeholder:\n%s", got)
	}
	if !strings.Contains(got, "## Interests\nx") {
		t.Errorf("filled section lost:\n%s", got)
	}
	if strings.Contains(got, "\n\n\n") {
		t.Errorf("dropped paragraphs left a gap:\n%q", got)
	}
}

// A paragraph mixing filled and empty placeholders is kept, not dropped.
func TestMixedParagraphKept(t *testing.T) {
	got := RenderSystemPrompt(FilterConfig{
		InterestProfile: "x",
		Prompt:          "Interests {interest_profile} and feed {feed_filter}.\n\n{response_format}",
	})
	if !strings.Contains(got, "Interests x and feed .") {
		t.Errorf("mixed paragraph mangled:\n%s", got)
	}
}

// Values are inserted, never re-scanned: a profile containing a placeholder
// name stays literal text.
func TestSubstitutionIsSinglePass(t *testing.T) {
	got := RenderSystemPrompt(FilterConfig{
		InterestProfile: "I write about {top_tags} syntax",
		Library:         &LibraryContext{TopTags: []string{"go", "rust"}},
	})
	if !strings.Contains(got, "I write about {top_tags} syntax") {
		t.Errorf("value was re-substituted:\n%s", got)
	}
	if !strings.Contains(got, "go, rust") {
		t.Errorf("real placeholder not filled:\n%s", got)
	}
}

// Unknown placeholders are left alone rather than silently blanked.
func TestUnknownPlaceholderPreserved(t *testing.T) {
	got := RenderSystemPrompt(FilterConfig{
		InterestProfile: "x",
		Prompt:          "{interest_profile} and {not_a_placeholder}\n\n{response_format}",
	})
	if !strings.Contains(got, "{not_a_placeholder}") {
		t.Errorf("unknown placeholder was eaten:\n%s", got)
	}
}

// The JSON example in the response format must not be treated as a placeholder.
func TestResponseFormatBracesSurvive(t *testing.T) {
	got := RenderSystemPrompt(FilterConfig{InterestProfile: "x"})
	if !strings.Contains(got, `{"verdict": "ingest|skip|maybe", "reason": "one sentence why", "collections": ["suggested-collection-slugs"]}`) {
		t.Errorf("JSON contract was mangled:\n%s", got)
	}
}

func TestBlankPromptFallsBackToDefault(t *testing.T) {
	got := RenderSystemPrompt(FilterConfig{InterestProfile: "x", Prompt: "   \n  "})
	if !strings.Contains(got, "content relevance filter") {
		t.Errorf("whitespace-only prompt should fall back to the default:\n%s", got)
	}
}

func TestSummaryMaxCharsHonoured(t *testing.T) {
	item := Item{Title: "T", Summary: strings.Repeat("a", 900)}

	got := buildFilterUserMessage(item, 100)
	if !strings.Contains(got, strings.Repeat("a", 100)+"...") {
		t.Error("summary not truncated at the configured limit")
	}
	if strings.Contains(got, strings.Repeat("a", 101)) {
		t.Error("summary exceeded the configured limit")
	}

	def := buildFilterUserMessage(item, 0)
	if !strings.Contains(def, strings.Repeat("a", DefaultSummaryMaxChars)+"...") {
		t.Error("zero should mean DefaultSummaryMaxChars")
	}
}

// The prompts must reach the log at debug level — that is the only way to see
// what an item was judged by once a prompt is user-editable.
func TestFilterLogsPromptsAtDebug(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	chat := func(_ context.Context, _, _ string) (string, error) {
		return `{"verdict": "skip", "reason": "not relevant"}`, nil
	}
	cfg := FilterConfig{InterestProfile: "transformer internals", FeedFilter: "only interpretability"}
	item := Item{Title: "Some Paper", Link: "https://example.com/p", Summary: "a summary"}

	if _, err := Filter(context.Background(), chat, cfg, item); err != nil {
		t.Fatalf("Filter: %v", err)
	}

	logged := buf.String()
	for _, want := range []string{
		"feed filter request",
		"feed filter response",
		"Some Paper",
		"https://example.com/p",
		"a summary",
		`"verdict`,
	} {
		if !strings.Contains(logged, want) {
			t.Errorf("missing %q in debug log:\n%s", want, logged)
		}
	}
	if !strings.Contains(logged, "fnv32a=") {
		t.Errorf("system prompt fingerprint missing:\n%s", logged)
	}
}

// At info level none of it appears — payload logging stays opt-in.
func TestFilterSilentAboveDebug(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	chat := func(_ context.Context, _, _ string) (string, error) {
		return `{"verdict": "ingest", "reason": "ok"}`, nil
	}
	if _, err := Filter(context.Background(), chat, FilterConfig{InterestProfile: "x"}, Item{Title: "T"}); err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("filter logged at info level:\n%s", buf.String())
	}
}

func TestPromptFingerprintDistinguishesPrompts(t *testing.T) {
	a := PromptFingerprint("one prompt")
	b := PromptFingerprint("another prompt")
	if a == b {
		t.Error("different prompts produced the same fingerprint")
	}
	if PromptFingerprint("one prompt") != a {
		t.Error("fingerprint is not stable")
	}
}

package feed

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log/slog"
	"strings"
)

// Verdict is the relevance decision for a feed item.
type Verdict string

const (
	VerdictIngest Verdict = "ingest"
	VerdictSkip   Verdict = "skip"
	VerdictMaybe  Verdict = "maybe"
)

// FilterResult is the LLM's relevance decision for a single item.
type FilterResult struct {
	Verdict     Verdict  `json:"verdict"`
	Reason      string   `json:"reason"`
	Collections []string `json:"collections,omitempty"`
}

// ChatFunc is a minimal LLM interface: system prompt + user message → response.
// This keeps the feed package decoupled from any specific LLM library.
type ChatFunc func(ctx context.Context, system, user string) (string, error)

// LibraryContext provides read-only knowledge-base awareness to the filter.
// All fields are optional; absent fields are silently omitted from the prompt.
type LibraryContext struct {
	RecentTitles []string // last N article titles, newest first
	TopTags      []string // most frequent tags in the library
	Collections  []string // "slug: description" format
}

// FilterConfig describes what the user cares about.
type FilterConfig struct {
	// InterestProfile is a free-text description of the user's interests (required).
	InterestProfile string

	// FeedFilter is an optional per-feed narrowing instruction.
	FeedFilter string

	// Prompt is the system prompt template. Empty means DefaultFilterPrompt.
	// See DefaultFilterPrompt for the placeholders it may contain.
	Prompt string

	// SummaryMaxChars caps the feed summary included in the per-item message.
	// Zero means DefaultSummaryMaxChars.
	SummaryMaxChars int

	// Collections is an optional list of existing collection names with descriptions.
	// Format: "slug: description"
	Collections []string

	// Library provides live knowledge-base context (recent reads, top tags).
	// When non-nil, the filter prompt becomes library-aware.
	Library *LibraryContext
}

// Filter evaluates a feed item against the user's interest profile using an LLM.
func Filter(ctx context.Context, chat ChatFunc, cfg FilterConfig, item Item) (FilterResult, error) {
	system := RenderSystemPrompt(cfg)
	user := buildFilterUserMessage(item, cfg.SummaryMaxChars)

	// The system prompt is identical for every item in a feed, so logging it
	// per item would bury the log in copies. Callers log it once per feed;
	// the fingerprint here ties each item back to the prompt it was judged by.
	slog.Debug("feed filter request",
		"title", item.Title,
		"url", item.Link,
		"system_prompt", PromptFingerprint(system),
		"user_prompt", user,
	)

	resp, err := chat(ctx, system, user)
	if err != nil {
		return FilterResult{}, fmt.Errorf("filter LLM call: %w", err)
	}
	slog.Debug("feed filter response", "title", item.Title, "response", resp)

	return parseFilterResponse(resp)
}

// PromptFingerprint identifies a rendered prompt compactly: length plus a hash,
// enough to tell two prompts apart in a log without reprinting either.
func PromptFingerprint(s string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return fmt.Sprintf("%d chars, fnv32a=%08x", len(s), h.Sum32())
}

// FilterItems evaluates multiple items and returns results in the same order.
func FilterItems(ctx context.Context, chat ChatFunc, cfg FilterConfig, items []Item) ([]FilterResult, error) {
	results := make([]FilterResult, len(items))
	for i, item := range items {
		r, err := Filter(ctx, chat, cfg, item)
		if err != nil {
			// On LLM failure, default to "maybe" so we don't silently drop articles.
			results[i] = FilterResult{Verdict: VerdictMaybe, Reason: fmt.Sprintf("filter error: %v", err)}
			continue
		}
		results[i] = r
	}
	return results, nil
}

// DefaultSummaryMaxChars caps the feed summary sent to the filter when
// FilterConfig.SummaryMaxChars is unset.
const DefaultSummaryMaxChars = 500

// Placeholder names understood by a filter prompt template. Values are
// computed from config and the library; the template decides where they go.
const (
	phInterestProfile = "{interest_profile}"
	phFeedFilter      = "{feed_filter}"
	phRecentTitles    = "{recent_titles}"
	phTopTags         = "{top_tags}"
	phCollections     = "{collections}"
	phResponseFormat  = "{response_format}"
)

// ResponseFormat is the machine contract between the prompt and
// parseFilterResponse. It is substituted into {response_format}, or appended
// when a template omits the placeholder, so an edited prompt cannot leave the
// response unparseable. Exported so the agent config template can quote it
// verbatim in its comments.
const ResponseFormat = `Evaluate the article below. Respond with JSON only, no markdown fences:
{"verdict": "ingest|skip|maybe", "reason": "one sentence why", "collections": ["suggested-collection-slugs"]}`

// DefaultFilterPrompt is the built-in filter prompt. Users override it via
// filter_prompt in the agent config; this text is the starting point.
//
// A paragraph whose placeholders all resolve to empty is dropped, so a feed
// with no filter of its own does not emit a bare heading.
const DefaultFilterPrompt = `You are a content relevance filter for a personal knowledge library.

## User interests
{interest_profile}

## Feed-specific instructions
{feed_filter}

## Recently ingested articles (newest first)
{recent_titles}

## Most frequent topics in the library
{top_tags}

## Existing collections
{collections}

{response_format}

Rules:
- "ingest" = clearly relevant to the user's interests
- "skip" = not relevant or too shallow/clickbait
- "maybe" = borderline, might be interesting
- Use the recently ingested list to avoid re-ingesting already-covered topics.
- Be selective. When in doubt, skip.
- Non-English articles: skip unless the topic is exceptionally relevant.`

// RenderSystemPrompt builds the filter system prompt for cfg. Exported so
// callers can log — or preview — the exact text an item is judged by.
func RenderSystemPrompt(cfg FilterConfig) string {
	tmpl := cfg.Prompt
	if strings.TrimSpace(tmpl) == "" {
		tmpl = DefaultFilterPrompt
	}

	// Library context wins over the standalone Collections field when present,
	// matching how callers populate the two.
	var recent, tags, collections []string
	if cfg.Library != nil {
		recent = cfg.Library.RecentTitles
		tags = cfg.Library.TopTags
		collections = cfg.Library.Collections
	}
	if len(collections) == 0 {
		collections = cfg.Collections
	}

	vals := map[string]string{
		phInterestProfile: strings.TrimSpace(cfg.InterestProfile),
		phFeedFilter:      strings.TrimSpace(cfg.FeedFilter),
		phRecentTitles:    bulletList(recent),
		phTopTags:         strings.Join(tags, ", "),
		phCollections:     bulletList(collections),
		phResponseFormat:  ResponseFormat,
	}

	out := renderPrompt(tmpl, vals)
	if !strings.Contains(tmpl, phResponseFormat) {
		out += "\n\n" + ResponseFormat
	}
	return out
}

// renderPrompt substitutes placeholder values into tmpl, dropping any
// paragraph whose placeholders all resolve to empty — that is what keeps a
// heading from outliving the content it introduces. Substitution is a single
// pass, so a value that happens to contain "{top_tags}" is left as text.
func renderPrompt(tmpl string, vals map[string]string) string {
	paragraphs := strings.Split(strings.ReplaceAll(tmpl, "\r\n", "\n"), "\n\n")

	kept := make([]string, 0, len(paragraphs))
	for _, p := range paragraphs {
		found, allEmpty := 0, true
		for name, v := range vals {
			if strings.Contains(p, name) {
				found++
				if v != "" {
					allEmpty = false
				}
			}
		}
		if found > 0 && allEmpty {
			continue
		}
		if rendered := strings.TrimRight(substitute(p, vals), " \t\n"); rendered != "" {
			kept = append(kept, rendered)
		}
	}
	return strings.Join(kept, "\n\n")
}

// substitute replaces known placeholders in one pass. Braces that do not open
// a known placeholder — the JSON example in responseFormat, say — pass through.
func substitute(s string, vals map[string]string) string {
	var sb strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '{' {
			sb.WriteByte(s[i])
			i++
			continue
		}
		end := strings.IndexByte(s[i:], '}')
		if end < 0 {
			sb.WriteString(s[i:])
			break
		}
		name := s[i : i+end+1]
		if v, ok := vals[name]; ok {
			sb.WriteString(v)
		} else {
			sb.WriteString(name)
		}
		i += end + 1
	}
	return sb.String()
}

func bulletList(items []string) string {
	if len(items) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, it := range items {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString("- ")
		sb.WriteString(it)
	}
	return sb.String()
}

func buildFilterUserMessage(item Item, summaryMaxChars int) string {
	if summaryMaxChars <= 0 {
		summaryMaxChars = DefaultSummaryMaxChars
	}
	var sb strings.Builder
	sb.WriteString("Title: ")
	sb.WriteString(item.Title)
	sb.WriteString("\n")

	if item.Author != "" {
		sb.WriteString("Author: ")
		sb.WriteString(item.Author)
		sb.WriteString("\n")
	}

	if len(item.Tags) > 0 {
		sb.WriteString("Tags: ")
		sb.WriteString(strings.Join(item.Tags, ", "))
		sb.WriteString("\n")
	}

	if item.Summary != "" {
		// Strip HTML tags from summary for cleaner input.
		summary := stripHTML(item.Summary)
		if len(summary) > summaryMaxChars {
			summary = summary[:summaryMaxChars] + "..."
		}
		sb.WriteString("\n")
		sb.WriteString(summary)
	}

	return sb.String()
}

func parseFilterResponse(resp string) (FilterResult, error) {
	resp = strings.TrimSpace(resp)

	// Extract the first JSON object, ignoring any text before or after it.
	// Haiku sometimes wraps the JSON in markdown fences or appends reasoning text.
	start := strings.Index(resp, "{")
	end := strings.LastIndex(resp, "}")
	if start == -1 || end == -1 || end < start {
		return FilterResult{}, fmt.Errorf("parse filter response: no JSON object found\nresponse: %s", resp)
	}
	resp = resp[start : end+1]

	var result FilterResult
	if err := json.Unmarshal([]byte(resp), &result); err != nil {
		return FilterResult{}, fmt.Errorf("parse filter response: %w\nresponse: %s", err, resp)
	}

	// Normalize verdict to lowercase.
	result.Verdict = Verdict(strings.ToLower(string(result.Verdict)))

	switch result.Verdict {
	case VerdictIngest, VerdictSkip, VerdictMaybe:
		// valid
	default:
		result.Verdict = VerdictMaybe
	}

	return result, nil
}

// stripHTML removes HTML tags from a string. Simple and sufficient for feed summaries.
func stripHTML(s string) string {
	var out strings.Builder
	inTag := false
	for _, r := range s {
		if r == '<' {
			inTag = true
			continue
		}
		if r == '>' {
			inTag = false
			continue
		}
		if !inTag {
			out.WriteRune(r)
		}
	}
	return strings.TrimSpace(out.String())
}

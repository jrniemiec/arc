package feed

import (
	"context"
	"encoding/json"
	"fmt"
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

	// Collections is an optional list of existing collection names with descriptions.
	// Format: "slug: description"
	Collections []string

	// Library provides live knowledge-base context (recent reads, top tags).
	// When non-nil, the filter prompt becomes library-aware.
	Library *LibraryContext
}

// Filter evaluates a feed item against the user's interest profile using an LLM.
func Filter(ctx context.Context, chat ChatFunc, cfg FilterConfig, item Item) (FilterResult, error) {
	system := buildFilterSystemPrompt(cfg)
	user := buildFilterUserMessage(item)

	resp, err := chat(ctx, system, user)
	if err != nil {
		return FilterResult{}, fmt.Errorf("filter LLM call: %w", err)
	}

	return parseFilterResponse(resp)
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

func buildFilterSystemPrompt(cfg FilterConfig) string {
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

func buildFilterUserMessage(item Item) string {
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
		if len(summary) > 500 {
			summary = summary[:500] + "..."
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

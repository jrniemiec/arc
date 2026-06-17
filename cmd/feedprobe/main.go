// feedprobe is a standalone tool for exploring RSS/Atom feeds.
// It wraps the ingest/feed package for experimentation.
//
// Usage:
//
//	feedprobe <url>                          # fetch and print all items
//	feedprobe --new --statedir <dir> <url>   # only show new items
//	feedprobe --mark --statedir <dir> <url>  # mark current items as seen
//	feedprobe --filter "interests..." <url>  # LLM relevance filter
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jrniemiec/arc/ingest/feed"
	"github.com/jrniemiec/llm"
)

func main() {
	var (
		statedir   = flag.String("statedir", "", "directory for state files (enables state tracking)")
		newOnly    = flag.Bool("new", false, "only show items not yet seen (requires --statedir)")
		mark       = flag.Bool("mark", false, "mark all current items as seen (requires --statedir)")
		verbose    = flag.Bool("v", false, "show item summaries")
		filter     = flag.String("filter", "", "interest profile for LLM relevance filtering")
		feedFilter = flag.String("feed-filter", "", "per-feed narrowing instructions (used with --filter)")
		model      = flag.String("model", "claude-haiku-4-5-20251001", "model for filtering")
		provider   = flag.String("provider", "anthropic", "LLM provider (anthropic, openai)")
	)
	flag.Parse()

	if flag.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "usage: feedprobe [flags] <feed-url>\n")
		os.Exit(1)
	}
	url := flag.Arg(0)

	timeout := 30 * time.Second
	if *filter != "" {
		timeout = 120 * time.Second // LLM calls need more time
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	f, err := feed.Parse(ctx, url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Feed: %s\n", f.Title)
	if f.Description != "" {
		fmt.Printf("Desc: %s\n", f.Description)
	}
	fmt.Printf("Link: %s\n", f.Link)
	fmt.Printf("Items: %d\n\n", len(f.Items))

	items := f.Items

	var store *feed.Store
	if *statedir != "" {
		store, err = feed.NewStore(*statedir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	}

	// --mark: persist all current GUIDs and exit
	if *mark {
		if store == nil {
			fmt.Fprintf(os.Stderr, "error: --mark requires --statedir\n")
			os.Exit(1)
		}
		if err := store.MarkSeen(url, items); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Marked %d items as seen.\n", len(items))
		return
	}

	// --new: filter to unseen items only
	if *newOnly {
		if store == nil {
			fmt.Fprintf(os.Stderr, "error: --new requires --statedir\n")
			os.Exit(1)
		}
		items, err = store.NewItems(url, items)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("New items: %d\n\n", len(items))
	}

	// --filter: run LLM relevance filter
	var filterResults []feed.FilterResult
	if *filter != "" {
		apiKey := resolveAPIKey(*provider)
		if apiKey == "" {
			fmt.Fprintf(os.Stderr, "error: no API key found for provider %q (set ANTHROPIC_API_KEY or OPENAI_API_KEY)\n", *provider)
			os.Exit(1)
		}

		p, err := llm.New(llm.ProviderConfig{
			Provider: *provider,
			Model:    *model,
			APIKey:   apiKey,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}

		chatFn := func(ctx context.Context, system, user string) (string, error) {
			out, _, err := p.Chat(ctx, system, []llm.Message{{Role: llm.RoleUser, Content: user}})
			return out, err
		}

		cfg := feed.FilterConfig{
			InterestProfile: *filter,
			FeedFilter:      *feedFilter,
		}

		fmt.Printf("Filtering with %s...\n\n", *model)
		filterResults, err = feed.FilterItems(ctx, chatFn, cfg, items)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	}

	for i, item := range items {
		date := ""
		if !item.Published.IsZero() {
			date = item.Published.Format("2006-01-02")
		}

		// Verdict prefix
		prefix := fmt.Sprintf("[%d]", i+1)
		if filterResults != nil {
			r := filterResults[i]
			switch r.Verdict {
			case feed.VerdictIngest:
				prefix = fmt.Sprintf("[%d] \033[32m✓ INGEST\033[0m", i+1)
			case feed.VerdictSkip:
				prefix = fmt.Sprintf("[%d] \033[31m✗ SKIP\033[0m  ", i+1)
			case feed.VerdictMaybe:
				prefix = fmt.Sprintf("[%d] \033[33m? MAYBE\033[0m ", i+1)
			}
		}

		fmt.Printf("%s %s\n", prefix, item.Title)
		if date != "" {
			fmt.Printf("    Date:   %s\n", date)
		}
		if item.Author != "" {
			fmt.Printf("    Author: %s\n", item.Author)
		}
		fmt.Printf("    Link:   %s\n", item.Link)
		if len(item.Tags) > 0 {
			fmt.Printf("    Tags:   %s\n", strings.Join(item.Tags, ", "))
		}
		if filterResults != nil {
			r := filterResults[i]
			fmt.Printf("    Reason: %s\n", r.Reason)
			if len(r.Collections) > 0 {
				fmt.Printf("    Collections: %s\n", strings.Join(r.Collections, ", "))
			}
		}
		if *verbose && item.Summary != "" {
			summary := item.Summary
			if len(summary) > 300 {
				summary = summary[:300] + "..."
			}
			fmt.Printf("    Summary: %s\n", summary)
		}
		fmt.Println()
	}

	// Print summary when filtering
	if filterResults != nil {
		var ingest, skip, maybe int
		for _, r := range filterResults {
			switch r.Verdict {
			case feed.VerdictIngest:
				ingest++
			case feed.VerdictSkip:
				skip++
			case feed.VerdictMaybe:
				maybe++
			}
		}
		fmt.Printf("--- Filter summary: %d ingest, %d skip, %d maybe ---\n", ingest, skip, maybe)
	}
}

func resolveAPIKey(provider string) string {
	switch strings.ToLower(provider) {
	case "anthropic":
		for _, k := range []string{"ARC_ANTHROPIC_API_KEY", "ANTHROPIC_API_KEY"} {
			if v := strings.TrimSpace(os.Getenv(k)); v != "" {
				return v
			}
		}
	case "openai":
		for _, k := range []string{"ARC_OPENAI_API_KEY", "OPENAI_API_KEY"} {
			if v := strings.TrimSpace(os.Getenv(k)); v != "" {
				return v
			}
		}
	}
	return ""
}

// Package agent implements the autonomous feed ingestion agent.
// It polls configured RSS/Atom feeds, filters items using an LLM
// against the user's interest profile and library context, and
// ingests relevant articles through the standard pipeline.
package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	llmlib "github.com/jrniemiec/llm"

	"github.com/jrniemiec/arc/config"
	"github.com/jrniemiec/arc/ingest/feed"
	"github.com/jrniemiec/arc/ingest/pipeline"
	"github.com/jrniemiec/arc/store/sqlite"
)

// FilterConcurrency is the max number of simultaneous LLM filter calls per feed.
const FilterConcurrency = 5

// IngestConcurrency is the max number of simultaneous pipeline.Run calls per feed.
// Each ingest does HTTP fetch + multi-chunk LLM summarization, so 3 is enough to
// keep the API saturated without hammering rate limits.
const IngestConcurrency = 3

// RunOptions controls a single agent execution.
type RunOptions struct {
	// ArcConfig is the main arc configuration (required).
	ArcConfig config.Config

	// AgentConfig is the agent-specific configuration (required).
	AgentCfg AgentConfig

	// DB is the arc SQLite store (required).
	DB *sqlite.Store

	// FeedStateDir is the directory where per-feed seen-GUIDs state is stored.
	// Typically cfg.AgentPath + "/state".
	FeedStateDir string

	// RunsPath is the path to the agent/runs.jsonl file.
	RunsPath string

	// DryRun: filter and log decisions but do not ingest.
	DryRun bool

	// Focus overrides AgentConfig.Focus for this run only.
	Focus string

	// DecisionsDir, if non-empty, is the directory where the decisions file is
	// written after a normal run. Filename: decisions-<run-id>.json.
	// Ignored during dry-run.
	DecisionsDir string

	// Status, if non-nil, is called to update a status display.
	// slot 0 is the main spinner line (feed fetch, filter progress).
	// slots 1..IngestConcurrency are per-ingest sub-lines (one per parallel ingest).
	// Setting a slot to "" clears it. Safe to leave nil.
	Status func(slot int, msg string)
}

// RunFeeds executes one full agent cycle:
//  1. Build library context from the arc database.
//  2. For each configured feed: fetch, filter new items via LLM, ingest approved ones.
//  3. Append a RunRecord to runs.jsonl.
//
// Returns a summary RunRecord. Errors from individual feeds are non-fatal and
// recorded in the per-feed FeedRecord.Error field.
func RunFeeds(ctx context.Context, opts RunOptions) (RunRecord, error) {
	runID := NewRunID()
	startedAt := time.Now().UTC()

	slog.Info("agent run started", "run_id", runID, "feeds", len(opts.AgentCfg.Feeds))

	// Build library context (recent titles + top tags).
	libCtx, err := BuildLibraryContext(ctx, opts.DB)
	if err != nil {
		slog.Warn("could not build library context", "err", err)
		libCtx = &feed.LibraryContext{} // degrade gracefully
	}

	// Resolve LLM filter function.
	filterChat, err := resolveFilterChat(opts)
	if err != nil {
		return RunRecord{}, fmt.Errorf("resolve filter LLM: %w", err)
	}

	// Build filter config — shared across all feeds, per-feed filter appended inline.
	baseCfg := feed.FilterConfig{
		InterestProfile: buildInterestProfile(opts),
		Library:         libCtx,
	}

	// Open feed state store.
	stateStore, err := feed.NewStore(opts.FeedStateDir)
	if err != nil {
		return RunRecord{}, fmt.Errorf("open feed state store: %w", err)
	}

	rec := RunRecord{
		RunID:     runID,
		RunType:   "daily",
		StartedAt: startedAt,
	}

	// Count active feeds for the progress prefix.
	totalFeeds := 0
	for _, f := range opts.AgentCfg.Feeds {
		if !f.Disabled {
			totalFeeds++
		}
	}
	feedIdx := 0

	for _, feedCfg := range opts.AgentCfg.Feeds {
		if feedCfg.Disabled {
			slog.Debug("feed disabled, skipping", "url", feedCfg.URL)
			continue
		}

		// Stop processing new feeds if the context was cancelled (SIGINT/SIGTERM).
		if ctx.Err() != nil {
			slog.Info("agent run interrupted, saving partial results",
				"completed_feeds", feedIdx, "total_feeds", totalFeeds)
			rec.Error = "interrupted: " + ctx.Err().Error()
			break
		}

		feedIdx++
		name := feedCfg.Name
		if name == "" {
			name = feedCfg.URL
		}
		prefix := fmt.Sprintf("[%d/%d]", feedIdx, totalFeeds)
		status(opts, 0, fmt.Sprintf("%s %s fetching...", prefix, name))
		fr := runFeed(ctx, opts, feedCfg, baseCfg, filterChat, stateStore, runID, prefix)
		rec.Feeds = append(rec.Feeds, fr)
		rec.TotalNew += fr.New
		rec.TotalFilter += fr.Filter
		rec.TotalIngest += fr.Ingest
		rec.TotalMaybe += fr.Maybe
		rec.TotalSkip += fr.Skip
		rec.TotalCostUSD += fr.CostUSD
		rec.IngestedSlugs = append(rec.IngestedSlugs, fr.Slugs...)
	}

	rec.FinishedAt = time.Now().UTC()
	slog.Info("agent run finished",
		"run_id", runID,
		"new", rec.TotalNew,
		"ingest", rec.TotalIngest,
		"maybe", rec.TotalMaybe,
		"skip", rec.TotalSkip,
	)

	// Persist run record.
	if opts.RunsPath != "" {
		if err := AppendRun(opts.RunsPath, rec); err != nil {
			slog.Warn("could not append run record", "err", err)
		}
	}

	// Write decisions file. Also written in dry-run — the file is the intended
	// output of a dry-run, with approved items pre-marked action:"+" and
	// skips as action:"-" for user review.
	if opts.DecisionsDir != "" {
		df := buildDecisionsFile(runID, startedAt, rec)
		decisionsPath := filepath.Join(opts.DecisionsDir, "decisions-"+runID+".json")
		if err := WriteDecisionsFile(decisionsPath, df); err != nil {
			slog.Warn("could not write decisions file", "err", err)
		} else {
			slog.Info("decisions file written", "path", decisionsPath)
		}
	}

	return rec, nil
}

// runFeed handles one feed within a run cycle.
func runFeed(
	ctx context.Context,
	opts RunOptions,
	feedCfg FeedConfig,
	baseCfg feed.FilterConfig,
	filterChat feed.ChatFunc,
	stateStore *feed.Store,
	runID string,
	prefix string,
) FeedRecord {
	fr := FeedRecord{URL: feedCfg.URL, Name: feedCfg.Name}

	// Fetch feed.
	f, err := feed.Parse(ctx, feedCfg.URL)
	if err != nil {
		fr.Error = fmt.Sprintf("fetch: %v", err)
		slog.Warn("feed fetch failed", "url", feedCfg.URL, "err", err)
		return fr
	}
	if fr.Name == "" {
		fr.Name = f.Title
	}

	// Pre-filter by feed-native tags if configured.
	items := f.Items
	if len(feedCfg.Tags) > 0 {
		items = tagFilter(items, feedCfg.Tags)
	}

	// Find new items (not seen in prior runs).
	newItems, err := stateStore.NewItems(feedCfg.URL, items)
	if err != nil {
		fr.Error = fmt.Sprintf("state: %v", err)
		slog.Warn("feed state check failed", "url", feedCfg.URL, "err", err)
		return fr
	}
	fr.New = len(newItems)

	if len(newItems) == 0 {
		slog.Debug("no new items", "feed", fr.Name)
		return fr
	}

	// URL-level dedup: skip items already in the arc store.
	dedupedItems, err := dedupByURL(ctx, opts.DB, newItems)
	if err != nil {
		slog.Warn("url dedup failed, proceeding without dedup", "err", err)
		dedupedItems = newItems
	}

	fr.Filter = len(dedupedItems)
	if fr.Filter == 0 {
		slog.Debug("all new items already ingested", "feed", fr.Name)
		// Still mark as seen so we don't re-check them next run.
		_ = stateStore.MarkSeen(feedCfg.URL, newItems)
		return fr
	}

	// Assemble per-feed filter config.
	filterCfg := baseCfg
	filterCfg.FeedFilter = feedCfg.Filter

	// ── Phase 1: parallel LLM filter ─────────────────────────────────────────
	// Run up to FilterConcurrency filter calls simultaneously.
	// Results are written into a pre-allocated slice to preserve order.
	total := len(dedupedItems)
	results := make([]feed.FilterResult, total)
	var (
		sem  = make(chan struct{}, FilterConcurrency)
		wg   sync.WaitGroup
		done atomic.Int32 // completed filter calls
	)
	for i, item := range dedupedItems {
		wg.Add(1)
		go func(idx int, it feed.Item) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			r, err := feed.Filter(ctx, filterChat, filterCfg, it)
			if err != nil {
				r = feed.FilterResult{Verdict: feed.VerdictMaybe, Reason: fmt.Sprintf("filter error: %v", err)}
			}
			results[idx] = r

			// Update status after completion so each title is visible as it lands.
			title := it.Title
			if len(title) > 60 {
				title = title[:57] + "..."
			}
			n := int(done.Add(1))
			status(opts, 0, fmt.Sprintf("%s %s %d/%d: %q", prefix, fr.Name, n, total, title))
		}(i, item)
	}
	wg.Wait()

	// ── Phase 2: parallel ingest ─────────────────────────────────────────────
	// Each pipeline.Run writes to its own slug directory (no filesystem races).
	// appendEvent uses O_APPEND (POSIX-atomic for small writes).
	// Counters (fr.Ingest/Maybe/Skip) are set on the main goroutine before any
	// goroutine is launched, so no mutex is needed.
	//
	// Each ingest goroutine owns a spinner sub-slot (1..IngestConcurrency) so
	// all parallel ingests are visible simultaneously.

	// Count items to ingest for the [n/toIngest] display index.
	toIngest := 0
	for _, r := range results {
		if r.Verdict == feed.VerdictIngest || r.Verdict == feed.VerdictMaybe {
			toIngest++
		}
	}

	decisions := make([]ItemDecision, total)
	costs := make([]float64, total)  // per-item cost, written by each goroutine at its own index
	slugs := make([]string, total)   // per-item slug, written by each goroutine at its own index
	var (
		ingestSem = make(chan struct{}, IngestConcurrency)
		ingestWg  sync.WaitGroup
	)
	ingestNum := 0 // sequential counter for display index (main goroutine only)

	for i, item := range dedupedItems {
		result := results[i]
		decision := ItemDecision{
			Verdict: string(result.Verdict),
			Title:   item.Title,
			URL:     item.Link,
			Reason:  result.Reason,
		}

		switch result.Verdict {
		case feed.VerdictSkip:
			fr.Skip++
			decision.Status = "pending"
			decision.Action = "-"
			slog.Debug("skip", "title", item.Title, "reason", result.Reason)
			decisions[i] = decision

		case feed.VerdictIngest, feed.VerdictMaybe:
			if result.Verdict == feed.VerdictIngest {
				fr.Ingest++
			} else {
				fr.Maybe++
			}

			if opts.DryRun {
				// In dry-run: mark as pending with "+" so the decisions file
				// is ready to use directly — approved items pre-marked for ingest.
				decision.Status = "pending"
				decision.Action = "+"
				slog.Info("dry-run: would ingest",
					"title", item.Title,
					"verdict", result.Verdict,
					"reason", result.Reason,
				)
				decisions[i] = decision
				continue
			}

			decision.Status = "done"
			decisions[i] = decision

			ingestNum++
			// Assign a spinner sub-slot so parallel ingests are each visible.
			slot := (ingestNum-1)%IngestConcurrency + 1

			ingestWg.Add(1)
			go func(idx int, it feed.Item, r feed.FilterResult, displayIdx int, spinSlot int) {
				defer ingestWg.Done()
				defer status(opts, spinSlot, "") // clear slot when done

				ingestSem <- struct{}{}
				defer func() { <-ingestSem }()

				title := it.Title
				if len(title) > 55 {
					title = title[:52] + "..."
				}
				status(opts, spinSlot, fmt.Sprintf("[%d/%d] %s: %q", displayIdx, toIngest, fr.Name, title))

				pReq := pipeline.Request{
					URL:              it.Link,
					AgentRunID:       runID,
					AgentVerdict:     string(r.Verdict),
					AgentReason:      r.Reason,
					SummaryModel:     opts.AgentCfg.SummaryProfileName(),
					AllowedLanguages: opts.AgentCfg.Languages,
				}
				if len(r.Collections) > 0 {
					pReq.Collection = r.Collections[0]
				}

				pResult, pErr := pipeline.Run(ctx, opts.ArcConfig, pReq)
				if pErr != nil {
					slog.Warn("ingest failed",
						"title", it.Title,
						"url", it.Link,
						"err", pErr,
					)
				} else if pResult.Skipped {
					slog.Info("ingest skipped",
						"title", it.Title,
						"reason", pResult.SkipReason,
					)
				} else {
					costs[idx] = pResult.Cost.TotalUSD
					slugs[idx] = pResult.Slug
					slog.Info("ingested",
						"slug", pResult.Slug,
						"title", it.Title,
						"verdict", r.Verdict,
						"reason", r.Reason,
						"cost_usd", pResult.Cost.TotalUSD,
					)
				}
			}(i, item, result, ingestNum, slot)
		}
	}

	ingestWg.Wait()
	fr.Items = decisions
	for i, c := range costs {
		fr.CostUSD += c
		if slugs[i] != "" {
			fr.Slugs = append(fr.Slugs, slugs[i])
		}
	}

	// Mark all new items as seen (even skipped ones, to avoid re-filtering next run).
	// Skipped in dry-run — state must not change when we're only previewing.
	if !opts.DryRun {
		if err := stateStore.MarkSeen(feedCfg.URL, newItems); err != nil {
			slog.Warn("mark seen failed", "feed", fr.Name, "err", err)
		}
	}

	return fr
}

// tagFilter returns items whose Tags intersect with the allowed set.
// Items with no tags are kept (conservative — let the LLM decide).
func tagFilter(items []feed.Item, allowed []string) []feed.Item {
	set := make(map[string]bool, len(allowed))
	for _, t := range allowed {
		set[t] = true
	}
	var out []feed.Item
	for _, item := range items {
		if len(item.Tags) == 0 {
			out = append(out, item)
			continue
		}
		for _, t := range item.Tags {
			if set[t] {
				out = append(out, item)
				break
			}
		}
	}
	return out
}

// dedupByURL removes items whose URL is already present in the arc store.
func dedupByURL(ctx context.Context, db *sqlite.Store, items []feed.Item) ([]feed.Item, error) {
	var out []feed.Item
	for _, item := range items {
		if item.Link == "" {
			out = append(out, item)
			continue
		}
		exists, err := db.ExistsByURL(ctx, item.Link)
		if err != nil {
			return nil, err
		}
		if exists {
			slog.Info("skipping already ingested URL", "url", item.Link, "title", item.Title)
		} else {
			out = append(out, item)
		}
	}
	return out, nil
}

// buildInterestProfile composes the interest profile string from agent config and run options.
func buildInterestProfile(opts RunOptions) string {
	profile := opts.AgentCfg.InterestProfile

	focus := opts.Focus
	if focus == "" {
		focus = opts.AgentCfg.Focus
	}
	if focus != "" {
		profile += "\n\nCurrent focus: " + focus
	}

	for _, note := range opts.AgentCfg.Notes {
		profile += "\nNote: " + note
	}

	for _, goal := range opts.AgentCfg.LearningGoals {
		profile += fmt.Sprintf("\nLearning goal (%s): %s", goal.Depth, goal.Topic)
	}

	return profile
}

// resolveFilterChat returns a ChatFunc backed by the configured filter LLM profile.
func resolveFilterChat(opts RunOptions) (feed.ChatFunc, error) {
	profileName := opts.AgentCfg.FilterProfileName()
	p, ok := opts.ArcConfig.Profile(profileName)
	if !ok {
		return nil, fmt.Errorf("filter profile %q not found in arc config", profileName)
	}

	apiKey := resolveAPIKey(p.Provider)
	provider, err := llmlib.New(llmlib.ProviderConfig{
		Provider: p.Provider,
		Model:    p.Model,
		Host:     p.Host,
		APIKey:   apiKey,
	})
	if err != nil {
		return nil, fmt.Errorf("init filter LLM provider: %w", err)
	}

	return func(ctx context.Context, system, user string) (string, error) {
		resp, _, err := provider.Chat(ctx, system, []llmlib.Message{
			{Role: llmlib.RoleUser, Content: user},
		})
		return resp, err
	}, nil
}

// buildDecisionsFile assembles a DecisionsFile from a completed RunRecord.
// Only feeds with items are included; feeds with no items are omitted.
func buildDecisionsFile(runID string, createdAt time.Time, rec RunRecord) DecisionsFile {
	df := DecisionsFile{RunID: runID, CreatedAt: createdAt}
	for _, fr := range rec.Feeds {
		if len(fr.Items) == 0 {
			continue
		}
		dfr := DecisionsFeedRecord{Name: fr.Name}
		dfr.Items = append(dfr.Items, fr.Items...)
		df.Feeds = append(df.Feeds, dfr)
	}
	return df
}

// RunDecisions reads a decisions file, ingests all items where Action=="+",
// rewrites the file keeping only still-pending items, and returns a RunRecord.
func RunDecisions(ctx context.Context, opts RunOptions, decisionsPath string) (RunRecord, error) {
	df, err := LoadDecisionsFile(decisionsPath)
	if err != nil {
		return RunRecord{}, err
	}

	runID := NewRunID()
	startedAt := time.Now().UTC()
	rec := RunRecord{
		RunID:       runID,
		RunType:     "decisions",
		SourceRunID: df.RunID,
		StartedAt:   startedAt,
	}

	slog.Info("decisions run started", "run_id", runID, "source", decisionsPath, "source_run_id", df.RunID)

	for _, dfr := range df.Feeds {
		fr := FeedRecord{Name: dfr.Name}
		var remainingItems []ItemDecision
		var allItems []ItemDecision

		for _, item := range dfr.Items {
			if item.Action != "+" || item.Status == "done" {
				remainingItems = append(remainingItems, item)
				allItems = append(allItems, item)
				continue
			}

			rec.TotalFilter++
			title := item.Title
			if len(title) > 60 {
				title = title[:57] + "..."
			}
			status(opts, 0, fmt.Sprintf("[decisions] %s: %q", dfr.Name, title))

			if opts.DryRun {
				slog.Info("dry-run: would ingest (decisions override)", "title", item.Title)
				fr.Ingest++
				remainingItems = append(remainingItems, item) // leave pending in dry-run
				allItems = append(allItems, item)
				continue
			}

			pResult, pErr := pipeline.Run(ctx, opts.ArcConfig, pipeline.Request{
				URL:          item.URL,
				AgentRunID:   runID,
				AgentVerdict: "ingest",
				AgentReason:  item.Reason,
				SummaryModel: opts.AgentCfg.SummaryProfileName(),
			})
			if pErr != nil {
				slog.Warn("decisions ingest failed", "url", item.URL, "err", pErr)
				remainingItems = append(remainingItems, item) // leave pending on failure
				allItems = append(allItems, item)
				continue
			}

			slog.Info("decisions ingest done", "slug", pResult.Slug, "title", item.Title)
			fr.Ingest++
			fr.CostUSD += pResult.Cost.TotalUSD
			rec.TotalIngest++
			rec.TotalCostUSD += pResult.Cost.TotalUSD
			rec.IngestedSlugs = append(rec.IngestedSlugs, pResult.Slug)
			item.Verdict = "ingest"
			item.Status = "done"
			allItems = append(allItems, item) // for verbose report
			// NOT added to remainingItems — it's done
		}

		fr.Items = allItems // full list for verbose report
		rec.Feeds = append(rec.Feeds, fr)

		// Rebuild decisions feed with only pending items.
		dfr.Items = remainingItems
	}

	rec.FinishedAt = time.Now().UTC()
	slog.Info("decisions run finished", "run_id", runID, "ingested", rec.TotalIngest)

	if !opts.DryRun {
		// Rewrite decisions file — keep only pending items.
		trimmed := DecisionsFile{RunID: df.RunID, CreatedAt: df.CreatedAt}
		for i, dfr := range df.Feeds {
			items := rec.Feeds[i].Items
			if len(items) > 0 {
				trimmed.Feeds = append(trimmed.Feeds, DecisionsFeedRecord{Name: dfr.Name, Items: items})
			}
		}
		if err := WriteDecisionsFile(decisionsPath, trimmed); err != nil {
			slog.Warn("could not rewrite decisions file", "err", err)
		}
		if opts.RunsPath != "" {
			if err := AppendRun(opts.RunsPath, rec); err != nil {
				slog.Warn("could not append run record", "err", err)
			}
		}
	}

	return rec, nil
}

// status calls opts.Status if non-nil. slot 0 = main line; 1..n = ingest sub-lines.
func status(opts RunOptions, slot int, msg string) {
	if opts.Status != nil {
		opts.Status(slot, msg)
	}
}

// resolveAPIKey looks up the API key for a given provider from environment variables.
// Checks ARC_<PROVIDER>_API_KEY first, then <PROVIDER>_API_KEY.
func resolveAPIKey(provider string) string {
	upper := strings.ToUpper(provider)
	if key := os.Getenv("ARC_" + upper + "_API_KEY"); key != "" {
		return key
	}
	return os.Getenv(upper + "_API_KEY")
}

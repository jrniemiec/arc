// Package pipeline orchestrates the full arc ingest flow:
// extract → title → slug → summarize → flash → flashcards → write → index
package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"log/slog"

	briefchunk "github.com/jrniemiec/brief/chunk"
	briefllm "github.com/jrniemiec/brief/llm"
	briefsummarize "github.com/jrniemiec/brief/summarize"
	brieftypes "github.com/jrniemiec/brief/types"
	"github.com/jrniemiec/arc/config"
	"github.com/jrniemiec/arc/ingest/embed"
	"github.com/jrniemiec/arc/ingest/extractor"
	"github.com/jrniemiec/arc/store"
	"github.com/jrniemiec/arc/store/fs"
	"github.com/jrniemiec/arc/store/vector"
	"github.com/jrniemiec/llm"
)

// SummarizeRequest describes a standalone summarize operation.
type SummarizeRequest struct {
	Text     string // plain text to summarize
	Title    string // optional, used for document metadata
	Source   string // optional URL or file path
	Style    string // overrides config ingest.summary_style
	Profile  string // profile name, overrides config ingest.summary_profile
	Progress func(string)
}

// SummarizeResult holds the output of a standalone summarize operation.
type SummarizeResult struct {
	Text  string
	Model string
	Style string
	Usage llm.Usage
}

// Summarize runs just the summarize step against plain text.
// It does not write any files.
func Summarize(ctx context.Context, cfg config.Config, req SummarizeRequest) (SummarizeResult, error) {
	progress := req.Progress
	if progress == nil {
		progress = func(string) {}
	}

	style := first(req.Style, cfg.Ingest.SummaryStyle)
	profileName := first(req.Profile, cfg.Ingest.SummaryProfile)

	prof, err := lookupProfile(cfg, profileName)
	if err != nil {
		return SummarizeResult{}, fmt.Errorf("profile: %w", err)
	}

	p, err := llm.New(llm.ProviderConfig{
		Provider: prof.Provider,
		Model:    prof.Model,
		Host:     prof.Host,
		APIKey:   resolveAPIKey(prof.Provider),
	})
	if err != nil {
		return SummarizeResult{}, fmt.Errorf("llm provider: %w", err)
	}

	text, usage, err := summarizeText(ctx, p, req.Text, req.Title, req.Source, style, prof.Model,
		cfg.Ingest.ChunkTokens, cfg.Ingest.SummaryMaxTokens, cfg.StylePrompt(style), progress)
	if err != nil {
		return SummarizeResult{}, err
	}

	return SummarizeResult{Text: text, Model: prof.Model, Style: style, Usage: usage}, nil
}

// FlashRequest describes a standalone flash generation operation.
type FlashRequest struct {
	Text     string // text to flash-summarize (summary or body)
	Profile  string // profile name override
	Progress func(string)
}

// FlashResult holds the output of a standalone flash operation.
type FlashResult struct {
	Text  string
	Model string
	Usage llm.Usage
}

// Flash runs just the flash generation step against plain text.
// It does not write any files.
func Flash(ctx context.Context, cfg config.Config, req FlashRequest) (FlashResult, error) {
	progress := req.Progress
	if progress == nil {
		progress = func(string) {}
	}

	profileName := first(req.Profile, cfg.Ingest.FlashProfile)
	prof, err := lookupProfile(cfg, profileName)
	if err != nil {
		return FlashResult{}, fmt.Errorf("profile: %w", err)
	}

	p, err := llm.New(llm.ProviderConfig{
		Provider: prof.Provider,
		Model:    prof.Model,
		Host:     prof.Host,
		APIKey:   resolveAPIKey(prof.Provider),
	})
	if err != nil {
		return FlashResult{}, fmt.Errorf("llm provider: %w", err)
	}

	progress(fmt.Sprintf("generating flash (model: %s)...", prof.Model))
	text, usage, err := generateFlash(ctx, p, req.Text, cfg.Ingest.FlashSystemPrompt, cfg.Ingest.FlashMaxTokens)
	if err != nil {
		return FlashResult{}, err
	}

	return FlashResult{Text: text, Model: prof.Model, Usage: usage}, nil
}

// FlashcardsRequest describes a standalone flashcard generation operation.
type FlashcardsRequest struct {
	Text     string // text to generate flashcards from
	Style    string // "socratic" | "cloze"
	Profile  string // profile name override
	Progress func(string)
}

// FlashcardsResult holds the output of a standalone flashcard operation.
type FlashcardsResult struct {
	JSON  []byte
	Model string
	Style string
	Usage llm.Usage
}

// Flashcards runs just the flashcard generation step against plain text.
// It does not write any files.
func Flashcards(ctx context.Context, cfg config.Config, req FlashcardsRequest) (FlashcardsResult, error) {
	progress := req.Progress
	if progress == nil {
		progress = func(string) {}
	}

	style := first(req.Style, cfg.Ingest.FlashcardStyle)
	profileName := first(req.Profile, cfg.Ingest.FlashcardProfile)

	prof, err := lookupProfile(cfg, profileName)
	if err != nil {
		return FlashcardsResult{}, fmt.Errorf("profile: %w", err)
	}

	p, err := llm.New(llm.ProviderConfig{
		Provider: prof.Provider,
		Model:    prof.Model,
		Host:     prof.Host,
		APIKey:   resolveAPIKey(prof.Provider),
	})
	if err != nil {
		return FlashcardsResult{}, fmt.Errorf("llm provider: %w", err)
	}

	progress(fmt.Sprintf("generating flashcards (style: %s, model: %s)...", style, prof.Model))
	data, usage, err := generateFlashcards(ctx, p, req.Text, style, cfg.FlashcardStylePrompt(style))
	if err != nil {
		return FlashcardsResult{}, err
	}

	return FlashcardsResult{JSON: data, Model: prof.Model, Style: style, Usage: usage}, nil
}

// Request describes one ingest operation.
type Request struct {
	// Exactly one of URL or File must be set. File="-" reads stdin.
	URL  string
	File string

	// Optional metadata overrides
	Title      string // if empty, LLM generates it
	Collection string

	// Generation model/style overrides (empty = use config defaults)
	SummaryModel   string
	SummaryStyle   string
	FlashModel     string
	FlashcardModel string
	FlashcardStyle string

	// AllowedLanguages, if non-empty, skips the article if its detected language
	// is not in the list. Uses ISO 639-1 codes (e.g. "en", "de").
	AllowedLanguages []string

	// Flags
	Flashcards   bool // force-enable flashcard generation regardless of config default
	NoFlashcards bool // force-disable flashcard generation regardless of config default
	NoEmbed      bool
	DryRun       bool

	// Agent provenance — set only when ingested by the feed agent.
	AgentRunID   string // run ID, e.g. "agent-20260609-120000"
	AgentVerdict string // "ingest" | "maybe"
	AgentReason  string // LLM's one-sentence justification

	// VectorStore, if non-nil, receives the article embedding after generation.
	// If nil or NoEmbed is true, the embed step is skipped.
	VectorStore *vector.Store

	// Progress is called with a human-readable status message at each pipeline step.
	// May be nil.
	Progress func(msg string)

	// OnCostEstimate is called once after extraction and before any LLM calls,
	// with the number of text chunks and the estimated total USD cost.
	// Only fires for non-teaser articles with at least one chunk. May be nil.
	OnCostEstimate func(nChunks int, estimatedUSD float64)
}

// Result is returned after a successful ingest.
type Result struct {
	Slug            string
	Cost            store.CostRecord
	ExtractionStats string // human-readable extraction stats line
	Teaser          bool   // true if article was below min_words threshold
	Skipped         bool   // true if article was skipped (e.g. language filter)
	SkipReason      string // human-readable reason for skip
}

// Run executes the full ingest pipeline and returns the new article slug.
// If req.DryRun is true it returns without writing anything.
func Run(ctx context.Context, cfg config.Config, req Request) (Result, error) {
	source := req.URL
	if source == "" {
		source = req.File
	}
	slog.Info("ingest start", "source", source)

	// progress logs every step to the log file AND forwards to the caller's callback.
	// This ensures background runs (batch, agents) are fully reconstructable from the log.
	rawProgress := req.Progress
	progress := func(msg string) {
		slog.Info(msg, "source", source)
		if rawProgress != nil {
			rawProgress(msg)
		}
	}

	// ── 1. Extract ────────────────────────────────────────────────────────
	var extracted extractor.Result
	var sourceType string

	switch {
	case req.URL != "":
		progress("fetching " + req.URL)
		var err error
		extracted, err = extractor.FromURLWithCookies(ctx, req.URL, cfg.CookieJars)
		if err != nil {
			slog.Error("extraction failed", "source", source, "type", "url", "err", err)
			return Result{}, fmt.Errorf("extract url: %w", err)
		}
		sourceType = "url"

	default:
		progress("reading " + req.File)
		var err error
		extracted, err = extractor.FromFile(ctx, req.File)
		if err != nil {
			slog.Error("extraction failed", "source", source, "type", "file", "err", err)
			return Result{}, fmt.Errorf("extract file: %w", err)
		}
		sourceType = "file"
	}

	if strings.TrimSpace(extracted.Text) == "" {
		slog.Error("extraction produced no text", "source", source)
		return Result{}, fmt.Errorf("extraction produced no text")
	}

	stats := extracted.Stats()
	progress(stats)

	// ── Language filter ───────────────────────────────────────────────────
	if len(req.AllowedLanguages) > 0 && extracted.Language != "" {
		allowed := false
		for _, lang := range req.AllowedLanguages {
			if strings.EqualFold(extracted.Language, lang) {
				allowed = true
				break
			}
		}
		if !allowed {
			reason := fmt.Sprintf("language %q not in allowed list %v", extracted.Language, req.AllowedLanguages)
			slog.Info("skipping article: language filter", "source", source, "language", extracted.Language)
			progress("skipped: " + reason)
			return Result{ExtractionStats: stats, Skipped: true, SkipReason: reason}, nil
		}
	}

	// ── Teaser detection ──────────────────────────────────────────────────
	minWords := cfg.Ingest.MinWords
	if minWords <= 0 {
		minWords = 300
	}
	wordCount := len(strings.Fields(extracted.Text))
	isTeaser := wordCount < minWords
	if isTeaser && req.URL != "" {
		// Step 1: resolve canonical URL from the fetched HTML — the original URL may be
		// an alias (e.g. medium.com/publication/slug) that serves a teaser while the
		// full article lives at a different domain (e.g. levelup.gitconnected.com/slug).
		canonicalURL := extractor.CanonicalURL(extracted, req.URL)
		if canonicalURL != "" {
			slog.Info("teaser: canonical URL differs, retrying with canonical",
				"source", source, "canonical", canonicalURL, "words", wordCount)
			progress(fmt.Sprintf("teaser (%d words) — retrying with canonical URL %s...", wordCount, canonicalURL))
			canonicalResult, err := extractor.FromURLWithCookies(ctx, canonicalURL, cfg.CookieJars)
			if err != nil {
				slog.Warn("canonical URL fetch failed", "source", source, "canonical", canonicalURL, "err", err)
			} else if len(strings.Fields(canonicalResult.Text)) >= minWords {
				slog.Info("canonical URL fetch succeeded", "source", source, "words", len(strings.Fields(canonicalResult.Text)))
				progress(fmt.Sprintf("canonical URL returned full text (%d words)", len(strings.Fields(canonicalResult.Text))))
				extracted = canonicalResult
				wordCount = len(strings.Fields(extracted.Text))
				isTeaser = false
			} else {
				slog.Info("canonical URL also teaser, trying Jina on canonical",
					"source", source, "canonical", canonicalURL, "words", len(strings.Fields(canonicalResult.Text)))
				progress(fmt.Sprintf("canonical also short (%d words) — retrying via Jina...", len(strings.Fields(canonicalResult.Text))))
				jinaResult, err := extractor.FromURLViaJina(ctx, canonicalURL)
				if err != nil {
					slog.Warn("jina retry on canonical failed", "source", source, "err", err)
				} else if len(strings.Fields(jinaResult.Text)) >= minWords {
					slog.Info("jina on canonical succeeded", "source", source, "words", len(strings.Fields(jinaResult.Text)))
					progress(fmt.Sprintf("jina returned full text (%d words)", len(strings.Fields(jinaResult.Text))))
					extracted = jinaResult
					wordCount = len(strings.Fields(extracted.Text))
					isTeaser = false
				} else {
					slog.Warn("paywalled content, all strategies exhausted",
						"source", source, "canonical", canonicalURL,
						"canonical_host", canonicalURL,
					)
					progress(fmt.Sprintf("paywalled — full text at %s — add cookie jar for that host in config", canonicalURL))
				}
			}
		} else {
			// No canonical URL difference — try Jina on the original URL.
			slog.Info("teaser detected, retrying via Jina", "source", source, "words", wordCount, "threshold", minWords)
			progress(fmt.Sprintf("teaser (%d words) — retrying via Jina...", wordCount))
			jinaResult, err := extractor.FromURLViaJina(ctx, req.URL)
			if err != nil {
				slog.Warn("jina teaser retry failed", "source", source, "err", err)
			} else if len(strings.Fields(jinaResult.Text)) >= minWords {
				slog.Info("jina teaser retry succeeded", "source", source, "words", len(strings.Fields(jinaResult.Text)))
				progress(fmt.Sprintf("jina returned full text (%d words)", len(strings.Fields(jinaResult.Text))))
				extracted = jinaResult
				wordCount = len(strings.Fields(extracted.Text))
				isTeaser = false
			} else {
				slog.Info("jina teaser retry also short", "source", source, "words", len(strings.Fields(jinaResult.Text)))
				progress(fmt.Sprintf("jina also returned teaser (%d words) — paywall likely", len(strings.Fields(jinaResult.Text))))
			}
		}
	}
	if isTeaser {
		slog.Info("teaser detected", "source", source, "words", wordCount, "threshold", minWords)
		progress(fmt.Sprintf("teaser detected (%d words, threshold %d) — skipping LLM generation", wordCount, minWords))
	}

	if req.DryRun {
		return Result{ExtractionStats: stats, Teaser: isTeaser}, nil
	}

	// ── Cost estimate (fires before any LLM call) ─────────────────────────
	if !isTeaser && req.OnCostEstimate != nil {
		flashcardsEnabled := (cfg.Ingest.Flashcards || req.Flashcards) && !req.NoFlashcards
		// Apply per-request overrides so the estimate reflects the actual profiles used.
		estCfg := cfg
		if req.SummaryModel != "" {
			estCfg.Ingest.SummaryProfile = req.SummaryModel
			estCfg.Ingest.FlashProfile = req.SummaryModel
			estCfg.Ingest.FlashcardProfile = req.SummaryModel
		}
		if req.FlashModel != "" {
			estCfg.Ingest.FlashProfile = req.FlashModel
		}
		if req.FlashcardModel != "" {
			estCfg.Ingest.FlashcardProfile = req.FlashcardModel
		}
		if req.SummaryStyle != "" {
			estCfg.Ingest.SummaryStyle = req.SummaryStyle
		}
		nChunks, usd := estimateIngestCost(extracted.Text, estCfg, flashcardsEnabled)
		if nChunks > 0 {
			req.OnCostEstimate(nChunks, usd)
		}
	}

	// ── 2. Resolve profiles and styles ───────────────────────────────────
	summaryStyle := first(req.SummaryStyle, cfg.Ingest.SummaryStyle)
	flashcardStyle := first(req.FlashcardStyle, cfg.Ingest.FlashcardStyle)

	summaryProf, err := lookupProfile(cfg, first(req.SummaryModel, cfg.Ingest.SummaryProfile))
	if err != nil {
		return Result{}, fmt.Errorf("summary profile: %w", err)
	}
	flashProf, err := lookupProfile(cfg, first(req.FlashModel, cfg.Ingest.FlashProfile))
	if err != nil {
		return Result{}, fmt.Errorf("flash profile: %w", err)
	}
	flashcardProf, err := lookupProfile(cfg, first(req.FlashcardModel, cfg.Ingest.FlashcardProfile))
	if err != nil {
		return Result{}, fmt.Errorf("flashcard profile: %w", err)
	}

	// ── 3. Build LLM providers ────────────────────────────────────────────
	newProvider := func(prof config.Profile) (llm.Provider, error) {
		return llm.New(llm.ProviderConfig{
			Provider: prof.Provider,
			Model:    prof.Model,
			Host:     prof.Host,
			APIKey:   resolveAPIKey(prof.Provider),
		})
	}

	var costRec store.CostRecord

	// ── Teaser fast-path: skip all LLM steps ─────────────────────────────
	if isTeaser {
		title := req.Title
		if title == "" {
			if extracted.Title != "" {
				title = extracted.Title
			} else {
				title = "Untitled"
			}
		}
		progress("title: " + title)
		date := time.Now().Format("20060102")
		slug := date + "-" + slugify(title)
		progress("slug: " + slug)
		progress("writing files (teaser)...")

		dir := filepath.Join(cfg.ArticlesRoot, slug)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return Result{}, fmt.Errorf("mkdir %s: %w", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "body.txt"), []byte(extracted.Text), 0644); err != nil {
			return Result{}, fmt.Errorf("write body: %w", err)
		}
		if req.URL != "" {
			_ = os.WriteFile(filepath.Join(dir, "source.url"), []byte(req.URL+"\n"), 0644)
		}

		numID, err := fs.AllocNumID(cfg.DataRoot)
		if err != nil {
			return Result{}, fmt.Errorf("alloc num_id: %w", err)
		}

		now := time.Now().UTC().Format(time.RFC3339)
		meta := fs.Meta{
			ID:           slug,
			NumID:        numID,
			Title:        title,
			URL:          req.URL,
			SourceType:   sourceType,
			Author:       extracted.Author,
			Language:     extracted.Language,
			IngestedAt:   now,
			Tags:         []fs.MetaTag{{Value: "teaser", Source: "auto"}},
			AgentRunID:   req.AgentRunID,
			AgentVerdict: req.AgentVerdict,
			AgentReason:  req.AgentReason,
		}
		if err := fs.WriteMeta(dir, meta); err != nil {
			return Result{}, fmt.Errorf("write meta: %w", err)
		}

		// Still embed — useful for search even with short content.
		var embedModel string
		if !req.NoEmbed && req.VectorStore != nil && cfg.Ingest.EmbedProfile != "" {
			if embedProf, err := lookupProfile(cfg, cfg.Ingest.EmbedProfile); err == nil {
				if embedClient, err := embed.NewClient(embedProf.Model); err == nil {
					if er, err := embedClient.Embed(ctx, extracted.Text); err == nil {
						if uErr := req.VectorStore.Upsert(ctx, slug, er.Embedding, extracted.Text); uErr == nil {
							embedModel = embedProf.Model
						}
					}
				}
			}
		}
		if embedModel != "" {
			meta.EmbedModel = embedModel
			_ = fs.WriteMeta(dir, meta)
		}

		if req.Collection != "" {
			_ = fs.CreateCollection(cfg.DataRoot, req.Collection, "")
			if err := fs.AddArticleToCollection(cfg.DataRoot, cfg.ArticlesRoot, slug, req.Collection); err != nil && err != fs.ErrAlreadyInCollection {
				slog.Warn("could not link teaser to collection", "slug", slug, "collection", req.Collection, "err", err)
			}
		}
		appendEvent(cfg.EventsPath, store.Event{
			TS: time.Now().UTC(), Type: "ingest", ArticleID: slug,
		})
		return Result{Slug: slug, Teaser: true, ExtractionStats: stats}, nil
	}

	// ── 4. Title ──────────────────────────────────────────────────────────
	title := req.Title
	if title == "" {
		if extracted.Title != "" {
			title = extracted.Title
			progress("title: " + title)
		} else {
			progress("generating title...")
			p, err := newProvider(flashProf)
			if err != nil {
				return Result{}, fmt.Errorf("llm provider: %w", err)
			}
			snippet := truncateWords(extracted.Text, 500)
			t, u, err := chat(ctx, p,
				"You are a precise editor. Return only a short article title, no punctuation at the end, no quotes.",
				"Generate a concise title (5–10 words) for this article:\n\n"+snippet)
			if err != nil {
				return Result{}, fmt.Errorf("generate title: %w", err)
			}
			title = strings.TrimSpace(t)
			costRec.Flash.InputTokens += u.InputTokens
			costRec.Flash.OutputTokens += u.OutputTokens
			progress("title: " + title)
		}
	}

	// ── 5. Slug ───────────────────────────────────────────────────────────
	date := time.Now().Format("20060102")
	slug := date + "-" + slugify(title)
	progress("slug: " + slug)

	// ── 6. Summarize (map-reduce over chunks) ─────────────────────────────
	summaryProvider, err := newProvider(summaryProf)
	if err != nil {
		return Result{}, fmt.Errorf("llm provider: %w", err)
	}
	summaryText, su, err := summarizeText(ctx, summaryProvider, extracted.Text, title, req.URL, summaryStyle, summaryProf.Model, cfg.Ingest.ChunkTokens, cfg.Ingest.SummaryMaxTokens, cfg.StylePrompt(summaryStyle), progress)
	if err != nil {
		// Retry once with oai-mini fallback if primary model failed (e.g. timeout on reduce).
		slog.Warn("summarize failed, retrying with oai-mini fallback", "slug", slug, "err", err)
		progress(fmt.Sprintf("summarize failed (%v) — retrying with oai-mini...", err))
		fallbackProf, ferr := lookupProfile(cfg, "oai-mini")
		if ferr != nil {
			slog.Error("summarize failed, no fallback available", "slug", slug, "err", err)
			return Result{}, fmt.Errorf("summarize: %w", err)
		}
		fallbackProvider, ferr := newProvider(fallbackProf)
		if ferr != nil {
			slog.Error("summarize fallback provider failed", "slug", slug, "err", ferr)
			return Result{}, fmt.Errorf("summarize: %w", err)
		}
		summaryText, su, err = summarizeText(ctx, fallbackProvider, extracted.Text, title, req.URL, summaryStyle, fallbackProf.Model, cfg.Ingest.ChunkTokens, cfg.Ingest.SummaryMaxTokens, cfg.StylePrompt(summaryStyle), progress)
		if err != nil {
			slog.Error("summarize fallback also failed", "slug", slug, "err", err)
			return Result{}, fmt.Errorf("summarize: %w", err)
		}
		summaryProf = fallbackProf
		slog.Info("summarize fallback succeeded", "slug", slug, "model", fallbackProf.Model)
		progress(fmt.Sprintf("summarize fallback succeeded (model: %s)", fallbackProf.Model))
	}
	costRec.Summary = store.CostEntry{
		Model:        summaryProf.Model,
		InputTokens:  su.InputTokens,
		OutputTokens: su.OutputTokens,
		USD:          cfg.CalcCost(summaryProf.Model, su.InputTokens, su.OutputTokens),
	}
	progress(fmt.Sprintf("summary done ($%.4f)", costRec.Summary.USD))

	// ── 7. Flash ──────────────────────────────────────────────────────────
	progress(fmt.Sprintf("generating flash (model: %s)...", flashProf.Model))
	flashProvider, err := newProvider(flashProf)
	if err != nil {
		return Result{}, fmt.Errorf("llm provider: %w", err)
	}
	flashText, fu, err := generateFlash(ctx, flashProvider, summaryText, cfg.Ingest.FlashSystemPrompt, cfg.Ingest.FlashMaxTokens)
	if err != nil {
		slog.Error("flash generation failed", "slug", slug, "model", flashProf.Model, "err", err)
		return Result{}, fmt.Errorf("flash: %w", err)
	}
	costRec.Flash.Model = flashProf.Model
	costRec.Flash.InputTokens += fu.InputTokens
	costRec.Flash.OutputTokens += fu.OutputTokens
	costRec.Flash.USD = cfg.CalcCost(flashProf.Model, costRec.Flash.InputTokens, costRec.Flash.OutputTokens)

	// ── 8. Flashcards ─────────────────────────────────────────────────────
	var flashcardsJSON []byte
	if (cfg.Ingest.Flashcards || req.Flashcards) && !req.NoFlashcards {
		progress(fmt.Sprintf("generating flashcards (model: %s)...", flashcardProf.Model))
		fcProvider, err := newProvider(flashcardProf)
		if err != nil {
			return Result{}, fmt.Errorf("llm provider: %w", err)
		}
		fj, cu, err := generateFlashcards(ctx, fcProvider, summaryText, flashcardStyle, cfg.FlashcardStylePrompt(flashcardStyle))
		if err != nil {
			// Flashcard failure is non-fatal — log and continue without them.
			slog.Warn("flashcards failed, continuing without them", "slug", slug, "err", err)
			progress(fmt.Sprintf("flashcards skipped: %v", err))
		} else {
			flashcardsJSON = fj
			costRec.Flashcards = store.CostEntry{
				Model:        flashcardProf.Model,
				InputTokens:  cu.InputTokens,
				OutputTokens: cu.OutputTokens,
				USD:          cfg.CalcCost(flashcardProf.Model, cu.InputTokens, cu.OutputTokens),
			}
		}
	}

	// ── 9. Embed ──────────────────────────────────────────────────────────
	var embedModel string
	if !req.NoEmbed && req.VectorStore != nil && summaryText != "" && cfg.Ingest.EmbedProfile != "" {
		embedProf, err := lookupProfile(cfg, cfg.Ingest.EmbedProfile)
		if err == nil {
			progress(fmt.Sprintf("embedding (model: %s)...", embedProf.Model))
			embedClient, err := embed.NewClient(embedProf.Model)
			if err == nil {
				er, err := embedClient.Embed(ctx, summaryText)
				if err == nil {
					if uErr := req.VectorStore.Upsert(ctx, slug, er.Embedding, summaryText); uErr == nil {
						embedModel = embedProf.Model
						embedUSD := float64(er.Tokens) * embedProf.Info.Pricing.Input / 1_000_000
						costRec.Embed = store.EmbedCostEntry{
							Model:  embedProf.Model,
							Tokens: er.Tokens,
							USD:    embedUSD,
						}
						progress(fmt.Sprintf("embedding done ($%.5f)", embedUSD))
					} else {
						slog.Error("vector upsert failed", "slug", slug, "model", embedProf.Model, "err", uErr)
						progress(fmt.Sprintf("embedding skipped: %v", uErr))
					}
				} else {
					slog.Error("embed API call failed", "slug", slug, "model", embedProf.Model, "err", err)
					progress(fmt.Sprintf("embedding skipped: %v", err))
				}
			} else {
				slog.Error("embed client init failed", "slug", slug, "profile", cfg.Ingest.EmbedProfile, "err", err)
				progress(fmt.Sprintf("embedding skipped: %v", err))
			}
		}
	}

	costRec.TotalUSD = costRec.Summary.USD + costRec.Flash.USD + costRec.Flashcards.USD + costRec.Embed.USD

	// ── 10. Write files ──────────────────────────────────────────────────
	progress("writing files...")
	dir := filepath.Join(cfg.ArticlesRoot, slug)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return Result{}, fmt.Errorf("mkdir %s: %w", dir, err)
	}

	if err := os.WriteFile(filepath.Join(dir, "body.txt"), []byte(extracted.Text), 0644); err != nil {
		return Result{}, fmt.Errorf("write body: %w", err)
	}

	summaryFile := fmt.Sprintf("summary.%s.%s.txt", summaryStyle, summaryProf.Model)
	if err := os.WriteFile(filepath.Join(dir, summaryFile), []byte(summaryText), 0644); err != nil {
		return Result{}, fmt.Errorf("write summary: %w", err)
	}

	flashFile := fmt.Sprintf("flash.%s.txt", flashProf.Model)
	if err := os.WriteFile(filepath.Join(dir, flashFile), []byte(flashText), 0644); err != nil {
		return Result{}, fmt.Errorf("write flash: %w", err)
	}

	if len(flashcardsJSON) > 0 {
		fcFile := fmt.Sprintf("flashcards.%s.%s.json", flashcardStyle, flashcardProf.Model)
		if err := os.WriteFile(filepath.Join(dir, fcFile), flashcardsJSON, 0644); err != nil {
			return Result{}, fmt.Errorf("write flashcards: %w", err)
		}
	}

	// Source sidecar
	if req.URL != "" {
		if err := os.WriteFile(filepath.Join(dir, "source.url"), []byte(req.URL+"\n"), 0644); err != nil {
			return Result{}, fmt.Errorf("write source.url: %w", err)
		}
	} else if req.File != "" && req.File != "-" {
		if strings.ToLower(filepath.Ext(req.File)) == ".pdf" {
			data, _ := os.ReadFile(req.File)
			_ = os.WriteFile(filepath.Join(dir, "source.pdf"), data, 0644)
		}
	}

	// ── 11. meta.json ─────────────────────────────────────────────────────
	numID, err := fs.AllocNumID(cfg.DataRoot)
	if err != nil {
		return Result{}, fmt.Errorf("alloc num_id: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	meta := fs.Meta{
		ID:             slug,
		NumID:          numID,
		Title:          title,
		URL:            req.URL,
		SourceType:     sourceType,
		Author:         extracted.Author,
		Language:       extracted.Language,
		IngestedAt:     now,
		SummaryModel:   summaryProf.Model,
		SummaryStyle:   summaryStyle,
		FlashModel:     flashProf.Model,
		FlashcardModel: func() string {
			if len(flashcardsJSON) > 0 {
				return flashcardProf.Model
			}
			return ""
		}(),
		FlashcardStyle: func() string {
			if len(flashcardsJSON) > 0 {
				return flashcardStyle
			}
			return ""
		}(),
		EmbedModel:     embedModel,
		AgentRunID:     req.AgentRunID,
		AgentVerdict:   req.AgentVerdict,
		AgentReason:    req.AgentReason,
	}
	if err := fs.WriteMeta(dir, meta); err != nil {
		return Result{}, fmt.Errorf("write meta: %w", err)
	}

	// ── 12. Collection symlink ────────────────────────────────────────────
	if req.Collection != "" {
		_ = fs.CreateCollection(cfg.DataRoot, req.Collection, "")
		if err := fs.AddArticleToCollection(cfg.DataRoot, cfg.ArticlesRoot, slug, req.Collection); err != nil && err != fs.ErrAlreadyInCollection {
			slog.Warn("could not link article to collection", "slug", slug, "collection", req.Collection, "err", err)
		}
	}

	// ── 13. Append to events.jsonl ───────────────────────────────────────
	appendEvent(cfg.EventsPath, store.Event{
		TS:        time.Now().UTC(),
		Type:      "ingest",
		ArticleID: slug,
		Cost:      &costRec,
	})

	slog.Info("ingest done", "slug", slug, "cost_usd", costRec.TotalUSD)
	return Result{Slug: slug, Cost: costRec, ExtractionStats: stats}, nil
}

// ── LLM helpers ───────────────────────────────────────────────────────────────

// chat wraps llm.Provider.Chat for single-turn calls using brief's interface.
func chat(ctx context.Context, p llm.Provider, system, prompt string) (string, llm.Usage, error) {
	return p.Chat(ctx, system, []llm.Message{{Role: llm.RoleUser, Content: prompt}})
}

// briefAdapter bridges github.com/jrniemiec/llm.Provider → brief/llm.Provider.
// brief expects a simpler single-turn interface; we wrap the multi-turn one.
// onCall, if set, is invoked before the first attempt of each LLM call.
// onRetry, if set, is invoked on each failed attempt before retrying.
type briefAdapter struct {
	p       llm.Provider
	model   string
	onCall  func()
	onDone  func(elapsed time.Duration, u llm.Usage)
	onRetry func(attempt int, err error)
}

const maxChunkRetries = 3

func (a *briefAdapter) Name() string { return a.model }
func (a *briefAdapter) Chat(ctx context.Context, system, prompt string) (string, briefllm.Usage, error) {
	if a.onCall != nil {
		a.onCall()
	}
	start := time.Now()
	var out string
	var u llm.Usage
	var err error
	for attempt := 1; attempt <= maxChunkRetries; attempt++ {
		out, u, err = a.p.Chat(ctx, system, []llm.Message{{Role: llm.RoleUser, Content: prompt}})
		if err == nil {
			break
		}
		// Don't retry if the caller canceled (user pressed Esc).
		// context.DeadlineExceeded is the transport timeout — that IS worth retrying.
		if errors.Is(err, context.Canceled) {
			break
		}
		if attempt < maxChunkRetries {
			if a.onRetry != nil {
				a.onRetry(attempt, err)
			}
		}
	}
	if err == nil && a.onDone != nil {
		a.onDone(time.Since(start), u)
	}
	return out, briefllm.Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens}, err
}

// summarizeText uses brief's paragraph-aware chunking and map-reduce summarization.
func summarizeText(ctx context.Context, p llm.Provider, text, title, source, style, model string, chunkTokens, maxTokens int, systemPrompt string, progress func(string)) (string, llm.Usage, error) {
	doc := brieftypes.Document{
		ID:     brieftypes.ShortID(brieftypes.HashText(text)),
		Hash:   brieftypes.HashText(text),
		Title:  title,
		Source: source,
		Text:   text,
	}

	chunks, err := briefchunk.ChunkDocument(doc, chunkTokens)
	if err != nil {
		return "", llm.Usage{}, fmt.Errorf("chunk: %w", err)
	}

	// Build per-chunk token counts up front so the progress adapter can report them.
	chunkTokenCounts := make([]int, len(chunks))
	for i, ch := range chunks {
		chunkTokenCounts[i] = briefchunk.ApproxTokens(ch.Text)
	}

	n := len(chunks)
	call := 0 // tracks which LLM call we're on (map calls first, then reduce)
	adapter := &briefAdapter{
		p:     p,
		model: model,
		onCall: func() {
			call++
		},
		onDone: func(elapsed time.Duration, u llm.Usage) {
			if call <= n {
				progress(fmt.Sprintf("chunk %d/%d (~%d in, style: %s, model: %s) out=%d %.1fs", call, n, chunkTokenCounts[call-1], style, model, u.OutputTokens, elapsed.Seconds()))
			} else {
				progress(fmt.Sprintf("reducing %d chunk summaries (style: %s) out=%d %.1fs", n, style, u.OutputTokens, elapsed.Seconds()))
			}
		},
		onRetry: func(attempt int, err error) {
			// Truncate the error to its last ": <detail>" segment for readability.
			errStr := err.Error()
			if i := strings.LastIndex(errStr, ": "); i >= 0 {
				errStr = errStr[i+2:]
			}
			if call <= n {
				progress(fmt.Sprintf("chunk %d/%d retry %d/%d: %s", call, n, attempt, maxChunkRetries, errStr))
			} else {
				progress(fmt.Sprintf("reduce retry %d/%d: %s", attempt, maxChunkRetries, errStr))
			}
		},
	}

	summary, bu, err := briefsummarize.SummarizeDocument(ctx, io.Discard, adapter, doc, chunks, style, systemPrompt, maxTokens, false)
	if err != nil {
		return "", llm.Usage{}, fmt.Errorf("summarize: %w", err)
	}

	return summary.Markdown, llm.Usage{InputTokens: bu.InputTokens, OutputTokens: bu.OutputTokens}, nil
}


func generateFlash(ctx context.Context, p llm.Provider, text, systemPrompt string, maxTokens int) (string, llm.Usage, error) {
	out, u, err := p.Chat(ctx, systemPrompt, []llm.Message{
		{Role: llm.RoleUser, Content: "Write a flash summary for this text:\n\n" + text},
	})
	_ = maxTokens // passed to provider config in future; LLMs self-limit short outputs well
	return out, u, err
}

func generateFlashcards(ctx context.Context, p llm.Provider, text, style, systemPrompt string) ([]byte, llm.Usage, error) {
	raw, u, err := chat(ctx, p,
		systemPrompt,
		"Generate flashcards from this text. Return valid JSON only, no markdown fences:\n\n"+text)
	if err != nil {
		return nil, u, err
	}

	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	if !json.Valid([]byte(raw)) {
		return nil, u, fmt.Errorf("flashcards LLM returned invalid JSON")
	}
	return []byte(raw), u, nil
}

// ── Utilities ─────────────────────────────────────────────────────────────────

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(title string) string {
	s := strings.ToLower(title)
	s = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return '-'
	}, s)
	s = nonAlnum.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 60 {
		s = s[:60]
		if i := strings.LastIndex(s, "-"); i > 30 {
			s = s[:i]
		}
	}
	return s
}

func first(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func truncateWords(text string, maxWords int) string {
	words := strings.Fields(text)
	if len(words) <= maxWords {
		return text
	}
	return strings.Join(words[:maxWords], " ")
}

// estimateIngestCost computes an approximate USD cost for ingesting text.
// It chunks the text (pure computation), then estimates token costs for the
// map-reduce summary, flash, optional flashcards, and embed steps.
// Returns (0, 0) if pricing is unavailable for the configured profiles.
func estimateIngestCost(text string, cfg config.Config, flashcardsEnabled bool) (nChunks int, usd float64) {
	chunkTokens := cfg.Ingest.ChunkTokens
	if chunkTokens <= 0 {
		chunkTokens = 900
	}

	doc := brieftypes.Document{
		ID:   "estimate",
		Text: text,
	}
	chunks, err := briefchunk.ChunkDocument(doc, chunkTokens)
	if err != nil || len(chunks) == 0 {
		return 0, 0
	}
	nChunks = len(chunks)

	// Per-chunk map output estimate: brief produces a partial summary per chunk.
	// Use a conservative 400 tokens as a typical chunk summary length.
	const perChunkOutputEst = 400

	// Summary map phase
	summaryProf, err := lookupProfile(cfg, cfg.Ingest.SummaryProfile)
	if err == nil {
		for _, ch := range chunks {
			inTok := briefchunk.ApproxTokens(ch.Text) + 200 // +200 system prompt overhead
			usd += cfg.CalcCost(summaryProf.Model, inTok, perChunkOutputEst)
		}
		// Summary reduce phase
		reduceInTok := nChunks*perChunkOutputEst + 200
		reduceOutTok := cfg.Ingest.SummaryMaxTokens
		if reduceOutTok <= 0 {
			reduceOutTok = 2048
		}
		usd += cfg.CalcCost(summaryProf.Model, reduceInTok, reduceOutTok)

		// Flash (input ≈ reduce output)
		if flashProf, err := lookupProfile(cfg, cfg.Ingest.FlashProfile); err == nil {
			flashOut := cfg.Ingest.FlashMaxTokens
			if flashOut <= 0 {
				flashOut = 256
			}
			usd += cfg.CalcCost(flashProf.Model, reduceOutTok+200, flashOut)
		}

		// Flashcards (optional)
		if flashcardsEnabled {
			if fcProf, err := lookupProfile(cfg, cfg.Ingest.FlashcardProfile); err == nil {
				fcOut := cfg.Ingest.FlashcardMaxTokens
				if fcOut <= 0 {
					fcOut = 2048
				}
				usd += cfg.CalcCost(fcProf.Model, reduceOutTok+200, fcOut)
			}
		}

		// Embed (the summary text, ≈ reduceOutTok tokens)
		if cfg.Ingest.EmbedProfile != "" {
			if embedProf, err := lookupProfile(cfg, cfg.Ingest.EmbedProfile); err == nil && embedProf.Info.Pricing != nil {
				usd += float64(reduceOutTok) * embedProf.Info.Pricing.Input / 1_000_000
			}
		}
	}

	return nChunks, usd
}

// lookupProfile resolves a profile name from config. Returns an error if not found.
func lookupProfile(cfg config.Config, name string) (config.Profile, error) {
	p, ok := cfg.Profile(name)
	if !ok {
		return config.Profile{}, fmt.Errorf("unknown profile %q — check ~/.arc/config.json or run: arc profiles", name)
	}
	return p, nil
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

// ── Collection suggestion ─────────────────────────────────────────────────────

// CollectionSuggestRequest describes a library-wide collection suggestion call.
type CollectionSuggestRequest struct {
	Articles    []CollectionSuggestArticle
	Existing    []CollectionSuggestCollection // already-created collections to avoid duplicating
	Profile     string                        // profile name override; falls back to CollectionSuggestProfileName
	Progress    func(string)
}

// CollectionSuggestArticle is one article entry for the suggestion prompt.
type CollectionSuggestArticle struct {
	Slug  string
	Title string
}

// CollectionSuggestResult is one proposed collection from library-wide analysis.
type CollectionSuggestResult struct {
	Slug        string
	Description string
	Articles    []string
}

// CollectionSuggest calls the LLM to suggest collections for the whole library.
func CollectionSuggest(ctx context.Context, cfg config.Config, req CollectionSuggestRequest) ([]CollectionSuggestResult, error) {
	profileName := req.Profile
	if profileName == "" {
		profileName = cfg.CollectionSuggestProfileName()
	}
	prof, err := lookupProfile(cfg, profileName)
	if err != nil {
		return nil, fmt.Errorf("profile: %w", err)
	}
	p, err := llm.New(llm.ProviderConfig{
		Provider: prof.Provider,
		Model:    prof.Model,
		Host:     prof.Host,
		APIKey:   resolveAPIKey(prof.Provider),
	})
	if err != nil {
		return nil, fmt.Errorf("llm provider: %w", err)
	}

	// Build prompt input
	var sb strings.Builder
	sb.WriteString("Articles:\n")
	for _, a := range req.Articles {
		sb.WriteString(fmt.Sprintf("- slug: %s | title: %s\n", a.Slug, a.Title))
	}
	if len(req.Existing) > 0 {
		sb.WriteString("\nExisting collections (do not suggest these):\n")
		for _, c := range req.Existing {
			if c.Description != "" {
				sb.WriteString(fmt.Sprintf("- %s: %s\n", c.Slug, c.Description))
			} else {
				sb.WriteString(fmt.Sprintf("- %s\n", c.Slug))
			}
		}
	}

	userMsg := sb.String()
	systemPrompt := cfg.CollectionSuggestPrompt()

	slog.Info("collection suggest request",
		"profile", profileName,
		"model", prof.Model,
		"articles", len(req.Articles),
		"existing_collections", len(req.Existing),
		"system_prompt", systemPrompt,
		"user_message", userMsg,
	)
	if req.Progress != nil {
		req.Progress(fmt.Sprintf("suggesting collections for %d articles (model: %s)...", len(req.Articles), prof.Model))
		req.Progress("--- system prompt ---\n" + systemPrompt)
		req.Progress("--- user message ---\n" + userMsg)
	}

	resp, _, err := p.Chat(ctx, systemPrompt, []llm.Message{
		{Role: llm.RoleUser, Content: userMsg},
	})
	if err != nil {
		return nil, fmt.Errorf("llm: %w", err)
	}

	return parseCollectionSuggestions(resp)
}

// CollectionArticleSuggestRequest describes a per-article collection suggestion call.
type CollectionArticleSuggestRequest struct {
	ArticleSlug  string
	ArticleTitle string
	ArticleFlash string // flash summary for context; may be empty
	Collections  []CollectionSuggestCollection
	Profile      string
	Progress     func(string)
}

// CollectionSuggestCollection is one existing collection passed to the LLM.
type CollectionSuggestCollection struct {
	Slug        string
	Description string
}

// CollectionArticleMatchResult is one ranked collection match for an article.
// If Slug is empty, NewSlug/NewDescription propose a new collection to create.
type CollectionArticleMatchResult struct {
	Slug           string
	Reason         string
	NewSlug        string // set when LLM proposes a new collection
	NewDescription string // description for the proposed new collection
}

// CollectionArticleSuggest calls the LLM to suggest which existing collections
// a specific article fits.
func CollectionArticleSuggest(ctx context.Context, cfg config.Config, req CollectionArticleSuggestRequest) ([]CollectionArticleMatchResult, error) {
	profileName := req.Profile
	if profileName == "" {
		profileName = cfg.CollectionSuggestProfileName()
	}
	prof, err := lookupProfile(cfg, profileName)
	if err != nil {
		return nil, fmt.Errorf("profile: %w", err)
	}
	p, err := llm.New(llm.ProviderConfig{
		Provider: prof.Provider,
		Model:    prof.Model,
		Host:     prof.Host,
		APIKey:   resolveAPIKey(prof.Provider),
	})
	if err != nil {
		return nil, fmt.Errorf("llm provider: %w", err)
	}

	// Build prompt input
	var articleDesc strings.Builder
	articleDesc.WriteString(fmt.Sprintf("Title: %s\n", req.ArticleTitle))
	if req.ArticleFlash != "" {
		articleDesc.WriteString("Flash summary: " + req.ArticleFlash + "\n")
	}

	var colList strings.Builder
	for _, c := range req.Collections {
		desc := c.Description
		if desc == "" {
			desc = "no description"
		}
		colList.WriteString(fmt.Sprintf("- %s: %s\n", c.Slug, desc))
	}

	userMsg := "Article:\n" + articleDesc.String() + "\nExisting collections:\n" + colList.String()
	systemPrompt := config.DefaultCollectionArticleSuggestPrompt

	slog.Info("collection article suggest request",
		"profile", profileName,
		"model", prof.Model,
		"article", req.ArticleSlug,
		"collections", len(req.Collections),
		"system_prompt", systemPrompt,
		"user_message", userMsg,
	)
	if req.Progress != nil {
		req.Progress(fmt.Sprintf("suggesting collections for %s (model: %s)...", req.ArticleSlug, prof.Model))
		req.Progress("--- system prompt ---\n" + systemPrompt)
		req.Progress("--- user message ---\n" + userMsg)
	}

	resp, _, err := p.Chat(ctx, systemPrompt, []llm.Message{
		{Role: llm.RoleUser, Content: userMsg},
	})
	if err != nil {
		return nil, fmt.Errorf("llm: %w", err)
	}

	return parseCollectionMatches(resp)
}

// CollectionDescribeRequest describes a request to generate a description for one collection.
type CollectionDescribeRequest struct {
	Slug     string
	Titles   []string // member article titles
	Profile  string
	Progress func(string)
}

// CollectionDescribe calls the LLM to generate a one-sentence description for a collection.
func CollectionDescribe(ctx context.Context, cfg config.Config, req CollectionDescribeRequest) (string, error) {
	profileName := req.Profile
	if profileName == "" {
		profileName = cfg.CollectionSuggestProfileName()
	}
	prof, err := lookupProfile(cfg, profileName)
	if err != nil {
		return "", fmt.Errorf("profile: %w", err)
	}
	p, err := llm.New(llm.ProviderConfig{
		Provider: prof.Provider,
		Model:    prof.Model,
		Host:     prof.Host,
		APIKey:   resolveAPIKey(prof.Provider),
	})
	if err != nil {
		return "", fmt.Errorf("llm provider: %w", err)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Collection: %s\n\nMember articles:\n", req.Slug))
	for _, t := range req.Titles {
		sb.WriteString(fmt.Sprintf("- %s\n", t))
	}

	userMsg := sb.String()
	systemPrompt := cfg.CollectionDescribePrompt()

	slog.Info("collection describe request",
		"profile", profileName,
		"model", prof.Model,
		"slug", req.Slug,
		"articles", len(req.Titles),
	)
	if req.Progress != nil {
		req.Progress(fmt.Sprintf("generating description for %s (model: %s)...", req.Slug, prof.Model))
	}

	resp, _, err := p.Chat(ctx, systemPrompt, []llm.Message{
		{Role: llm.RoleUser, Content: userMsg},
	})
	if err != nil {
		return "", fmt.Errorf("llm: %w", err)
	}

	return strings.TrimSpace(resp), nil
}

// parseCollectionSuggestions parses the LLM JSON response for library-wide suggestions.
func parseCollectionSuggestions(resp string) ([]CollectionSuggestResult, error) {
	// Strip markdown code fences if present
	resp = strings.TrimSpace(resp)
	if i := strings.Index(resp, "["); i > 0 {
		resp = resp[i:]
	}
	if i := strings.LastIndex(resp, "]"); i >= 0 {
		resp = resp[:i+1]
	}

	var raw []struct {
		Slug        string   `json:"slug"`
		Description string   `json:"description"`
		Articles    []string `json:"articles"`
	}
	if err := json.Unmarshal([]byte(resp), &raw); err != nil {
		return nil, fmt.Errorf("parse collection suggestions: %w\nresponse: %s", err, resp)
	}
	out := make([]CollectionSuggestResult, 0, len(raw))
	for _, r := range raw {
		if r.Slug == "" {
			continue
		}
		out = append(out, CollectionSuggestResult{
			Slug:        r.Slug,
			Description: r.Description,
			Articles:    r.Articles,
		})
	}
	return out, nil
}

// parseCollectionMatches parses the LLM JSON response for per-article matches.
func parseCollectionMatches(resp string) ([]CollectionArticleMatchResult, error) {
	resp = strings.TrimSpace(resp)
	if i := strings.Index(resp, "["); i > 0 {
		resp = resp[i:]
	}
	if i := strings.LastIndex(resp, "]"); i >= 0 {
		resp = resp[:i+1]
	}

	var raw []struct {
		Slug          *string `json:"slug"`
		Reason        string  `json:"reason"`
		NewCollection *struct {
			Slug        string `json:"slug"`
			Description string `json:"description"`
		} `json:"new_collection"`
	}
	if err := json.Unmarshal([]byte(resp), &raw); err != nil {
		return nil, fmt.Errorf("parse collection matches: %w\nresponse: %s", err, resp)
	}
	out := make([]CollectionArticleMatchResult, 0, len(raw))
	for _, r := range raw {
		if r.Slug != nil && *r.Slug != "" {
			out = append(out, CollectionArticleMatchResult{Slug: *r.Slug, Reason: r.Reason})
		} else if r.NewCollection != nil && r.NewCollection.Slug != "" {
			out = append(out, CollectionArticleMatchResult{
				NewSlug:        r.NewCollection.Slug,
				NewDescription: r.NewCollection.Description,
				Reason:         r.Reason,
			})
		}
	}
	return out, nil
}

func appendEvent(path string, ev store.Event) {
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(data, '\n'))
}

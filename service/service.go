package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jrniemiec/arc/config"
	"github.com/jrniemiec/arc/flashcards"
	"github.com/jrniemiec/arc/ingest/embed"
	"github.com/jrniemiec/arc/ingest/extractor"
	"github.com/jrniemiec/arc/ingest/pipeline"
	"github.com/jrniemiec/arc/library"
	"github.com/jrniemiec/arc/store"
	"github.com/jrniemiec/arc/store/fs"
	"github.com/jrniemiec/arc/store/vector"
)

// Service is the arc business logic layer. All frontends (CLI, TUI, MCP, bot)
// call Service methods — never library or store directly.
type Service struct {
	lib *library.Library
	cfg config.Config
	vec *vector.Store // nil if vector index could not be opened
}

// New creates a Service from an open Library.
// It attempts to open the vector index at cfg.VectorPath; failures are non-fatal.
func New(lib *library.Library, cfg config.Config) *Service {
	svc := &Service{lib: lib, cfg: cfg}
	if vs, err := vector.Open(cfg.VectorPath); err == nil {
		svc.vec = vs
	}
	return svc
}

// Library returns the underlying library for callers that need direct access.
func (s *Service) Library() *library.Library {
	return s.lib
}

// ReindexResult holds counts from a reindex operation.
type ReindexResult struct {
	Articles    int
	Collections int
}

// Reindex rebuilds the SQLite metadata and FTS5 indexes from the filesystem.
// The vector index is not touched — see Embed.
// progress is called with (indexed, total) after each article; may be nil.
func (s *Service) Reindex(ctx context.Context, progress func(indexed, total int)) (ReindexResult, error) {
	slog.Info("reindex start", "articles_root", s.cfg.ArticlesRoot)

	r, err := s.lib.Reindex(ctx, progress)
	if err != nil {
		slog.Error("reindex failed", "articles", r.Articles, "collections", r.Collections, "err", err)
		return ReindexResult{Articles: r.Articles, Collections: r.Collections}, err
	}

	slog.Info("reindex done", "articles", r.Articles, "collections", r.Collections)
	return ReindexResult{Articles: r.Articles, Collections: r.Collections}, nil
}

// EmbedOptions controls which articles Embed processes.
type EmbedOptions struct {
	All    bool // re-embed every eligible article, ignoring existing vectors
	DryRun bool // select and estimate cost only; makes no API calls
}

// EmbedResult holds counts and cost from an embed run.
type EmbedResult struct {
	Model     string   // embedding model used
	Eligible  int      // articles with a summary available to embed
	Pending   int      // articles selected for embedding
	Embedded  int      // articles successfully embedded
	Failed    int      // articles that errored
	Tokens    int      // input tokens consumed (estimated when DryRun)
	USD       float64  // cost (estimated when DryRun)
	Errors    []string // per-article failures, capped
	DryRun    bool
	Estimated bool // true when Tokens/USD are estimates rather than measured
}

// maxEmbedErrors caps how many per-article failures are retained for reporting.
const maxEmbedErrors = 10

// Embed rebuilds the vector index used for semantic search.
//
// Selection is driven by the vector store itself, not by embed_model in
// meta.json: an article is embedded when the index has no document for it.
// That makes a lost or partially-copied index recoverable — trusting meta.json
// would skip every article and leave semantic search silently empty.
//
// With opts.All every eligible article is re-embedded regardless of what the
// index already holds — the path to take after changing the embedding model.
//
// progress is called with (done, pending) after each article; may be nil.
func (s *Service) Embed(ctx context.Context, opts EmbedOptions, progress func(done, total int)) (EmbedResult, error) {
	res := EmbedResult{DryRun: opts.DryRun}

	slog.Info("embed start", "profile", s.cfg.Ingest.EmbedProfile,
		"all", opts.All, "dry_run", opts.DryRun)

	if s.vec == nil {
		slog.Error("embed failed", "err", "vector store not available")
		return res, fmt.Errorf("vector store not available")
	}
	if s.cfg.Ingest.EmbedProfile == "" {
		slog.Error("embed failed", "err", "embed_profile not configured")
		return res, fmt.Errorf("embed_profile not configured")
	}
	prof, ok := s.cfg.Profiles[s.cfg.Ingest.EmbedProfile]
	if !ok {
		slog.Error("embed failed", "err", "profile not found", "profile", s.cfg.Ingest.EmbedProfile)
		return res, fmt.Errorf("embed profile %q not found", s.cfg.Ingest.EmbedProfile)
	}
	res.Model = prof.Model

	articles, err := s.lib.List(ctx, store.Filter{})
	if err != nil {
		slog.Error("embed failed", "err", err)
		return res, fmt.Errorf("list articles: %w", err)
	}

	// Eligible: has a resolvable summary. Pending: not already in the index.
	var pending []store.Article
	for _, a := range articles {
		if a.SummaryModel == "" || a.Files.Summary == "" {
			slog.Debug("embed skip: no summary", "slug", a.ID)
			continue
		}
		res.Eligible++
		if opts.All || !s.vec.Has(ctx, a.ID) {
			pending = append(pending, a)
		}
	}
	res.Pending = len(pending)

	slog.Info("embed selected", "model", prof.Model,
		"eligible", res.Eligible, "pending", res.Pending, "indexed", s.vec.Count())

	if progress == nil {
		progress = func(int, int) {}
	}

	// Dry run: estimate from summary length on disk. Deliberately does not
	// construct the embed client, so it works without an API key.
	if opts.DryRun {
		res.Estimated = true
		for i, a := range pending {
			text, err := s.lib.ReadSummary(a)
			if err != nil || strings.TrimSpace(text) == "" {
				continue
			}
			tokens := (len(text) + 3) / 4
			res.Tokens += tokens
			slog.Debug("embed would embed", "slug", a.ID, "n", i+1, "of", len(pending),
				"est_tokens", tokens)
		}
		res.USD = embedCostUSD(prof, res.Tokens)
		slog.Info("embed dry-run done", "pending", res.Pending, "tokens", res.Tokens, "usd", res.USD)
		return res, nil
	}

	embedClient, err := embed.NewClient(prof.Model)
	if err != nil {
		return res, fmt.Errorf("embed client: %w", err)
	}

	done := 0
	for _, a := range pending {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		summaryText, err := s.lib.ReadSummary(a)
		if err != nil || strings.TrimSpace(summaryText) == "" {
			res.recordEmbedError(a.ID, "read summary", err)
			continue
		}
		er, err := embedClient.Embed(ctx, summaryText)
		if err != nil {
			res.recordEmbedError(a.ID, "embed", err)
			continue
		}
		if err := s.vec.Upsert(ctx, a.ID, er.Embedding, summaryText); err != nil {
			res.recordEmbedError(a.ID, "vector upsert", err)
			continue
		}
		// Record the model so meta.json agrees with the index.
		if err := s.updateMetaModels(a.ID, metaModelUpdate{EmbedModel: prof.Model}); err != nil {
			slog.Warn("embed: could not record embed_model", "slug", a.ID, "err", err)
		}
		res.Embedded++
		res.Tokens += er.Tokens
		done++
		slog.Debug("embed article", "slug", a.ID, "n", done, "of", len(pending),
			"tokens", er.Tokens, "total_tokens", res.Tokens,
			"total_usd", embedCostUSD(prof, res.Tokens))
		progress(done, len(pending))
	}
	res.USD = embedCostUSD(prof, res.Tokens)

	if res.Embedded > 0 {
		appendEvent(s.cfg.EventsPath, store.Event{
			TS:   time.Now().UTC(),
			Type: "embed",
			Cost: &store.CostRecord{
				Embed:    store.EmbedCostEntry{Model: prof.Model, Tokens: res.Tokens, USD: res.USD},
				TotalUSD: res.USD,
			},
		})
	}

	slog.Info("embed done", "embedded", res.Embedded, "failed", res.Failed,
		"tokens", res.Tokens, "usd", res.USD)
	return res, nil
}

// recordEmbedError counts a per-article failure and retains the first few.
func (r *EmbedResult) recordEmbedError(id, stage string, err error) {
	r.Failed++
	if len(r.Errors) < maxEmbedErrors {
		if err == nil {
			err = fmt.Errorf("empty summary")
		}
		r.Errors = append(r.Errors, fmt.Sprintf("%s: %s: %v", id, stage, err))
	}
	slog.Warn("embed failed", "slug", id, "stage", stage, "err", err)
}

// appendEvent appends a single event to the cost log. Best-effort: failures
// never abort the operation being logged.
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

// embedCostUSD prices tokens against the profile's input rate (USD per 1M).
// Profiles without pricing metadata yield 0.
func embedCostUSD(prof config.Profile, tokens int) float64 {
	if prof.Info.Pricing == nil {
		return 0
	}
	return float64(tokens) * prof.Info.Pricing.Input / 1_000_000
}

// Reprocess re-runs generation steps on existing articles.
// It respects the No* flags and optionally cleans or refetches before regenerating.
// After all articles are processed it rebuilds the SQLite index from the filesystem.
func (s *Service) Reprocess(ctx context.Context, req ReprocessRequest) (ReprocessResult, error) {
	var articles []store.Article
	if req.All {
		var err error
		articles, err = s.lib.List(ctx, store.Filter{})
		if err != nil {
			return ReprocessResult{}, fmt.Errorf("list articles: %w", err)
		}
	} else if req.Collection != "" {
		slugs, err := s.ListCollectionArticles(ctx, req.Collection)
		if err != nil {
			return ReprocessResult{}, fmt.Errorf("collection %q: %w", req.Collection, err)
		}
		for _, slug := range slugs {
			a, err := s.lib.Get(ctx, slug)
			if err != nil {
				continue
			}
			articles = append(articles, a)
		}
	} else {
		id, err := s.ResolveSlug(ctx, req.Slug)
		if err != nil {
			return ReprocessResult{}, err
		}
		a, err := s.lib.Get(ctx, id)
		if err != nil {
			return ReprocessResult{}, err
		}
		articles = []store.Article{a}
	}

	progress := req.Progress
	if progress == nil {
		progress = func(string) {}
	}

	var result ReprocessResult
	for i, a := range articles {
		prefix := fmt.Sprintf("[%d/%d] %s: ", i+1, len(articles), a.ID)
		articleReq := req
		articleReq.Progress = func(msg string) { progress(prefix + msg) }

		if req.Missing {
			needsSummary := !req.NoSummary && a.Files.Summary == ""
			needsFlash := !req.NoFlash && a.Files.Flash == ""
			needsFlashcards := (s.cfg.Ingest.Flashcards || req.Flashcards) && !req.NoFlashcards && a.Files.Flashcards == ""
			needsEmbed := !req.NoEmbed && a.EmbedModel == ""
			if !needsSummary && !needsFlash && !needsFlashcards && !needsEmbed {
				result.Skipped++
				continue
			}
		}

		cost, err := s.reprocessOne(ctx, a, articleReq)
		result.CostUSD += cost
		if err != nil {
			progress(prefix + "error: " + err.Error())
			result.Skipped++
			continue
		}
		result.Processed++
	}

	// Rebuild SQLite index from updated filesystem state.
	if _, err := s.lib.Reindex(ctx, nil); err != nil {
		return result, fmt.Errorf("reindex: %w", err)
	}

	return result, nil
}

// reprocessOne runs the reprocess pipeline for a single article.
func (s *Service) reprocessOne(ctx context.Context, a store.Article, req ReprocessRequest) (float64, error) {
	articleDir := filepath.Join(s.cfg.ArticlesRoot, a.ID)
	progress := req.Progress
	if progress == nil {
		progress = func(string) {}
	}

	// 1. Replace body from file/stdin.
	if req.BodyFile != "" {
		var data []byte
		var err error
		if req.BodyFile == "-" {
			data, err = io.ReadAll(os.Stdin)
		} else {
			data, err = os.ReadFile(req.BodyFile)
		}
		if err != nil {
			return 0, fmt.Errorf("read body file: %w", err)
		}
		if err := os.WriteFile(filepath.Join(articleDir, "body.txt"), data, 0644); err != nil {
			return 0, fmt.Errorf("write body.txt: %w", err)
		}
		progress("replaced body.txt")
	}

	// 2. Refetch from source URL or PDF.
	if req.Refetch {
		if a.URL != "" {
			progress("fetching " + a.URL)
			res, err := extractor.FromURLWithCookies(ctx, a.URL, s.cfg.CookieJars)
			if err != nil {
				return 0, fmt.Errorf("refetch: %w", err)
			}
			if err := os.WriteFile(filepath.Join(articleDir, "body.txt"), []byte(res.Text), 0644); err != nil {
				return 0, fmt.Errorf("write body.txt: %w", err)
			}
			progress("refetched from URL")
		} else if a.Files.SourcePDF != "" {
			progress("re-extracting PDF")
			res, err := extractor.FromPDF(ctx, a.Files.SourcePDF)
			if err != nil {
				return 0, fmt.Errorf("refetch pdf: %w", err)
			}
			if err := os.WriteFile(filepath.Join(articleDir, "body.txt"), []byte(res.Text), 0644); err != nil {
				return 0, fmt.Errorf("write body.txt: %w", err)
			}
			progress("re-extracted from PDF")
		} else {
			return 0, fmt.Errorf("no source URL or PDF — use --body to replace body.txt manually")
		}
	}

	// 3. Clean existing variant files and model state.
	if req.Clean {
		for _, f := range fs.ListSummaries(articleDir) {
			os.Remove(f)
		}
		for _, f := range fs.ListFlashes(articleDir) {
			os.Remove(f)
		}
		for _, f := range fs.ListFlashcards(articleDir) {
			os.Remove(f)
		}
		if s.vec != nil {
			s.vec.Delete(ctx, a.ID)
		}
		if err := s.clearMetaModels(a.ID); err != nil {
			return 0, fmt.Errorf("clear meta: %w", err)
		}
		progress("cleaned variants")
	}

	var totalCost float64
	var mu metaModelUpdate

	// 4. Summarize.
	if !req.NoSummary {
		progress("summarizing...")
		r, err := s.Summarize(ctx, SummarizeRequest{Slug: a.ID, Write: true})
		if err != nil {
			return totalCost, fmt.Errorf("summarize: %w", err)
		}
		totalCost += r.CostUSD
		mu.SummaryModel = r.Model
		mu.SummaryStyle = r.Style
		progress(fmt.Sprintf("summarized (%s, $%.4f)", r.Model, r.CostUSD))
	}

	// 5. Flash.
	if !req.NoFlash {
		progress("flash...")
		r, err := s.Flash(ctx, FlashRequest{Slug: a.ID, Write: true})
		if err != nil {
			return totalCost, fmt.Errorf("flash: %w", err)
		}
		totalCost += r.CostUSD
		mu.FlashModel = r.Model
		progress(fmt.Sprintf("flash (%s, $%.4f)", r.Model, r.CostUSD))
	}

	// 6. Flashcards.
	if (s.cfg.Ingest.Flashcards || req.Flashcards) && !req.NoFlashcards {
		progress("flashcards...")
		r, err := s.Flashcards(ctx, FlashcardsRequest{Slug: a.ID, Write: true})
		if err != nil {
			return totalCost, fmt.Errorf("flashcards: %w", err)
		}
		totalCost += r.CostUSD
		mu.FlashcardModel = r.Model
		mu.FlashcardStyle = r.Style
		progress(fmt.Sprintf("flashcards (%s, $%.4f)", r.Model, r.CostUSD))
	}

	// 7. Embed.
	if !req.NoEmbed && s.vec != nil && s.cfg.Ingest.EmbedProfile != "" {
		if prof, ok := s.cfg.Profiles[s.cfg.Ingest.EmbedProfile]; ok {
			// Re-get article to resolve newly written summary file.
			a2, _ := s.lib.Get(ctx, a.ID)
			if summaryText, err := s.lib.ReadSummary(a2); err == nil && strings.TrimSpace(summaryText) != "" {
				if client, err := embed.NewClient(prof.Model); err == nil {
					if er, err := client.Embed(ctx, summaryText); err == nil {
						if err := s.vec.Upsert(ctx, a.ID, er.Embedding, summaryText); err == nil {
							mu.EmbedModel = prof.Model
							cost := s.cfg.CalcCost(prof.Model, er.Tokens, 0)
							totalCost += cost
							progress(fmt.Sprintf("embedded (%s)", prof.Model))
						}
					}
				}
			}
		}
	}

	// 8. Persist updated model/style metadata.
	if err := s.updateMetaModels(a.ID, mu); err != nil {
		return totalCost, fmt.Errorf("update meta: %w", err)
	}

	return totalCost, nil
}

type metaModelUpdate struct {
	SummaryModel   string
	SummaryStyle   string
	FlashModel     string
	FlashcardModel string
	FlashcardStyle string
	EmbedModel     string
}

func (s *Service) clearMetaModels(id string) error {
	dir := filepath.Join(s.cfg.ArticlesRoot, id)
	metaPath := filepath.Join(dir, "meta.json")
	m, err := fs.ReadMeta(metaPath)
	if err != nil {
		return err
	}
	m.SummaryModel = ""
	m.SummaryStyle = ""
	m.FlashModel = ""
	m.FlashcardModel = ""
	m.FlashcardStyle = ""
	m.EmbedModel = ""
	return fs.WriteMeta(dir, m)
}

func (s *Service) updateMetaModels(id string, u metaModelUpdate) error {
	if u == (metaModelUpdate{}) {
		return nil // nothing to update
	}
	dir := filepath.Join(s.cfg.ArticlesRoot, id)
	metaPath := filepath.Join(dir, "meta.json")
	m, err := fs.ReadMeta(metaPath)
	if err != nil {
		return err
	}
	if u.SummaryModel != "" {
		m.SummaryModel = u.SummaryModel
	}
	if u.SummaryStyle != "" {
		m.SummaryStyle = u.SummaryStyle
	}
	if u.FlashModel != "" {
		m.FlashModel = u.FlashModel
	}
	if u.FlashcardModel != "" {
		m.FlashcardModel = u.FlashcardModel
	}
	if u.FlashcardStyle != "" {
		m.FlashcardStyle = u.FlashcardStyle
	}
	if u.EmbedModel != "" {
		m.EmbedModel = u.EmbedModel
	}
	return fs.WriteMeta(dir, m)
}

// Search runs a keyword (FTS5), semantic (vector), or combined search.
func (s *Service) Search(ctx context.Context, req SearchRequest) ([]SearchResult, error) {
	if strings.TrimSpace(req.Query) == "" {
		return nil, fmt.Errorf("search query cannot be empty")
	}

	limit := req.Limit
	if limit <= 0 {
		limit = 20
	}

	// Determine effective mode: fall back to keyword if no vector store available.
	mode := req.Mode
	if mode != store.QueryKeyword && s.vec == nil {
		mode = store.QueryKeyword
	}

	switch mode {
	case store.QuerySemantic:
		return s.searchSemantic(ctx, req.Query, limit)
	case store.QueryCombined:
		return s.searchCombined(ctx, req, limit)
	default:
		return s.searchKeyword(ctx, req, limit)
	}
}

func (s *Service) searchKeyword(ctx context.Context, req SearchRequest, limit int) ([]SearchResult, error) {
	q := store.Query{
		Text: req.Query,
		Mode: store.QueryKeyword,
		TopK: limit,
		Filter: store.Filter{
			Collection: req.Collection,
			Tags:       req.Tags,
			Slugs:      req.Slugs,
		},
	}
	hits, err := s.lib.Search(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("fts search: %w", err)
	}
	results := make([]SearchResult, 0, len(hits))
	for _, h := range hits {
		results = append(results, SearchResult{
			Article: h.Article,
			Score:   h.Score,
			Excerpt: h.Excerpt,
			Source:  "fts",
		})
	}
	return results, nil
}

func (s *Service) searchSemantic(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	embedding, err := s.embedQuery(ctx, query)
	if err != nil {
		return nil, err
	}
	hits, err := s.vec.Query(ctx, embedding, limit, 0.5)
	if err != nil {
		return nil, fmt.Errorf("vector search: %w", err)
	}
	results := make([]SearchResult, 0, len(hits))
	for _, h := range hits {
		a, err := s.lib.Get(ctx, h.ID)
		if err != nil {
			continue
		}
		results = append(results, SearchResult{
			Article: a,
			Score:   float64(h.Similarity),
			Source:  "vector",
		})
	}
	return results, nil
}

func (s *Service) searchCombined(ctx context.Context, req SearchRequest, limit int) ([]SearchResult, error) {
	// Run FTS and vector searches concurrently.
	type ftsOut struct {
		results []SearchResult
		err     error
	}
	type vecOut struct {
		results []SearchResult
		err     error
	}
	ftsCh := make(chan ftsOut, 1)
	vecCh := make(chan vecOut, 1)

	go func() {
		r, err := s.searchKeyword(ctx, req, limit)
		ftsCh <- ftsOut{r, err}
	}()
	go func() {
		r, err := s.searchSemantic(ctx, req.Query, limit)
		vecCh <- vecOut{r, err}
	}()

	ftsResult := <-ftsCh
	vecResult := <-vecCh

	// If both fail, return the FTS error.
	if ftsResult.err != nil && vecResult.err != nil {
		return nil, ftsResult.err
	}

	// Merge: normalize each set to [0,1] then sum.
	type merged struct {
		r     SearchResult
		score float64
	}
	byID := make(map[string]*merged)

	normalize := func(results []SearchResult) {
		if len(results) == 0 {
			return
		}
		// BM25 scores are negative (lower = better); invert and normalize.
		// Vector similarity is already [0,1].
		maxAbs := 0.0
		for _, r := range results {
			if abs := math.Abs(r.Score); abs > maxAbs {
				maxAbs = abs
			}
		}
		for _, r := range results {
			norm := 0.0
			if maxAbs > 0 {
				norm = 1.0 - math.Abs(r.Score)/maxAbs
			}
			if m, ok := byID[r.Article.ID]; ok {
				m.score += norm
				if m.r.Source != r.Source {
					m.r.Source = "both"
				}
				if m.r.Excerpt == "" {
					m.r.Excerpt = r.Excerpt
				}
			} else {
				rc := r
				byID[r.Article.ID] = &merged{r: rc, score: norm}
			}
		}
	}

	if ftsResult.err == nil {
		normalize(ftsResult.results)
	}
	if vecResult.err == nil {
		normalize(vecResult.results)
	}

	// Collect, sort by combined score descending, trim to limit.
	out := make([]SearchResult, 0, len(byID))
	for _, m := range byID {
		m.r.Score = m.score
		out = append(out, m.r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// embedQuery generates an embedding for a search query using the configured embed profile.
func (s *Service) embedQuery(ctx context.Context, query string) ([]float32, error) {
	if s.cfg.Ingest.EmbedProfile == "" {
		return nil, fmt.Errorf("embed_profile not configured")
	}
	prof, ok := s.cfg.Profiles[s.cfg.Ingest.EmbedProfile]
	if !ok {
		return nil, fmt.Errorf("embed profile %q not found", s.cfg.Ingest.EmbedProfile)
	}
	client, err := embed.NewClient(prof.Model)
	if err != nil {
		return nil, fmt.Errorf("embed client: %w", err)
	}
	result, err := client.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	return result.Embedding, nil
}

// List returns articles matching the filter.
func (s *Service) List(ctx context.Context, f store.Filter) ([]store.Article, error) {
	return s.lib.List(ctx, f)
}

// Read returns the text content of the requested part of an article.
func (s *Service) Read(ctx context.Context, req ReadRequest) (string, error) {
	// Apply overrides to config if specified
	cfg := s.cfg
	if req.Model != "" {
		cfg.PreferredModels = append([]string{req.Model}, cfg.PreferredModels...)
	}
	if req.Style != "" {
		cfg.PreferredStyles = append([]string{req.Style}, cfg.PreferredStyles...)
	}

	a, err := s.lib.Get(ctx, req.ID)
	if err != nil {
		return "", fmt.Errorf("get article %s: %w", req.ID, err)
	}

	switch req.Part {
	case PartBody:
		return s.lib.ReadBody(a)
	case PartSummary:
		return s.lib.ReadSummary(a)
	case PartFlash:
		return s.lib.ReadFlash(a)
	case PartFlashcards:
		data, err := s.lib.ReadFlashcards(a)
		if err != nil {
			return "", err
		}
		return string(data), nil
	default:
		return "", fmt.Errorf("unknown part: %d", req.Part)
	}
}

// ReadCollection concatenates the requested part across all articles in a collection.
// Only PartSummary and PartFlash are supported; others return an error.
func (s *Service) ReadCollection(ctx context.Context, slug string, req ReadRequest) (string, error) {
	if req.Part == PartBody || req.Part == PartFlashcards {
		return "", fmt.Errorf("--body and --flashcards are not supported for collections; use --summary or --flash")
	}

	articles, err := s.ListCollectionArticles(ctx, slug)
	if err != nil {
		return "", err
	}
	if len(articles) == 0 {
		return "", fmt.Errorf("collection %q has no articles", slug)
	}

	cfg := s.cfg
	if req.Model != "" {
		cfg.PreferredModels = append([]string{req.Model}, cfg.PreferredModels...)
	}
	if req.Style != "" {
		cfg.PreferredStyles = append([]string{req.Style}, cfg.PreferredStyles...)
	}

	var sb strings.Builder
	skipped := 0
	for _, articleSlug := range articles {
		a, err := s.lib.Get(ctx, articleSlug)
		if err != nil {
			skipped++
			continue
		}

		var text string
		switch req.Part {
		case PartSummary:
			text, err = s.lib.ReadSummary(a)
		case PartFlash:
			text, err = s.lib.ReadFlash(a)
		}
		if err != nil || strings.TrimSpace(text) == "" {
			skipped++
			continue
		}

		if sb.Len() > 0 {
			sb.WriteString("\n\n---\n\n")
		}
		sb.WriteString("## " + a.Title + "\n\n")
		sb.WriteString(strings.TrimSpace(text))
		sb.WriteString("\n")
	}

	if sb.Len() == 0 {
		return "", fmt.Errorf("no readable content in collection %q (skipped %d articles)", slug, skipped)
	}
	return sb.String(), nil
}

// GetArticle returns a single article by ID with Files populated from disk.
func (s *Service) GetArticle(ctx context.Context, id string) (store.Article, error) {
	return s.lib.Get(ctx, id)
}

// GetArticleByNumID returns an article by its numeric ID with files resolved.
func (s *Service) GetArticleByNumID(ctx context.Context, numID int) (store.Article, error) {
	return s.lib.GetByNumID(ctx, numID)
}

// IsCollectionNumID checks if a numeric ID belongs to a collection.
func (s *Service) IsCollectionNumID(ctx context.Context, numID int) (bool, error) {
	return s.lib.IsCollectionNumID(ctx, numID)
}

// ReadFlash reads the preferred flash summary for an article.
func (s *Service) ReadFlash(a store.Article) (string, error) {
	return s.lib.ReadFlash(a)
}

// ReadSummary reads the preferred summary for an article.
func (s *Service) ReadSummary(a store.Article) (string, error) {
	return s.lib.ReadSummary(a)
}

// ReadBody reads the body text for an article.
func (s *Service) ReadBody(a store.Article) (string, error) {
	return s.lib.ReadBody(a)
}

// DeleteArticle removes an article from the filesystem, SQLite, collection symlinks, and vector index.
func (s *Service) DeleteArticle(ctx context.Context, id string) error {
	a, err := s.lib.Get(ctx, id)
	if err != nil {
		return fmt.Errorf("get article: %w", err)
	}
	if s.vec != nil {
		_ = s.vec.Delete(ctx, id)
	}
	return s.lib.DeleteArticle(ctx, a)
}

// MarkRead records that an article was read.
func (s *Service) MarkRead(ctx context.Context, id string) error {
	return s.lib.MarkRead(ctx, id, time.Now())
}

// MarkUnread clears the read_at timestamp for an article.
func (s *Service) MarkUnread(ctx context.Context, id string) error {
	return s.lib.MarkUnread(ctx, id)
}

// MarkFavorite marks an article as a favorite.
func (s *Service) MarkFavorite(ctx context.Context, id string) error {
	return s.lib.MarkFavorite(ctx, id, time.Now())
}

// UnmarkFavorite removes the favorite mark from an article.
func (s *Service) UnmarkFavorite(ctx context.Context, id string) error {
	return s.lib.UnmarkFavorite(ctx, id)
}

// MarkPlayed records that an article was played via TTS.
func (s *Service) MarkPlayed(ctx context.Context, id string) error {
	return s.lib.MarkPlayed(ctx, id, time.Now())
}

// CreateCollection creates a new collection directory and registers it in SQLite.
func (s *Service) CreateCollection(ctx context.Context, slug, description string) error {
	if err := fs.CreateCollection(s.cfg.DataRoot, slug, description); err != nil {
		return fmt.Errorf("create collection: %w", err)
	}
	// Read back meta to get the allocated NumID
	m, err := fs.ReadCollectionMeta(s.cfg.DataRoot, slug)
	if err != nil {
		return fmt.Errorf("read collection meta after create: %w", err)
	}
	return s.lib.UpsertCollection(ctx, store.Collection{
		ID:          m.Slug,
		NumID:       m.NumID,
		Name:        m.Name,
		Description: m.Description,
		CreatedAt:   m.CreatedAt,
	})
}

// ListCollections returns all collections with article counts.
func (s *Service) ListCollections(ctx context.Context) ([]CollectionInfo, error) {
	metas, err := fs.ListCollections(s.cfg.DataRoot)
	if err != nil {
		return nil, fmt.Errorf("list collections: %w", err)
	}
	// Get article counts from SQLite
	counts, err := s.lib.CollectionCounts(ctx)
	if err != nil {
		counts = map[string]int{} // non-fatal
	}
	out := make([]CollectionInfo, 0, len(metas))
	for _, m := range metas {
		colDir := fs.CollectionDir(s.cfg.DataRoot, m.Slug)
		hasSummary := false
		entries, _ := os.ReadDir(colDir)
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "meta-summary.") {
				hasSummary = true
				break
			}
		}
		_, hasSystemErr := os.Stat(filepath.Join(colDir, "system.txt"))
		out = append(out, CollectionInfo{
			Slug:         m.Slug,
			NumID:        m.NumID,
			Name:         m.Name,
			Description:  m.Description,
			CreatedAt:    m.CreatedAt,
			ArticleCount: counts[m.Slug],
			HasSummary:   hasSummary,
			HasSystem:    hasSystemErr == nil,
		})
	}
	return out, nil
}

// GetCollection returns info for a single collection.
func (s *Service) GetCollection(ctx context.Context, slug string) (CollectionInfo, error) {
	m, err := fs.ReadCollectionMeta(s.cfg.DataRoot, slug)
	if err != nil {
		return CollectionInfo{}, err
	}
	counts, _ := s.lib.CollectionCounts(ctx)
	colDir := fs.CollectionDir(s.cfg.DataRoot, slug)
	hasSummary := false
	entries, _ := os.ReadDir(colDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "meta-summary.") {
			hasSummary = true
			break
		}
	}
	_, hasSystemErr := os.Stat(filepath.Join(colDir, "system.txt"))
	return CollectionInfo{
		Slug:         m.Slug,
		Name:         m.Name,
		Description:  m.Description,
		CreatedAt:    m.CreatedAt,
		ArticleCount: counts[slug],
		HasSummary:   hasSummary,
		HasSystem:    hasSystemErr == nil,
	}, nil
}

// AddToCollection links an article to a collection via symlink and updates SQLite.
func (s *Service) AddToCollection(ctx context.Context, articleSlug, collectionSlug string) error {
	// Verify article exists
	articleDir := filepath.Join(s.cfg.ArticlesRoot, articleSlug)
	if _, err := os.Stat(articleDir); os.IsNotExist(err) {
		return fmt.Errorf("article %q not found", articleSlug)
	}
	// Verify collection exists
	if _, err := fs.ReadCollectionMeta(s.cfg.DataRoot, collectionSlug); err != nil {
		return fmt.Errorf("collection %q not found — create it first with: arc collections create %s", collectionSlug, collectionSlug)
	}
	if err := fs.AddArticleToCollection(s.cfg.DataRoot, s.cfg.ArticlesRoot, articleSlug, collectionSlug); err != nil {
		return err // includes ErrAlreadyInCollection
	}
	return s.lib.AddArticleToCollection(ctx, articleSlug, collectionSlug)
}

// RemoveFromCollection removes an article's symlink from a collection and updates SQLite.
func (s *Service) RemoveFromCollection(ctx context.Context, articleSlug, collectionSlug string) error {
	if err := fs.RemoveArticleFromCollection(s.cfg.DataRoot, collectionSlug, articleSlug); err != nil {
		return err
	}
	return s.lib.RemoveArticleFromCollection(ctx, articleSlug, collectionSlug)
}

// SetCollectionDescription updates the description in the collection's meta.json.
func (s *Service) SetCollectionDescription(ctx context.Context, slug, text string) error {
	m, err := fs.ReadCollectionMeta(s.cfg.DataRoot, slug)
	if err != nil {
		return fmt.Errorf("collection %q not found", slug)
	}
	m.Description = text
	return fs.WriteCollectionMeta(s.cfg.DataRoot, m)
}

// ListCollectionArticles returns article slugs linked in a collection.
func (s *Service) ListCollectionArticles(ctx context.Context, slug string) ([]string, error) {
	if _, err := fs.ReadCollectionMeta(s.cfg.DataRoot, slug); err != nil {
		return nil, fmt.Errorf("collection %q not found", slug)
	}
	articles, broken, err := fs.ListCollectionArticles(s.cfg.DataRoot, slug)
	if err != nil {
		return nil, err
	}
	for _, b := range broken {
		slog.Warn("broken collection symlink", "collection", slug, "article", b)
	}
	return articles, nil
}

// RenameCollection renames a collection slug on disk and in SQLite.
func (s *Service) RenameCollection(ctx context.Context, oldSlug, newSlug string) error {
	if err := fs.RenameCollection(s.cfg.DataRoot, oldSlug, newSlug); err != nil {
		return err
	}
	return s.lib.RenameCollection(ctx, oldSlug, newSlug)
}

// DeleteCollection removes a collection and optionally purges exclusively-owned articles.
// If purge is true, articles that belong only to this collection are also deleted from disk.
func (s *Service) DeleteCollection(ctx context.Context, slug string, purge bool) (purged []string, err error) {
	if _, err := fs.ReadCollectionMeta(s.cfg.DataRoot, slug); err != nil {
		return nil, fmt.Errorf("collection %q not found", slug)
	}

	if purge {
		// Find articles exclusively in this collection.
		membership, err := fs.ScanCollectionMembership(s.cfg.DataRoot)
		if err != nil {
			return nil, fmt.Errorf("scan membership: %w", err)
		}
		articles, _, err := fs.ListCollectionArticles(s.cfg.DataRoot, slug)
		if err != nil {
			return nil, fmt.Errorf("list collection articles: %w", err)
		}
		for _, articleSlug := range articles {
			cols := membership[articleSlug]
			if len(cols) == 1 && cols[0] == slug {
				articleDir := filepath.Join(s.cfg.ArticlesRoot, articleSlug)
				if err := os.RemoveAll(articleDir); err != nil {
					return purged, fmt.Errorf("delete article %s: %w", articleSlug, err)
				}
				purged = append(purged, articleSlug)
			}
		}
	}

	if err := fs.DeleteCollection(s.cfg.DataRoot, slug); err != nil {
		return purged, err
	}
	return purged, s.lib.DeleteCollection(ctx, slug)
}

// ResolveCollectionSlug resolves a user-supplied query to a collection slug.
// Tries exact match first, then substring match on slug.
func (s *Service) ResolveCollectionSlug(ctx context.Context, query string) (string, error) {
	cols, err := fs.ListCollections(s.cfg.DataRoot)
	if err != nil {
		return "", fmt.Errorf("list collections: %w", err)
	}

	// Exact match first.
	for _, c := range cols {
		if c.Slug == query {
			return c.Slug, nil
		}
	}

	// Substring match.
	q := strings.ToLower(query)
	var matches []string
	for _, c := range cols {
		if strings.Contains(strings.ToLower(c.Slug), q) {
			matches = append(matches, c.Slug)
		}
	}

	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no collection matching %q", query)
	case 1:
		return matches[0], nil
	default:
		msg := fmt.Sprintf("%q matches multiple collections — be more specific:\n", query)
		for _, m := range matches {
			msg += fmt.Sprintf("  %s\n", m)
		}
		return "", fmt.Errorf("%s", strings.TrimRight(msg, "\n"))
	}
}

// SearchCollections searches collections by name or description using FTS5.
func (s *Service) SearchCollections(ctx context.Context, query string) ([]CollectionInfo, error) {
	cols, err := s.lib.SearchCollections(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("search collections: %w", err)
	}
	counts, _ := s.lib.CollectionCounts(ctx)
	out := make([]CollectionInfo, 0, len(cols))
	for _, c := range cols {
		numID := 0
		if m, err := fs.ReadCollectionMeta(s.cfg.DataRoot, c.ID); err == nil {
			numID = m.NumID
		}
		out = append(out, CollectionInfo{
			Slug:         c.ID,
			NumID:        numID,
			Name:         c.Name,
			Description:  c.Description,
			CreatedAt:    c.CreatedAt,
			ArticleCount: counts[c.ID],
		})
	}
	return out, nil
}

// SuggestCollections calls the LLM to propose a set of collections for the whole library.
func (s *Service) SuggestCollections(ctx context.Context, profile string, count, minArticles int, progress func(string)) ([]CollectionSuggestion, error) {
	articles, err := s.lib.List(ctx, store.Filter{Uncollected: true})
	if err != nil {
		return nil, fmt.Errorf("list uncollected articles: %w", err)
	}

	titles := make([]string, 0, len(articles))
	for _, a := range articles {
		titles = append(titles, a.Title)
	}

	existing, err := s.ListCollections(ctx)
	if err != nil {
		existing = nil // non-fatal
	}
	pipeExisting := make([]pipeline.CollectionSuggestCollection, 0, len(existing))
	for _, c := range existing {
		pipeExisting = append(pipeExisting, pipeline.CollectionSuggestCollection{
			Slug:        c.Slug,
			Description: c.Description,
		})
	}

	results, err := pipeline.CollectionSuggest(ctx, s.cfg, pipeline.CollectionSuggestRequest{
		Titles:      titles,
		Existing:    pipeExisting,
		Profile:     profile,
		Count:       count,
		MinArticles: minArticles,
		Progress:    progress,
	})
	if err != nil {
		return nil, err
	}

	out := make([]CollectionSuggestion, 0, len(results))
	for _, r := range results {
		out = append(out, CollectionSuggestion{
			Slug:           r.Slug,
			Description:    r.Description,
			EstimatedCount: r.EstimatedCount,
		})
	}
	return out, nil
}

// AssignCollections assigns uncollected articles to existing collections using
// the LLM in batches of 50. Returns a list of {slug, collection} assignments.
//
// A non-empty target restricts the run to that one collection: the LLM sees only
// it, and the candidate pool becomes every article not already a member — not
// just the uncollected ones, since an article can belong to several collections
// and a fresh collection usually draws from ones that already exist. Pass fresh
// alongside target to narrow the pool back to articles in no collection at all.
func (s *Service) AssignCollections(ctx context.Context, profile string, limit int, fresh bool, target string, progress func(string)) ([]CollectionAssignment, error) {
	f := store.Filter{Uncollected: true}
	switch {
	case fresh:
		f = store.Filter{UncollectedFresh: true}
	case target != "":
		f = store.Filter{}
	}
	articles, err := s.lib.List(ctx, f)
	if err != nil {
		return nil, fmt.Errorf("list candidate articles: %w", err)
	}

	if target != "" {
		members, _, err := fs.ListCollectionArticles(s.cfg.DataRoot, target)
		if err != nil {
			return nil, fmt.Errorf("list articles in %s: %w", target, err)
		}
		inTarget := make(map[string]bool, len(members))
		for _, m := range members {
			inTarget[m] = true
		}
		kept := articles[:0]
		for _, a := range articles {
			if !inTarget[a.ID] {
				kept = append(kept, a)
			}
		}
		articles = kept
	}

	if limit > 0 && len(articles) > limit {
		articles = articles[:limit]
	}

	pipeArticles := make([]pipeline.CollectionAssignArticle, 0, len(articles))
	for _, a := range articles {
		pipeArticles = append(pipeArticles, pipeline.CollectionAssignArticle{
			Slug:  a.ID,
			Title: a.Title,
		})
	}

	cols, err := s.ListCollections(ctx)
	if err != nil {
		return nil, fmt.Errorf("list collections: %w", err)
	}
	pipeCols := make([]pipeline.CollectionSuggestCollection, 0, len(cols))
	for _, c := range cols {
		if target != "" && c.Slug != target {
			continue
		}
		pipeCols = append(pipeCols, pipeline.CollectionSuggestCollection{
			Slug:        c.Slug,
			Description: c.Description,
		})
	}
	if target != "" && len(pipeCols) == 0 {
		return nil, fmt.Errorf("collection %q not found", target)
	}

	results, err := pipeline.CollectionAssign(ctx, s.cfg, pipeline.CollectionAssignRequest{
		Articles:    pipeArticles,
		Collections: pipeCols,
		Profile:     profile,
		Progress:    progress,
	})
	if err != nil {
		return nil, err
	}

	out := make([]CollectionAssignment, 0, len(results))
	for _, r := range results {
		// In targeted mode the prompt's "uncollected" fallback is the reject
		// path: anything not placed in the target simply doesn't belong.
		if target != "" && r.Collection != target {
			continue
		}
		out = append(out, CollectionAssignment{
			ArticleSlug:    r.Slug,
			CollectionSlug: r.Collection,
		})
	}
	return out, nil
}

// SuggestCollectionsForArticle calls the LLM to suggest which existing collections
// the given article fits.
func (s *Service) SuggestCollectionsForArticle(ctx context.Context, articleSlug, profile string, progress func(string)) ([]CollectionMatch, error) {
	a, err := s.lib.Get(ctx, articleSlug)
	if err != nil {
		return nil, fmt.Errorf("get article %s: %w", articleSlug, err)
	}

	// Try to read flash summary for richer context; non-fatal if missing.
	flashText := ""
	if flashData, err := s.lib.ReadFlash(a); err == nil {
		flashText = flashData
	}

	cols, err := s.ListCollections(ctx)
	if err != nil {
		return nil, fmt.Errorf("list collections: %w", err)
	}
	if len(cols) == 0 {
		return nil, nil
	}

	pipeCols := make([]pipeline.CollectionSuggestCollection, 0, len(cols))
	for _, c := range cols {
		pipeCols = append(pipeCols, pipeline.CollectionSuggestCollection{
			Slug:        c.Slug,
			Description: c.Description,
		})
	}

	results, err := pipeline.CollectionArticleSuggest(ctx, s.cfg, pipeline.CollectionArticleSuggestRequest{
		ArticleSlug:  a.ID,
		ArticleTitle: a.Title,
		ArticleFlash: flashText,
		Collections:  pipeCols,
		Profile:      profile,
		Progress:     progress,
	})
	if err != nil {
		return nil, err
	}

	out := make([]CollectionMatch, 0, len(results))
	for _, r := range results {
		out = append(out, CollectionMatch{
			Slug:           r.Slug,
			Reason:         r.Reason,
			NewSlug:        r.NewSlug,
			NewDescription: r.NewDescription,
		})
	}
	return out, nil
}

// GenerateCollectionDescription calls the LLM to generate a one-sentence description
// for a collection based on its slug and member article titles.
func (s *Service) GenerateCollectionDescription(ctx context.Context, slug, profile string, progress func(string)) (string, error) {
	// Get member article titles
	articleSlugs, _, err := fs.ListCollectionArticles(s.cfg.DataRoot, slug)
	if err != nil {
		return "", fmt.Errorf("list collection articles: %w", err)
	}
	if len(articleSlugs) == 0 {
		return "", fmt.Errorf("collection %q has no articles", slug)
	}

	titles := make([]string, 0, len(articleSlugs))
	for _, as := range articleSlugs {
		a, err := s.lib.Get(ctx, as)
		if err != nil {
			continue // skip broken links
		}
		titles = append(titles, a.Title)
	}

	return pipeline.CollectionDescribe(ctx, s.cfg, pipeline.CollectionDescribeRequest{
		Slug:     slug,
		Titles:   titles,
		Profile:  profile,
		Progress: progress,
	})
}

// Relate creates a relation between two articles.
func (s *Service) Relate(ctx context.Context, fromID, toID string, t store.RelationType) error {
	return s.lib.Relate(ctx, fromID, toID, t)
}

// Stats returns a snapshot of the knowledge base.
func (s *Service) Stats(ctx context.Context) (Stats, error) {
	articles, err := s.lib.List(ctx, store.Filter{})
	if err != nil {
		return Stats{}, fmt.Errorf("list articles: %w", err)
	}

	cols, err := fs.ListCollections(s.cfg.DataRoot)
	if err != nil {
		return Stats{}, fmt.Errorf("read collections: %w", err)
	}

	// Collect unique tags, unread/unplayed counts, and article breakdowns
	tagSet := make(map[string]struct{})
	byModel := make(map[string]int)
	byStyle := make(map[string]int)
	byCollection := make(map[string]int)
	embedByCollection := make(map[string]int)
	var unread, unplayed, embedCoverage int
	for _, a := range articles {
		for _, t := range a.Tags {
			tagSet[t.Value] = struct{}{}
		}
		if a.ReadAt == nil {
			unread++
		}
		if a.PlayedAt == nil {
			unplayed++
		}
		if a.SummaryModel != "" {
			byModel[a.SummaryModel]++
		}
		if a.SummaryStyle != "" {
			byStyle[a.SummaryStyle]++
		}
		if a.EmbedModel != "" {
			embedCoverage++
		}
		if len(a.Collections) == 0 {
			byCollection["(uncollected)"]++
			if a.EmbedModel != "" {
				embedByCollection["(uncollected)"]++
			}
		} else {
			for _, c := range a.Collections {
				byCollection[c]++
				if a.EmbedModel != "" {
					embedByCollection[c]++
				}
			}
		}
	}

	// Aggregated stats from events.jsonl
	ea := s.aggregateEventStats()

	// Efficiency averages
	var avgIngest, avgChat, avgAskX float64
	if n := ea.RequestsByType["ingest"]; n > 0 {
		avgIngest = ea.CostByType["ingest"] / float64(n)
	}
	if n := ea.RequestsByType["chat"]; n > 0 {
		avgChat = ea.CostByType["chat"] / float64(n)
	}
	if n := ea.RequestsByType["askx_call"]; n > 0 {
		avgAskX = ea.CostByType["askx_call"] / float64(n)
	}

	return Stats{
		TotalArticles:        len(articles),
		TotalCollections:     len(cols),
		TotalTags:            len(tagSet),
		Unread:               unread,
		Unplayed:             unplayed,
		EmbedCoverage:        embedCoverage,
		CostToday:            ea.CostToday,
		CostThisWeek:         ea.CostThisWeek,
		CostThisMonth:        ea.CostThisMonth,
		CostTotal:            ea.CostTotal,
		CostByModel:          ea.CostByModel,
		ArticlesByModel:      byModel,
		ArticlesByStyle:      byStyle,
		ArticlesByCollection: byCollection,
		EmbedByCollection:    embedByCollection,
		TotalInputTokens:     ea.TotalInputTokens,
		TotalOutputTokens:    ea.TotalOutputTokens,
		TokensByModel:        ea.TokensByModel,
		TotalRequests:        ea.TotalRequests,
		RequestsByModel:      ea.RequestsByModel,
		RequestsByType:       ea.RequestsByType,
		CostByType:           ea.CostByType,
		AvgCostPerIngest:     avgIngest,
		AvgCostPerChatTurn:   avgChat,
		AvgCostPerAskX:       avgAskX,
	}, nil
}

// ResolveSlug resolves a user-supplied query to an article slug.
// Tries exact match first, then case-insensitive substring match on slug and title.
// Returns an error listing candidates if more than one article matches.
func (s *Service) ResolveSlug(ctx context.Context, query string) (string, error) {
	// Exact match
	if _, err := s.lib.Get(ctx, query); err == nil {
		return query, nil
	}

	articles, err := s.lib.List(ctx, store.Filter{})
	if err != nil {
		return "", fmt.Errorf("list articles: %w", err)
	}

	q := strings.ToLower(query)
	var matches []store.Article
	for _, a := range articles {
		if strings.Contains(strings.ToLower(a.ID), q) ||
			strings.Contains(strings.ToLower(a.Title), q) {
			matches = append(matches, a)
		}
	}

	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no article matching %q", query)
	case 1:
		return matches[0].ID, nil
	default:
		msg := fmt.Sprintf("%q matches multiple articles — be more specific:\n", query)
		for _, a := range matches {
			msg += fmt.Sprintf("  %s  %s\n", a.ID, a.Title)
		}
		return "", fmt.Errorf("%s", strings.TrimRight(msg, "\n"))
	}
}

// Summarize runs the summarize step on an existing article (by slug) or raw text.
// If req.Write is true and a slug is provided, the summary is written as a new
// variant file alongside existing files in the article directory.
func (s *Service) Summarize(ctx context.Context, req SummarizeRequest) (SummarizeResult, error) {
	text := req.Text
	var articleDir string

	if req.Slug != "" {
		a, err := s.lib.Get(ctx, req.Slug)
		if err != nil {
			return SummarizeResult{}, fmt.Errorf("get article %s: %w", req.Slug, err)
		}
		body, err := s.lib.ReadBody(a)
		if err != nil {
			return SummarizeResult{}, fmt.Errorf("read body: %w", err)
		}
		text = body
		articleDir = filepath.Join(s.cfg.ArticlesRoot, req.Slug)
	}

	if strings.TrimSpace(text) == "" {
		return SummarizeResult{}, fmt.Errorf("no text to summarize")
	}

	pr, err := pipeline.Summarize(ctx, s.cfg, pipeline.SummarizeRequest{
		Text:     text,
		Style:    req.Style,
		Profile:  req.Profile,
		Progress: req.Progress,
	})
	if err != nil {
		return SummarizeResult{}, err
	}

	result := SummarizeResult{
		Text:    pr.Text,
		Model:   pr.Model,
		Style:   pr.Style,
		CostUSD: s.cfg.CalcCost(pr.Model, pr.Usage.InputTokens, pr.Usage.OutputTokens),
	}

	if req.Write && articleDir != "" {
		fname := fmt.Sprintf("summary.%s.%s.txt", pr.Style, pr.Model)
		fpath := filepath.Join(articleDir, fname)
		if err := os.WriteFile(fpath, []byte(pr.Text), 0644); err != nil {
			return result, fmt.Errorf("write summary file: %w", err)
		}
		result.Written = true
		result.WritePath = fpath
	}

	return result, nil
}

// Flash runs the flash generation step on an existing article (by slug) or raw text.
func (s *Service) Flash(ctx context.Context, req FlashRequest) (FlashResult, error) {
	text := req.Text
	var articleDir string

	if req.Slug != "" {
		a, err := s.lib.Get(ctx, req.Slug)
		if err != nil {
			return FlashResult{}, fmt.Errorf("get article %s: %w", req.Slug, err)
		}
		if req.FromBody {
			text, err = s.lib.ReadBody(a)
		} else {
			text, err = s.lib.ReadSummary(a)
		}
		if err != nil {
			return FlashResult{}, fmt.Errorf("read article: %w", err)
		}
		articleDir = filepath.Join(s.cfg.ArticlesRoot, req.Slug)
	}

	if strings.TrimSpace(text) == "" {
		return FlashResult{}, fmt.Errorf("no text to flash")
	}

	pr, err := pipeline.Flash(ctx, s.cfg, pipeline.FlashRequest{
		Text:     text,
		Profile:  req.Profile,
		Progress: req.Progress,
	})
	if err != nil {
		return FlashResult{}, err
	}

	result := FlashResult{
		Text:    pr.Text,
		Model:   pr.Model,
		CostUSD: s.cfg.CalcCost(pr.Model, pr.Usage.InputTokens, pr.Usage.OutputTokens),
	}

	if req.Write && articleDir != "" {
		fname := fmt.Sprintf("flash.%s.txt", pr.Model)
		fpath := filepath.Join(articleDir, fname)
		if err := os.WriteFile(fpath, []byte(pr.Text), 0644); err != nil {
			return result, fmt.Errorf("write flash file: %w", err)
		}
		result.Written = true
		result.WritePath = fpath
	}

	return result, nil
}

// Flashcards runs the flashcard generation step on an existing article or raw text.
func (s *Service) Flashcards(ctx context.Context, req FlashcardsRequest) (FlashcardsResult, error) {
	text := req.Text
	var articleDir string
	// sourceWords sizes the deck. Prefer the body: summaries are compressed and
	// flatten length differences between a 1400-word and a 5000-word article.
	sourceWords := len(strings.Fields(req.Text))

	if req.Slug != "" {
		a, err := s.lib.Get(ctx, req.Slug)
		if err != nil {
			return FlashcardsResult{}, fmt.Errorf("get article %s: %w", req.Slug, err)
		}
		if req.FromBody {
			text, err = s.lib.ReadBody(a)
		} else {
			text, err = s.lib.ReadSummary(a)
		}
		if err != nil {
			return FlashcardsResult{}, fmt.Errorf("read article: %w", err)
		}
		articleDir = filepath.Join(s.cfg.ArticlesRoot, req.Slug)

		sourceWords = len(strings.Fields(text))
		if !req.FromBody {
			// text is the summary; re-read the body purely to size the deck.
			if body, err := s.lib.ReadBody(a); err == nil {
				sourceWords = len(strings.Fields(body))
			}
		}
	}

	if strings.TrimSpace(text) == "" {
		return FlashcardsResult{}, fmt.Errorf("no text to generate flashcards from")
	}

	count := req.Count
	if count <= 0 {
		count = s.cfg.CardCountForWords(sourceWords)
	}

	pr, err := pipeline.Flashcards(ctx, s.cfg, pipeline.FlashcardsRequest{
		Text:     text,
		Style:    req.Style,
		Profile:  req.Profile,
		Count:    count,
		Progress: req.Progress,
	})
	if err != nil {
		return FlashcardsResult{}, err
	}

	result := FlashcardsResult{
		JSON:    pr.JSON,
		Style:   pr.Style,
		Model:   pr.Model,
		Count:   pr.Count,
		CostUSD: s.cfg.CalcCost(pr.Model, pr.Usage.InputTokens, pr.Usage.OutputTokens),
	}

	if req.Write && articleDir != "" {
		fname := fmt.Sprintf("flashcards.%s.%s.json", pr.Style, pr.Model)
		fpath := filepath.Join(articleDir, fname)
		if err := os.WriteFile(fpath, pr.JSON, 0644); err != nil {
			return result, fmt.Errorf("write flashcards file: %w", err)
		}
		result.Written = true
		result.WritePath = fpath

		// Record the variant in meta.json so `arc list` and the SQLite index
		// know the article has cards. Without this the file exists but nothing
		// outside the article directory reports it.
		if err := s.updateMetaModels(req.Slug, metaModelUpdate{
			FlashcardModel: pr.Model,
			FlashcardStyle: pr.Style,
		}); err != nil {
			slog.Warn("flashcards written but meta.json not updated", "slug", req.Slug, "err", err)
		}
	}

	return result, nil
}

// DeleteFlashcards removes flashcard decks from an article.
//
// With no Style or Model filter it removes every deck — "I don't want cards for
// this article" and "clear this before regenerating" are the cases that matter.
// Filters narrow it to a single variant.
func (s *Service) DeleteFlashcards(ctx context.Context, req DeleteFlashcardsRequest) (DeleteFlashcardsResult, error) {
	if req.Slug == "" {
		return DeleteFlashcardsResult{}, fmt.Errorf("no article specified")
	}

	a, err := s.lib.Get(ctx, req.Slug)
	if err != nil {
		return DeleteFlashcardsResult{}, fmt.Errorf("get article %s: %w", req.Slug, err)
	}
	articleDir := filepath.Join(s.cfg.ArticlesRoot, req.Slug)

	all := fs.ListFlashcards(articleDir)
	if len(all) == 0 {
		return DeleteFlashcardsResult{}, fmt.Errorf("article %s has no flashcards", req.Slug)
	}

	var matched []string
	for _, path := range all {
		style, model, ok := fs.ParseFlashcardsName(path)
		if !ok {
			continue
		}
		if req.Style != "" && req.Style != style {
			continue
		}
		if req.Model != "" && req.Model != model {
			continue
		}
		matched = append(matched, path)
	}
	if len(matched) == 0 {
		return DeleteFlashcardsResult{}, fmt.Errorf("no flashcards match style=%q model=%q", req.Style, req.Model)
	}

	result := DeleteFlashcardsResult{
		Deleted:   matched,
		Remaining: len(all) - len(matched),
		Cards:     countCardsIn(matched),
	}
	if req.DryRun {
		return result, nil
	}

	for _, path := range matched {
		if err := os.Remove(path); err != nil {
			return result, fmt.Errorf("remove %s: %w", filepath.Base(path), err)
		}
		slog.Info("deleted flashcards", "slug", req.Slug, "file", filepath.Base(path))
	}

	// With no decks left, meta.json must stop advertising cards or `arc list`
	// reports a variant that is gone. updateMetaModels only sets non-empty
	// fields, so clearing needs a direct read-modify-write.
	if result.Remaining == 0 {
		metaPath := filepath.Join(articleDir, "meta.json")
		if m, err := fs.ReadMeta(metaPath); err == nil {
			m.FlashcardModel = ""
			m.FlashcardStyle = ""
			if err := fs.WriteMeta(articleDir, m); err != nil {
				slog.Warn("flashcards deleted but meta.json not updated", "slug", req.Slug, "err", err)
			}
		}
	}

	// Re-index so the deleted questions leave the FTS index rather than
	// matching searches until the next full reindex.
	if err := s.lib.IndexSlugs(ctx, []string{a.ID}); err != nil {
		slog.Warn("flashcards deleted but article not re-indexed", "slug", req.Slug, "err", err)
	}

	return result, nil
}

// countCardsIn totals the cards across a set of flashcard files, for reporting.
// Unreadable files count as zero rather than failing the delete.
func countCardsIn(paths []string) int {
	total := 0
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if cards, err := flashcards.Parse("", data); err == nil {
			total += len(cards)
		}
	}
	return total
}

// BatchIngest ingests multiple URLs or files listed one per line in a file or stdin.
// Blank lines and lines starting with '#' are ignored.
// Errors are logged via Progress and counted; they do not abort the batch.
func (s *Service) BatchIngest(ctx context.Context, req BatchIngestRequest) (BatchIngestResult, error) {
	var data []byte
	var err error
	if req.File == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(req.File)
	}
	if err != nil {
		return BatchIngestResult{}, fmt.Errorf("read input: %w", err)
	}

	// Parse lines.
	var inputs []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		inputs = append(inputs, line)
	}
	if len(inputs) == 0 {
		return BatchIngestResult{}, fmt.Errorf("no URLs or files found in input")
	}

	progress := req.Progress
	if progress == nil {
		progress = func(string) {}
	}

	var result BatchIngestResult
	total := len(inputs)

	for i, input := range inputs {
		prefix := fmt.Sprintf("[%d/%d] %s: ", i+1, total, input)

		ir := IngestRequest{
			Collection:       req.Collection,
			SummaryStyle:     req.SummaryStyle,
			SummaryProfile:   req.SummaryProfile,
			FlashProfile:     req.FlashProfile,
			FlashcardProfile: req.FlashcardProfile,
			Flashcards:       req.Flashcards,
			NoFlashcards:     req.NoFlashcards,
			NoEmbed:          req.NoEmbed,
			DryRun:           req.DryRun,
			Force:            req.Force,
			Progress: func(msg string) {
				progress(prefix + msg)
			},
		}

		if strings.HasPrefix(input, "http://") || strings.HasPrefix(input, "https://") {
			ir.URL = input
		} else {
			ir.File = input
		}

		res, err := s.Ingest(ctx, ir)
		if err != nil {
			// Duplicate — count as skipped, not error.
			if strings.Contains(err.Error(), "already ingested") {
				progress(prefix + "skipped: " + err.Error())
				result.Skipped++
				continue
			}
			slog.Error("batch ingest item failed", "input", input, "index", i+1, "total", len(inputs), "err", err)
			progress(prefix + "error: " + err.Error())
			result.Errors++
			continue
		}

		if !req.DryRun {
			result.Slugs = append(result.Slugs, res.Slug)
			result.CostUSD += res.Cost.TotalUSD
			if res.Teaser {
				result.Teasers++
			}
		}
		result.Ingested++
	}

	return result, nil
}

// Ingest ingests a single article from a URL or file using the native Go pipeline.
func (s *Service) Ingest(ctx context.Context, req IngestRequest) (IngestResult, error) {
	if req.URL == "" && req.File == "" {
		return IngestResult{}, fmt.Errorf("ingest: URL or File must be specified")
	}

	// URL deduplication: check if this URL was already ingested.
	if req.URL != "" && !req.Force && !req.DryRun {
		articles, err := s.lib.List(ctx, store.Filter{})
		if err == nil {
			for _, a := range articles {
				if a.URL == req.URL {
					return IngestResult{}, fmt.Errorf("already ingested as %q — use --force to ingest again", a.ID)
				}
			}
		}
	}

	result, err := pipeline.Run(ctx, s.cfg, pipeline.Request{
		URL:            req.URL,
		File:           req.File,
		Title:          req.Title,
		Collection:     req.Collection,
		SummaryStyle:   req.SummaryStyle,
		SummaryModel:   req.SummaryProfile,
		FlashModel:     req.FlashProfile,
		FlashcardModel: req.FlashcardProfile,
		FlashcardStyle: req.FlashcardStyle,
		Flashcards:     req.Flashcards,
		NoFlashcards:   req.NoFlashcards,
		NoEmbed:        req.NoEmbed,
		VectorStore:    s.vec,
		DryRun:         req.DryRun,
		Progress:       req.Progress,
		OnCostEstimate: req.OnCostEstimate,
	})
	if err != nil {
		source := req.URL
		if source == "" {
			source = req.File
		}
		slog.Error("ingest pipeline failed", "source", source, "err", err)
		return IngestResult{}, fmt.Errorf("ingest pipeline: %w", err)
	}

	if req.DryRun {
		return IngestResult{DryRun: true, ExtractionStats: result.ExtractionStats}, nil
	}

	// Index the new article into SQLite
	if _, err := s.lib.Reindex(ctx, nil); err != nil {
		slog.Error("reindex after ingest failed", "err", err)
		return IngestResult{}, fmt.Errorf("reindex after ingest: %w", err)
	}

	return IngestResult{
		ArticleID: result.Slug,
		Slug:      result.Slug,
		Cost:      result.Cost,
		Teaser:    result.Teaser,
	}, nil
}

// eventAggregates holds all aggregated stats from events.jsonl.
type eventAggregates struct {
	CostToday, CostThisWeek, CostThisMonth, CostTotal float64
	CostByModel                                        map[string]float64
	CostByType                                         map[string]float64
	TotalInputTokens, TotalOutputTokens                int
	TokensByModel                                      map[string][2]int // [input, output]
	TotalRequests                                      int
	RequestsByModel                                    map[string]int
	RequestsByType                                     map[string]int
}

// aggregateEventStats reads events.jsonl and computes cost, token, and request aggregates.
func (s *Service) aggregateEventStats() eventAggregates {
	a := eventAggregates{
		CostByModel:     make(map[string]float64),
		CostByType:      make(map[string]float64),
		TokensByModel:   make(map[string][2]int),
		RequestsByModel: make(map[string]int),
		RequestsByType:  make(map[string]int),
	}
	data, err := os.ReadFile(s.cfg.EventsPath)
	if err != nil {
		return a
	}

	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	weekStart := now.AddDate(0, 0, -7)
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local)

	// addTimeBuckets adds USD to the appropriate time-window buckets.
	addTimeBuckets := func(ts time.Time, usd float64) {
		a.CostTotal += usd
		if ts.After(todayStart) {
			a.CostToday += usd
		}
		if ts.After(weekStart) {
			a.CostThisWeek += usd
		}
		if ts.After(monthStart) {
			a.CostThisMonth += usd
		}
	}

	// addTokens records token counts for a model.
	addTokens := func(model string, in, out int) {
		a.TotalInputTokens += in
		a.TotalOutputTokens += out
		if model != "" {
			t := a.TokensByModel[model]
			t[0] += in
			t[1] += out
			a.TokensByModel[model] = t
		}
	}

	// chatCost is a minimal struct to extract cost from non-ingest events.
	type chatCost struct {
		CostUSD      float64 `json:"cost_usd"`
		Model        string  `json:"model"`
		InputTokens  int     `json:"input_tokens"`
		OutputTokens int     `json:"output_tokens"`
	}
	type chatEvent struct {
		TS    time.Time `json:"ts"`
		Type  string    `json:"type"`
		Model string    `json:"model"`
		Cost  *chatCost `json:"cost"`
	}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Try ingest event first (has store.CostRecord under "cost").
		// Ingest events have a CostRecord with TotalUSD > 0; non-ingest events
		// unmarshal into CostRecord but all fields stay zero.
		var e store.Event
		if err := parseEventLine(line, &e); err == nil && e.Cost != nil && e.Cost.TotalUSD > 0 {
			usd := e.Cost.TotalUSD
			addTimeBuckets(e.TS, usd)
			a.CostByType["ingest"] += usd
			a.TotalRequests++
			a.RequestsByType["ingest"]++
			for _, entry := range []store.CostEntry{e.Cost.Summary, e.Cost.Flash, e.Cost.Flashcards} {
				if entry.Model != "" {
					a.CostByModel[entry.Model] += entry.USD
					a.RequestsByModel[entry.Model]++
					addTokens(entry.Model, entry.InputTokens, entry.OutputTokens)
				}
			}
			if e.Cost.Embed.Model != "" {
				a.CostByModel[e.Cost.Embed.Model] += e.Cost.Embed.USD
				addTokens(e.Cost.Embed.Model, e.Cost.Embed.Tokens, 0)
			}
			continue
		}

		// Try chat/askx/collection events.
		var ce chatEvent
		if err := json.Unmarshal([]byte(line), &ce); err != nil {
			continue
		}
		if ce.Cost == nil || ce.Cost.CostUSD == 0 {
			continue
		}
		// Only count turn_complete to avoid double-counting (api_call is per-round).
		if ce.Type != "chat_turn_complete" && ce.Type != "askx_call" && ce.Type != "collection_suggest" && ce.Type != "collection_assign" {
			continue
		}
		usd := ce.Cost.CostUSD
		addTimeBuckets(ce.TS, usd)

		// Normalize type for grouping: chat_turn_complete → "chat".
		opType := ce.Type
		switch opType {
		case "chat_turn_complete":
			opType = "chat"
		}
		a.CostByType[opType] += usd
		a.TotalRequests++
		a.RequestsByType[opType]++

		if ce.Model != "" {
			a.CostByModel[ce.Model] += usd
			a.RequestsByModel[ce.Model]++
		}
		addTokens(ce.Model, ce.Cost.InputTokens, ce.Cost.OutputTokens)
	}
	return a
}

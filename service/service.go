package service

import (
	"context"
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

// Reindex rebuilds the SQLite index from the filesystem.
// progress is called with (indexed, total) after each article; may be nil.
func (s *Service) Reindex(ctx context.Context, progress func(indexed, total int)) error {
	return s.lib.Reindex(ctx, progress)
}

// ReindexEmbed generates embeddings for articles that have a summary but no
// embed_model recorded. progress is called with (embedded, total) after each
// article. Returns the number of articles embedded.
func (s *Service) ReindexEmbed(ctx context.Context, progress func(done, total int)) (int, error) {
	if s.vec == nil {
		return 0, fmt.Errorf("vector store not available")
	}
	if s.cfg.Ingest.EmbedProfile == "" {
		return 0, fmt.Errorf("embed_profile not configured")
	}
	prof, ok := s.cfg.Profiles[s.cfg.Ingest.EmbedProfile]
	if !ok {
		return 0, fmt.Errorf("embed profile %q not found", s.cfg.Ingest.EmbedProfile)
	}
	embedClient, err := embed.NewClient(prof.Model)
	if err != nil {
		return 0, fmt.Errorf("embed client: %w", err)
	}

	articles, err := s.lib.List(ctx, store.Filter{})
	if err != nil {
		return 0, fmt.Errorf("list articles: %w", err)
	}

	// Only articles that have a summary but no embedding yet.
	var pending []store.Article
	for _, a := range articles {
		if a.EmbedModel == "" && a.SummaryModel != "" && a.Files.Summary != "" {
			pending = append(pending, a)
		}
	}

	if progress == nil {
		progress = func(int, int) {}
	}

	embedded := 0
	for _, a := range pending {
		summaryText, err := s.lib.ReadSummary(a)
		if err != nil || strings.TrimSpace(summaryText) == "" {
			continue
		}
		er, err := embedClient.Embed(ctx, summaryText)
		if err != nil {
			continue
		}
		if err := s.vec.Upsert(ctx, a.ID, er.Embedding, summaryText); err != nil {
			continue
		}
		embedded++
		progress(embedded, len(pending))
	}
	return embedded, nil
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
	if err := s.lib.Reindex(ctx, nil); err != nil {
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
	if !req.NoFlashcards {
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

// GetArticle returns a single article by ID with Files populated from disk.
func (s *Service) GetArticle(ctx context.Context, id string) (store.Article, error) {
	a, err := s.lib.Get(ctx, id)
	if err != nil {
		return store.Article{}, err
	}
	a.Files = fs.ProbeFiles(filepath.Join(s.cfg.ArticlesRoot, id))
	return a, nil
}

// MarkRead records that an article was read.
func (s *Service) MarkRead(ctx context.Context, id string) error {
	return s.lib.MarkRead(ctx, id, time.Now())
}

// MarkPlayed records that an article was played via TTS.
func (s *Service) MarkPlayed(ctx context.Context, id string) error {
	return s.lib.MarkPlayed(ctx, id, time.Now())
}

// Collections returns all defined collections.
func (s *Service) Collections(ctx context.Context) ([]store.Collection, error) {
	cols, err := fs.ReadCollections(s.cfg.ArticlesRoot)
	if err != nil {
		return nil, fmt.Errorf("read collections: %w", err)
	}
	return cols, nil
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

	cols, err := fs.ReadCollections(s.cfg.ArticlesRoot)
	if err != nil {
		return Stats{}, fmt.Errorf("read collections: %w", err)
	}

	// Collect unique tags, unread/unplayed counts, and article breakdowns
	tagSet := make(map[string]struct{})
	byModel := make(map[string]int)
	byStyle := make(map[string]int)
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
	}

	// Cost from events.jsonl
	costTotal, costMonth, costByModel := s.aggregateCosts()

	return Stats{
		TotalArticles:    len(articles),
		TotalCollections: len(cols),
		TotalTags:        len(tagSet),
		Unread:           unread,
		Unplayed:         unplayed,
		EmbedCoverage:    embedCoverage,
		CostThisMonth:    costMonth,
		CostTotal:        costTotal,
		CostByModel:      costByModel,
		ArticlesByModel:  byModel,
		ArticlesByStyle:  byStyle,
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
	}

	if strings.TrimSpace(text) == "" {
		return FlashcardsResult{}, fmt.Errorf("no text to generate flashcards from")
	}

	pr, err := pipeline.Flashcards(ctx, s.cfg, pipeline.FlashcardsRequest{
		Text:     text,
		Style:    req.Style,
		Profile:  req.Profile,
		Progress: req.Progress,
	})
	if err != nil {
		return FlashcardsResult{}, err
	}

	result := FlashcardsResult{
		JSON:    pr.JSON,
		Style:   pr.Style,
		Model:   pr.Model,
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
	}

	return result, nil
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
		NoFlashcards:   req.NoFlashcards,
		NoEmbed:        req.NoEmbed,
		VectorStore:    s.vec,
		DryRun:         req.DryRun,
		Progress:       req.Progress,
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
	if err := s.lib.Reindex(ctx, nil); err != nil {
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

// aggregateCosts reads events.jsonl and sums up costs.
// Returns total, thisMonth, and a per-model USD breakdown.
func (s *Service) aggregateCosts() (total, thisMonth float64, byModel map[string]float64) {
	byModel = make(map[string]float64)
	data, err := os.ReadFile(s.cfg.EventsPath)
	if err != nil {
		return 0, 0, byModel
	}

	now := time.Now()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e store.Event
		if err := parseEventLine(line, &e); err != nil {
			continue
		}
		if e.Cost == nil {
			continue
		}
		total += e.Cost.TotalUSD
		if e.TS.After(monthStart) {
			thisMonth += e.Cost.TotalUSD
		}
		// Accumulate per-model costs across all operation types
		for _, entry := range []store.CostEntry{e.Cost.Summary, e.Cost.Flash, e.Cost.Flashcards} {
			if entry.Model != "" {
				byModel[entry.Model] += entry.USD
			}
		}
		if e.Cost.Embed.Model != "" {
			byModel[e.Cost.Embed.Model] += e.Cost.Embed.USD
		}
	}
	return total, thisMonth, byModel
}

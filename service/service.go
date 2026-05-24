package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jrniemiec/arc/config"
	"github.com/jrniemiec/arc/ingest/pipeline"
	"github.com/jrniemiec/arc/library"
	"github.com/jrniemiec/arc/store"
	"github.com/jrniemiec/arc/store/fs"
)

// Service is the arc business logic layer. All frontends (CLI, TUI, MCP, bot)
// call Service methods — never library or store directly.
type Service struct {
	lib *library.Library
	cfg config.Config
}

// New creates a Service from an open Library.
func New(lib *library.Library, cfg config.Config) *Service {
	return &Service{lib: lib, cfg: cfg}
}

// Reindex rebuilds the SQLite index from the filesystem.
// progress is called with (indexed, total) after each article; may be nil.
func (s *Service) Reindex(ctx context.Context, progress func(indexed, total int)) error {
	return s.lib.Reindex(ctx, progress)
}

// Search runs a keyword (FTS5) or semantic search and returns ranked results.
func (s *Service) Search(ctx context.Context, req SearchRequest) ([]SearchResult, error) {
	if strings.TrimSpace(req.Query) == "" {
		return nil, fmt.Errorf("search query cannot be empty")
	}

	limit := req.Limit
	if limit <= 0 {
		limit = 20
	}

	q := store.Query{
		Text: req.Query,
		Mode: req.Mode,
		TopK: limit,
		Filter: store.Filter{
			Collection: req.Collection,
			Tags:       req.Tags,
		},
	}

	hits, err := s.lib.Search(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}

	results := make([]SearchResult, 0, len(hits))
	for _, h := range hits {
		results = append(results, SearchResult{
			Article: h.Article,
			Score:   h.Score,
			Excerpt: h.Excerpt,
			Source:  h.Source,
		})
	}
	return results, nil
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
	var unread, unplayed int
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
	}

	// Cost from events.jsonl
	costTotal, costMonth, costByModel := s.aggregateCosts()

	return Stats{
		TotalArticles:    len(articles),
		TotalCollections: len(cols),
		TotalTags:        len(tagSet),
		Unread:           unread,
		Unplayed:         unplayed,
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

// Ingest ingests a single article from a URL or file using the native Go pipeline.
func (s *Service) Ingest(ctx context.Context, req IngestRequest) (IngestResult, error) {
	if req.URL == "" && req.File == "" {
		return IngestResult{}, fmt.Errorf("ingest: URL or File must be specified")
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
		DryRun:         req.DryRun,
		Progress:       req.Progress,
	})
	if err != nil {
		return IngestResult{}, fmt.Errorf("ingest pipeline: %w", err)
	}

	if req.DryRun {
		return IngestResult{DryRun: true, ExtractionStats: result.ExtractionStats}, nil
	}

	// Index the new article into SQLite
	if err := s.lib.Reindex(ctx, nil); err != nil {
		return IngestResult{}, fmt.Errorf("reindex after ingest: %w", err)
	}

	return IngestResult{
		ArticleID: result.Slug,
		Slug:      result.Slug,
		Cost:      result.Cost,
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

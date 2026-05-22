package service

import (
	"context"
	"fmt"
	"os"
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

	// Collect unique tags and count unread/unplayed
	tagSet := make(map[string]struct{})
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
	}

	// Cost from events.jsonl
	costTotal, costMonth := s.aggregateCosts()

	return Stats{
		TotalArticles:    len(articles),
		TotalCollections: len(cols),
		TotalTags:        len(tagSet),
		Unread:           unread,
		Unplayed:         unplayed,
		CostThisMonth:    costMonth,
		CostTotal:        costTotal,
	}, nil
}

// Ingest ingests a single article from a URL or file using the native Go pipeline.
func (s *Service) Ingest(ctx context.Context, req IngestRequest) (IngestResult, error) {
	if req.URL == "" && req.File == "" {
		return IngestResult{}, fmt.Errorf("ingest: URL or File must be specified")
	}

	if req.DryRun {
		return IngestResult{DryRun: true}, nil
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
		Progress:       req.Progress,
	})
	if err != nil {
		return IngestResult{}, fmt.Errorf("ingest pipeline: %w", err)
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
func (s *Service) aggregateCosts() (total, thisMonth float64) {
	data, err := os.ReadFile(s.cfg.EventsPath)
	if err != nil {
		return 0, 0
	}

	now := time.Now()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Quick parse: look for total_usd field
		var e store.Event
		if err := parseEventLine(line, &e); err != nil {
			continue
		}
		if e.Cost != nil {
			total += e.Cost.TotalUSD
			if e.TS.After(monthStart) {
				thisMonth += e.Cost.TotalUSD
			}
		}
	}
	return total, thisMonth
}

package library

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jrniemiec/arc/config"
	"github.com/jrniemiec/arc/store"
	"github.com/jrniemiec/arc/store/fs"
	"github.com/jrniemiec/arc/store/sqlite"
)

// Library composes the filesystem store, SQLite index, and event log
// into the top-level arc knowledge base.
type Library struct {
	cfg config.Config
	fs  *fs.Store
	db  *sqlite.Store
}

// Open opens an existing arc library rooted at cfg.ArticlesRoot.
// It creates the data directory and database if they don't exist.
func Open(ctx context.Context, cfg config.Config) (*Library, error) {
	if err := os.MkdirAll(cfg.ArticlesRoot, 0755); err != nil {
		return nil, fmt.Errorf("create articles root: %w", err)
	}
	if err := os.MkdirAll(cfg.VectorPath, 0755); err != nil {
		return nil, fmt.Errorf("create vector path: %w", err)
	}

	fsStore := fs.New(cfg.ArticlesRoot)

	db, err := sqlite.Open(ctx, cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	return &Library{cfg: cfg, fs: fsStore, db: db}, nil
}

// Close releases all resources held by the library.
func (l *Library) Close() error {
	return l.db.Close()
}

// DB returns the underlying SQLite store for callers that need direct access
// (e.g. the agent package for ExistsByURL and TopTags queries).
func (l *Library) DB() *sqlite.Store {
	return l.db
}

// Get returns an article by ID with file paths resolved per config preferences.
func (l *Library) Get(ctx context.Context, id string) (store.Article, error) {
	a, err := l.db.Get(ctx, id)
	if err != nil {
		return store.Article{}, err
	}
	l.resolveFiles(&a)
	return a, nil
}

// List returns articles matching the filter with file paths resolved.
func (l *Library) List(ctx context.Context, f store.Filter) ([]store.Article, error) {
	articles, err := l.db.List(ctx, f)
	if err != nil {
		return nil, err
	}
	for i := range articles {
		l.resolveFiles(&articles[i])
	}
	return articles, nil
}

// Search runs a full-text search and returns ranked results.
func (l *Library) Search(ctx context.Context, q store.Query) ([]store.Result, error) {
	results, err := l.db.Search(ctx, q)
	if err != nil {
		return nil, err
	}
	for i := range results {
		l.resolveFiles(&results[i].Article)
	}
	return results, nil
}

// ReadBody reads the body text for an article.
func (l *Library) ReadBody(a store.Article) (string, error) {
	return l.fs.ReadBody(a)
}

// ReadSummary reads the preferred summary for an article.
func (l *Library) ReadSummary(a store.Article) (string, error) {
	return l.fs.ReadSummary(a)
}

// ReadFlash reads the preferred flash for an article.
func (l *Library) ReadFlash(a store.Article) (string, error) {
	return l.fs.ReadFlash(a)
}

// ReadFlashcards reads the preferred flashcards for an article.
func (l *Library) ReadFlashcards(a store.Article) ([]byte, error) {
	return l.fs.ReadFlashcards(a)
}

// Reindex walks the filesystem and rebuilds the SQLite index from scratch.
// progress is called with (indexed, total) after each article; may be nil.
func (l *Library) Reindex(ctx context.Context, progress func(indexed, total int)) error {
	// First pass: count articles for progress reporting
	total := 0
	if err := l.fs.Walk(func(_ string, _ store.Files) error {
		total++
		return nil
	}); err != nil {
		return fmt.Errorf("count articles: %w", err)
	}

	// Migrate old meta.json collections to symlinks (one-time, idempotent)
	_ = fs.MigrateMetaCollections(l.cfg.DataRoot, l.cfg.ArticlesRoot)

	// Sync collections from symlinks to SQLite
	membership, err := fs.ScanCollectionMembership(l.cfg.DataRoot)
	if err != nil {
		return fmt.Errorf("scan collections: %w", err)
	}
	// Upsert all discovered collections
	colMetas, _ := fs.ListCollections(l.cfg.DataRoot)
	for _, m := range colMetas {
		if err := l.db.UpsertCollection(ctx, store.Collection{
			ID:        m.Slug,
			Name:      m.Name,
			CreatedAt: m.CreatedAt,
		}); err != nil {
			return fmt.Errorf("upsert collection %s: %w", m.Slug, err)
		}
	}
	// membership is used below during article upsert
	_ = membership

	// Second pass: index each article
	indexed := 0
	return l.fs.Walk(func(id string, files store.Files) error {
		meta, err := fs.ReadMeta(files.Meta)
		if err != nil {
			return fmt.Errorf("read meta %s: %w", id, err)
		}

		a := meta.ToArticle(id, files)
		// Override collections from symlinks (authoritative) rather than meta.json
		a.Collections = membership[id]
		l.resolveFiles(&a)

		// Read summary text for FTS indexing
		summaryText := ""
		if a.Files.Summary != "" {
			if text, err := l.fs.ReadSummary(a); err == nil {
				summaryText = text
			}
		}

		if err := l.db.Upsert(ctx, a, summaryText); err != nil {
			return fmt.Errorf("upsert %s: %w", id, err)
		}

		indexed++
		if progress != nil {
			progress(indexed, total)
		}
		return nil
	})
}

// MarkRead records that an article was read at time t.
func (l *Library) MarkRead(ctx context.Context, id string, t time.Time) error {
	return l.db.MarkRead(ctx, id, t)
}

// MarkPlayed records that an article was played at time t.
func (l *Library) MarkPlayed(ctx context.Context, id string, t time.Time) error {
	return l.db.MarkPlayed(ctx, id, t)
}

// Relate creates a directed relation between two articles.
func (l *Library) Relate(ctx context.Context, fromID, toID string, t store.RelationType) error {
	return l.db.UpsertRelation(ctx, fromID, toID, t)
}

// UpsertCollection upserts a collection definition in SQLite.
func (l *Library) UpsertCollection(ctx context.Context, c store.Collection) error {
	return l.db.UpsertCollection(ctx, c)
}

// CollectionCounts returns article counts per collection.
func (l *Library) CollectionCounts(ctx context.Context) (map[string]int, error) {
	return l.db.CollectionCounts(ctx)
}

// AddArticleToCollection inserts an article→collection membership in SQLite.
func (l *Library) AddArticleToCollection(ctx context.Context, articleID, collectionID string) error {
	return l.db.AddArticleToCollection(ctx, articleID, collectionID)
}

// RemoveArticleFromCollection removes an article→collection membership from SQLite.
func (l *Library) RemoveArticleFromCollection(ctx context.Context, articleID, collectionID string) error {
	return l.db.RemoveArticleFromCollection(ctx, articleID, collectionID)
}

// DeleteArticle removes an article from the filesystem, SQLite, and all collection symlinks.
func (l *Library) DeleteArticle(ctx context.Context, a store.Article) error {
	// Remove collection symlinks pointing to this article.
	collections, err := l.db.CollectionsForArticle(ctx, a.ID)
	if err == nil {
		for _, colID := range collections {
			_ = fs.RemoveArticleFromCollection(l.cfg.DataRoot, colID, a.ID)
		}
	}

	// Remove from SQLite.
	if err := l.db.Delete(ctx, a.ID); err != nil {
		return fmt.Errorf("delete from db: %w", err)
	}

	// Remove article directory from filesystem.
	dir := l.fs.ArticleDir(a.ID)
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove article dir: %w", err)
	}

	return nil
}

// DeleteCollection removes a collection and all its membership rows from SQLite.
func (l *Library) DeleteCollection(ctx context.Context, id string) error {
	return l.db.DeleteCollection(ctx, id)
}

// RenameCollection updates the collection ID in SQLite.
func (l *Library) RenameCollection(ctx context.Context, oldID, newID string) error {
	return l.db.RenameCollection(ctx, oldID, newID)
}

// resolveFiles fills in the preferred file paths (summary, flash, flashcards)
// based on config preferences. root_path must already be set in a.Files.Root.
func (l *Library) resolveFiles(a *store.Article) {
	if a.Files.Root == "" {
		return
	}
	probed := fs.ProbeFiles(a.Files.Root)
	if a.Files.Body == "" {
		a.Files.Body = probed.Body
	}
	if a.Files.SourceURL == "" {
		a.Files.SourceURL = probed.SourceURL
	}
	a.Files.Summary = fs.ResolveSummary(a.Files.Root, l.cfg.PreferredStyles, l.cfg.PreferredModels)
	a.Files.Flash = fs.ResolveFlash(a.Files.Root, l.cfg.PreferredModels)
	a.Files.Flashcards = fs.ResolveFlashcards(a.Files.Root, l.cfg.PreferredStyles, l.cfg.PreferredModels)
}

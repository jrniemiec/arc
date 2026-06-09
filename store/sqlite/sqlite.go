package sqlite

import (
	"context"
	"fmt"
	"strings"
	"time"

	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"

	"github.com/jrniemiec/arc/store"
)

// Store is the SQLite-backed metadata and search store.
type Store struct {
	pool *sqlitex.Pool
}

// Open opens (or creates) the SQLite database at path and applies the schema.
func Open(ctx context.Context, path string) (*Store, error) {
	pool, err := sqlitex.NewPool(path, sqlitex.PoolOptions{
		PoolSize: 4,
	})
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}

	s := &Store{pool: pool}
	if err := s.migrate(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

// Close closes the database pool.
func (s *Store) Close() error {
	return s.pool.Close()
}

func (s *Store) migrate(ctx context.Context) error {
	conn, err := s.pool.Take(ctx)
	if err != nil {
		return err
	}
	defer s.pool.Put(conn)
	return sqlitex.ExecuteScript(conn, schema, nil)
}

// Upsert inserts or replaces an article and its tags, collections, and FTS entry.
func (s *Store) Upsert(ctx context.Context, a store.Article, summaryText string) error {
	conn, err := s.pool.Take(ctx)
	if err != nil {
		return err
	}
	defer s.pool.Put(conn)

	endFn := sqlitex.Transaction(conn)
	err = s.upsertTx(conn, a, summaryText)
	endFn(&err)
	return err
}

func (s *Store) upsertTx(conn *sqlite.Conn, a store.Article, summaryText string) error {
	// Upsert article row
	err := sqlitex.Execute(conn, `
		INSERT INTO articles (
			id, title, url, source_type, feed, author, published_at, language,
			ingested_at, read_at, played_at,
			summary_model, summary_style, flash_model, flashcard_model, flashcard_style,
			embed_model, quality_score, root_path
		) VALUES (
			?, ?, ?, ?, ?, ?, ?, ?,
			?, ?, ?,
			?, ?, ?, ?, ?,
			?, ?, ?
		)
		ON CONFLICT(id) DO UPDATE SET
			title           = excluded.title,
			url             = excluded.url,
			source_type     = excluded.source_type,
			feed            = excluded.feed,
			author          = excluded.author,
			published_at    = excluded.published_at,
			language        = excluded.language,
			ingested_at     = excluded.ingested_at,
			summary_model   = excluded.summary_model,
			summary_style   = excluded.summary_style,
			flash_model     = excluded.flash_model,
			flashcard_model = excluded.flashcard_model,
			flashcard_style = excluded.flashcard_style,
			embed_model     = excluded.embed_model,
			quality_score   = excluded.quality_score,
			root_path       = excluded.root_path
	`, &sqlitex.ExecOptions{
		Args: []any{
			a.ID, a.Title, a.URL, a.SourceType, a.Feed, a.Author, a.PublishedAt, a.Language,
			a.IngestedAt.UTC().Format(time.RFC3339),
			timePtr(a.ReadAt), timePtr(a.PlayedAt),
			a.SummaryModel, a.SummaryStyle, a.FlashModel, a.FlashcardModel, a.FlashcardStyle,
			a.EmbedModel, a.QualityScore, a.Files.Root,
		},
	})
	if err != nil {
		return fmt.Errorf("upsert article: %w", err)
	}

	// Replace tags
	if err := sqlitex.Execute(conn, `DELETE FROM tags WHERE article_id = ?`,
		&sqlitex.ExecOptions{Args: []any{a.ID}}); err != nil {
		return err
	}
	for _, t := range a.Tags {
		if err := sqlitex.Execute(conn,
			`INSERT OR IGNORE INTO tags (article_id, tag, source) VALUES (?, ?, ?)`,
			&sqlitex.ExecOptions{Args: []any{a.ID, t.Value, string(t.Source)}}); err != nil {
			return fmt.Errorf("insert tag %s: %w", t.Value, err)
		}
	}

	// Replace collection memberships
	if err := sqlitex.Execute(conn, `DELETE FROM article_collections WHERE article_id = ?`,
		&sqlitex.ExecOptions{Args: []any{a.ID}}); err != nil {
		return err
	}
	for _, col := range a.Collections {
		if err := sqlitex.Execute(conn,
			`INSERT OR IGNORE INTO article_collections (article_id, collection_id) VALUES (?, ?)`,
			&sqlitex.ExecOptions{Args: []any{a.ID, col}}); err != nil {
			return fmt.Errorf("insert collection %s: %w", col, err)
		}
	}

	// Update FTS entry: delete existing row then insert fresh.
	if err := sqlitex.Execute(conn,
		`DELETE FROM articles_fts WHERE article_id = ?`,
		&sqlitex.ExecOptions{Args: []any{a.ID}}); err != nil {
		_ = err // best-effort: row may not exist yet
	}
	if err := sqlitex.Execute(conn,
		`INSERT INTO articles_fts (article_id, title, summary) VALUES (?, ?, ?)`,
		&sqlitex.ExecOptions{Args: []any{a.ID, a.Title, summaryText}}); err != nil {
		return fmt.Errorf("fts insert: %w", err)
	}

	return nil
}

// Delete removes an article and all related rows (cascade handles the rest).
func (s *Store) Delete(ctx context.Context, id string) error {
	conn, err := s.pool.Take(ctx)
	if err != nil {
		return err
	}
	defer s.pool.Put(conn)
	return sqlitex.Execute(conn, `DELETE FROM articles WHERE id = ?`,
		&sqlitex.ExecOptions{Args: []any{id}})
}

// Get returns a single article by ID.
func (s *Store) Get(ctx context.Context, id string) (store.Article, error) {
	conn, err := s.pool.Take(ctx)
	if err != nil {
		return store.Article{}, err
	}
	defer s.pool.Put(conn)

	var a store.Article
	found := false
	err = sqlitex.Execute(conn, `SELECT `+articleColumns+` FROM articles WHERE id = ?`,
		&sqlitex.ExecOptions{
			Args: []any{id},
			ResultFunc: func(stmt *sqlite.Stmt) error {
				a = scanArticle(stmt)
				found = true
				return nil
			},
		})
	if err != nil {
		return store.Article{}, fmt.Errorf("get article %s: %w", id, err)
	}
	if !found {
		return store.Article{}, fmt.Errorf("article not found: %s", id)
	}

	if err := s.loadTags(conn, &a); err != nil {
		return store.Article{}, err
	}
	if err := s.loadCollections(conn, &a); err != nil {
		return store.Article{}, err
	}
	return a, nil
}

// List returns articles matching the filter.
func (s *Store) List(ctx context.Context, f store.Filter) ([]store.Article, error) {
	conn, err := s.pool.Take(ctx)
	if err != nil {
		return nil, err
	}
	defer s.pool.Put(conn)

	q, args := buildListQuery(f)
	var articles []store.Article
	err = sqlitex.Execute(conn, q, &sqlitex.ExecOptions{
		Args: args,
		ResultFunc: func(stmt *sqlite.Stmt) error {
			articles = append(articles, scanArticle(stmt))
			return nil
		},
	})
	if err != nil {
		return nil, fmt.Errorf("list articles: %w", err)
	}

	for i := range articles {
		_ = s.loadTags(conn, &articles[i])
		_ = s.loadCollections(conn, &articles[i])
	}
	return articles, nil
}

// Search runs a full-text search and returns ranked results.
func (s *Store) Search(ctx context.Context, q store.Query) ([]store.Result, error) {
	conn, err := s.pool.Take(ctx)
	if err != nil {
		return nil, err
	}
	defer s.pool.Put(conn)

	topK := q.TopK
	if topK <= 0 {
		topK = 20
	}

	var results []store.Result
	err = sqlitex.Execute(conn, `
		SELECT `+articleColumnsQualified+`, bm25(articles_fts) AS score,
		       snippet(articles_fts, 2, '[', ']', '...', 20) AS excerpt
		FROM articles_fts
		JOIN articles a ON a.id = articles_fts.article_id
		WHERE articles_fts MATCH ?
		ORDER BY bm25(articles_fts)
		LIMIT ?
	`, &sqlitex.ExecOptions{
		Args: []any{q.Text, topK},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			a := scanArticle(stmt)
			score := stmt.ColumnFloat(columnCount)
			excerpt := stmt.ColumnText(columnCount + 1)
			results = append(results, store.Result{
				Article: a,
				Score:   score,
				Excerpt: excerpt,
				Source:  "fts",
			})
			return nil
		},
	})
	if err != nil {
		return nil, fmt.Errorf("fts search: %w", err)
	}
	return results, nil
}

// UpsertCollection inserts or replaces a collection definition.
func (s *Store) UpsertCollection(ctx context.Context, c store.Collection) error {
	conn, err := s.pool.Take(ctx)
	if err != nil {
		return err
	}
	defer s.pool.Put(conn)
	return sqlitex.Execute(conn, `
		INSERT INTO collections (id, name, description, created_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name        = excluded.name,
			description = excluded.description
	`, &sqlitex.ExecOptions{
		Args: []any{c.ID, c.Name, c.Description, c.CreatedAt.UTC().Format(time.RFC3339)},
	})
}

// CollectionCounts returns a map of collection_id → article count.
func (s *Store) CollectionCounts(ctx context.Context) (map[string]int, error) {
	conn, err := s.pool.Take(ctx)
	if err != nil {
		return nil, err
	}
	defer s.pool.Put(conn)
	result := make(map[string]int)
	err = sqlitex.Execute(conn,
		`SELECT collection_id, COUNT(*) FROM article_collections GROUP BY collection_id`,
		&sqlitex.ExecOptions{
			ResultFunc: func(stmt *sqlite.Stmt) error {
				result[stmt.ColumnText(0)] = int(stmt.ColumnInt64(1))
				return nil
			},
		})
	return result, err
}

// AddArticleToCollection inserts a row into article_collections.
func (s *Store) AddArticleToCollection(ctx context.Context, articleID, collectionID string) error {
	conn, err := s.pool.Take(ctx)
	if err != nil {
		return err
	}
	defer s.pool.Put(conn)
	return sqlitex.Execute(conn,
		`INSERT OR IGNORE INTO article_collections (article_id, collection_id) VALUES (?, ?)`,
		&sqlitex.ExecOptions{Args: []any{articleID, collectionID}})
}

// RemoveArticleFromCollection deletes a row from article_collections.
func (s *Store) RemoveArticleFromCollection(ctx context.Context, articleID, collectionID string) error {
	conn, err := s.pool.Take(ctx)
	if err != nil {
		return err
	}
	defer s.pool.Put(conn)
	return sqlitex.Execute(conn,
		`DELETE FROM article_collections WHERE article_id = ? AND collection_id = ?`,
		&sqlitex.ExecOptions{Args: []any{articleID, collectionID}})
}

// MarkRead sets read_at for an article.
func (s *Store) MarkRead(ctx context.Context, id string, t time.Time) error {
	conn, err := s.pool.Take(ctx)
	if err != nil {
		return err
	}
	defer s.pool.Put(conn)
	return sqlitex.Execute(conn, `UPDATE articles SET read_at = ? WHERE id = ?`,
		&sqlitex.ExecOptions{Args: []any{t.UTC().Format(time.RFC3339), id}})
}

// MarkPlayed sets played_at for an article.
func (s *Store) MarkPlayed(ctx context.Context, id string, t time.Time) error {
	conn, err := s.pool.Take(ctx)
	if err != nil {
		return err
	}
	defer s.pool.Put(conn)
	return sqlitex.Execute(conn, `UPDATE articles SET played_at = ? WHERE id = ?`,
		&sqlitex.ExecOptions{Args: []any{t.UTC().Format(time.RFC3339), id}})
}

// --- helpers ---

const articleColumns = `id, title, url, source_type, feed, author, published_at, language,
	ingested_at, read_at, played_at,
	summary_model, summary_style, flash_model, flashcard_model, flashcard_style,
	embed_model, quality_score, root_path`

// articleColumnsQualified is used in JOINs to avoid ambiguous column names.
const articleColumnsQualified = `a.id, a.title, a.url, a.source_type, a.feed, a.author, a.published_at, a.language,
	a.ingested_at, a.read_at, a.played_at,
	a.summary_model, a.summary_style, a.flash_model, a.flashcard_model, a.flashcard_style,
	a.embed_model, a.quality_score, a.root_path`

// columnCount is the number of columns in articleColumns (for offset calculations in Search).
const columnCount = 19

func scanArticle(stmt *sqlite.Stmt) store.Article {
	a := store.Article{}
	a.ID = stmt.ColumnText(0)
	a.Title = stmt.ColumnText(1)
	a.URL = stmt.ColumnText(2)
	a.SourceType = stmt.ColumnText(3)
	a.Feed = stmt.ColumnText(4)
	a.Author = stmt.ColumnText(5)
	a.PublishedAt = stmt.ColumnText(6)
	a.Language = stmt.ColumnText(7)

	if s := stmt.ColumnText(8); s != "" {
		t, _ := time.Parse(time.RFC3339, s)
		a.IngestedAt = t
	}
	if s := stmt.ColumnText(9); s != "" {
		t, _ := time.Parse(time.RFC3339, s)
		a.ReadAt = &t
	}
	if s := stmt.ColumnText(10); s != "" {
		t, _ := time.Parse(time.RFC3339, s)
		a.PlayedAt = &t
	}

	a.SummaryModel = stmt.ColumnText(11)
	a.SummaryStyle = stmt.ColumnText(12)
	a.FlashModel = stmt.ColumnText(13)
	a.FlashcardModel = stmt.ColumnText(14)
	a.FlashcardStyle = stmt.ColumnText(15)
	a.EmbedModel = stmt.ColumnText(16)
	a.QualityScore = stmt.ColumnFloat(17)
	a.Files.Root = stmt.ColumnText(18)

	return a
}

func (s *Store) loadTags(conn *sqlite.Conn, a *store.Article) error {
	return sqlitex.Execute(conn, `SELECT tag, source FROM tags WHERE article_id = ?`,
		&sqlitex.ExecOptions{
			Args: []any{a.ID},
			ResultFunc: func(stmt *sqlite.Stmt) error {
				a.Tags = append(a.Tags, store.Tag{
					Value:  stmt.ColumnText(0),
					Source: store.TagSource(stmt.ColumnText(1)),
				})
				return nil
			},
		})
}

func (s *Store) loadCollections(conn *sqlite.Conn, a *store.Article) error {
	return sqlitex.Execute(conn, `SELECT collection_id FROM article_collections WHERE article_id = ?`,
		&sqlitex.ExecOptions{
			Args: []any{a.ID},
			ResultFunc: func(stmt *sqlite.Stmt) error {
				a.Collections = append(a.Collections, stmt.ColumnText(0))
				return nil
			},
		})
}

func buildListQuery(f store.Filter) (string, []any) {
	var where []string
	var args []any

	if f.Collection != "" {
		where = append(where, `id IN (SELECT article_id FROM article_collections WHERE collection_id = ?)`)
		args = append(args, f.Collection)
	}
	for _, tag := range f.Tags {
		where = append(where, `id IN (SELECT article_id FROM tags WHERE tag = ?)`)
		args = append(args, tag)
	}
	if f.SourceType != "" {
		where = append(where, `source_type = ?`)
		args = append(args, f.SourceType)
	}
	if f.After != nil {
		where = append(where, `ingested_at >= ?`)
		args = append(args, f.After.UTC().Format(time.RFC3339))
	}
	if f.Before != nil {
		where = append(where, `ingested_at <= ?`)
		args = append(args, f.Before.UTC().Format(time.RFC3339))
	}
	if f.Unread {
		where = append(where, `read_at IS NULL`)
	}
	if f.Unplayed {
		where = append(where, `played_at IS NULL`)
	}

	q := `SELECT ` + articleColumns + ` FROM articles`
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, ` AND `)
	}
	q += ` ORDER BY ingested_at DESC`
	if f.Limit > 0 {
		q += fmt.Sprintf(` LIMIT %d`, f.Limit)
	}
	if f.Offset > 0 {
		q += fmt.Sprintf(` OFFSET %d`, f.Offset)
	}
	return q, args
}

func timePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

// UpsertRelation inserts or replaces a relation between two articles.
func (s *Store) UpsertRelation(ctx context.Context, fromID, toID string, t store.RelationType) error {
	conn, err := s.pool.Take(ctx)
	if err != nil {
		return err
	}
	defer s.pool.Put(conn)
	return sqlitex.Execute(conn, `
		INSERT INTO relations (from_id, to_id, type, detected_by, detected_at)
		VALUES (?, ?, ?, 'user', ?)
		ON CONFLICT(from_id, to_id, type) DO NOTHING
	`, &sqlitex.ExecOptions{
		Args: []any{fromID, toID, string(t), time.Now().UTC().Format(time.RFC3339)},
	})
}


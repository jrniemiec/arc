package fs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/jrniemiec/arc/store"
)

// Store handles filesystem operations for the arc article store.
type Store struct {
	root string // ArticlesRoot
}

// New creates a Store rooted at articlesRoot.
func New(articlesRoot string) *Store {
	return &Store{root: articlesRoot}
}

// Root returns the articles root directory.
func (s *Store) Root() string { return s.root }

// ArticleDir returns the absolute path to an article directory.
func (s *Store) ArticleDir(id string) string {
	return filepath.Join(s.root, id)
}

// WriteFile writes data to a named file inside an article directory.
func (s *Store) WriteFile(id, filename string, data []byte) error {
	dir := s.ArticleDir(id)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, filename)
	return os.WriteFile(path, data, 0644)
}

// ReadFile reads a named file from an article directory.
func (s *Store) ReadFile(id, filename string) ([]byte, error) {
	path := filepath.Join(s.ArticleDir(id), filename)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return data, nil
}

// ReadBody reads the body.txt for an article.
func (s *Store) ReadBody(a store.Article) (string, error) {
	if a.Files.Body == "" {
		return "", fmt.Errorf("article %s: no body file", a.ID)
	}
	data, err := os.ReadFile(a.Files.Body)
	if err != nil {
		return "", fmt.Errorf("read body %s: %w", a.Files.Body, err)
	}
	return string(data), nil
}

// ReadSummary reads the preferred summary file for an article.
func (s *Store) ReadSummary(a store.Article) (string, error) {
	if a.Files.Summary == "" {
		return "", fmt.Errorf("article %s: no summary file", a.ID)
	}
	data, err := os.ReadFile(a.Files.Summary)
	if err != nil {
		return "", fmt.Errorf("read summary %s: %w", a.Files.Summary, err)
	}
	return string(data), nil
}

// ReadFlash reads the preferred flash file for an article.
func (s *Store) ReadFlash(a store.Article) (string, error) {
	if a.Files.Flash == "" {
		return "", fmt.Errorf("article %s: no flash file", a.ID)
	}
	data, err := os.ReadFile(a.Files.Flash)
	if err != nil {
		return "", fmt.Errorf("read flash %s: %w", a.Files.Flash, err)
	}
	return string(data), nil
}

// ReadFlashcards reads the preferred flashcards file for an article.
func (s *Store) ReadFlashcards(a store.Article) ([]byte, error) {
	if a.Files.Flashcards == "" {
		return nil, fmt.Errorf("article %s: no flashcards file", a.ID)
	}
	data, err := os.ReadFile(a.Files.Flashcards)
	if err != nil {
		return nil, fmt.Errorf("read flashcards %s: %w", a.Files.Flashcards, err)
	}
	return data, nil
}

// Walk calls fn for each valid article directory under the articles root.
// Supports both flat (articles/<slug>/) and nested (articles/<group>/<slug>/) layouts.
// Hidden directories (starting with '.') are skipped.
func (s *Store) Walk(fn func(id string, files store.Files) error) error {
	return s.walkDir(s.root, "", fn)
}

func (s *Store) walkDir(dir, prefix string, fn func(id string, files store.Files) error) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read dir %s: %w", dir, err)
	}

	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		subdir := filepath.Join(dir, entry.Name())
		var id string
		if prefix == "" {
			id = entry.Name()
		} else {
			id = prefix + "/" + entry.Name()
		}
		files := ProbeFiles(subdir)
		if files.Body != "" {
			if err := fn(id, files); err != nil {
				return err
			}
			continue
		}
		// no body found — recurse one more level (collection dir)
		if prefix == "" {
			if err := s.walkDir(subdir, entry.Name(), fn); err != nil {
				return err
			}
		}
	}
	return nil
}

// ReadMeta parses meta.json from an article directory.
// Returns a zero-value Meta and no error if meta.json doesn't exist.
func ReadMeta(metaPath string) (Meta, error) {
	if metaPath == "" {
		return Meta{}, nil
	}
	data, err := os.ReadFile(metaPath)
	if err != nil {
		if os.IsNotExist(err) {
			return Meta{}, nil
		}
		return Meta{}, fmt.Errorf("read meta %s: %w", metaPath, err)
	}
	var m Meta
	if err := json.Unmarshal(data, &m); err != nil {
		return Meta{}, fmt.Errorf("parse meta %s: %w", metaPath, err)
	}
	return m, nil
}

// WriteMeta serialises a Meta to meta.json inside the article directory.
func WriteMeta(dir string, m Meta) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal meta: %w", err)
	}
	path := filepath.Join(dir, "meta.json")
	return os.WriteFile(path, data, 0644)
}

// Meta is the on-disk representation of meta.json.
// It is intentionally separate from store.Article to allow forward compatibility.
type Meta struct {
	ID             string     `json:"id"`
	Title          string     `json:"title"`
	URL            string     `json:"url"`
	SourceType     string     `json:"source_type"`
	Feed           string     `json:"feed,omitempty"`
	Author         string     `json:"author,omitempty"`
	PublishedAt    string     `json:"published_at,omitempty"`
	Language       string     `json:"language,omitempty"`
	IngestedAt     string     `json:"ingested_at"`
	ReadAt         *string    `json:"read_at,omitempty"`
	PlayedAt       *string    `json:"played_at,omitempty"`
	Collections    []string   `json:"collections,omitempty"`
	Tags           []MetaTag  `json:"tags,omitempty"`
	SummaryModel   string     `json:"summary_model,omitempty"`
	SummaryStyle   string     `json:"summary_style,omitempty"`
	FlashModel     string     `json:"flash_model,omitempty"`
	FlashcardModel string     `json:"flashcard_model,omitempty"`
	FlashcardStyle string     `json:"flashcard_style,omitempty"`
	EmbedModel     string     `json:"embed_model,omitempty"`
	QualityScore   float64    `json:"quality_score,omitempty"`
	Relations      []MetaRelation `json:"relations,omitempty"`
}

type MetaTag struct {
	Value  string `json:"value"`
	Source string `json:"source"` // "llm" | "user"
}

type MetaRelation struct {
	Slug        string `json:"slug"`
	Type        string `json:"type"`
	DetectedBy  string `json:"detected_by"`
	DetectedAt  string `json:"detected_at"`
}

// ToArticle converts a Meta + Files into a store.Article.
// id is the article directory name (slug).
func (m Meta) ToArticle(id string, files store.Files) store.Article {
	a := store.Article{
		ID:             nonEmpty(m.ID, id),
		Title:          nonEmpty(m.Title, titleFromID(id)),
		URL:            m.URL,
		SourceType:     m.SourceType,
		Feed:           m.Feed,
		Author:         m.Author,
		PublishedAt:    m.PublishedAt,
		Language:       m.Language,
		Collections:    m.Collections,
		SummaryModel:   m.SummaryModel,
		SummaryStyle:   m.SummaryStyle,
		FlashModel:     m.FlashModel,
		FlashcardModel: m.FlashcardModel,
		FlashcardStyle: m.FlashcardStyle,
		EmbedModel:     m.EmbedModel,
		QualityScore:   m.QualityScore,
		Files:          files,
	}

	if t, err := time.Parse(time.RFC3339, m.IngestedAt); err == nil {
		a.IngestedAt = t
	} else {
		a.IngestedAt = time.Now()
	}

	for _, t := range m.Tags {
		a.Tags = append(a.Tags, store.Tag{
			Value:  t.Value,
			Source: store.TagSource(t.Source),
		})
	}

	for _, r := range m.Relations {
		det, _ := time.Parse(time.RFC3339, r.DetectedAt)
		a.Relations = append(a.Relations, store.Relation{
			ToID:       r.Slug,
			Type:       store.RelationType(r.Type),
			DetectedBy: r.DetectedBy,
			DetectedAt: det,
		})
	}

	return a
}

// ── Collections ───────────────────────────────────────────────────────────────

// CollectionMeta is the on-disk representation of a collection's meta.json.
type CollectionMeta struct {
	Slug        string    `json:"slug"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// CollectionsRoot returns the path to the collections directory.
func CollectionsRoot(dataRoot string) string {
	return filepath.Join(dataRoot, "collections")
}

// CollectionDir returns the path to a specific collection directory.
func CollectionDir(dataRoot, slug string) string {
	return filepath.Join(dataRoot, "collections", slug)
}

// CreateCollection creates a new collection directory and writes meta.json.
// Returns an error if the collection already exists.
func CreateCollection(dataRoot, slug string) error {
	dir := CollectionDir(dataRoot, slug)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir collection %s: %w", slug, err)
	}
	metaPath := filepath.Join(dir, "meta.json")
	if pathExists(metaPath) {
		return nil // already exists — idempotent
	}
	m := CollectionMeta{Slug: slug, Name: slug, CreatedAt: time.Now().UTC()}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(metaPath, data, 0644)
}

// ReadCollectionMeta reads meta.json from a collection directory.
func ReadCollectionMeta(dataRoot, slug string) (CollectionMeta, error) {
	path := filepath.Join(CollectionDir(dataRoot, slug), "meta.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return CollectionMeta{}, fmt.Errorf("collection %q not found", slug)
		}
		return CollectionMeta{}, fmt.Errorf("read collection meta %s: %w", slug, err)
	}
	var m CollectionMeta
	if err := json.Unmarshal(data, &m); err != nil {
		return CollectionMeta{}, fmt.Errorf("parse collection meta %s: %w", slug, err)
	}
	return m, nil
}

// WriteCollectionMeta writes meta.json to a collection directory.
func WriteCollectionMeta(dataRoot string, m CollectionMeta) error {
	path := filepath.Join(CollectionDir(dataRoot, m.Slug), "meta.json")
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// ListCollections walks the collections root and returns metadata for all
// collections found. Missing or malformed meta.json entries are skipped with
// a warning written to slog.
func ListCollections(dataRoot string) ([]CollectionMeta, error) {
	root := CollectionsRoot(dataRoot)
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read collections dir: %w", err)
	}
	var cols []CollectionMeta
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		m, err := ReadCollectionMeta(dataRoot, e.Name())
		if err != nil {
			continue // skip malformed
		}
		cols = append(cols, m)
	}
	return cols, nil
}

// AddArticleToCollection creates a relative symlink from the collection
// directory to the article directory. Returns ErrAlreadyInCollection if the
// symlink already exists.
func AddArticleToCollection(dataRoot, articlesRoot, articleSlug, collectionSlug string) error {
	colDir := CollectionDir(dataRoot, collectionSlug)
	linkPath := filepath.Join(colDir, articleSlug)

	// Check if symlink already exists.
	if _, err := os.Lstat(linkPath); err == nil {
		return ErrAlreadyInCollection
	}

	// Compute relative path from collection dir to article dir.
	articleDir := filepath.Join(articlesRoot, articleSlug)
	rel, err := filepath.Rel(colDir, articleDir)
	if err != nil {
		return fmt.Errorf("compute rel path: %w", err)
	}
	return os.Symlink(rel, linkPath)
}

// ErrAlreadyInCollection is returned when an article is already linked to the collection.
var ErrAlreadyInCollection = fmt.Errorf("article already in collection")

// RemoveArticleFromCollection removes the symlink for an article from a collection.
func RemoveArticleFromCollection(dataRoot, collectionSlug, articleSlug string) error {
	linkPath := filepath.Join(CollectionDir(dataRoot, collectionSlug), articleSlug)
	info, err := os.Lstat(linkPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("article %q not in collection %q", articleSlug, collectionSlug)
		}
		return err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("%s is not a symlink — refusing to delete", linkPath)
	}
	return os.Remove(linkPath)
}

// RenameCollection moves a collection directory to a new slug and updates meta.json.
// Symlinks inside the directory are relative and remain valid after the move.
func RenameCollection(dataRoot, oldSlug, newSlug string) error {
	oldDir := CollectionDir(dataRoot, oldSlug)
	newDir := CollectionDir(dataRoot, newSlug)

	if _, err := os.Stat(oldDir); os.IsNotExist(err) {
		return fmt.Errorf("collection %q not found", oldSlug)
	}
	if _, err := os.Stat(newDir); err == nil {
		return fmt.Errorf("collection %q already exists", newSlug)
	}

	if err := os.Rename(oldDir, newDir); err != nil {
		return fmt.Errorf("rename collection dir: %w", err)
	}

	// Update meta.json with new slug and name.
	m, err := ReadCollectionMeta(dataRoot, newSlug)
	if err != nil {
		return fmt.Errorf("read meta after rename: %w", err)
	}
	m.Slug = newSlug
	m.Name = newSlug
	return WriteCollectionMeta(dataRoot, m)
}

// DeleteCollection removes the collection directory and all symlinks inside it.
// Article directories are never touched.
func DeleteCollection(dataRoot, slug string) error {
	dir := CollectionDir(dataRoot, slug)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("collection %q not found", slug)
	}
	return os.RemoveAll(dir)
}

// ListCollectionArticles returns the article slugs linked in a collection.
// Broken symlinks are reported in the broken return value.
func ListCollectionArticles(dataRoot, slug string) (articles []string, broken []string, err error) {
	dir := CollectionDir(dataRoot, slug)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("read collection dir %s: %w", slug, err)
	}
	for _, e := range entries {
		// Only process symlinks — skip meta.json, system.txt, resources/, chat/
		info, err := os.Lstat(filepath.Join(dir, e.Name()))
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		target := filepath.Join(dir, e.Name())
		if _, err := os.Stat(target); err != nil {
			broken = append(broken, e.Name())
			continue
		}
		articles = append(articles, e.Name())
	}
	return articles, broken, nil
}

// ScanCollectionMembership walks all collection directories and returns a
// map of articleSlug → []collectionSlug. Used by reindex to rebuild SQLite.
// Broken symlinks are logged and skipped.
func ScanCollectionMembership(dataRoot string) (map[string][]string, error) {
	root := CollectionsRoot(dataRoot)
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read collections root: %w", err)
	}

	result := make(map[string][]string)
	for _, colEntry := range entries {
		if !colEntry.IsDir() || strings.HasPrefix(colEntry.Name(), ".") {
			continue
		}
		colSlug := colEntry.Name()
		colDir := filepath.Join(root, colSlug)

		items, err := os.ReadDir(colDir)
		if err != nil {
			continue
		}
		for _, item := range items {
			linfo, err := os.Lstat(filepath.Join(colDir, item.Name()))
			if err != nil || linfo.Mode()&os.ModeSymlink == 0 {
				continue
			}
			target := filepath.Join(colDir, item.Name())
			if _, err := os.Stat(target); err != nil {
				// broken symlink
				continue
			}
			result[item.Name()] = append(result[item.Name()], colSlug)
		}
	}
	return result, nil
}

// MigrateMetaCollections reads meta.json collections fields from articles and
// creates the corresponding symlinks if they don't already exist. Called once
// during reindex to migrate from the old collections.json approach.
func MigrateMetaCollections(dataRoot, articlesRoot string) error {
	entries, err := os.ReadDir(articlesRoot)
	if err != nil {
		return nil // articles root doesn't exist yet
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		metaPath := filepath.Join(articlesRoot, e.Name(), "meta.json")
		data, err := os.ReadFile(metaPath)
		if err != nil {
			continue
		}
		var m struct {
			Collections []string `json:"collections"`
		}
		if err := json.Unmarshal(data, &m); err != nil || len(m.Collections) == 0 {
			continue
		}
		for _, col := range m.Collections {
			if col == "" {
				continue
			}
			// Ensure collection dir exists
			_ = CreateCollection(dataRoot, col)
			// Create symlink — ignore ErrAlreadyInCollection
			err := AddArticleToCollection(dataRoot, articlesRoot, e.Name(), col)
			if err != nil && err != ErrAlreadyInCollection {
				// non-fatal
				_ = err
			}
		}
	}
	return nil
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func nonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// titleFromID converts "20260521-sparse-attention-survey" → "Sparse Attention Survey"
// Also handles nested IDs like "anthropic/claude-code-guide" → "Claude Code Guide"
func titleFromID(id string) string {
	// use only the last path segment
	s := id
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	// strip leading date prefix if present (YYYYMMDD-)
	if len(s) >= 9 && s[8] == '-' {
		allDigits := true
		for _, c := range s[:8] {
			if !unicode.IsDigit(c) {
				allDigits = false
				break
			}
		}
		if allDigits {
			s = s[9:]
		}
	}
	s = strings.ReplaceAll(s, "-", " ")
	if len(s) == 0 {
		return id
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

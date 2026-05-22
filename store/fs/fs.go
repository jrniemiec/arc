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
// Hidden directories (starting with '.') and files at depth < 2 are skipped.
func (s *Store) Walk(fn func(id string, files store.Files) error) error {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return fmt.Errorf("read articles root %s: %w", s.root, err)
	}

	for _, entry := range entries {
		// skip files and hidden dirs at root level
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		// skip collections.json and other files
		dir := filepath.Join(s.root, entry.Name())
		files := ProbeFiles(dir)
		if files.Body == "" {
			// not a valid article dir
			continue
		}
		if err := fn(entry.Name(), files); err != nil {
			return err
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

// CollectionsFile returns the path to collections.json in the articles root.
func CollectionsFile(articlesRoot string) string {
	return filepath.Join(articlesRoot, "collections.json")
}

// ReadCollections reads collections.json from the articles root.
func ReadCollections(articlesRoot string) ([]store.Collection, error) {
	path := CollectionsFile(articlesRoot)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read collections: %w", err)
	}

	var raw []struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		CreatedAt   string `json:"created_at"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse collections: %w", err)
	}

	cols := make([]store.Collection, 0, len(raw))
	for _, r := range raw {
		t, _ := time.Parse(time.RFC3339, r.CreatedAt)
		cols = append(cols, store.Collection{
			ID:          r.ID,
			Name:        r.Name,
			Description: r.Description,
			CreatedAt:   t,
		})
	}
	return cols, nil
}

// WriteCollections writes the collections list to collections.json.
func WriteCollections(articlesRoot string, cols []store.Collection) error {
	type raw struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
		CreatedAt   string `json:"created_at"`
	}
	out := make([]raw, 0, len(cols))
	for _, c := range cols {
		out = append(out, raw{
			ID:          c.ID,
			Name:        c.Name,
			Description: c.Description,
			CreatedAt:   c.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(CollectionsFile(articlesRoot), data, 0644)
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
func titleFromID(id string) string {
	// strip leading date prefix if present (YYYYMMDD-)
	s := id
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

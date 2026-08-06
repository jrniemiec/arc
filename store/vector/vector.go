// Package vector wraps chromem-go for arc's persistent vector index.
// All articles are stored in a single collection ("articles").
// Embeddings are pre-computed externally and passed in — this package
// does not call any embedding API.
package vector

import (
	"context"
	"fmt"
	"log/slog"

	chromem "github.com/philippgille/chromem-go"
)

const collectionName = "articles"

// Store is the vector index for arc articles.
type Store struct {
	db         *chromem.DB
	collection *chromem.Collection
}

// Result is a single similarity search result.
type Result struct {
	ID         string
	Similarity float32 // cosine similarity [0, 1]
}

// Open opens (or creates) the persistent vector index at the given directory.
func Open(path string) (*Store, error) {
	db, err := chromem.NewPersistentDB(path, false)
	if err != nil {
		return nil, fmt.Errorf("open vector db: %w", err)
	}

	// nil EmbeddingFunc — we always supply pre-computed embeddings.
	col, err := db.GetOrCreateCollection(collectionName, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("get collection: %w", err)
	}

	return &Store{db: db, collection: col}, nil
}

// Upsert adds or replaces a document in the index.
// embedding must be the pre-computed vector for the document text.
// If a document with the same id already exists it is deleted first.
func (s *Store) Upsert(ctx context.Context, id string, embedding []float32, text string) error {
	// chromem-go has no native upsert — delete then add.
	_ = s.collection.Delete(ctx, nil, nil, id)

	doc := chromem.Document{
		ID:        id,
		Embedding: embedding,
		Content:   text,
	}
	if err := s.collection.AddDocument(ctx, doc); err != nil {
		return fmt.Errorf("vector upsert %s: %w", id, err)
	}
	return nil
}

// Delete removes a document from the index. No-op if not present.
func (s *Store) Delete(ctx context.Context, id string) error {
	return s.collection.Delete(ctx, nil, nil, id)
}

// Has reports whether the index holds a document for the given id.
// This is the authoritative check for "is this article embedded" — meta.json
// records the embed model but can disagree with the index (e.g. after the
// index directory is lost or copied between machines).
func (s *Store) Has(ctx context.Context, id string) bool {
	_, err := s.collection.GetByID(ctx, id)
	return err == nil
}

// Query returns the top-n most similar documents to the given embedding.
// Results with similarity below minSimilarity are excluded; pass 0 for no filter.
func (s *Store) Query(ctx context.Context, embedding []float32, n int, minSimilarity float32) ([]Result, error) {
	if s.collection.Count() == 0 {
		return nil, nil
	}
	if n > s.collection.Count() {
		n = s.collection.Count()
	}
	results, err := s.collection.QueryEmbedding(ctx, embedding, n, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("vector query: %w", err)
	}
	// Log every candidate with its raw score, kept or not. Without this a
	// threshold set too high is indistinguishable from an empty index: both
	// return zero hits and no error.
	out := make([]Result, 0, len(results))
	dropped := 0
	for _, r := range results {
		if r.Similarity >= minSimilarity {
			slog.Debug("vector hit", "id", r.ID, "similarity", r.Similarity, "min", minSimilarity)
			out = append(out, Result{ID: r.ID, Similarity: r.Similarity})
			continue
		}
		dropped++
		slog.Debug("vector hit below threshold", "id", r.ID, "similarity", r.Similarity, "min", minSimilarity)
	}
	if len(out) == 0 && dropped > 0 {
		slog.Info("vector search: all candidates below threshold",
			"candidates", dropped, "min", minSimilarity, "best", results[0].Similarity)
	}
	return out, nil
}

// Count returns the number of documents in the index.
func (s *Store) Count() int {
	return s.collection.Count()
}

// Package vector wraps chromem-go for arc's persistent vector index.
// All articles are stored in a single collection ("articles").
// Embeddings are pre-computed externally and passed in — this package
// does not call any embedding API.
package vector

import (
	"context"
	"fmt"

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
	out := make([]Result, 0, len(results))
	for _, r := range results {
		if r.Similarity >= minSimilarity {
			out = append(out, Result{ID: r.ID, Similarity: r.Similarity})
		}
	}
	return out, nil
}

// Count returns the number of documents in the index.
func (s *Store) Count() int {
	return s.collection.Count()
}

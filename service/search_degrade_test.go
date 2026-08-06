package service_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jrniemiec/arc/config"
	"github.com/jrniemiec/arc/service"
	"github.com/jrniemiec/arc/store"
)

// A combined search whose semantic half fails must still return keyword hits
// and say so. Silently degrading to keyword-only is indistinguishable from a
// working search, which is how a revoked embedding key went unnoticed.
func TestCombinedSearchWarnsWhenSemanticHalfFails(t *testing.T) {
	svc, _ := newTestService(t)

	// No embedding credentials — embedQuery fails, the vector half with it.
	t.Setenv("ARC_OPENAI_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")

	res, err := svc.Search(context.Background(), service.SearchRequest{
		Query: "sparse attention",
		Mode:  store.QueryCombined,
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("combined search must survive a failing semantic half: %v", err)
	}
	if len(res.Hits) == 0 {
		t.Fatal("expected keyword hits from the surviving FTS half")
	}
	if res.Warning == "" {
		t.Fatal("semantic half failed but Warning was empty — the degradation is invisible")
	}
	if !strings.Contains(res.Warning, "semantic search unavailable") {
		t.Errorf("Warning does not name the failing half: %q", res.Warning)
	}
	// The warning is rendered on one line next to results; the full error goes to the log.
	if strings.Contains(res.Warning, "\n") {
		t.Errorf("Warning must be a single line, got %q", res.Warning)
	}
	for _, h := range res.Hits {
		if h.Source != "fts" {
			t.Errorf("expected fts-only hits, got source %q", h.Source)
		}
	}
}

// Keyword mode asks for no semantic half, so a missing key is not a degradation.
func TestKeywordSearchDoesNotWarn(t *testing.T) {
	svc, _ := newTestService(t)

	t.Setenv("ARC_OPENAI_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")

	res, err := svc.Search(context.Background(), service.SearchRequest{
		Query: "sparse attention",
		Mode:  store.QueryKeyword,
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("keyword search: %v", err)
	}
	if res.Warning != "" {
		t.Errorf("keyword-only search must not warn, got %q", res.Warning)
	}
}

// Semantic mode has nothing to fall back to: reporting zero hits would claim
// the library holds nothing on the topic, so the failure must be fatal.
func TestSemanticSearchFailsLoudlyWithNoFallback(t *testing.T) {
	svc, _ := newTestService(t)

	t.Setenv("ARC_OPENAI_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")

	res, err := svc.Search(context.Background(), service.SearchRequest{
		Query: "sparse attention",
		Mode:  store.QuerySemantic,
		Limit: 10,
	})
	if err == nil {
		t.Fatalf("expected an error, got %d hits", len(res.Hits))
	}
}

func TestMinSimilarityComesFromConfig(t *testing.T) {
	if config.Default().Search.MinSimilarity != config.DefaultMinSimilarity {
		t.Fatal("Default() must set Search.MinSimilarity explicitly, not leave a Go zero")
	}
	if config.DefaultMinSimilarity >= 0.5 {
		t.Fatalf("default floor %.2f is back in unreachable territory for text-embedding-3-small",
			config.DefaultMinSimilarity)
	}
}

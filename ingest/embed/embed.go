// Package embed generates vector embeddings for arc articles via OpenAI.
package embed

import (
	"context"
	"fmt"
	"os"
	"strings"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// Result holds the output of a single embedding call.
type Result struct {
	Embedding []float32
	Tokens    int // input tokens consumed
}

// Client calls the OpenAI embeddings API.
type Client struct {
	client openai.Client
	model  string
}

// NewClient creates an embedding client for the given model.
// API key is read from ARC_OPENAI_API_KEY or OPENAI_API_KEY.
func NewClient(model string) (*Client, error) {
	apiKey := ""
	for _, k := range []string{"ARC_OPENAI_API_KEY", "OPENAI_API_KEY"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			apiKey = v
			break
		}
	}
	if apiKey == "" {
		return nil, fmt.Errorf("embed: OPENAI_API_KEY not set")
	}
	return &Client{
		client: openai.NewClient(option.WithAPIKey(apiKey)),
		model:  model,
	}, nil
}

// Embed generates an embedding for the given text.
func (c *Client) Embed(ctx context.Context, text string) (Result, error) {
	resp, err := c.client.Embeddings.New(ctx, openai.EmbeddingNewParams{
		Input: openai.EmbeddingNewParamsInputUnion{
			OfString: openai.String(text),
		},
		Model:          openai.EmbeddingModel(c.model),
		EncodingFormat: openai.EmbeddingNewParamsEncodingFormatFloat,
	})
	if err != nil {
		return Result{}, fmt.Errorf("embed %q: %w", c.model, err)
	}
	if len(resp.Data) == 0 {
		return Result{}, fmt.Errorf("embed: empty response from API")
	}

	// Convert []float64 → []float32 (chromem-go uses float32)
	f64 := resp.Data[0].Embedding
	f32 := make([]float32, len(f64))
	for i, v := range f64 {
		f32[i] = float32(v)
	}

	return Result{
		Embedding: f32,
		Tokens:    int(resp.Usage.PromptTokens),
	}, nil
}

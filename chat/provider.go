package chat

import "context"

// Usage holds token counts for a single LLM call.
type Usage struct {
	InputTokens  int
	OutputTokens int
}

// Provider is the interface all LLM backends implement.
type Provider interface {
	Name() string
	Chat(ctx context.Context, systemPrompt string, messages []Message) (string, Usage, error)
	ChatStream(ctx context.Context, systemPrompt string, messages []Message, onDelta func(string) error) (string, Usage, error)
}

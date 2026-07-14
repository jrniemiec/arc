package chat

import (
	"context"
	"encoding/json"
)

// Usage holds token counts for a single LLM call.
type Usage struct {
	InputTokens  int
	OutputTokens int
}

// ToolDef describes a tool the model may invoke.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// ContentBlock represents one block in a model response.
// Either Text or ToolUse fields are populated, never both.
type ContentBlock struct {
	Type string // "text" or "tool_use"

	// Text block.
	Text string

	// Tool-use block.
	ToolUseID    string
	ToolUseName  string
	ToolUseInput json.RawMessage
}

// StreamResponse is the complete result of a streamed tool-aware request.
type StreamResponse struct {
	Content    []ContentBlock
	StopReason string // "end_turn" or "tool_use"
	Usage      Usage
}

// Provider is the interface all LLM backends implement.
type Provider interface {
	Name() string
	Chat(ctx context.Context, systemPrompt string, messages []Message) (string, Usage, error)
	ChatStream(ctx context.Context, systemPrompt string, messages []Message, onDelta func(string) error) (string, Usage, error)

	// ChatStreamWithTools sends a streaming request with tool definitions.
	// The provider translates internal message types (including tool-result)
	// to the wire format expected by the backend.
	ChatStreamWithTools(
		ctx context.Context,
		systemPrompt string,
		messages []Message,
		tools []ToolDef,
		onTextDelta func(string) error,
		onToolStart func(toolName string) error,
	) (StreamResponse, error)
}

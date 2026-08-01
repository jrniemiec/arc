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
// For Anthropic server tools (e.g. web_search), Type is set and
// Description/InputSchema are empty — the server handles execution.
type ToolDef struct {
	Type        string          `json:"type,omitempty"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

// ContentBlock represents one block in a model response.
// Type is "text", "tool_use", "server_tool_use", or "web_search_tool_result".
type ContentBlock struct {
	Type string

	// Text block.
	Text string

	// Tool-use block (client-side tool_use).
	ToolUseID    string
	ToolUseName  string
	ToolUseInput json.RawMessage

	// Raw preserves the original JSON block for server tool content
	// (server_tool_use, web_search_tool_result) that must be passed back
	// verbatim on subsequent turns (e.g. encrypted_content).
	Raw json.RawMessage
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

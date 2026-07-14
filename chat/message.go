package chat

import (
	"encoding/json"
	"time"
)

const (
	RoleSystem     = "system"
	RoleUser       = "user"
	RoleAssistant  = "assistant"
	RoleNote       = "note"        // personal note, never sent to LLM
	RoleToolResult = "tool-result" // tool execution result, correlated by ToolCallID
)

// ToolCall represents a single tool invocation requested by the model.
type ToolCall struct {
	ID    string          `json:"id"`    // provider-assigned, e.g. "toolu_abc123"
	Name  string          `json:"name"`  // "read_article", "search_workspace", etc.
	Input json.RawMessage `json:"input"` // raw JSON args
}

// Message is a single turn in a conversation.
type Message struct {
	Role    string    `json:"role"`
	Content string    `json:"content"`
	Time    time.Time `json:"time,omitempty"`
	Profile string    `json:"profile,omitempty"` // profile that produced this message (assistant only)

	// Tool fields — zero values for pre-tool messages; omitted from JSON when empty.
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`   // assistant only: tool invocations requested
	ToolCallID string     `json:"tool_call_id,omitempty"` // tool-result only: correlates to ToolCall.ID

	// Commented messages are kept in history but excluded from LLM context.
	Commented bool `json:"commented,omitempty"`
}

// History is the full append-only message log for a workspace chat.
type History struct {
	Msgs []Message `json:"messages"`
}

func NewHistory() *History {
	return &History{Msgs: []Message{}}
}

func (h *History) Append(role, content string) {
	h.Msgs = append(h.Msgs, Message{
		Role:    role,
		Content: content,
		Time:    time.Now(),
	})
}

// AppendAssistant appends an assistant message with an explicit timestamp and
// profile name. The caller captures time.Now() once so the events.jsonl entry
// shares the exact same timestamp.
func (h *History) AppendAssistant(content, profile string, ts time.Time) {
	h.Msgs = append(h.Msgs, Message{
		Role:    RoleAssistant,
		Content: content,
		Time:    ts,
		Profile: profile,
	})
}

// CollapseForContext reduces history to user + final-assistant pairs,
// stripping tool-result messages and intermediate assistant messages
// (those with ToolCalls). Notes are also stripped.
//
// This is the primary method for building API context: prior turns
// contribute only user text and the assistant's final answer. Tool
// blocks are never re-sent — if the model needs old content, it
// re-fetches via tools.
func (h *History) CollapseForContext() []Message {
	if h == nil || len(h.Msgs) == 0 {
		return nil
	}
	out := make([]Message, 0, len(h.Msgs))
	for _, m := range h.Msgs {
		switch {
		case m.Role == RoleNote:
			continue
		case m.Commented:
			continue
		case m.Role == RoleToolResult:
			continue
		case m.Role == RoleAssistant && len(m.ToolCalls) > 0:
			// Intermediate assistant message (requested tools) — skip.
			continue
		default:
			out = append(out, m)
		}
	}
	return out
}


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

// NonNoteMsgs returns all messages except notes, for LLM context building.
func (h *History) NonNoteMsgs() []Message {
	out := make([]Message, 0, len(h.Msgs))
	for _, m := range h.Msgs {
		if m.Role != RoleNote {
			out = append(out, m)
		}
	}
	return out
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

// ToMessages returns messages covering the last maxUserMessages user turns,
// excluding note messages which are never sent to the LLM.
func (h *History) ToMessages(maxUserMessages int) []Message {
	if h == nil || len(h.Msgs) == 0 {
		return nil
	}
	userCount := 0
	start := 0
	for i := len(h.Msgs) - 1; i >= 0; i-- {
		if h.Msgs[i].Role == RoleUser {
			userCount++
			if userCount >= maxUserMessages {
				start = i
				break
			}
		}
	}
	msgs := h.Msgs[start:]
	out := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		if m.Role != RoleNote {
			out = append(out, m)
		}
	}
	return out
}

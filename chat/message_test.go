package chat

import (
	"encoding/json"
	"testing"
	"time"
)

func toolCalls() []ToolCall {
	return []ToolCall{{ID: "tid", Name: "read_article", Input: json.RawMessage(`{}`)}}
}

func msg(role, content string) Message {
	return Message{Role: role, Content: content, Time: time.Now()}
}

func msgAt(role, content string, ts time.Time) Message {
	return Message{Role: role, Content: content, Time: ts}
}

func commented(m Message) Message {
	m.Commented = true
	return m
}

func withTools(m Message) Message {
	m.ToolCalls = toolCalls()
	return m
}

func toolResult(content string) Message {
	return Message{Role: RoleToolResult, Content: content, ToolCallID: "tid", Time: time.Now()}
}

func roles(msgs []Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Role
	}
	return out
}

func contents(msgs []Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Content
	}
	return out
}

func TestCollapseForContext_Empty(t *testing.T) {
	h := NewHistory()
	got := h.CollapseForContext()
	if got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestCollapseForContext_NilHistory(t *testing.T) {
	var h *History
	got := h.CollapseForContext()
	if got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestCollapseForContext_PlainConversation(t *testing.T) {
	h := &History{Msgs: []Message{
		msg(RoleUser, "hello"),
		msg(RoleAssistant, "hi"),
	}}
	got := h.CollapseForContext()
	if len(got) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(got))
	}
	if got[0].Role != RoleUser || got[1].Role != RoleAssistant {
		t.Errorf("unexpected roles: %v", roles(got))
	}
}

func TestCollapseForContext_StripsNotes(t *testing.T) {
	h := &History{Msgs: []Message{
		msg(RoleUser, "u1"),
		msg(RoleNote, "my note"),
		msg(RoleAssistant, "a1"),
	}}
	got := h.CollapseForContext()
	if len(got) != 2 {
		t.Fatalf("expected 2, got %d: %v", len(got), roles(got))
	}
	for _, m := range got {
		if m.Role == RoleNote {
			t.Error("note should be stripped")
		}
	}
}

func TestCollapseForContext_StripsToolResult(t *testing.T) {
	h := &History{Msgs: []Message{
		msg(RoleUser, "u1"),
		withTools(msg(RoleAssistant, "")),
		toolResult("tool output"),
		msg(RoleAssistant, "final answer"),
	}}
	got := h.CollapseForContext()
	if len(got) != 2 {
		t.Fatalf("expected 2, got %d: %v", len(got), roles(got))
	}
	if got[0].Role != RoleUser {
		t.Errorf("expected user first, got %s", got[0].Role)
	}
	if got[1].Content != "final answer" {
		t.Errorf("expected final assistant, got %q", got[1].Content)
	}
}

func TestCollapseForContext_StripsIntermediateAssistant(t *testing.T) {
	intermediate := withTools(msg(RoleAssistant, "calling tools"))
	h := &History{Msgs: []Message{
		msg(RoleUser, "u1"),
		intermediate,
		toolResult("result"),
		msg(RoleAssistant, "final"),
	}}
	got := h.CollapseForContext()
	for _, m := range got {
		if m.Role == RoleAssistant && len(m.ToolCalls) > 0 {
			t.Error("intermediate assistant (with tool calls) should be stripped")
		}
	}
}

func TestCollapseForContext_StripsCommented(t *testing.T) {
	h := &History{Msgs: []Message{
		commented(msg(RoleUser, "u1")),
		commented(msg(RoleAssistant, "a1")),
	}}
	got := h.CollapseForContext()
	if len(got) != 0 {
		t.Errorf("expected 0 messages, got %d", len(got))
	}
}

func TestCollapseForContext_CommentedMidConversation(t *testing.T) {
	h := &History{Msgs: []Message{
		msg(RoleUser, "u1"),
		msg(RoleAssistant, "a1"),
		commented(msg(RoleUser, "u2")),
		commented(msg(RoleAssistant, "a2")),
		msg(RoleUser, "u3"),
		msg(RoleAssistant, "a3"),
	}}
	got := h.CollapseForContext()
	if len(got) != 4 {
		t.Fatalf("expected 4, got %d: %v", len(got), contents(got))
	}
	want := []string{"u1", "a1", "u3", "a3"}
	for i, w := range want {
		if got[i].Content != w {
			t.Errorf("msg[%d]: want %q got %q", i, w, got[i].Content)
		}
	}
}

func TestCollapseForContext_MultiRoundToolLoop(t *testing.T) {
	// user → asst(tools) → tool-result → asst(tools) → tool-result → asst(final)
	h := &History{Msgs: []Message{
		msg(RoleUser, "question"),
		withTools(msg(RoleAssistant, "")),
		toolResult("r1"),
		withTools(msg(RoleAssistant, "")),
		toolResult("r2"),
		msg(RoleAssistant, "final answer"),
	}}
	got := h.CollapseForContext()
	if len(got) != 2 {
		t.Fatalf("expected 2, got %d: %v", len(got), roles(got))
	}
	if got[0].Role != RoleUser || got[1].Content != "final answer" {
		t.Errorf("unexpected result: %v", contents(got))
	}
}

func TestCollapseForContext_MultiTurn(t *testing.T) {
	// Two complete turns with tool loops each.
	h := &History{Msgs: []Message{
		msg(RoleUser, "q1"),
		withTools(msg(RoleAssistant, "")),
		toolResult("r1"),
		msg(RoleAssistant, "a1"),
		msg(RoleUser, "q2"),
		withTools(msg(RoleAssistant, "")),
		toolResult("r2"),
		msg(RoleAssistant, "a2"),
	}}
	got := h.CollapseForContext()
	if len(got) != 4 {
		t.Fatalf("expected 4, got %d: %v", len(got), roles(got))
	}
	want := []string{"q1", "a1", "q2", "a2"}
	for i, w := range want {
		if got[i].Content != w {
			t.Errorf("msg[%d]: want %q got %q", i, w, got[i].Content)
		}
	}
}

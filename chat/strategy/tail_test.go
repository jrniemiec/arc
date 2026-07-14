package strategy

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jrniemiec/arc/chat"
)

func uMsg(content string) chat.Message {
	return chat.Message{Role: chat.RoleUser, Content: content, Time: time.Now()}
}

func aMsg(content string) chat.Message {
	return chat.Message{Role: chat.RoleAssistant, Content: content, Time: time.Now()}
}

func noteMsg(content string) chat.Message {
	return chat.Message{Role: chat.RoleNote, Content: content, Time: time.Now()}
}

func toolResultMsg(content string) chat.Message {
	return chat.Message{Role: chat.RoleToolResult, Content: content, ToolCallID: "tid", Time: time.Now()}
}

func intermediateAssistant() chat.Message {
	return chat.Message{
		Role:      chat.RoleAssistant,
		Content:   "",
		ToolCalls: []chat.ToolCall{{ID: "tid", Name: "read_article", Input: json.RawMessage(`{}`)}},
		Time:      time.Now(),
	}
}

func commented(m chat.Message) chat.Message {
	m.Commented = true
	return m
}

func contents(msgs []chat.Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Content
	}
	return out
}

func TestTail_Empty(t *testing.T) {
	s := &TailStrategy{MaxUserMessages: 50}
	got := s.Apply(&chat.History{Msgs: []chat.Message{}}, "")
	if got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestTail_FewerTurnsThanLimit(t *testing.T) {
	s := &TailStrategy{MaxUserMessages: 50}
	h := &chat.History{Msgs: []chat.Message{
		uMsg("u1"), aMsg("a1"),
		uMsg("u2"), aMsg("a2"),
	}}
	got := s.Apply(h, "")
	if len(got) != 4 {
		t.Errorf("expected 4, got %d", len(got))
	}
}

func TestTail_TrimsToLastNUserTurns(t *testing.T) {
	s := &TailStrategy{MaxUserMessages: 2}
	h := &chat.History{Msgs: []chat.Message{
		uMsg("u1"), aMsg("a1"),
		uMsg("u2"), aMsg("a2"),
		uMsg("u3"), aMsg("a3"),
	}}
	got := s.Apply(h, "")
	// Should contain u2,a2,u3,a3.
	if len(got) != 4 {
		t.Fatalf("expected 4, got %d: %v", len(got), contents(got))
	}
	if got[0].Content != "u2" {
		t.Errorf("first message should be u2, got %q", got[0].Content)
	}
}

func TestTail_StripsToolMessages(t *testing.T) {
	s := &TailStrategy{MaxUserMessages: 50}
	h := &chat.History{Msgs: []chat.Message{
		uMsg("question"),
		intermediateAssistant(),
		toolResultMsg("tool output"),
		aMsg("final answer"),
	}}
	got := s.Apply(h, "")
	if len(got) != 2 {
		t.Fatalf("expected 2, got %d: %v", len(got), contents(got))
	}
	if got[1].Content != "final answer" {
		t.Errorf("expected final answer, got %q", got[1].Content)
	}
}

func TestTail_StripsNotes(t *testing.T) {
	s := &TailStrategy{MaxUserMessages: 50}
	h := &chat.History{Msgs: []chat.Message{
		uMsg("u1"),
		noteMsg("my note"),
		aMsg("a1"),
	}}
	got := s.Apply(h, "")
	if len(got) != 2 {
		t.Fatalf("expected 2, got %d", len(got))
	}
	for _, m := range got {
		if m.Role == chat.RoleNote {
			t.Error("note should be stripped")
		}
	}
}

func TestTail_StripsCommented(t *testing.T) {
	s := &TailStrategy{MaxUserMessages: 50}
	h := &chat.History{Msgs: []chat.Message{
		commented(uMsg("u1")),
		commented(aMsg("a1")),
		uMsg("u2"),
		aMsg("a2"),
	}}
	got := s.Apply(h, "")
	if len(got) != 2 {
		t.Fatalf("expected 2, got %d: %v", len(got), contents(got))
	}
	if got[0].Content != "u2" || got[1].Content != "a2" {
		t.Errorf("unexpected: %v", contents(got))
	}
}

func TestTail_MaxUserZeroMeansNoLimit(t *testing.T) {
	s := &TailStrategy{MaxUserMessages: 0}
	var msgs []chat.Message
	for i := 0; i < 10; i++ {
		msgs = append(msgs, uMsg("u"), aMsg("a"))
	}
	h := &chat.History{Msgs: msgs}
	got := s.Apply(h, "")
	if len(got) != 20 {
		t.Errorf("expected 20, got %d", len(got))
	}
}

func TestTail_CommentedTurnsDontCountTowardLimit(t *testing.T) {
	// With limit=2, commented turns should not count. We should get u2,a2,u3,a3.
	s := &TailStrategy{MaxUserMessages: 2}
	h := &chat.History{Msgs: []chat.Message{
		commented(uMsg("u1")),
		commented(aMsg("a1")),
		uMsg("u2"), aMsg("a2"),
		uMsg("u3"), aMsg("a3"),
	}}
	got := s.Apply(h, "")
	if len(got) != 4 {
		t.Fatalf("expected 4, got %d: %v", len(got), contents(got))
	}
	if got[0].Content != "u2" {
		t.Errorf("first should be u2, got %q", got[0].Content)
	}
}

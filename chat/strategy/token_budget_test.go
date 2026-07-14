package strategy

import (
	"testing"

	"github.com/jrniemiec/arc/chat"
)

// approxContent returns a string of approximately n*4 bytes so ApproxTokens returns ~n.
func approxContent(tokens int) string {
	b := make([]byte, tokens*4)
	for i := range b {
		b[i] = 'x'
	}
	return string(b)
}

func TestTokenBudget_Empty(t *testing.T) {
	s := &TokenBudgetStrategy{Budget: 1000}
	got := s.Apply(&chat.History{Msgs: []chat.Message{}}, "")
	if got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestTokenBudget_AllFits(t *testing.T) {
	s := &TokenBudgetStrategy{Budget: 10000}
	h := &chat.History{Msgs: []chat.Message{
		uMsg("u1"), aMsg("a1"),
		uMsg("u2"), aMsg("a2"),
	}}
	got := s.Apply(h, "")
	if len(got) != 4 {
		t.Errorf("expected 4, got %d", len(got))
	}
}

func TestTokenBudget_DropsOldestToFit(t *testing.T) {
	// Each message is ~500 tokens; budget fits only 2.
	large := approxContent(500)
	s := &TokenBudgetStrategy{Budget: 1200}
	h := &chat.History{Msgs: []chat.Message{
		uMsg(large), aMsg(large),
		uMsg("u2"), aMsg("a2"),
	}}
	got := s.Apply(h, "")
	// u2 and a2 are tiny; the large ones should be dropped.
	for _, m := range got {
		if m.Content == large {
			t.Error("large old messages should have been dropped")
		}
	}
	if len(got) == 0 {
		t.Error("expected at least the recent small messages")
	}
}

func TestTokenBudget_StripsToolMessages(t *testing.T) {
	s := &TokenBudgetStrategy{Budget: 10000}
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
	if got[0].Role != chat.RoleUser || got[1].Content != "final answer" {
		t.Errorf("unexpected result: %v", contents(got))
	}
}

func TestTokenBudget_ZeroBudget(t *testing.T) {
	s := &TokenBudgetStrategy{Budget: 0}
	h := &chat.History{Msgs: []chat.Message{uMsg("u"), aMsg("a")}}
	got := s.Apply(h, "")
	if got != nil {
		t.Errorf("expected nil for zero budget, got %v", got)
	}
}

func TestTokenBudget_BudgetTooTightForAnything(t *testing.T) {
	large := approxContent(5000)
	s := &TokenBudgetStrategy{Budget: 10}
	h := &chat.History{Msgs: []chat.Message{uMsg(large), aMsg(large)}}
	got := s.Apply(h, "")
	if got != nil {
		t.Errorf("expected nil when nothing fits, got %v", got)
	}
}

func TestTokenBudget_StripsCommented(t *testing.T) {
	s := &TokenBudgetStrategy{Budget: 10000}
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
	if got[0].Content != "u2" {
		t.Errorf("first should be u2, got %q", got[0].Content)
	}
}

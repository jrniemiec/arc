package strategy

import (
	"context"
	"testing"
	"time"

	"github.com/jrniemiec/arc/chat"
)

// --- mocks -------------------------------------------------------------------

type mockStore struct {
	text          string
	coversThrough time.Time
	saved         bool
}

func (m *mockStore) LoadSummary() (string, time.Time, error) {
	return m.text, m.coversThrough, nil
}

func (m *mockStore) SaveSummary(text string, ts time.Time) error {
	m.text = text
	m.coversThrough = ts
	m.saved = true
	return nil
}

type mockProvider struct {
	response string
	called   bool
}

func (m *mockProvider) Name() string { return "mock" }

func (m *mockProvider) Chat(_ context.Context, _ string, _ []chat.Message) (string, chat.Usage, error) {
	m.called = true
	return m.response, chat.Usage{}, nil
}

func (m *mockProvider) ChatStream(_ context.Context, _ string, _ []chat.Message, _ func(string) error) (string, chat.Usage, error) {
	return "", chat.Usage{}, nil
}

func (m *mockProvider) ChatStreamWithTools(_ context.Context, _ string, _ []chat.Message, _ []chat.ToolDef, _ func(string) error, _ func(string) error) (chat.StreamResponse, error) {
	return chat.StreamResponse{}, nil
}

// --- helpers -----------------------------------------------------------------

func newSummarizer(budget int, store SummaryStore, provider chat.Provider) *SummarizeStrategy {
	return &SummarizeStrategy{
		SummarizerProvider: provider,
		Budget:             budget,
		VerbatimRatio:      0.4,
		WorkspaceName:      "test",
		Store:              store,
		Ctx:                context.Background(),
	}
}

func tsMsg(role, content string, ts time.Time) chat.Message {
	return chat.Message{Role: role, Content: content, Time: ts}
}

// --- tests -------------------------------------------------------------------

func TestSummarize_NoCompactionNeeded(t *testing.T) {
	store := &mockStore{}
	provider := &mockProvider{response: "summary"}
	s := newSummarizer(10000, store, provider)

	h := &chat.History{Msgs: []chat.Message{
		uMsg("u1"), aMsg("a1"),
		uMsg("u2"), aMsg("a2"),
	}}
	got := s.Apply(h, "")

	// History fits in budget: no compaction, provider not called.
	if provider.called {
		t.Error("provider should not be called when history fits")
	}
	if store.saved {
		t.Error("store should not be saved when no compaction")
	}
	if len(got) == 0 {
		t.Error("expected non-empty context")
	}
}

func TestSummarize_StripsToolMessagesBeforeCompaction(t *testing.T) {
	store := &mockStore{}
	provider := &mockProvider{response: "summary"}
	s := newSummarizer(10000, store, provider)

	h := &chat.History{Msgs: []chat.Message{
		uMsg("question"),
		intermediateAssistant(),
		toolResultMsg("tool output"),
		aMsg("final answer"),
	}}
	got := s.Apply(h, "")

	// Tool messages should not appear in context.
	for _, m := range got {
		if m.Role == chat.RoleToolResult {
			t.Error("tool-result should not appear in context")
		}
		if m.Role == chat.RoleAssistant && len(m.ToolCalls) > 0 {
			t.Error("intermediate assistant should not appear in context")
		}
	}
	if len(got) != 2 {
		t.Errorf("expected 2 (user + final assistant), got %d: %v", len(got), contents(got))
	}
}

func TestSummarize_ExistingSummaryPrependedToContext(t *testing.T) {
	// Store has a summary but history hasn't grown past budget — no compaction.
	t1 := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	store := &mockStore{
		text:          "previous summary",
		coversThrough: t1,
	}
	provider := &mockProvider{}
	s := newSummarizer(10000, store, provider)

	// History starts after t1 so coversThrough points past all msgs.
	t2 := t1.Add(time.Minute)
	h := &chat.History{Msgs: []chat.Message{
		tsMsg(chat.RoleUser, "u2", t2),
		tsMsg(chat.RoleAssistant, "a2", t2.Add(time.Second)),
	}}
	got := s.Apply(h, "")

	// First message should be the summary injected as an assistant message.
	if len(got) == 0 {
		t.Fatal("expected non-empty context")
	}
	if got[0].Role != chat.RoleAssistant || got[0].Content != "[Context summary]\nprevious summary" {
		t.Errorf("first message should be summary block, got role=%q content=%q", got[0].Role, got[0].Content)
	}
}

func TestSummarize_CompactionTriggered(t *testing.T) {
	store := &mockStore{}
	provider := &mockProvider{response: "new compact summary"}
	// Very tight budget to force compaction.
	s := newSummarizer(50, store, provider)

	t1 := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	var msgs []chat.Message
	for i := 0; i < 10; i++ {
		msgs = append(msgs,
			tsMsg(chat.RoleUser, approxContent(20), t1.Add(time.Duration(i*2)*time.Second)),
			tsMsg(chat.RoleAssistant, approxContent(20), t1.Add(time.Duration(i*2+1)*time.Second)),
		)
	}
	h := &chat.History{Msgs: msgs}
	_ = s.Apply(h, "")

	if !provider.called {
		t.Error("expected provider to be called for compaction")
	}
	if !store.saved {
		t.Error("expected store to be saved after compaction")
	}
	if store.coversThrough.IsZero() {
		t.Error("expected non-zero coversThrough timestamp after compaction")
	}
}

func TestSummarize_TimestampSurvivesCrossSession(t *testing.T) {
	// Simulate: session 1 compacts and saves a timestamp.
	// Session 2: tool messages are added to raw history.
	// The timestamp should still resolve to the correct message.

	t1 := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Minute)
	t3 := t2.Add(time.Minute)

	// The message whose timestamp is saved as the compaction boundary.
	compactedMsg := tsMsg(chat.RoleAssistant, "a1", t2)

	store := &mockStore{
		text:          "compacted summary",
		coversThrough: compactedMsg.Time,
	}
	provider := &mockProvider{}
	s := newSummarizer(10000, store, provider)

	// Session 2 history: same messages as session 1 + new tool messages inserted.
	h := &chat.History{Msgs: []chat.Message{
		tsMsg(chat.RoleUser, "u1", t1),
		compactedMsg,
		// Tool messages added after compaction — these are stripped by CollapseForContext.
		{Role: chat.RoleToolResult, Content: "result", ToolCallID: "x", Time: t2.Add(time.Second)},
		// New turn after compaction.
		tsMsg(chat.RoleUser, "u2", t3),
		tsMsg(chat.RoleAssistant, "a2", t3.Add(time.Second)),
	}}

	got := s.Apply(h, "")

	// The summary should be prepended and only u2/a2 should be in verbatim window.
	if len(got) == 0 {
		t.Fatal("expected non-empty context")
	}
	if got[0].Role != chat.RoleAssistant || got[0].Content != "[Context summary]\ncompacted summary" {
		t.Errorf("expected summary block first, got role=%s content=%q", got[0].Role, got[0].Content)
	}
	// u1 and a1 are covered by summary, so only u2/a2 verbatim.
	for _, m := range got[1:] {
		if m.Content == "u1" || m.Content == "a1" {
			t.Errorf("message covered by summary should not appear verbatim: %q", m.Content)
		}
	}
}

func TestSummarize_OldIntBasedSummaryResets(t *testing.T) {
	// Store returns zero time (mimicking what happens after old-format reset).
	store := &mockStore{
		text:          "",      // reset to empty by old-format detection
		coversThrough: time.Time{}, // zero = no summary
	}
	provider := &mockProvider{response: "fresh summary"}
	s := newSummarizer(50, store, provider)

	t1 := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	var msgs []chat.Message
	for i := 0; i < 8; i++ {
		msgs = append(msgs,
			tsMsg(chat.RoleUser, approxContent(20), t1.Add(time.Duration(i*2)*time.Second)),
			tsMsg(chat.RoleAssistant, approxContent(20), t1.Add(time.Duration(i*2+1)*time.Second)),
		)
	}
	h := &chat.History{Msgs: msgs}

	// Should not panic or error; treats as fresh start.
	got := s.Apply(h, "")
	if got == nil && len(msgs) > 0 {
		t.Error("expected some context output")
	}
}

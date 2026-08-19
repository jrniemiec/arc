package tui

import "testing"

// busy() decides whether a running job counts as activity. Getting it wrong in
// the false direction releases the xlock mid-job; in the true direction it never
// releases at all.
func TestBusy(t *testing.T) {
	tests := []struct {
		name string
		set  func(*Model)
		want bool
	}{
		{"idle", func(m *Model) {}, false},
		{"ingest", func(m *Model) { m.ingestRunning = true }, true},
		{"flashcards", func(m *Model) { m.cardsRunning = true }, true},
		{"populate", func(m *Model) { m.populateRunning = true }, true},
		{"workspace chat", func(m *Model) { m.chatStreaming = true }, true},
		{"article chat", func(m *Model) { m.achatStreaming = true }, true},
		{"askX", func(m *Model) { m.askxStreaming = true }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m Model // svc nil, ttsPlayer nil — neither should panic
			tt.set(&m)
			if got := m.busy(); got != tt.want {
				t.Errorf("busy() = %v, want %v", got, tt.want)
			}
		})
	}
}

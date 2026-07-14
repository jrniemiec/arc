package chat

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseSummaryFile_NewFormat(t *testing.T) {
	ts := time.Date(2026, 7, 14, 10, 0, 0, 123000000, time.UTC)
	data := "covers_through_ts: 2026-07-14T10:00:00.123Z\n---\nsome summary text"
	text, got, err := parseSummaryFile(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "some summary text" {
		t.Errorf("text: want %q got %q", "some summary text", text)
	}
	if !got.Equal(ts) {
		t.Errorf("ts: want %v got %v", ts, got)
	}
}

func TestParseSummaryFile_OldFormatResetsGracefully(t *testing.T) {
	data := "covers_through: 5\n---\nsome summary text"
	text, ts, err := parseSummaryFile(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "" {
		t.Errorf("expected empty text on old format reset, got %q", text)
	}
	if !ts.IsZero() {
		t.Errorf("expected zero time on old format reset, got %v", ts)
	}
}

func TestParseSummaryFile_MissingSeparator(t *testing.T) {
	_, _, err := parseSummaryFile("covers_through_ts: 2026-07-14T10:00:00Z no separator")
	if err == nil {
		t.Error("expected error for missing separator")
	}
}

func TestParseSummaryFile_BadTimestamp(t *testing.T) {
	_, _, err := parseSummaryFile("covers_through_ts: not-a-date\n---\ntext")
	if err == nil {
		t.Error("expected error for bad timestamp")
	}
}

func TestParseSummaryFile_UnknownHeader(t *testing.T) {
	_, _, err := parseSummaryFile("unknown_header: value\n---\ntext")
	if err == nil {
		t.Error("expected error for unknown header")
	}
}

func TestParseSummaryFile_EmptyBody(t *testing.T) {
	ts := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	data := "covers_through_ts: 2026-07-14T10:00:00Z\n---\n"
	text, got, err := parseSummaryFile(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "" {
		t.Errorf("expected empty body, got %q", text)
	}
	if !got.Equal(ts) {
		t.Errorf("ts: want %v got %v", ts, got)
	}
}

func TestSaveSummary_LoadSummary_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	st := &ChatStore{dataRoot: dir, workspaceName: "test-ws"}

	// File does not exist: LoadSummary returns zero values.
	text, ts, err := st.LoadSummary()
	if err != nil {
		t.Fatalf("LoadSummary on missing file: %v", err)
	}
	if text != "" || !ts.IsZero() {
		t.Errorf("expected empty on missing file, got %q / %v", text, ts)
	}

	// Save and load back.
	wantTS := time.Date(2026, 7, 14, 12, 30, 0, 0, time.UTC)
	wantText := "This is the summary.\nWith multiple lines."
	if err := st.SaveSummary(wantText, wantTS); err != nil {
		t.Fatalf("SaveSummary: %v", err)
	}

	gotText, gotTS, err := st.LoadSummary()
	if err != nil {
		t.Fatalf("LoadSummary: %v", err)
	}
	if gotText != wantText {
		t.Errorf("text: want %q got %q", wantText, gotText)
	}
	if !gotTS.Equal(wantTS) {
		t.Errorf("ts: want %v got %v", wantTS, gotTS)
	}
}

func TestLoadSummary_OldFormatResets(t *testing.T) {
	dir := t.TempDir()
	st := &ChatStore{dataRoot: dir, workspaceName: "test-ws"}

	// Write an old-format summary file directly.
	chatDir := filepath.Join(dir, "workspaces", "test-ws", "chat")
	if err := os.MkdirAll(chatDir, 0755); err != nil {
		t.Fatal(err)
	}
	oldContent := "covers_through: 7\n---\nold summary text"
	if err := os.WriteFile(filepath.Join(chatDir, "summary.txt"), []byte(oldContent), 0644); err != nil {
		t.Fatal(err)
	}

	text, ts, err := st.LoadSummary()
	if err != nil {
		t.Fatalf("unexpected error on old format: %v", err)
	}
	if text != "" || !ts.IsZero() {
		t.Errorf("old format should reset: got text=%q ts=%v", text, ts)
	}
}

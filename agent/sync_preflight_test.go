package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jrniemiec/arc/gitsync"
)

// stubCoord implements only the two methods syncPreflight calls. The embedded
// nil interface supplies the rest, so any other call panics rather than
// silently returning a zero value.
type stubCoord struct {
	gitsync.Coordinator
	enabled bool
	pullErr error
}

func (s stubCoord) Enabled() bool              { return s.enabled }
func (s stubCoord) Pull(context.Context) error { return s.pullErr }

// A diverged clone must stop the run before any feed is polled, and the failure
// must reach runs.jsonl — the briefing sends mail by finding rec.Error, so an
// unrecorded abort is a silent one. This is the gap that let a real divergence
// run for six days behind one ERROR line per day.
func TestRunFeedsAbortsOnDivergedClone(t *testing.T) {
	runsPath := filepath.Join(t.TempDir(), "runs.jsonl")

	opts := RunOptions{
		RunsPath: runsPath,
		Sync:     stubCoord{enabled: true, pullErr: &gitsync.Diverged{Commits: []string{"abc1234"}}},
		AgentCfg: AgentConfig{
			Feeds: []FeedConfig{{URL: "https://example.com/feed.xml"}},
		},
	}

	// DB is nil on purpose: reaching it would mean the guard ran too late.
	rec, err := RunFeeds(context.Background(), opts)
	if err != nil {
		t.Fatalf("RunFeeds returned err = %v, want nil (the abort is carried in rec.Error)", err)
	}

	if rec.Error == "" {
		t.Fatal("rec.Error is empty; the briefing would emit nothing and no mail would be sent")
	}
	if !strings.Contains(rec.Error, "unpushed commit") {
		t.Errorf("rec.Error = %q, want it to explain the divergence", rec.Error)
	}
	if rec.RunType != "daily" {
		t.Errorf("RunType = %q, want %q", rec.RunType, "daily")
	}
	if rec.TotalNew != 0 || rec.TotalIngest != 0 {
		t.Errorf("polled feeds despite divergence: new=%d ingest=%d", rec.TotalNew, rec.TotalIngest)
	}
	if rec.FinishedAt.IsZero() {
		t.Error("FinishedAt is zero; the record reads as a run still in flight")
	}

	data, readErr := os.ReadFile(runsPath)
	if readErr != nil {
		t.Fatalf("runs.jsonl was not written: %v", readErr)
	}
	if !strings.Contains(string(data), rec.RunID) {
		t.Error("run record was not appended to runs.jsonl")
	}
}

// Offline is not divergence. Refusing to ingest because a fetch failed would be
// a worse failure than the one being prevented.
func TestSyncPreflightIgnoresUnreachableRemote(t *testing.T) {
	opts := RunOptions{
		Sync: stubCoord{enabled: true, pullErr: errors.New("dial tcp: no route to host")},
	}
	if err := syncPreflight(context.Background(), opts); err != nil {
		t.Errorf("syncPreflight = %v, want nil for an unreachable remote", err)
	}
}

// Standalone mode and tests leave Sync nil.
func TestSyncPreflightSkipsWhenSyncAbsentOrDisabled(t *testing.T) {
	if err := syncPreflight(context.Background(), RunOptions{}); err != nil {
		t.Errorf("nil Sync: got %v, want nil", err)
	}

	opts := RunOptions{
		Sync: stubCoord{enabled: false, pullErr: &gitsync.Diverged{Commits: []string{"abc1234"}}},
	}
	if err := syncPreflight(context.Background(), opts); err != nil {
		t.Errorf("disabled Sync: got %v, want nil (Pull must not be consulted)", err)
	}
}

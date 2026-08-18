package gitsync

import (
	"encoding/json"
	"testing"
	"time"
)

func lock(holder string, activity time.Time, idle time.Duration) XLock {
	return XLock{Holder: holder, LastActivity: activity, IdleTimeout: Duration(idle)}
}

func TestEvaluate(t *testing.T) {
	now := time.Date(2026, 8, 17, 14, 0, 0, 0, time.UTC)
	const margin = 2 * time.Minute

	tests := []struct {
		name string
		x    XLock
		want State
	}{
		{"empty holder is free", XLock{}, StateFree},
		{"self is held", lock("desktop", now.Add(-time.Minute), 15*time.Minute), StateHeld},
		{"other, timer live", lock("laptop", now.Add(-time.Minute), 15*time.Minute), StateBlocked},
		{"other, idle but inside margin", lock("laptop", now.Add(-16*time.Minute), 15*time.Minute), StateBlocked},
		{"other, past idle+margin", lock("laptop", now.Add(-18*time.Minute), 15*time.Minute), StateSeizable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.x.Evaluate("desktop", now, margin); got != tt.want {
				t.Errorf("Evaluate() = %v, want %v", got, tt.want)
			}
		})
	}
}

// The observer must judge the holder by the holder's published timeout, not its
// own: a laptop configured at 5m must not seize a desktop configured at 15m.
func TestEvaluateUsesHoldersTimeout(t *testing.T) {
	now := time.Date(2026, 8, 17, 14, 0, 0, 0, time.UTC)
	x := lock("desktop", now.Add(-10*time.Minute), 15*time.Minute)

	if got := x.Evaluate("laptop", now, 2*time.Minute); got != StateBlocked {
		t.Errorf("with holder timeout 15m, 10m idle should block, got %v", got)
	}
}

func TestExpiredFor(t *testing.T) {
	now := time.Date(2026, 8, 17, 14, 0, 0, 0, time.UTC)

	// Self-release uses the holder's own timeout with no takeover margin — the
	// margin exists for observers.
	x := lock("desktop", now.Add(-16*time.Minute), 15*time.Minute)
	if !x.ExpiredFor("desktop", now) {
		t.Error("16m idle against a 15m timeout should be expired")
	}
	if x.ExpiredFor("laptop", now) {
		t.Error("ExpiredFor must only report on the holder itself")
	}

	fresh := lock("desktop", now.Add(-time.Minute), 15*time.Minute)
	if fresh.ExpiredFor("desktop", now) {
		t.Error("1m idle against a 15m timeout should not be expired")
	}
}

func TestSeizableIn(t *testing.T) {
	now := time.Date(2026, 8, 17, 14, 0, 0, 0, time.UTC)
	x := lock("laptop", now.Add(-10*time.Minute), 15*time.Minute)

	// 10m elapsed of 15m + 2m margin leaves 7m.
	if got := x.SeizableIn(now, 2*time.Minute); got != 7*time.Minute {
		t.Errorf("SeizableIn() = %v, want 7m", got)
	}

	past := lock("laptop", now.Add(-time.Hour), 15*time.Minute)
	if got := past.SeizableIn(now, 2*time.Minute); got != 0 {
		t.Errorf("already seizable should report 0, got %v", got)
	}
}

func TestParseXLockRoundTrip(t *testing.T) {
	orig := XLock{
		Holder:       "desktop",
		LastActivity: time.Date(2026, 8, 17, 14, 32, 11, 0, time.UTC),
		IdleTimeout:  Duration(15 * time.Minute),
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseXLock(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.Holder != orig.Holder || !got.LastActivity.Equal(orig.LastActivity) ||
		got.IdleTimeout != orig.IdleTimeout {
		t.Errorf("round trip mismatch:\n got %+v\nwant %+v", got, orig)
	}
}

// A missing or empty xlock is a legitimate free state, not an error.
func TestParseXLockEmpty(t *testing.T) {
	got, err := ParseXLock(nil)
	if err != nil {
		t.Fatalf("empty content should parse as free, got %v", err)
	}
	if got.Holder != "" {
		t.Errorf("holder = %q, want empty", got.Holder)
	}
}

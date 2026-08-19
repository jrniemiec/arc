package gitsync

import (
	"testing"
	"time"
)

// The idle clock must be resettable, which is what lets a running job count as
// activity rather than merely deferring the release.
func TestIdleForResetsOnTouch(t *testing.T) {
	c := &coordinator{lastActivity: time.Now().Add(-40 * time.Minute)}

	if got := c.IdleFor(); got < 39*time.Minute {
		t.Fatalf("IdleFor() = %v, want ≈40m", got)
	}
	c.Touch()
	if got := c.IdleFor(); got > time.Second {
		t.Errorf("after Touch, IdleFor() = %v, want ≈0", got)
	}
}

// Self-release must reset the idle clock whatever the outcome. The tick driving
// it fires every 100ms once the idle period has elapsed, so without a reset it
// would retry continuously.
func TestSelfReleaseResetsClock(t *testing.T) {
	c := &coordinator{
		machine:      "desktop",
		dataRoot:     t.TempDir(), // no xlock.json → holder "" → nothing to release
		git:          &Git{Dir: t.TempDir(), Remote: "origin", Branch: "main"},
		lastActivity: time.Now().Add(-time.Hour),
	}
	_ = c.SelfRelease(t.Context())
	if got := c.IdleFor(); got > time.Second {
		t.Errorf("idle clock not reset: IdleFor() = %v, want ≈0", got)
	}
}

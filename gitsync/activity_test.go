package gitsync

import (
	"sync"
	"testing"
)

// Operations overlap — the startup pull runs on its own goroutine while writes
// arrive from the UI's — so they finish in any order, not LIFO. Whichever ends
// last must leave the label empty; a leftover label spins the banner forever
// over an operation that has already finished.
func TestActivityOverlappingOperations(t *testing.T) {
	c := &coordinator{}

	endA := c.setActivity("syncing")
	endB := c.setActivity("acquiring xlock")

	if got := c.Activity(); got != "acquiring xlock" {
		t.Errorf("Activity() = %q, want %q", got, "acquiring xlock")
	}

	// A finishes first: B is still running and must remain reported.
	endA()
	if got := c.Activity(); got != "acquiring xlock" {
		t.Errorf("after outer ended, Activity() = %q, want %q", got, "acquiring xlock")
	}

	endB()
	if got := c.Activity(); got != "" {
		t.Errorf("after all ended, Activity() = %q, want empty", got)
	}
}

// Nesting is the common case: claiming the xlock pushes, so "pushing" starts
// inside "acquiring xlock". The inner label must not clear the outer one.
func TestActivityNested(t *testing.T) {
	c := &coordinator{}

	endClaim := c.setActivity("acquiring xlock")
	endPush := c.setActivity("pushing")
	if got := c.Activity(); got != "pushing" {
		t.Errorf("Activity() = %q, want %q", got, "pushing")
	}
	endPush()
	if got := c.Activity(); got != "acquiring xlock" {
		t.Errorf("after inner ended, Activity() = %q, want %q", got, "acquiring xlock")
	}
	endClaim()
	if got := c.Activity(); got != "" {
		t.Errorf("after outer ended, Activity() = %q, want empty", got)
	}
}

// The same label can be in flight more than once — two concurrent pulls both
// report "syncing". Ending one must not clear the other's entry.
func TestActivityDuplicateLabels(t *testing.T) {
	c := &coordinator{}

	end1 := c.setActivity("syncing")
	end2 := c.setActivity("syncing")
	end1()
	if got := c.Activity(); got != "syncing" {
		t.Errorf("Activity() = %q, want %q", got, "syncing")
	}
	end2()
	if got := c.Activity(); got != "" {
		t.Errorf("Activity() = %q, want empty", got)
	}
}

// Run under -race: the UI reads Activity() every 100ms tick while git
// subprocesses start and end on other goroutines.
func TestActivityConcurrent(t *testing.T) {
	c := &coordinator{}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer c.setActivity("syncing")()
			c.Activity()
		}()
	}
	wg.Wait()

	if got := c.Activity(); got != "" {
		t.Errorf("after all goroutines finished, Activity() = %q, want empty", got)
	}
}

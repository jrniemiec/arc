package cmd

import (
	"fmt"
	"os"
	"sync/atomic"
	"time"
)

const spinnerClearLine = "\r\033[K"

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Spinner writes a rotating status display to stderr.
// Slot 0 is the main spinner line. Slots 1..n are sub-lines rendered below it,
// intended for concurrent operations (e.g. parallel ingest goroutines).
// Sub-lines with an empty string are hidden; the display height adjusts each tick.
//
// Safe for concurrent use — callers update messages via SetSlot.
type Spinner struct {
	slots  []atomic.Pointer[string]
	done   chan struct{}
	exited chan struct{}
}

// NewSpinner creates and starts a spinner with nSubLines extra status lines
// below the main spinner line. Pass 0 for classic single-line behavior.
func NewSpinner(nSubLines int) *Spinner {
	s := &Spinner{
		slots:  make([]atomic.Pointer[string], 1+nSubLines),
		done:   make(chan struct{}),
		exited: make(chan struct{}),
	}
	empty := ""
	for i := range s.slots {
		s.slots[i].Store(&empty)
	}
	go s.run()
	return s
}

// Set updates the main status message (slot 0). Backward-compatible with old API.
func (s *Spinner) Set(msg string) {
	s.slots[0].Store(&msg)
}

// SetSlot updates the message for the given slot.
// Slot 0 is the main spinner line; slots 1..n are sub-lines.
// Setting a slot to "" clears it (hides the sub-line).
func (s *Spinner) SetSlot(slot int, msg string) {
	if slot >= 0 && slot < len(s.slots) {
		s.slots[slot].Store(&msg)
	}
}

// Stop halts the spinner and clears all rendered lines.
func (s *Spinner) Stop() {
	close(s.done)
	<-s.exited
	// Clear however many lines are currently visible.
	nSubs := len(s.slots) - 1
	if nSubs > 0 {
		// Go up nSubs lines to reach the main line, then clear each line downward.
		fmt.Fprintf(os.Stderr, "\033[%dA\r\033[K", nSubs)
		for i := 0; i < nSubs; i++ {
			fmt.Fprint(os.Stderr, "\n\r\033[K")
		}
		// Return cursor to the main line.
		fmt.Fprintf(os.Stderr, "\033[%dA\r", nSubs)
	} else {
		fmt.Fprint(os.Stderr, "\r\033[K")
	}
}

func (s *Spinner) run() {
	defer close(s.exited)

	ticker := time.NewTicker(80 * time.Millisecond)
	defer ticker.Stop()

	nSubs := len(s.slots) - 1
	frame := 0
	prevSubLines := 0 // number of non-empty sub-lines written in the previous tick

	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			// Collect current slot values.
			main := *s.slots[0].Load()
			subs := make([]string, nSubs)
			activeSubs := 0
			for i := range subs {
				subs[i] = *s.slots[i+1].Load()
				if subs[i] != "" {
					activeSubs++
				}
			}

			// Rewind cursor: go up to the main line.
			if prevSubLines > 0 {
				fmt.Fprintf(os.Stderr, "\033[%dA\r", prevSubLines)
			} else {
				fmt.Fprint(os.Stderr, "\r")
			}

			// Render main line.
			spinFrame := spinnerFrames[frame%len(spinnerFrames)]
			if len(main) > 100 {
				main = main[:97] + "..."
			}
			fmt.Fprintf(os.Stderr, "%s %s\033[K", spinFrame, main)

			// Render active sub-lines, blank-pad if count shrank since last tick.
			written := 0
			for _, msg := range subs {
				if msg == "" {
					continue
				}
				if len(msg) > 96 {
					msg = msg[:93] + "..."
				}
				fmt.Fprintf(os.Stderr, "\n  ↳ %s\033[K", msg)
				written++
			}
			// Clear any leftover lines from a previous tick with more sub-lines.
			for written < prevSubLines {
				fmt.Fprint(os.Stderr, "\n\033[K")
				written++
			}
			prevSubLines = activeSubs

			frame++
		}
	}
}

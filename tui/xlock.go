package tui

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/jrniemiec/arc/gitsync"
	"github.com/jrniemiec/arc/service"
)

// xlockState is what the banner renders. It is a cache of what the remote last
// said, refreshed on a timer; the remote is the source of truth.
type xlockState struct {
	enabled    bool
	holder     string // "" when free
	isSelf     bool
	seizableAt time.Time // zero when not applicable
	unpushed   int
	diverged   bool
	activity   string // non-empty while a sync operation is in flight
	loaded     bool
}

// xlockTickMsg triggers a banner refresh.
type xlockTickMsg struct{}

// xlockStatusMsg carries a refreshed snapshot back to the model.
type xlockStatusMsg struct {
	state xlockState
}

// xlockTick schedules the next banner refresh.
func xlockTick(every time.Duration) tea.Cmd {
	if every <= 0 {
		every = time.Minute
	}
	return tea.Tick(every, func(time.Time) tea.Msg { return xlockTickMsg{} })
}

// refreshXLock fetches the remote ref and reads xlock.json from it.
//
// Fetch only — never merge. Merging behind the user's back would swap working
// files mid-read, which is exactly what this design avoids. Reading the remote
// ref directly gives the remote's copy without touching the working tree.
func refreshXLock(svc *service.Service, machine string, margin time.Duration) tea.Cmd {
	return refreshXLockWith(svc, machine, margin, true)
}

// refreshXLockLocal updates the banner without touching the network.
//
// Everything it reads is local: xlock.json as of the last fetch, plus this
// machine's own pushes, which update the remote-tracking ref directly. That
// makes it cheap enough to run after every command, so acquiring or releasing
// the xlock shows up immediately instead of waiting for the next tick. Only
// changes made on *another* machine need the fetch.
func refreshXLockLocal(svc *service.Service, machine string, margin time.Duration) tea.Cmd {
	return refreshXLockWith(svc, machine, margin, false)
}

func refreshXLockWith(svc *service.Service, machine string, margin time.Duration, fetch bool) tea.Cmd {
	return func() tea.Msg {
		if svc == nil || !svc.Sync().Enabled() {
			return xlockStatusMsg{state: xlockState{loaded: true}}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		coord := svc.Sync()
		if fetch {
			// A failed fetch is not an error worth surfacing: the banner simply
			// shows the last known state until the next tick.
			_ = coord.FetchOnly(ctx)
		}

		st, err := coord.Status(ctx)
		if err != nil {
			return xlockStatusMsg{state: xlockState{enabled: true, loaded: true}}
		}

		out := xlockState{
			enabled:  true,
			holder:   st.Holder,
			isSelf:   st.IsSelf,
			unpushed: st.Unpushed,
			diverged: st.Diverged,
			loaded:   true,
		}

		// Prefer the remote's copy for another machine's state — the local file
		// only changes on pull, so on its own it would show stale ownership
		// indefinitely.
		if remote, err := coord.RemoteXLock(ctx); err == nil && remote.Holder != "" {
			out.holder = remote.Holder
			out.isSelf = remote.Holder == machine
			if !out.isSelf {
				out.seizableAt = remote.SeizableAt(margin)
			}
		}
		slog.Debug("xlock banner refresh",
			"fetch", fetch, "holder", out.holder, "isSelf", out.isSelf,
			"unpushed", out.unpushed, "diverged", out.diverged)
		return xlockStatusMsg{state: out}
	}
}

// renderXLockBanner returns the status-bar segment and its display width, or
// ("", 0) in standalone mode.
//
// The width is returned alongside because the styled string carries ANSI escape
// sequences; measuring it directly would overstate the width and break the
// centring arithmetic.
//
// The token "xlock" is shown literally rather than softened into plain language:
// X-lock is the standard term for an exclusive lock, so it states the condition
// exactly, and it is the same word used in xlock.json, config.jsonc, arc sync
// status, and the log. The text after the token does the plain-language work.
func (m Model) renderXLockBanner() (string, int) {
	s := m.xlock
	if !s.enabled {
		return "", 0
	}
	t := ActiveTheme

	// An operation in flight takes precedence over the resting state. Acquiring
	// the xlock happens implicitly inside the first write and costs a network
	// round trip each way, so without this the user waits several seconds with
	// no indication of what is happening or why.
	if s.activity != "" {
		frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
		spin := frames[m.spinnerFrame%len(frames)]
		text := spin + " xlock: " + s.activity + "…"
		return fg(t.XLockBlocked, text), lipgloss.Width(text)
	}

	var text string
	var colour lipgloss.Color
	bold := false

	switch {
	case s.diverged:
		// The only genuine error state: the app will not open until it is repaired.
		text, colour, bold = "✖ xlock: diverged", t.StatusError, true

	case s.isSelf && s.unpushed > 0:
		// Pushes are synchronous, so an unpushed commit means a push actually
		// failed. The work is safe — the commit is kept — but it has not reached
		// the remote and will not without help.
		text, colour = fmt.Sprintf("⚠ xlock: %d unpushed", s.unpushed), t.XLockBlocked

	case s.isSelf:
		// The common case, and the one that matters: you may write.
		text, colour = "● xlock: this machine", t.XLockHeld

	case s.holder == "":
		// Regular foreground, not dimmed. The banner's presence is what signals
		// multi-client mode, so the most neutral state must still be legible.
		// StatusText is mid-gray — ContentText is the actual body foreground.
		text, colour = "○ xlock: free", t.ContentText

	default:
		// Writes are blocked — a constraint, not information.
		colour = t.XLockBlocked
		switch remaining := time.Until(s.seizableAt); {
		case s.seizableAt.IsZero():
			text = "◐ xlock: " + s.holder
		case remaining <= 0:
			text = "◐ xlock: " + s.holder + " · idle"
		default:
			text = fmt.Sprintf("◐ xlock: %s · %s", s.holder, shortDuration(remaining))
		}
	}

	if bold {
		return fgBold(colour, text), lipgloss.Width(text)
	}
	return fg(colour, text), lipgloss.Width(text)
}

// shortDuration renders a countdown compactly: "6m", "45s".
func shortDuration(d time.Duration) string {
	if d >= time.Minute {
		return fmt.Sprintf("%dm", int(d.Minutes())+1)
	}
	return fmt.Sprintf("%ds", int(d.Seconds())+1)
}

// xlockBlockedMessage renders the refusal shown when another machine holds the
// xlock. It names the holder and the wait, and points at the way out.
func xlockBlockedMessage(b *gitsync.Blocked) string {
	if b.SeizableIn <= 0 {
		return fmt.Sprintf("✗ %s holds the xlock — /xlock-take to take it", b.Holder)
	}
	return fmt.Sprintf("✗ %s holds the xlock · free in %s — /xlock-take to take it now",
		b.Holder, shortDuration(b.SeizableIn))
}

// svcTouch records local activity so the idle timer does not fire while the user
// is working. No-op in standalone.
func (m *Model) svcTouch() {
	if m.svc != nil {
		m.svc.Sync().Touch()
	}
}

// mutationDoneMsg reports the outcome of a mutation that has no other result to
// deliver — marking read, favouriting, pinning, deleting.
//
// These used to return a bare nil, which had two consequences: the banner did
// not refresh when the mutation acquired the xlock, and a refused write was
// swallowed, leaving an optimistic "✓ marked as read" on screen for something
// that never happened.
type mutationDoneMsg struct{ err error }

// mutationDone wraps a mutation result for delivery to Update.
func mutationDone(err error) tea.Msg { return mutationDoneMsg{err: err} }

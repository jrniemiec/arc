package tui

import (
	"context"
	"errors"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jrniemiec/arc/gitsync"
	"github.com/jrniemiec/arc/service"
)

// dispatchSyncCommand handles the /sync-* and /xlock-* commands.
//
// It reports handled=false for anything it does not own, so the caller falls
// through to the rest of the dispatch chain.
//
// In standalone mode these commands are not offered by completion or /help, but
// they are still reachable by typing. Rather than pretending they do not exist,
// each explains the mode it needs — a command that silently does nothing is
// worse than one that says why.
func (m *Model) dispatchSyncCommand(cmd, arg string) (tea.Cmd, bool) {
	switch cmd {
	case "/sync":
		return m.cmdSyncStatus(), true
	case "/sync-pull":
		return m.cmdSyncPull(), true
	case "/sync-push":
		return m.cmdSyncPush(), true
	case "/sync-enable":
		return m.cmdSyncEnable(), true
	case "/sync-disable":
		return m.cmdSyncDisable(), true
	case "/xlock-take":
		return m.cmdXLockTake(), true
	case "/xlock-release":
		return m.cmdXLockRelease(), true
	}
	return nil, false
}

// requireMultiClient reports whether multi-client mode is active, setting an
// explanatory status message when it is not.
func (m *Model) requireMultiClient() bool {
	if m.svc == nil {
		m.statusMsg = "✗ service unavailable"
		return false
	}
	if !m.svc.Sync().Enabled() {
		m.statusMsg = "✗ standalone mode — run 'arc sync init <repo-url>' to share this tree"
		return false
	}
	return true
}

func (m *Model) cmdSyncStatus() tea.Cmd {
	if m.svc == nil {
		m.statusMsg = "✗ service unavailable"
		return nil
	}
	svc := m.svc
	return func() tea.Msg {
		st, err := svc.Sync().Status(context.Background())
		if err != nil {
			return cmdDoneMsg{err: "sync: " + err.Error()}
		}
		if !st.Enabled {
			return cmdDoneMsg{statusLines: []string{
				"mode:      standalone",
				"",
				"arc performs no git operations in this mode.",
				"Run 'arc sync init <repo-url>' to share this tree across machines.",
			}}
		}
		lines := []string{
			"mode:      multi-client",
			"machine:   " + st.Machine,
			"remote:    " + st.Remote + "/" + st.Branch,
		}
		switch {
		case st.Diverged:
			lines = append(lines, "xlock:     DIVERGED — repair required")
		case st.Holder == "":
			lines = append(lines, "xlock:     free")
		case st.IsSelf:
			lines = append(lines, "xlock:     this machine")
		case st.SeizableIn == 0:
			lines = append(lines, "xlock:     "+st.Holder+" (idle — seizable now)")
		default:
			lines = append(lines, fmt.Sprintf("xlock:     %s (seizable in %s)",
				st.Holder, st.SeizableIn.Round(time.Second)))
		}
		lines = append(lines, fmt.Sprintf("unpushed:  %d", st.Unpushed))
		if !st.LastPush.IsZero() {
			lines = append(lines, "last push: "+st.LastPush.Format("15:04:05"))
		}
		return cmdDoneMsg{statusLines: lines}
	}
}

func (m *Model) cmdSyncPull() tea.Cmd {
	if !m.requireMultiClient() {
		return nil
	}
	svc := m.svc
	m.statusMsg = "pulling…"
	return func() tea.Msg {
		if err := svc.SyncPull(context.Background()); err != nil {
			return cmdDoneMsg{err: syncErrorMessage(err)}
		}
		return cmdDoneMsg{statusMsg: "✓ up to date", reloadNav: true, reloadWorkspaces: true}
	}
}

func (m *Model) cmdSyncPush() tea.Cmd {
	if !m.requireMultiClient() {
		return nil
	}
	svc := m.svc
	m.statusMsg = "pushing…"
	return func() tea.Msg {
		err := svc.Sync().Push(context.Background())
		if err != nil {
			return cmdDoneMsg{err: syncErrorMessage(err)}
		}
		return cmdDoneMsg{statusMsg: "✓ pushed"}
	}
}

func (m *Model) cmdSyncEnable() tea.Cmd {
	m.statusMsg = "✗ run 'arc sync enable' in the terminal — if it reports a diverged tree, repair with git merge"
	return nil
}

func (m *Model) cmdSyncDisable() tea.Cmd {
	if !m.requireMultiClient() {
		return nil
	}
	svc := m.svc
	return func() tea.Msg {
		if err := svc.Sync().Release(context.Background()); err != nil {
			return cmdDoneMsg{err: syncErrorMessage(err)}
		}
		return cmdDoneMsg{statusLines: []string{
			"Pushed outstanding work and released the xlock.",
			"",
			"Run 'arc sync disable' in the terminal to complete the switch —",
			"the mode itself is written to config.jsonc, which arc reads at startup.",
		}}
	}
}

// cmdXLockTake seizes the xlock, confirming first when the holder's timer is
// still live.
//
// Seizure strands any work the other machine has not pushed, and arc cannot
// reach that machine to check — so the confirmation states the consequence
// rather than asking a bare yes/no.
func (m *Model) cmdXLockTake() tea.Cmd {
	if !m.requireMultiClient() {
		return nil
	}
	svc := m.svc
	return func() tea.Msg {
		err := svc.Sync().Take(context.Background(), false)

		var blocked *gitsync.Blocked
		if errors.As(err, &blocked) {
			return xlockConfirmMsg{holder: blocked.Holder, seizableIn: blocked.SeizableIn}
		}
		if err != nil {
			return cmdDoneMsg{err: syncErrorMessage(err)}
		}
		return cmdDoneMsg{statusMsg: "✓ xlock: this machine"}
	}
}

func (m *Model) cmdXLockRelease() tea.Cmd {
	if !m.requireMultiClient() {
		return nil
	}
	svc := m.svc
	return func() tea.Msg {
		if err := svc.Sync().Release(context.Background()); err != nil {
			return cmdDoneMsg{err: syncErrorMessage(err)}
		}
		return cmdDoneMsg{statusMsg: "✓ xlock: free"}
	}
}

// xlockConfirmMsg asks the user to confirm an early takeover.
type xlockConfirmMsg struct {
	holder     string
	seizableIn time.Duration
}

// forceTakeXLock seizes without the confirmation gate, after the user agreed.
//
// It takes the service rather than the Model: the confirmation closure outlives
// the Update call that created it, and Update operates on a copy of the Model.
func forceTakeXLock(svc *service.Service) tea.Cmd {
	return func() tea.Msg {
		if err := svc.Sync().Take(context.Background(), true); err != nil {
			return cmdDoneMsg{err: syncErrorMessage(err)}
		}
		return cmdDoneMsg{statusMsg: "✓ xlock: this machine"}
	}
}

// syncErrorMessage turns a sync error into something a TUI user can act on.
// Raw git output ("Not possible to fast-forward") means nothing here.
func syncErrorMessage(err error) string {
	var blocked *gitsync.Blocked
	if errors.As(err, &blocked) {
		return xlockBlockedMessage(blocked)
	}
	var diverged *gitsync.Diverged
	if errors.As(err, &diverged) {
		return "diverged: this machine has unpushed commits the remote does not have — " +
			"repair in the terminal with: git fetch origin && git merge origin/main && git push"
	}
	switch {
	case errors.Is(err, gitsync.ErrDirtyTree):
		return "uncommitted changes present — run 'arc sync push' first"
	case errors.Is(err, gitsync.ErrPushRejected):
		return "push rejected: another machine pushed first — commit kept, pull and retry"
	case errors.Is(err, gitsync.ErrNoRemoteRepo):
		return "cannot reach the remote — check the network and your SSH key"
	case errors.Is(err, gitsync.ErrNoGit):
		return "git not found on PATH"
	}
	return "sync: " + err.Error()
}

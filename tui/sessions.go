//go:build arcsessions

package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jrniemiec/arc/session"
)

// paneSessions is numbered well clear of the built-in panes so adding one to
// the untagged list can never collide with it.
const paneSessions focusPane = 1000

// sessionsRefresh is how often the list re-reads while it is open. Each pass
// costs a subprocess per session plus one AppleScript call, so this trades a
// little background work for a busy/idle column that is worth trusting.
const sessionsRefresh = 3 * time.Second

// procName is the process the switcher looks for.
const procName = "claude"

// sessions holds the overlay's state.
//
// A package variable rather than a Model field on purpose: a field would put
// add-on state in the untagged struct, which is exactly what the build tag is
// meant to avoid. Safe because one process runs one Model.
var sessions struct {
	rows    []session.Session
	cursor  int
	loading bool
	err     string
	preFoc  focusPane
	gen     int // discards results from a pass started before the last close
}

func init() {
	globalCommands = append(globalCommands, cmdCompletion{
		"/sessions", "", "switch between running Claude Code sessions",
	})
	addonDispatch = dispatchSessions
	addonKey = sessionsKey
	addonView = sessionsView
	addonMsg = handleSessionsMsg
}

// ── messages ──────────────────────────────────────────────────────────────────

type sessionsLoadedMsg struct {
	gen  int
	rows []session.Session
	err  error
}

type sessionsTickMsg struct{ gen int }

// ── dispatch ──────────────────────────────────────────────────────────────────

func dispatchSessions(m *Model, cmd, _ string) (tea.Cmd, bool) {
	if cmd != "/sessions" {
		return nil, false
	}
	sessions.preFoc = m.focus
	sessions.cursor = 0
	sessions.loading = true
	sessions.err = ""
	sessions.gen++
	m.focus = paneSessions
	return loadSessions(sessions.gen), true
}

// loadSessions runs discovery off the UI goroutine — it costs roughly 50ms per
// session, so inline it would freeze the interface for half a second.
func loadSessions(gen int) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		rows, err := session.Discover(ctx, procName)
		return sessionsLoadedMsg{gen: gen, rows: rows, err: err}
	}
}

func sessionsTick(gen int) tea.Cmd {
	return tea.Tick(sessionsRefresh, func(time.Time) tea.Msg {
		return sessionsTickMsg{gen: gen}
	})
}

// HandleSessionsMsg processes the add-on's own messages. Returns false for
// anything else.
func handleSessionsMsg(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case sessionsLoadedMsg:
		if msg.gen != sessions.gen {
			return nil, true // a stale pass finished after the pane closed
		}
		sessions.loading = false
		if msg.err != nil {
			sessions.err = msg.err.Error()
		} else {
			sessions.err = ""
			sessions.rows = msg.rows
		}
		if sessions.cursor >= len(sessions.rows) {
			sessions.cursor = max(0, len(sessions.rows)-1)
		}
		return sessionsTick(msg.gen), true

	case sessionsTickMsg:
		if msg.gen != sessions.gen {
			return nil, true
		}
		return loadSessions(msg.gen), true
	}
	return nil, false
}

// ── keys ──────────────────────────────────────────────────────────────────────

func sessionsKey(m *Model, msg tea.KeyMsg) (tea.Cmd, bool) {
	if m.focus != paneSessions {
		return nil, false
	}
	switch msg.String() {
	case "q", "esc", "ctrl+x":
		closeSessions(m)
	case "j", "down":
		if sessions.cursor < len(sessions.rows)-1 {
			sessions.cursor++
		}
	case "k", "up":
		if sessions.cursor > 0 {
			sessions.cursor--
		}
	case "r":
		sessions.loading = true
		sessions.gen++
		return loadSessions(sessions.gen), true
	case "enter":
		if sessions.cursor < len(sessions.rows) {
			row := sessions.rows[sessions.cursor]
			closeSessions(m)
			return focusSession(m, row), true
		}
	}
	return nil, true
}

func closeSessions(m *Model) {
	m.focus = sessions.preFoc
	sessions.gen++ // orphans any pass still running, and stops the tick
	sessions.loading = false
}

// focusSession switches iTerm2 to the chosen window. arc keeps running; the
// terminal simply comes forward.
func focusSession(m *Model, row session.Session) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := session.Focus(ctx, row.TTY, row.App); err != nil {
			return cmdDoneMsg{err: err.Error()}
		}
		return cmdDoneMsg{statusMsg: "→ " + shortDir(row.Dir)}
	}
}

// ── view ──────────────────────────────────────────────────────────────────────

func sessionsView(m Model) (string, bool) {
	if m.focus != paneSessions {
		return "", false
	}
	t := ActiveTheme
	w := m.width

	var out []string
	head := "  arc │ sessions"
	if sessions.loading {
		head += "  (scanning…)"
	} else {
		head += fmt.Sprintf("  %d found", len(sessions.rows))
	}
	out = append(out, fgBold(t.ContentTitle, padRight(head, w)))
	out = append(out, fg(t.BoxBorder, strings.Repeat("─", w)))

	switch {
	case sessions.err != "":
		out = append(out, fg(t.StatusError, "  "+sessions.err))
	case len(sessions.rows) == 0 && !sessions.loading:
		out = append(out, fgFaint(t.Dimmed, "  no running "+procName+" sessions"))
	}

	for i, r := range sessions.rows {
		mark, markCol := "○", t.Dimmed
		if r.Busy {
			mark, markCol = "●", t.NavMark
		}
		cursor := "  "
		textCol := t.ContentText
		if i == sessions.cursor {
			cursor = fg(t.FocusMark, "> ")
			textCol = t.ContentTitle
		}

		// A session arc cannot switch to is dimmed and labelled rather than
		// hidden: it is genuinely running, and silently dropping it would leave
		// the user wondering where it went.
		title := r.Title
		where := r.TTY
		if !r.Reachable() {
			textCol = t.Dimmed
			where = r.TTY + " ⚠"
			if title == "" {
				title = "(not in iTerm2 or Terminal)"
			}
		}
		line := fmt.Sprintf("%-22s %-34s %6s  %s",
			truncate(shortDir(r.Dir), 22),
			truncate(title, 34),
			idleLabel(r.Idle),
			where,
		)
		out = append(out, cursor+fg(markCol, mark)+" "+fg(textCol, padRight(line, max(0, w-6))))
	}

	for len(out) < m.height-2 {
		out = append(out, "")
	}
	out = append(out, fg(t.BoxBorder, strings.Repeat("─", w)))
	out = append(out, fgFaint(t.Dimmed, "  ↵ switch   j/k move   r refresh   q close"))

	return strings.Join(out[:m.height], "\n"), true
}

// shortDir trims the common leading path so the column shows what
// distinguishes a session rather than the prefix it shares with every other one.
func shortDir(dir string) string {
	if dir == "" {
		return "(unknown)"
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return dir
	}
	// Longest first: "~/dev/go/" must win over "~/".
	for _, p := range []string{"dev/go/", ".arc/workspaces/", ""} {
		prefix := filepath.Join(home, p) + string(filepath.Separator)
		if strings.HasPrefix(dir, prefix) {
			return strings.TrimPrefix(dir, prefix)
		}
	}
	return dir
}

func idleLabel(d time.Duration) string {
	switch {
	case d == 0:
		return "—"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
}

//go:build arcsessions

// Package session discovers terminal sessions running a given program and
// brings their window to the front.
//
// It exists behind a build tag: the join it relies on is macOS- and
// iTerm2-specific, and switching between editor windows is a personal workflow
// rather than something a knowledge base should ship with.
//
// Nothing here is Claude-specific. The process name to match is a parameter, so
// the same code finds an editor or a dev server.
package session

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Session is one discovered process and the terminal window showing it.
type Session struct {
	PID   string
	TTY   string        // "s003" — the join key between the process and the window
	Dir   string        // working directory
	Title string        // terminal title, set by the program itself
	App   string        // terminal showing it; "" when none could be found
	Busy  bool          // the title says it is working
	Idle  time.Duration // since its transcript was last written; 0 if unknown
}

// Reachable reports whether Focus can switch to this session. A session in a
// terminal arc cannot drive is still listed — hiding it would be a worse lie
// than showing it as unreachable.
func (s Session) Reachable() bool { return s.App != "" }

// transcriptRoot is where Claude Code keeps per-directory session logs. Only
// used to age a session; discovery works without it.
var transcriptRoot = filepath.Join(os.Getenv("HOME"), ".claude", "projects")

// Discover lists every running process whose command contains match, joined to
// the terminal window showing it.
//
// Costs roughly 50ms per process (one lsof each) plus one AppleScript round
// trip, so callers should run it off the UI goroutine.
func Discover(ctx context.Context, match string) ([]Session, error) {
	procs, err := processes(ctx, match)
	if err != nil {
		return nil, err
	}
	titles := windowTitles(ctx) // tty -> window; empty when no terminal answers

	for i := range procs {
		procs[i].Dir = processCwd(ctx, procs[i].PID)
		if w, ok := titles[procs[i].TTY]; ok {
			procs[i].Title = cleanTitle(w.Title)
			procs[i].Busy = busyFromTitle(w.Title)
			procs[i].App = w.App
		}
		procs[i].Idle = idleFor(procs[i].Dir)
	}
	return procs, nil
}

// processes runs ps and keeps the lines whose command contains match.
func processes(ctx context.Context, match string) ([]Session, error) {
	out, err := exec.CommandContext(ctx, "ps", "-eo", "pid=,tty=,comm=").Output()
	if err != nil {
		return nil, err
	}

	var found []Session
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || !strings.Contains(f[2], match) {
			continue
		}
		// A process with no controlling terminal cannot be switched to.
		if f[1] == "??" || f[1] == "-" {
			continue
		}
		found = append(found, Session{PID: f[0], TTY: f[1]})
	}
	return found, nil
}

// processCwd resolves a pid's working directory. Returns "" when it cannot,
// which is not fatal — the session is still listed and still switchable.
func processCwd(ctx context.Context, pid string) string {
	out, err := exec.CommandContext(ctx, "lsof", "-a", "-p", pid, "-d", "cwd", "-Fn").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "n") {
			return line[1:]
		}
	}
	return ""
}

// idleFor reports how long since the newest transcript for dir was written.
//
// Claude Code encodes the directory into a folder name by replacing both "/"
// and "." with "-". Returns 0 when there is no transcript, which the caller
// displays as unknown rather than as "just now".
func idleFor(dir string) time.Duration {
	if dir == "" {
		return 0
	}
	encoded := strings.NewReplacer("/", "-", ".", "-").Replace(dir)
	entries, err := os.ReadDir(filepath.Join(transcriptRoot, encoded))
	if err != nil {
		return 0
	}

	var newest time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	if newest.IsZero() {
		return 0
	}
	return time.Since(newest)
}

// busyFromTitle reports whether a title carries a spinner frame. Claude Code
// animates one while it works and shows a static mark when it is waiting, so
// the terminal title already answers "is this one running?".
func busyFromTitle(title string) bool {
	return strings.ContainsAny(title, "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏⠂⠄⠆")
}

// cleanTitle strips the status mark and any trailing "(dir)" suffix, leaving
// the task description.
func cleanTitle(title string) string {
	t := strings.TrimSpace(title)
	for _, mark := range []string{"✳", "⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏", "⠂", "⠄", "⠆"} {
		t = strings.TrimPrefix(t, mark)
	}
	t = strings.TrimSpace(t)
	if i := strings.LastIndex(t, " ("); i > 0 && strings.HasSuffix(t, ")") {
		t = t[:i]
	}
	if t == "" {
		return "(no title)"
	}
	return t
}

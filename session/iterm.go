//go:build arcsessions

package session

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// iTerm2 is driven through AppleScript rather than its Python API: the API needs
// a daemon and a Python environment, while osascript ships with macOS and keeps
// arc a single dependency-free binary.
//
// Every session exposes its tty, which is what makes the join to a process
// possible at all — no other terminal-side identifier appears in ps output.

const listSessionsScript = `
tell application "iTerm2"
  set out to ""
  repeat with w from 1 to count of windows
    repeat with t from 1 to count of tabs of window w
      repeat with s from 1 to count of sessions of tab t of window w
        set sess to session s of tab t of window w
        set out to out & (tty of sess) & "\t" & (name of sess) & "\n"
      end repeat
    end repeat
  end repeat
  return out
end tell`

// window is what a locator knows about one terminal window.
type window struct {
	Title string
	App   string // "iTerm2" or "Terminal"; "" means no window was found
}

// windowTitles maps tty to window across every terminal arc can drive.
//
// iTerm2 wins a tie because it reports a per-session name, while Terminal.app
// only offers a composite window title that has to be parsed.
func windowTitles(ctx context.Context) map[string]window {
	out := make(map[string]window)
	for tty, title := range itermTitles(ctx) {
		out[tty] = window{Title: title, App: "iTerm2"}
	}
	for tty, title := range terminalAppTitles(ctx) {
		if _, taken := out[tty]; !taken {
			out[tty] = window{Title: title, App: "Terminal"}
		}
	}
	return out
}

// Focus brings the window showing tty to the front, using whichever terminal
// owns it. app comes from Discover.
func Focus(ctx context.Context, tty, app string) error {
	switch app {
	case "iTerm2":
		return focusITerm(ctx, tty)
	case "Terminal":
		return focusTerminalApp(ctx, tty)
	default:
		return fmt.Errorf("%s is not in a terminal arc can drive — it may be inside tmux, or in another app", tty)
	}
}

// itermTitles maps tty to session name. A missing or unresponsive iTerm2 yields
// an empty map: discovery still works, those rows just carry no title.
func itermTitles(ctx context.Context) map[string]string {
	out, err := exec.CommandContext(ctx, "osascript", "-e", listSessionsScript).Output()
	if err != nil {
		return map[string]string{}
	}

	titles := make(map[string]string)
	for _, line := range strings.Split(string(out), "\n") {
		tty, title, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		titles[shortTTY(tty)] = strings.TrimSpace(title)
	}
	return titles
}

// shortTTY reduces "/dev/ttys003" to "ttys003" — the exact form ps reports, so
// the two sides of the join compare equal. Trimming the "tty" prefix as well
// silently breaks the join and every row loses its title.
func shortTTY(dev string) string {
	return strings.TrimPrefix(strings.TrimSpace(dev), "/dev/")
}

// focusITerm brings the iTerm2 window showing tty to the front.
//
// Selecting window, tab and session in turn is required: selecting only the
// session leaves it behind whichever tab is currently active.
func focusITerm(ctx context.Context, tty string) error {
	script := fmt.Sprintf(`
tell application "iTerm2"
  repeat with w from 1 to count of windows
    repeat with t from 1 to count of tabs of window w
      repeat with s from 1 to count of sessions of tab t of window w
        if tty of session s of tab t of window w is %q then
          select window w
          select tab t of window w
          select session s of tab t of window w
          activate
          return "ok"
        end if
      end repeat
    end repeat
  end repeat
  return "not found"
end tell`, "/dev/"+tty)

	out, err := exec.CommandContext(ctx, "osascript", "-e", script).Output()
	if err != nil {
		return fmt.Errorf("could not talk to iTerm2: %w", err)
	}
	if strings.TrimSpace(string(out)) != "ok" {
		return fmt.Errorf("no iTerm2 window is showing %s", tty)
	}
	return nil
}

//go:build arcsessions

package session

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Terminal.app is supported alongside iTerm2 because a session opened there is
// still a session: leaving it out listed the row (ps sees every tty) but failed
// on Enter with "no iTerm2 window is showing ttys000", which reads as a bug
// rather than as an unsupported terminal.
//
// Unlike iTerm2 it exposes no per-session name. The window name is a composite —
// "arc — ✳ Confirm local folder write permissions — arc ◂ claude — 138×69" — so
// the title has to be picked out of it.

const listTerminalAppScript = `
tell application "Terminal"
  set out to ""
  repeat with w from 1 to count of windows
    repeat with t from 1 to count of tabs of window w
      set out to out & (tty of tab t of window w) & "\t" & (name of window w) & "\n"
    end repeat
  end repeat
  return out
end tell`

// terminalAppTitles maps tty to title for Terminal.app windows. An empty map
// when Terminal.app is not running, which is not an error.
func terminalAppTitles(ctx context.Context) map[string]string {
	out, err := exec.CommandContext(ctx, "osascript", "-e", listTerminalAppScript).Output()
	if err != nil {
		return map[string]string{}
	}

	titles := make(map[string]string)
	for _, line := range strings.Split(string(out), "\n") {
		tty, name, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		titles[shortTTY(tty)] = titleFromWindowName(name)
	}
	return titles
}

// titleFromWindowName pulls the program's title out of Terminal.app's composite
// window name, whose segments are separated by an em dash.
//
// The segment carrying a status mark is the one the program set. Without a mark
// there is nothing reliable to key on, so the first segment is used — it is the
// directory, which is still more useful than the window geometry.
func titleFromWindowName(name string) string {
	parts := strings.Split(name, " — ")
	for _, p := range parts {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		if strings.ContainsAny(p, "✳⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏⠂⠄⠆") {
			return p
		}
	}
	if len(parts) > 0 {
		return strings.TrimSpace(parts[0])
	}
	return ""
}

// focusTerminalApp raises the Terminal.app window showing tty.
func focusTerminalApp(ctx context.Context, tty string) error {
	script := fmt.Sprintf(`
tell application "Terminal"
  repeat with w from 1 to count of windows
    repeat with t from 1 to count of tabs of window w
      if tty of tab t of window w is %q then
        set selected of tab t of window w to true
        set index of window w to 1
        activate
        return "ok"
      end if
    end repeat
  end repeat
  return "not found"
end tell`, "/dev/"+tty)

	out, err := exec.CommandContext(ctx, "osascript", "-e", script).Output()
	if err != nil {
		return fmt.Errorf("could not talk to Terminal.app: %w", err)
	}
	if strings.TrimSpace(string(out)) != "ok" {
		return fmt.Errorf("no Terminal.app window is showing %s", tty)
	}
	return nil
}

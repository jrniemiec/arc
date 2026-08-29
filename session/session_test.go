//go:build arcsessions

package session

import (
	"context"
	"strings"
	"testing"
)

// The tty is the only join key between a process and its window. ps reports
// "ttys003" and AppleScript reports "/dev/ttys003"; normalising to different
// forms silently drops every title, and the list still renders, just blank.
func TestShortTTYMatchesPSForm(t *testing.T) {
	for in, want := range map[string]string{
		"/dev/ttys003":   "ttys003",
		"/dev/ttys003\n": "ttys003",
		"  /dev/ttys010": "ttys010",
		"ttys003":        "ttys003",
	} {
		if got := shortTTY(in); got != want {
			t.Errorf("shortTTY(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBusyFromTitle(t *testing.T) {
	busy := []string{
		"⠋ Analyze arc automation",
		"⠂ Analyze arc automation",
	}
	idle := []string{
		"✳ Claude Code",
		"Claude Code",
		"",
	}
	for _, s := range busy {
		if !busyFromTitle(s) {
			t.Errorf("busyFromTitle(%q) = false, want true", s)
		}
	}
	for _, s := range idle {
		if busyFromTitle(s) {
			t.Errorf("busyFromTitle(%q) = true, want false", s)
		}
	}
}

// The title is the label the user reads, so the status mark and the trailing
// "(dir)" that Claude Code appends both come off.
func TestCleanTitle(t *testing.T) {
	for in, want := range map[string]string{
		"✳ Evaluate job openings (arc)":  "Evaluate job openings",
		"⠹ Analyze arc automation (arc)": "Analyze arc automation",
		"Claude Code":                    "Claude Code",
		"✳ Claude Code":                  "Claude Code",
		"":                               "(no title)",
		"✳":                              "(no title)",
	} {
		if got := cleanTitle(in); got != want {
			t.Errorf("cleanTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

// A directory with no transcript reports zero, which the UI shows as unknown
// rather than as "just now".
func TestIdleForUnknownDirIsZero(t *testing.T) {
	if d := idleFor("/nonexistent/path/xyz"); d != 0 {
		t.Errorf("idleFor(unknown) = %v, want 0", d)
	}
	if d := idleFor(""); d != 0 {
		t.Errorf("idleFor(\"\") = %v, want 0", d)
	}
}

// Terminal.app has no per-session name, only a composite window title. The
// segment carrying a status mark is the one the program set; the rest is the
// directory, the command, and the window size.
func TestTitleFromWindowName(t *testing.T) {
	for in, want := range map[string]string{
		"arc — ✳ Confirm local folder write permissions — arc ◂ claude — 138×69": "✳ Confirm local folder write permissions",
		"arc — ⠹ Analyze automation — arc ◂ claude — 138×69":                     "⠹ Analyze automation",
		"ollama — tail -f /var/log/ollama.log — 156×89":                          "ollama",
		"": "",
	} {
		if got := titleFromWindowName(in); got != want {
			t.Errorf("titleFromWindowName(%q) = %q, want %q", in, got, want)
		}
	}
}

// A session in a terminal arc cannot drive must fail before any AppleScript
// runs, with a message that explains itself. The bug this replaces reported
// "no iTerm2 window is showing ttys000" for a session that was simply in
// Terminal.app, which reads as a defect rather than a limitation.
func TestFocusUnknownAppExplainsItself(t *testing.T) {
	err := Focus(context.Background(), "ttys000", "")
	if err == nil {
		t.Fatal("Focus with no app returned nil")
	}
	for _, want := range []string{"ttys000", "tmux"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

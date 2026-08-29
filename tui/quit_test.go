package tui

import (
	"testing"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
)

// quits reports whether a tea.Cmd is tea.Quit, by running it and inspecting the
// message. Comparing function values would not work.
func quits(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

// Quit moved to uppercase so lowercase q is free to mean "close this"
// everywhere — which it already did in the overlays and the review prompts,
// leaving q meaning two different things depending on focus.
func TestQuitIsUppercaseQ(t *testing.T) {
	m := &Model{focus: paneNav}
	if !quits(m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'Q'}})) {
		t.Error("Q did not quit")
	}

	m = &Model{focus: paneNav}
	if quits(m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})) {
		t.Error("lowercase q quit; it must stay free for close/back")
	}
}

// Typing a capital Q into a query must not end the session. /exit is the way
// out from the command pane.
func TestCommandPaneSwallowsQ(t *testing.T) {
	m := &Model{focus: paneCommand, width: 80, height: 24}
	m.input = textarea.New()
	m.input.SetWidth(40)
	m.input.SetHeight(1)
	m.input.Focus() // an unfocused textarea discards input

	if quits(m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'Q'}})) {
		t.Error("Q quit while the command pane had focus")
	}
	if got := m.input.Value(); got != "Q" {
		t.Errorf("input = %q, want the letter to have been typed", got)
	}
}

// The command exists because every other action in arc is a slash command, and
// from the command pane the only alternative was ctrl+c, which reads as killing
// the app rather than leaving it.
func TestExitCommandQuits(t *testing.T) {
	for _, cmd := range []string{"/exit", "/quit"} {
		m := &Model{focus: paneCommand}
		if !quits(m.dispatchCommand(cmd)) {
			t.Errorf("%s did not quit", cmd)
		}
	}
}

// Registration has to reach the completion lists too, or the command works but
// cannot be discovered.
//
// Both names need their own entry, not one mentioning the other: completion
// matches on the command prefix (update.go), so an alias that is only described
// in someone else's help text offers nothing when you start typing it.
func TestBothQuitCommandsAreListed(t *testing.T) {
	for name, list := range map[string][]cmdCompletion{
		"globalCommands": globalCommands,
		"chatCommands":   chatCommands,
	} {
		for _, want := range []string{"/exit", "/quit"} {
			found := false
			for _, c := range list {
				if c.cmd == want {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("%s missing from %s", want, name)
			}
		}
	}
}

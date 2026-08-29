package tui

import tea "github.com/charmbracelet/bubbletea"

// Add-on hooks.
//
// All three are nil in the default build. A file behind a build tag sets them
// from its init(), which is the only way an optional feature reaches the TUI —
// nothing here names a particular add-on, so shared code never has to know what
// was compiled in.
//
// Command names and help entries need no hook: globalCommands and helpGroups
// are package variables, so a tagged init() appends to them directly.
var (
	// addonDispatch handles a slash command. It reports false when the command
	// is not one of the add-on's, and dispatch falls through to the built-ins.
	addonDispatch func(m *Model, cmd, arg string) (tea.Cmd, bool)

	// addonKey handles a keypress while an add-on pane holds focus. It reports
	// false when focus is elsewhere.
	addonKey func(m *Model, msg tea.KeyMsg) (tea.Cmd, bool)

	// addonView renders a full-screen add-on pane. It reports false when none is
	// open, and the normal layout is drawn.
	addonView func(m Model) (string, bool)

	// addonMsg handles an add-on's own tea.Msg types. It reports false for
	// anything it does not recognise, so Update carries on to the built-ins.
	addonMsg func(msg tea.Msg) (tea.Cmd, bool)
)

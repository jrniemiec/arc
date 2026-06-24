package tui

import "github.com/charmbracelet/bubbles/key"

type keyMap struct {
	Tab1    key.Binding // 1 — Library tab
	Tab2    key.Binding // 2 — Agent tab
	Tab3    key.Binding // 3 — Stats tab
	TabPrev key.Binding // [ — previous tab
	TabNext key.Binding // ] — next tab

	NavUp   key.Binding // j / ↑
	NavDown key.Binding // k / ↓

	Select  key.Binding // Enter
	Back    key.Binding // Esc
	Expand  key.Binding // Space — expand/collapse group

	Command key.Binding // / — activate command input
	Help    key.Binding // ? — help overlay
	Quit    key.Binding // q / Ctrl+C
}

var keys = keyMap{
	Tab1: key.NewBinding(
		key.WithKeys("1"),
		key.WithHelp("1", "Library"),
	),
	Tab2: key.NewBinding(
		key.WithKeys("2"),
		key.WithHelp("2", "Agent"),
	),
	Tab3: key.NewBinding(
		key.WithKeys("3"),
		key.WithHelp("3", "Stats"),
	),
	TabPrev: key.NewBinding(
		key.WithKeys("["),
		key.WithHelp("[", "prev tab"),
	),
	TabNext: key.NewBinding(
		key.WithKeys("]"),
		key.WithHelp("]", "next tab"),
	),
	NavUp: key.NewBinding(
		key.WithKeys("k", "up"),
		key.WithHelp("k/↑", "up"),
	),
	NavDown: key.NewBinding(
		key.WithKeys("j", "down"),
		key.WithHelp("j/↓", "down"),
	),
	Select: key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "select"),
	),
	Back: key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "back"),
	),
	Expand: key.NewBinding(
		key.WithKeys(" "),
		key.WithHelp("space", "expand/collapse"),
	),
	Command: key.NewBinding(
		key.WithKeys("/"),
		key.WithHelp("/", "command"),
	),
	Help: key.NewBinding(
		key.WithKeys("?"),
		key.WithHelp("?", "help"),
	),
	Quit: key.NewBinding(
		key.WithKeys("q", "ctrl+c"),
		key.WithHelp("q", "quit"),
	),
}

package tui

import "github.com/charmbracelet/bubbles/key"

type keyMap struct {
	PaneNext key.Binding // Tab — cycle focus forward
	PanePrev key.Binding // Shift+Tab — cycle focus backward

	NavUp   key.Binding // j / ↑
	NavDown key.Binding // k / ↓
	PageUp   key.Binding // PgUp / Ctrl+U
	PageDown key.Binding // PgDn / Ctrl+D
	Home     key.Binding // Home / g
	End      key.Binding // End / G

	Select  key.Binding // Enter
	Back    key.Binding // Esc
	Expand  key.Binding // Space — expand/collapse group

	ContentTabNext key.Binding // l / → — next content sub-tab
	ContentTabPrev key.Binding // h / ← — prev content sub-tab

	Command    key.Binding // / — activate command input
	Help       key.Binding // ? — help overlay
	Open       key.Binding // o — open source URL in browser
	View       key.Binding // v — view article in external terminal
	MarkRead     key.Binding // r — mark article as read
	MarkUnread   key.Binding // u — mark article as unread
	ToggleFav    key.Binding // f — toggle favorite
	Delete       key.Binding // D — delete article
	Quit       key.Binding // q / Ctrl+C
}

var keys = keyMap{
	PaneNext: key.NewBinding(
		key.WithKeys("tab"),
		key.WithHelp("tab", "next pane"),
	),
	PanePrev: key.NewBinding(
		key.WithKeys("shift+tab"),
		key.WithHelp("shift+tab", "prev pane"),
	),
	NavUp: key.NewBinding(
		key.WithKeys("k", "up"),
		key.WithHelp("k/↑", "up"),
	),
	NavDown: key.NewBinding(
		key.WithKeys("j", "down"),
		key.WithHelp("j/↓", "down"),
	),
	PageUp: key.NewBinding(
		key.WithKeys("pgup", "ctrl+u"),
		key.WithHelp("PgUp", "page up"),
	),
	PageDown: key.NewBinding(
		key.WithKeys("pgdown", "ctrl+d"),
		key.WithHelp("PgDn", "page down"),
	),
	Home: key.NewBinding(
		key.WithKeys("home", "g"),
		key.WithHelp("Home/g", "go to top"),
	),
	End: key.NewBinding(
		key.WithKeys("end", "G"),
		key.WithHelp("End/G", "go to bottom"),
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
	ContentTabNext: key.NewBinding(
		key.WithKeys("l", "right", "ctrl+f"),
		key.WithHelp("l/→", "next tab"),
	),
	ContentTabPrev: key.NewBinding(
		key.WithKeys("h", "left", "ctrl+b"),
		key.WithHelp("h/←", "prev tab"),
	),
	Command: key.NewBinding(
		key.WithKeys("/"),
		key.WithHelp("/", "command"),
	),
	Help: key.NewBinding(
		key.WithKeys("?"),
		key.WithHelp("?", "help"),
	),
	Open: key.NewBinding(
		key.WithKeys("o"),
		key.WithHelp("o", "open in browser"),
	),
	View: key.NewBinding(
		key.WithKeys("v"),
		key.WithHelp("v", "view article in terminal"),
	),
	MarkRead: key.NewBinding(
		key.WithKeys("r"),
		key.WithHelp("r", "mark read"),
	),
	MarkUnread: key.NewBinding(
		key.WithKeys("u"),
		key.WithHelp("u", "mark unread"),
	),
	ToggleFav: key.NewBinding(
		key.WithKeys("f"),
		key.WithHelp("f", "toggle favorite"),
	),
	Delete: key.NewBinding(
		key.WithKeys("D"),
		key.WithHelp("D", "delete article"),
	),
	Quit: key.NewBinding(
		key.WithKeys("q", "ctrl+c"),
		key.WithHelp("q", "quit"),
	),
}

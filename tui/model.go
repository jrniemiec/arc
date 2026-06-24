package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// tab identifies the active top-level tab.
type tab int

const (
	tabLibrary tab = iota
	tabAgent
	tabStats
	tabCount // sentinel — number of tabs
)

func (t tab) String() string {
	switch t {
	case tabLibrary:
		return "Library"
	case tabAgent:
		return "Agent"
	case tabStats:
		return "Stats"
	default:
		return "?"
	}
}

// focusPane identifies which region has keyboard focus.
type focusPane int

const (
	paneNav     focusPane = iota // left navigator
	paneContent                  // right content pane
	paneCommand                  // command input line
)

// Model is the root bubbletea model for the arc TUI.
type Model struct {
	// Dimensions — set on WindowSizeMsg
	width  int
	height int

	// Active tab
	activeTab tab

	// Focus
	focus focusPane

	// Theme
	themeMode string // "auto" | "light" | "dark"

	// Spinner — drives cursor blink and future progress indicators
	spinnerFrame int
}

// spinnerTickMsg is sent on each spinner tick.
type spinnerTickMsg struct{}

func spinnerTick() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg {
		return spinnerTickMsg{}
	})
}

// New creates the initial Model.
func New(themeMode string) Model {
	DetectTerminal()
	ApplyTheme(themeMode)
	AdjustThemeForTerminal()

	return Model{
		activeTab: tabLibrary,
		focus:     paneNav,
		themeMode: themeMode,
	}
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		spinnerTick(),
		tea.EnableMouseCellMotion,
	)
}

package tui

import (
	"context"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jrniemiec/arc/config"
	"github.com/jrniemiec/arc/service"
	"github.com/jrniemiec/arc/store"
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

// navItem is one entry in the left navigator.
type navItem struct {
	id    string
	title string
	date  time.Time
	read  bool
	flash string // path to best flash file (empty if none)
}

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
	spinnerFrame  int
	cursorVisible bool // toggles every 4 ticks (~400 ms) for blinking cursor

	// Data
	svc *service.Service
	cfg config.Config

	// Library nav
	navItems  []navItem
	navCursor int
	navScroll int
	navLoaded bool
	navErr    string

	// Stats
	stats       service.Stats
	statsLoaded bool
}

// ── Bubbletea message types ───────────────────────────────────────────────────

type spinnerTickMsg struct{}

type navLoadedMsg struct {
	items []navItem
	err   string
}

type statsLoadedMsg struct {
	stats service.Stats
	err   string
}

// ── Cmds ─────────────────────────────────────────────────────────────────────

func spinnerTick() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg {
		return spinnerTickMsg{}
	})
}

func loadNav(svc *service.Service) tea.Cmd {
	return func() tea.Msg {
		articles, err := svc.List(context.Background(), store.Filter{})
		if err != nil {
			return navLoadedMsg{err: err.Error()}
		}
		items := make([]navItem, len(articles))
		for i, a := range articles {
			items[i] = navItem{
				id:    a.ID,
				title: a.Title,
				date:  a.IngestedAt,
				read:  a.ReadAt != nil,
				flash: a.Files.Flash,
			}
		}
		return navLoadedMsg{items: items}
	}
}

func loadStats(svc *service.Service) tea.Cmd {
	return func() tea.Msg {
		s, err := svc.Stats(context.Background())
		if err != nil {
			return statsLoadedMsg{err: err.Error()}
		}
		return statsLoadedMsg{stats: s}
	}
}

// ── Constructor ───────────────────────────────────────────────────────────────

// New creates the initial Model.
func New(svc *service.Service, cfg config.Config, themeMode string) Model {
	DetectTerminal()
	ApplyTheme(themeMode)
	AdjustThemeForTerminal()

	return Model{
		activeTab:     tabLibrary,
		focus:         paneNav,
		themeMode:     themeMode,
		cursorVisible: true,
		svc:           svc,
		cfg:           cfg,
	}
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		spinnerTick(),
		tea.EnableMouseCellMotion,
		tea.HideCursor, // we manage the cursor via fake reverse-video rendering
	}
	if m.svc != nil {
		cmds = append(cmds, loadNav(m.svc), loadStats(m.svc))
	}
	return tea.Batch(cmds...)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// flashContent reads the flash file for the selected nav item, or returns "".
func (m *Model) flashContent() string {
	if len(m.navItems) == 0 || m.navCursor < 0 || m.navCursor >= len(m.navItems) {
		return ""
	}
	path := m.navItems[m.navCursor].flash
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

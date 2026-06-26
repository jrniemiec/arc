package tui

import (
	"context"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jrniemiec/arc/config"
	"github.com/jrniemiec/arc/service"
	"github.com/jrniemiec/arc/store"
	storefs "github.com/jrniemiec/arc/store/fs"
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

// contentTab identifies the active sub-tab in the content pane.
type contentTab int

const (
	ctBody contentTab = iota
	ctSummary
	ctFlash
	ctCards
	ctCount
)

func (c contentTab) String() string {
	switch c {
	case ctBody:
		return "Body"
	case ctSummary:
		return "Summary"
	case ctFlash:
		return "Flash"
	case ctCards:
		return "Cards"
	default:
		return "?"
	}
}

// navItem is one entry in the left navigator.
type navItem struct {
	id         string
	title      string
	date       time.Time
	read       bool
	root       string // article directory (Files.Root)
	url        string // source URL
	tags       []string
	sourceType string
	summary    string // model/style label e.g. "bullets/sonnet"
	flashModel string // model label e.g. "haiku"
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

	// Nav pane layout
	navWidthOverride int  // 0 = use proportional; >0 = user-set width
	dragging         bool // true while dragging the divider
	dragCol          int  // terminal column of the divider at drag start

	// Library nav
	navItems  []navItem
	navCursor int
	navScroll int
	navLoaded bool
	navErr    string

	// Content pane — single concatenated document: Flash → Summary → Body → Cards
	contentScroll   int
	contentLines    []string        // all sections joined
	contentOffsets  [ctCount]int    // line index where each section starts (-1 = absent)
	contentHas      [ctCount]bool   // which sections exist
	contentFiles    store.Files
	contentLoading  bool

	// Stats
	stats       service.Stats
	statsLoaded bool

	// Browser
	chromeWindowID string // ID of the Chrome window opened via 'o', closed on exit

	// Command input
	inputValue        string
	inputCursor       int      // rune index into inputValue
	inputHistory      []string // oldest first, max 128
	inputHistoryIdx   int      // -1 = live editing; ≥0 = browsing history
	inputHistorySaved string   // draft saved when history browsing starts
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

type contentLoadedMsg struct {
	lines   []string
	offsets [ctCount]int
	has     [ctCount]bool
	files   store.Files
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
			tags := make([]string, len(a.Tags))
			for j, t := range a.Tags {
				tags[j] = t.Value
			}
			summaryLabel := ""
			if a.SummaryStyle != "" && a.SummaryModel != "" {
				summaryLabel = a.SummaryStyle + "/" + a.SummaryModel
			}
			items[i] = navItem{
				id:         a.ID,
				title:      a.Title,
				date:       a.IngestedAt,
				read:       a.ReadAt != nil,
				root:       a.Files.Root,
				url:        a.URL,
				tags:       tags,
				sourceType: a.SourceType,
				summary:    summaryLabel,
				flashModel: a.FlashModel,
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

// inputHistoryInit sets the initial history index (call from New).
func (m *Model) inputHistoryInit() {
	m.inputHistoryIdx = -1
}

// ChromeWindowID returns the ID of the Chrome window opened during this session.
func (m Model) ChromeWindowID() string {
	return m.chromeWindowID
}

// New creates the initial Model.
func New(svc *service.Service, cfg config.Config, themeMode string) Model {
	DetectTerminal()
	ApplyTheme(themeMode)
	AdjustThemeForTerminal()

	m := Model{
		activeTab:       tabLibrary,
		focus:           paneNav,
		themeMode:       themeMode,
		cursorVisible:   true,
		svc:             svc,
		cfg:             cfg,
		inputHistoryIdx: -1,
	}
	return m
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

// loadContent fires an async cmd to build a single concatenated document:
// Flash → Summary → Body → Cards, each preceded by a section header.
// Section order matches the natural reading flow: skim first, detail last.
func loadContent(root string, styles, models []string) tea.Cmd {
	return func() tea.Msg {
		files := storefs.ProbeFiles(root)
		files.Summary = storefs.ResolveSummary(root, styles, models)
		files.Flash = storefs.ResolveFlash(root, models)
		files.Flashcards = storefs.ResolveFlashcards(root, styles, models)

		// Section order for display
		order := []contentTab{ctFlash, ctSummary, ctBody, ctCards}

		var lines []string
		var offsets [ctCount]int
		var has [ctCount]bool

		// initialise all offsets to -1 (absent)
		for i := range offsets {
			offsets[i] = -1
		}

		for _, ct := range order {
			path := contentFilePath(files, ct)
			if path == "" {
				continue
			}
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			has[ct] = true
			offsets[ct] = len(lines)
			// Section header
			lines = append(lines, "── "+ct.String()+" ──")
			lines = append(lines, "")
			lines = append(lines, splitLines(string(data))...)
			lines = append(lines, "") // blank line between sections
		}

		return contentLoadedMsg{lines: lines, offsets: offsets, has: has, files: files}
	}
}

// contentFilePath returns the file path for the given content tab.
func contentFilePath(files store.Files, ct contentTab) string {
	switch ct {
	case ctBody:
		return files.Body
	case ctSummary:
		return files.Summary
	case ctFlash:
		return files.Flash
	case ctCards:
		return files.Flashcards
	}
	return ""
}

// splitLines splits text into lines, preserving empty lines, trimming trailing newline.
func splitLines(text string) []string {
	if text == "" {
		return nil
	}
	// strings.Split on a trailing \n produces a spurious empty last element
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	return lines
}

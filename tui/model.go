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
	paneTabBar  focusPane = iota // top tab bar (Library/Agent/Stats)
	paneNav                      // left navigator
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

// navSubTab identifies the active sub-tab inside the Library nav pane.
type navSubTab int

const (
	navSubTabArticles    navSubTab = iota
	navSubTabCollections
	navSubTabWorkspaces
	navSubTabCount
)

func (n navSubTab) String() string {
	switch n {
	case navSubTabArticles:
		return "Articles"
	case navSubTabCollections:
		return "Collections"
	case navSubTabWorkspaces:
		return "Workspaces"
	default:
		return "?"
	}
}

// navRowKind distinguishes collection header rows from article rows.
type navRowKind int

const (
	rowArticle    navRowKind = iota
	rowCollection            // a collection header row
)

// navRow is a unified display row for the Collections sub-tab tree.
type navRow struct {
	kind navRowKind

	// rowArticle fields
	item     *navItem
	indented bool // true when inside an expanded collection

	// rowCollection fields
	colSlug       string
	colName       string
	colDesc       string
	colCount      int
	colCreatedAt  time.Time
	colHasSummary bool
	colHasSystem  bool
	expanded      bool
}

// navItem is one entry in the left navigator.
type navItem struct {
	id         string
	title      string
	date       time.Time
	read       bool
	favorite   bool
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

	// Selection mode — screen frozen, mouse disabled for native text selection
	selectionMode bool

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

	// Library nav — sub-tab
	navSubTab navSubTab

	// Library nav — Articles sub-tab
	navItems  []navItem
	navCursor int
	navScroll int
	navLoaded bool
	navErr    string

	// Library nav — Collections sub-tab
	navRows           []navRow
	navRowsAll        []navRow // unfiltered copy; set on first load
	navRowCursor      int
	navRowScroll      int
	collectionsLoaded bool
	collectionsErr    string

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

	// Log viewer
	logViewerOpen bool // true while the tail window is open

	// Command input
	inputValue        string
	inputCursor       int      // rune index into inputValue
	inputHistory      []string // oldest first, max 128
	inputHistoryIdx   int      // -1 = live editing; ≥0 = browsing history
	inputHistorySaved string   // draft saved when history browsing starts

	// Command completions (first level: /prefix with no space)
	cmdComplete    []cmdCompletion // filtered completions (nil = none)
	cmdCompleteIdx int             // -1 = none highlighted; ≥0 = index

	// Param completions (second level: /cmd <partial arg>)
	paramItems []cmdCompletion // candidate values (cmd=value to insert, desc=display hint)
	paramIdx   int             // -1 = none highlighted; ≥0 = index

	// Nav filter
	navItemsAll []navItem // unfiltered copy; set on first load
	navFilter   string    // active filter description ("" = no filter)

	// Status bar
	statusMsg    string   // ephemeral 1-line command feedback
	statusLines  []string // multi-line status area (/help, /tags, command output)
	statusScroll int      // scroll offset into statusLines

	// Pending confirmation (/delete)
	pendingConfirm    func() tea.Cmd // action to run on "yes"
	pendingConfirmMsg string         // message shown while waiting
}

// cmdCompletion is one entry in the command completion popup.
type cmdCompletion struct {
	cmd  string // e.g. "/search"
	arg  string // e.g. "<query>"  (empty if no arg)
	desc string // e.g. "filter articles by text"
}

// libraryCommands is the command set for the Library tab.
var libraryCommands = []cmdCompletion{
	{"/search", "<query> [--limit N]", "full-text search (FTS5)"},
	{"/filter", "<tag>", "filter articles by tag"},
	{"/collection", "<name>", "filter articles by collection"},
	{"/clear", "", "clear active filter"},
	{"/tags", "", "list all tags"},
	{"/collections", "", "list all collections"},
	{"/open", "", "open source URL in Chrome"},
	{"/read", "", "mark current article as read"},
	{"/unread", "", "mark current article as unread"},
	{"/delete", "", "delete current article"},
	{"/reprocess", "", "regenerate summary/flash for current article"},
	{"/ingest", "<url>", "add a new article"},
	{"/stats", "", "show library stats"},
	{"/log", "", "open/close debug log tail in a new terminal window"},
	{"/help", "", "show command reference"},
}

// allCommands returns commands relevant to the active tab.
func (m *Model) allCommands() []cmdCompletion {
	switch m.activeTab {
	case tabLibrary:
		return libraryCommands
	default:
		return nil
	}
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

type collectionsLoadedMsg struct {
	collections []service.CollectionInfo
	err         string
}

type collectionArticlesLoadedMsg struct {
	slug   string
	items  []navItem
	err    string
	rowIdx int // index of the header row that triggered this load
}

type cmdDoneMsg struct {
	statusMsg   string
	statusLines []string
	err         string
	reloadNav   bool      // true = reload nav after completion
	navItems    []navItem // non-nil = replace navItems with this
	navFilter   string    // non-empty = set navFilter
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
				favorite:   a.FavoritedAt != nil,
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

func loadCollectionsTree(svc *service.Service) tea.Cmd {
	return func() tea.Msg {
		cols, err := svc.ListCollections(context.Background())
		if err != nil {
			return collectionsLoadedMsg{err: err.Error()}
		}
		return collectionsLoadedMsg{collections: cols}
	}
}

// loadCollectionArticlesCmd loads articles for one collection by filtering navItemsAll.
// all is captured by value (snapshot at dispatch time).
func loadCollectionArticlesCmd(svc *service.Service, all []navItem, slug string, rowIdx int) tea.Cmd {
	return func() tea.Msg {
		slugs, err := svc.ListCollectionArticles(context.Background(), slug)
		if err != nil {
			return collectionArticlesLoadedMsg{slug: slug, err: err.Error(), rowIdx: rowIdx}
		}
		slugSet := make(map[string]bool, len(slugs))
		for _, s := range slugs {
			slugSet[s] = true
		}
		var items []navItem
		for _, item := range all {
			if slugSet[item.id] {
				items = append(items, item)
			}
		}
		return collectionArticlesLoadedMsg{slug: slug, items: items, rowIdx: rowIdx}
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

// setStatusLines sets statusLines and resets the scroll offset.
func (m *Model) setStatusLines(lines []string) {
	m.statusLines = lines
	m.statusScroll = 0
}

// completionCount returns the number of lines currently expanding the status area.
func (m *Model) completionCount() int {
	if len(m.cmdComplete) > 0 {
		return len(m.cmdComplete)
	}
	if len(m.paramItems) > 0 {
		return len(m.paramItems)
	}
	return len(m.statusLines)
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
		inputHistory:    loadCommandHistory(historyPath(cfg.DataRoot)),
		inputHistoryIdx: -1,
		cmdCompleteIdx:  -1,
		paramIdx:        -1,
	}
	return m
}

// SaveHistory persists the command history to disk. Call after p.Run() exits.
func (m Model) SaveHistory() {
	saveCommandHistory(historyPath(m.cfg.DataRoot), m.inputHistory)
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		spinnerTick(),
		tea.EnableMouseCellMotion,
		tea.HideCursor, // we manage the cursor via fake reverse-video rendering
	}
	// On iTerm2: downgrade to basic click-only mouse mode after bubbletea
	// enables 1002h — keeps click events, drops motion tracking so native
	// drag-to-select works. Wheel is handled by alternate scroll mode (1007h).
	if ActiveTerminal == TermITerm2 {
		cmds = append(cmds, func() tea.Msg {
			DowngradeMouseMode()
			return nil
		})
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

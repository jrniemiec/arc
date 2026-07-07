package tui

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/jrniemiec/arc/chat"
	chatengine "github.com/jrniemiec/arc/chat/engine"
	"github.com/jrniemiec/arc/config"
	"github.com/jrniemiec/arc/service"
	"github.com/jrniemiec/arc/store"
	storefs "github.com/jrniemiec/arc/store/fs"
	"github.com/jrniemiec/arc/tts"
)

// resourceTTSBlock is a paragraph-level TTS unit for the resource viewer.
// text is the joined block to synthesise; cursorLine is the last line of the
// block — the resource cursor advances to it when this block starts playing.
type resourceTTSBlock struct {
	text       string
	cursorLine int
}

// scratchBlock is one navigable entry in the scratch pane.
// startLine/endLine are indices into Model.scratchLines (inclusive).
type scratchBlock struct {
	startLine int    // first display line of this block
	endLine   int    // last display line (inclusive)
	text      string // raw block text (for TTS / deletion)
	isSep     bool   // true for date separator headers (non-selectable)
}

// scratchVLine is one virtual display line in the scratch boxed view.
type scratchVLine struct {
	isBoxTop    bool
	isBoxBottom bool
	isSep       bool   // date separator line
	isHeader    bool   // header line inside selected box (hints)
	isEllipsis  bool   // collapsed indicator
	metaText    string // header/ellipsis text
	lineIdx     int    // index into scratchLines; -1 for non-content lines
	blockIdx    int    // which logical block this line belongs to
	isSelected  bool   // true when blockIdx == scratchBlockCursor
}

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
	paneStatus                   // status/output area (shell results, etc.)
	paneResource                 // full-screen resource file overlay
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
	navSubTabWorkspaces  navSubTab = iota
	navSubTabCollections
	navSubTabArticles
	navSubTabCount
)

func (n navSubTab) String() string {
	switch n {
	case navSubTabWorkspaces:
		return "Workspaces"
	case navSubTabCollections:
		return "Collections"
	case navSubTabArticles:
		return "Articles"
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

// workspaceItem is one entry in the Workspaces nav list.
type workspaceItem struct {
	name            string
	description     string
	status          string // "active" | "archived"
	createdAt       time.Time
	articleCount    int
	collectionCount int
	resourceCount   int
	outcomeCount    int
	hasSystem       bool
	hasHistory      bool
	chatProfile     string
	chatStrategy    string
	articles        []string          // slugs
	collectionSlugs []string          // slugs
	resources       []string          // resource file basenames
	outcomes        []string          // outcome file basenames

	// expand state
	expanded          bool
	expandedCols      map[string]bool // collection slug → expanded
	resourcesExpanded bool
	outcomesExpanded  bool
}

// wsRowKind distinguishes row types in the workspace tree.
type wsRowKind int

const (
	wsRowWorkspace      wsRowKind = iota
	wsRowScratch                  // scratch.md file (leaf, always present)
	wsRowCollection               // collection under workspace
	wsRowArticle                  // article (leaf)
	wsRowResourceGroup            // "Resources (N)" foldable header
	wsRowResource                 // resource file (leaf)
	wsRowOutcomeGroup             // "Outcomes (N)" foldable header
	wsRowOutcome                  // outcome file (leaf)
)

// wsRow is one display row in the workspace foldable tree.
type wsRow struct {
	kind   wsRowKind
	wsIdx  int    // index into workspaceItems
	colSlug      string // wsRowCollection rows
	slug         string // wsRowArticle rows
	title        string // article title (looked up from navItemsAll)
	count        int    // article count for wsRowCollection
	resourceName string // wsRowResource rows
	outcomeName  string // wsRowOutcome rows
}

// navItem is one entry in the left navigator.
type navItem struct {
	id           string
	title        string
	date         time.Time
	read         bool
	favorite     bool
	root         string // article directory (Files.Root)
	url          string // source URL
	tags         []string
	collections  []string
	sourceType   string
	author       string
	publishedAt  string
	feed         string
	agentReason  string
	qualityScore float64
	summary      string // model/style label e.g. "bullets/sonnet"
	flashModel   string // model label e.g. "haiku"
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
	selectionMode    bool
	preSelNavWidth   int  // saved navWidthOverride before selection mode
	selectionMaxPane focusPane // which pane is maximized during selection (paneNav or paneContent)

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

	// Library nav — Workspaces sub-tab
	workspaceItems    []workspaceItem    // current (possibly filtered) list
	workspaceItemsAll []workspaceItem    // unfiltered copy
	wsRows            []wsRow            // flat tree rows rebuilt on expand/collapse
	wsCursor          int
	wsScroll          int
	workspacesLoaded  bool
	workspacesErr     string

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

	// Input correction (Ctrl+G)
	correcting       bool   // true while correction LLM call is in flight
	correctionPrefix string // command prefix stripped before sending to LLM
	correctionFlash  string // non-empty: flash message shown in status bar

	// Command input (textarea for multi-line editing; rendering is manual)
	input             textarea.Model
	inputHistory      []string // oldest first, max 128
	inputHistoryIdx   int      // -1 = live editing; ≥0 = browsing history
	inputHistorySaved string   // draft saved when history browsing starts
	pastedBlob        string   // buffered paste content; submitted on Enter instead of inputValue

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
	statusErr    bool     // true = render statusMsg in error red
	statusLines  []string // multi-line status area (/help, /tags, command output)
	statusScroll int      // scroll offset into statusLines

	// Pending confirmation (/delete)
	pendingConfirm    func() tea.Cmd // action to run on "yes"
	pendingConfirmMsg string         // message shown while waiting

	// Chat mode
	chatMode           bool                  // true when workspace chat is active
	chatEngine         *chatengine.Engine    // nil until first message is sent (lazy init)
	chatWorkspace      string                // name of the workspace being chatted with
	chatDisplayLines   []chatLine            // rendered conversation lines (for display)
	chatScroll         int                   // scroll offset into chatDisplayLines
	chatStreaming       bool                  // true while LLM response is in flight
	chatStreamBuf      string                // accumulated streaming response text
	chatSharedBuf      *streamBuf            // goroutine-safe buffer written by streaming goroutine
	chatCancelStream   context.CancelFunc    // cancels the in-flight chat request
	chatLastUsage      *chat.Usage           // per-turn token counts (nil until first response)
	chatLastElapsed    time.Duration         // per-turn elapsed time
	chatAutoScroll     bool                  // auto-scroll to bottom (true unless user scrolled up)
	chatPendingPrompt  string                // prompt queued before engine is initialized
	chatRawMsgs        []chat.Message        // history msgs for display before engine is ready
	chatArticleCount   int                   // total articles in workspace (populated by loadChatHistoryCmd)
	chatRagMode        string                // effective RAG mode for this workspace ("open"/"strict"/"hybrid")
	chatBoxCursor      int                   // selected box index in boxed view (focus==paneContent)
	chatCollapsed      map[int]bool          // set of collapsed box indices
	programSend        *func(tea.Msg)        // p.Send closure for async streaming callbacks (shared pointer)

	// Scratch pane (split at bottom of content pane)
	scratchOpen        bool           // true when scratch split is visible
	scratchFocused     bool           // true when scratch region has focus (within paneContent)
	scratchScroll      int            // scroll offset into scratchLines
	scratchLines       []string       // cached content for rendering
	scratchBlocks      []scratchBlock // parsed blocks for block navigation
	scratchBlockCursor int            // selected block index
	scratchCollapsed   map[int]bool   // set of collapsed block indices
	// AskX pane (split at bottom of content pane, mutually exclusive with scratch)
	askxOpen          bool               // true when askX split is visible
	askxFocused       bool               // true when askX region has focus (within paneContent)
	askxScroll        int                // scroll offset into askxDisplayLines
	askxMsgs          []chat.Message     // structured message history (user + assistant pairs)
	askxDisplayLines  []chatLine         // rendered lines for display (reuses chat line types)
	askxBoxCursor     int                // selected box index (each box = user+assistant exchange)
	askxCollapsed     map[int]bool       // set of collapsed box indices
	askxStreaming        bool              // true while LLM response is in flight
	askxStreamBuf       string             // accumulated streaming response text
	askxSharedBuf       *streamBuf         // goroutine-safe buffer written by streaming goroutine
	askxCancelStream    context.CancelFunc // cancels the in-flight askX request
	askxResolvedProfile string             // profile name used for current/last askX query

	// Resource overlay (active when focus == paneResource)
	resourceLines    []string // file content split into lines
	resourceName     string   // file name shown in top bar
	resourceCursor   int      // highlighted line index
	resourceScroll   int      // scroll offset
	resourcePreFocus focusPane // focus to restore on close

	// TTS (macOS say(1))
	ttsPlayer        *tts.Player
	ttsGen           int                // tracks Player.Gen() to discard stale DoneMsgs
	ttsCurrentText   string             // text being spoken (for restart on rate change)
	resourceTTSText  string             // text of the resource block currently playing (for speed-change restart)
	resourceTTSQueue []resourceTTSBlock // paragraph blocks still to be spoken
	contentTTSText   string             // text of the content block currently playing
	contentTTSQueue  []resourceTTSBlock // paragraph blocks for content pane TTS
}

// cmdCompletion is one entry in the command completion popup.
type cmdCompletion struct {
	cmd  string // e.g. "/search"
	arg  string // e.g. "<query>"  (empty if no arg)
	desc string // e.g. "filter articles by text"
}

// globalCommands are available from any tab/sub-tab.
// They switch context before acting.
var globalCommands = []cmdCompletion{
	{"/article", "<cmd>", "article commands (list, search, ingest, …)"},
	{"/collection", "<cmd>", "collection commands (list, show, …)"},
	{"/workspace", "<cmd>", "workspace commands (list, new, delete, …)"},
	{"/scratch", "[msg]", "append to scratch / toggle scratch pane"},
	{"/askX", "<prompt>", "single-shot LLM query (no history)"},
	{"/help", "[group]", "show command reference"},
	{"/config", "", "show resolved configuration"},
	{"/config-edit", "", "open config file in $EDITOR"},
	{"/stats", "", "show library stats"},
	{"/models", "", "list available LLM profiles"},
	{"/log", "", "open/close debug log tail"},
}

// articleCommands are available when the Articles sub-tab is active.
var articleCommands = []cmdCompletion{
	{"/search", "<query> [--limit N]", "full-text search (FTS5)"},
	{"/filter", "<tag>", "filter by tag"},
	{"/favorites", "", "show only favorited articles"},
	{"/clear", "", "clear active filter"},
	{"/tags", "", "list all tags"},
	{"/collections", "", "list all collections"},
	{"/open", "", "open source URL in browser"},
	{"/read", "", "mark as read"},
	{"/unread", "", "mark as unread"},
	{"/favorite", "", "toggle favorite"},
	{"/delete", "[slug]", "delete article (selected or by name)"},
	{"/reprocess", "", "regenerate summary/flash"},
	{"/ingest", "<url>", "add a new article"},
}

// collectionCommands are available when the Collections sub-tab is active.
var collectionCommands = []cmdCompletion{
	{"/search", "<query>", "filter collections by name/slug"},
	{"/clear", "", "clear active filter"},
	{"/delete", "[slug]", "delete collection (selected or by name)"},
}

// workspaceCommands are available when the Workspaces sub-tab is active.
var workspaceCommands = []cmdCompletion{
	{"/search", "<query>", "filter workspaces by name/description"},
	{"/clear", "", "clear active filter"},
	{"/new", "<name>", "create a new workspace"},
	{"/delete", "[name]", "delete workspace (selected or by name)"},
	{"/rename", "<new-name>", "rename current workspace"},
	{"/describe", "<text>", "set workspace description"},
}

// chatCommands are available when workspace chat mode is active.
var chatCommands = []cmdCompletion{
	{"/clear", "", "clear conversation history"},
	{"/reload", "", "reset engine to pick up new articles/collections"},
	{"/stats", "", "show session token usage and cost"},
	{"/system", "", "print system prompt (includes RAG + knowledge base)"},
	{"/meta", "", "show workspace details"},
	{"/save", "[filename]", "save session to outcomes/<filename>.md"},
	{"/resource-list", "", "list files in workspace/resources/"},
	{"/resource-add", "<file>", "copy local file into workspace/resources/"},
	{"/resource-remove", "<name>", "delete a resource file (with confirmation)"},
	{"/resource-view", "<name>", "open resource file in viewer overlay"},
	{"/resource-edit", "<name>", "open resource file in $EDITOR"},
	{"/resource-new", "<name>", "create new resource file and open in $EDITOR"},
	{"/resource-save", "[filename]", "save chat session as a resource file"},
	{"/scratch", "[msg]", "append to scratch / toggle scratch pane"},
	{"/askX", "<prompt>", "single-shot LLM query (no history)"},
	{"/config", "", "show resolved configuration"},
	{"/config-edit", "", "open config file in $EDITOR"},
	{"/models", "", "list available LLM profiles"},
	{"/help", "", "show chat commands"},
}

// allCommands returns global commands plus context-specific commands for the active sub-tab.
func (m *Model) allCommands() []cmdCompletion {
	if m.chatMode {
		return chatCommands
	}
	if m.activeTab != tabLibrary {
		return globalCommands
	}
	var ctx []cmdCompletion
	switch m.navSubTab {
	case navSubTabArticles:
		ctx = articleCommands
	case navSubTabCollections:
		ctx = collectionCommands
	case navSubTabWorkspaces:
		ctx = workspaceCommands
	}
	out := make([]cmdCompletion, 0, len(ctx)+len(globalCommands))
	out = append(out, ctx...)
	out = append(out, globalCommands...)
	return out
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

type workspacesLoadedMsg struct {
	items []workspaceItem
	err   string
}

type collectionArticlesLoadedMsg struct {
	slug   string
	items  []navItem
	err    string
	rowIdx int // index of the header row that triggered this load
}

// chatHistoryLoadedMsg signals that workspace chat history has been read from disk.
type chatHistoryLoadedMsg struct {
	workspace    string
	msgs         []chat.Message
	err          string
	focus        bool   // true = user explicitly selected this workspace (Enter/click), switch focus to command pane
	articleCount int    // total articles in workspace (direct + via collections)
	ragMode      string // effective RAG mode ("open" / "strict" / "hybrid")
}

// chatReadyMsg signals that the chat engine has been constructed.
type chatReadyMsg struct {
	engine    *chatengine.Engine
	workspace string
	err       string
}

// chatStreamDoneMsg signals that the streaming response is complete.
type chatStreamDoneMsg struct {
	usage   chat.Usage
	elapsed time.Duration
	err     string
}

// askxStreamDoneMsg signals that the askX streaming response is complete.
type askxStreamDoneMsg struct {
	fullText string // complete response text
	err      string
}

// correctionDoneMsg is returned by doCorrection when the LLM call completes.
type correctionDoneMsg struct {
	text string // corrected text (empty on error)
	err  error
}

// correctionFlashMsg clears the correction flash after a delay.
type correctionFlashMsg struct{}

// ttsDoneMsg signals that TTS playback has completed or was interrupted.
type ttsDoneMsg struct {
	err error
	gen int
}

type cmdDoneMsg struct {
	statusMsg          string
	statusLines        []string
	err                string
	reloadNav          bool      // true = reload article nav after completion
	reloadCollections  bool      // true = reload collections tree after completion
	reloadWorkspaces   bool      // true = reload workspace list after completion
	navItems           []navItem // non-nil = replace navItems with this
	navFilter          string    // non-empty = set navFilter
	resetChatEngine    bool      // true = drop chatEngine for resetChatWorkspace (force re-init on next message)
	resetChatWorkspace string    // workspace name whose engine should be reset
}

// streamBuf is a goroutine-safe string buffer for streaming LLM responses.
// The streaming goroutine appends via Append; the UI reads via Get on each tick.
type streamBuf struct {
	mu  sync.Mutex
	buf string
}

func (b *streamBuf) Append(s string) {
	b.mu.Lock()
	b.buf += s
	b.mu.Unlock()
}

func (b *streamBuf) Get() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf
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
				id:           a.ID,
				title:        a.Title,
				date:         a.IngestedAt,
				read:         a.ReadAt != nil,
				favorite:     a.FavoritedAt != nil,
				root:         a.Files.Root,
				url:          a.URL,
				tags:         tags,
				collections:  a.Collections,
				sourceType:   a.SourceType,
				author:       a.Author,
				publishedAt:  a.PublishedAt,
				feed:         a.Feed,
				agentReason:  a.AgentReason,
				qualityScore: a.QualityScore,
				summary:      summaryLabel,
				flashModel:   a.FlashModel,
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

func loadWorkspaces(svc *service.Service) tea.Cmd {
	return func() tea.Msg {
		infos, err := svc.ListWorkspaces(context.Background(), false)
		if err != nil {
			return workspacesLoadedMsg{err: err.Error()}
		}
		items := make([]workspaceItem, len(infos))
		for i, w := range infos {
			items[i] = workspaceItem{
				name:            w.Name,
				description:     w.Description,
				status:          w.Status,
				createdAt:       w.CreatedAt,
				articleCount:    w.ArticleCount,
				collectionCount: w.CollectionCount,
				resourceCount:   w.ResourceCount,
				outcomeCount:    w.OutcomeCount,
				hasSystem:       w.HasSystem,
				hasHistory:      w.HasHistory,
				chatProfile:     w.ChatConfig.Profile,
				chatStrategy:    w.ChatConfig.Strategy,
				articles:        w.Articles,
				collectionSlugs: w.CollectionSlugs,
				resources:       w.ResourceNames,
				outcomes:        w.OutcomeNames,
				expandedCols:    make(map[string]bool),
			}
		}
		return workspacesLoadedMsg{items: items}
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

// askConfirm shows a confirmation prompt and moves focus to the command pane.
func (m *Model) askConfirm(msg string, fn func() tea.Cmd) {
	m.pendingConfirmMsg = msg
	m.pendingConfirm = fn
	m.focus = paneCommand
	m.cursorVisible = true
	m.input.SetValue("")
	m.input.CursorEnd()
}

// inputPrompt returns the prompt prefix for the command input pane.
func (m Model) inputPrompt() string {
	if m.chatMode {
		return m.chatWorkspace + "> "
	}
	return "> "
}

// inputVisualHeight returns the number of visual (wrapped) lines the input
// text occupies given the current terminal width, accounting for the prompt.
func (m Model) inputVisualHeight() int {
	if m.width == 0 {
		return 1
	}
	prompt := m.inputPrompt()
	const padW = 1
	line0W := m.width - padW - len([]rune(prompt))
	contW := m.width - padW
	if line0W < 1 {
		line0W = 1
	}
	if contW < 1 {
		contW = 1
	}
	total := 0
	for i, line := range strings.Split(m.input.Value(), "\n") {
		runes := []rune(line)
		wW := contW
		if i == 0 {
			wW = line0W
		}
		if len(runes) == 0 {
			total++
		} else {
			total += (len(runes) + wW - 1) / wW
		}
	}
	if total < 1 {
		total = 1
	}
	if total > 3 {
		total = 3
	}
	return total
}

// syncInputPrompt updates the textarea's prompt and width for layout calculation.
func (m *Model) syncInputPrompt() {
	prompt := m.inputPrompt()
	m.input.Prompt = prompt
	m.input.SetWidth(m.width - len([]rune(prompt)))
}

// syncInputHeight recalculates the textarea visual height and updates layout.
func (m *Model) syncInputHeight() {
	visualH := m.inputVisualHeight()
	if visualH != m.input.Height() {
		m.input.SetHeight(visualH)
	}
}

// stopTTS kills any in-flight say(1) process and clears all TTS queues.
func (m *Model) stopTTS() {
	m.ttsPlayer.Stop()
	m.ttsCurrentText = ""
	m.resourceTTSText = ""
	m.resourceTTSQueue = nil
	m.contentTTSText = ""
	m.contentTTSQueue = nil
}

func (m *Model) setStatusError(msg string) {
	m.statusMsg = msg
	m.statusErr = true
}

func (m *Model) clearStatusError() {
	m.statusErr = false
}

// completionCount returns the number of lines currently expanding the status area.
// chatBuildWidth returns the width to use when rebuilding chat display lines.
// In boxed mode (paneContent focus) 4 chars are reserved for "│ " and " │".
func (m *Model) chatBuildWidth() int {
	w := m.width - m.navWidth() - 1
	if m.focus == paneContent {
		w -= 4
	}
	if w < 10 {
		w = 10
	}
	return w
}

// chatTotalLines returns the number of scrollable lines for the chat pane.
// In boxed mode (paneContent focus) this includes the virtual box border lines.
func (m *Model) chatTotalLines() int {
	if vlines := m.buildChatVLines(); vlines != nil {
		return len(vlines)
	}
	return len(m.chatDisplayLines)
}

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

	ta := textarea.New()
	ta.Placeholder = ""
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.SetHeight(1)
	ta.Focus()

	// Style textarea with no background (raw ANSI rendering handles colors).
	noStyle := lipgloss.NewStyle()
	dimStyle := noStyle.Foreground(ActiveTheme.Dimmed)
	textStyle := noStyle.Foreground(ActiveTheme.TopBarText)
	promptStyle := noStyle.Foreground(ActiveTheme.InputPrompt)
	fullReset := textarea.Style{
		Base:             noStyle,
		CursorLine:       noStyle,
		CursorLineNumber: noStyle,
		EndOfBuffer:      noStyle,
		LineNumber:       dimStyle,
		Placeholder:      dimStyle,
		Prompt:           promptStyle,
		Text:             textStyle,
	}
	ta.FocusedStyle = fullReset
	ta.BlurredStyle = fullReset

	sendFn := func(tea.Msg) {} // placeholder, overwritten by SetProgramSend
	m := Model{
		activeTab:       tabLibrary,
		focus:           paneNav,
		themeMode:       themeMode,
		cursorVisible:   true,
		svc:             svc,
		cfg:             cfg,
		input:           ta,
		inputHistory:    loadCommandHistory(historyPath(cfg.DataRoot)),
		inputHistoryIdx: -1,
		cmdCompleteIdx:  -1,
		paramIdx:        -1,
		chatAutoScroll:  true,
		programSend:     &sendFn,
		ttsPlayer:       tts.NewPlayer(cfg.TTSVoice, cfg.TTSRate),
	}
	return m
}

// SetProgramSend stores a closure that sends messages into the tea.Program.
// Must be called after tea.NewProgram but before p.Run() so async goroutines
// (streaming) can deliver messages. Uses a shared pointer so the value is
// visible inside bubbletea's copy of the Model.
func (m *Model) SetProgramSend(send func(tea.Msg)) {
	*m.programSend = send
}

// SaveHistory persists the command history to disk. Call after p.Run() exits.
func (m Model) SaveHistory() {
	saveCommandHistory(historyPath(m.cfg.DataRoot), m.inputHistory)
}

// Cleanup releases resources that outlive the bubbletea program.
// Call after p.Run() exits.
func (m Model) Cleanup() {
	m.ttsPlayer.Stop()
}

// buildWsRows rebuilds the flat workspace tree from workspaceItems expand state.
// Article titles are looked up from navItemsAll. Call after any expand/collapse
// or after workspaceItems is set.
func (m Model) buildWsRows() []wsRow {
	// Build slug→title map from navItemsAll.
	titleOf := make(map[string]string, len(m.navItemsAll))
	for _, item := range m.navItemsAll {
		titleOf[item.id] = item.title
	}

	var rows []wsRow
	for i, ws := range m.workspaceItems {
		rows = append(rows, wsRow{kind: wsRowWorkspace, wsIdx: i})
		if !ws.expanded {
			continue
		}

		// Collections first — each collection shows ALL its articles globally.
		for _, colSlug := range ws.collectionSlugs {
			var colArticles []navItem
			for _, item := range m.navItemsAll {
				for _, c := range item.collections {
					if c == colSlug {
						colArticles = append(colArticles, item)
						break
					}
				}
			}
			rows = append(rows, wsRow{kind: wsRowCollection, wsIdx: i, colSlug: colSlug, count: len(colArticles)})
			if ws.expandedCols[colSlug] {
				for _, item := range colArticles {
					title := item.title
					if title == "" {
						title = item.id
					}
					rows = append(rows, wsRow{kind: wsRowArticle, wsIdx: i, colSlug: colSlug, slug: item.id, title: title})
				}
			}
		}

		// Then articles directly.
		for _, slug := range ws.articles {
			title := titleOf[slug]
			if title == "" {
				title = slug
			}
			rows = append(rows, wsRow{kind: wsRowArticle, wsIdx: i, slug: slug, title: title})
		}

		// Resources folder (always visible, like collections).
		rows = append(rows, wsRow{kind: wsRowResourceGroup, wsIdx: i, count: len(ws.resources)})
		if ws.resourcesExpanded {
			for _, name := range ws.resources {
				rows = append(rows, wsRow{kind: wsRowResource, wsIdx: i, resourceName: name})
			}
		}

		// Outcomes folder (always visible, like collections).
		rows = append(rows, wsRow{kind: wsRowOutcomeGroup, wsIdx: i, count: len(ws.outcomes)})
		if ws.outcomesExpanded {
			for _, name := range ws.outcomes {
				rows = append(rows, wsRow{kind: wsRowOutcome, wsIdx: i, outcomeName: name})
			}
		}

		// Scratch file — always last in expanded workspace.
		rows = append(rows, wsRow{kind: wsRowScratch, wsIdx: i})
	}
	return rows
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		spinnerTick(),
		tea.EnableMouseCellMotion,
		tea.EnableBracketedPaste,
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
		cmds = append(cmds, loadNav(m.svc), loadStats(m.svc), loadWorkspaces(m.svc))
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

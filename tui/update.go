package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/jrniemiec/arc/service"
)

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Selection mode: screen is frozen for native text selection.
	// Return unchanged model + no commands so bubbletea does not redraw.
	// Only the exit key breaks out.
	if m.selectionMode {
		if key, ok := msg.(tea.KeyMsg); ok {
			if key.Type == tea.KeyEsc || key.String() == "ctrl+\\" {
				m.selectionMode = false
				m.statusMsg = ""
				return m, tea.Batch(tea.EnableMouseCellMotion, spinnerTick())
			}
		}
		return m, nil
	}

	var cmds []tea.Cmd

	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case spinnerTickMsg:
		m.spinnerFrame = (m.spinnerFrame + 1) % 10
		// Blink cursor at ~400ms (every 4 ticks of 100ms), only when command pane focused.
		if m.spinnerFrame%4 == 0 {
			if m.focus == paneCommand {
				m.cursorVisible = !m.cursorVisible
			} else {
				m.cursorVisible = false
			}
		}
		cmds = append(cmds, spinnerTick())

	case navLoadedMsg:
		if msg.err != "" {
			m.navErr = msg.err
		} else {
			m.navItems = msg.items
			m.navItemsAll = msg.items
			m.navCursor = 0
			m.navScroll = 0
		}
		m.navLoaded = true
		// Trigger content load for the first item.
		if len(m.navItems) > 0 && m.navItems[0].root != "" {
			m.contentLoading = true
			cmds = append(cmds, loadContent(m.navItems[0].root, m.cfg.PreferredStyles, m.cfg.PreferredModels))
		}

	case statsLoadedMsg:
		if msg.err == "" {
			m.stats = msg.stats
			m.statsLoaded = true
		}

	case chromeOpenedMsg:
		if msg.err == nil && msg.windowID != "" {
			m.chromeWindowID = msg.windowID
		}

	case cmdDoneMsg:
		if msg.err != "" {
			m.statusMsg = "✗ " + msg.err
		} else {
			m.statusMsg = msg.statusMsg
			m.setStatusLines(msg.statusLines)
		}
		if msg.navItems != nil {
			m.navItems = msg.navItems
			m.navFilter = msg.navFilter
			m.navCursor = 0
			m.navScroll = 0
			if msg.err == "" {
				m.focus = paneNav
			}
			cmds = append(cmds, m.triggerContentLoad())
		}
		if msg.reloadNav && m.svc != nil {
			cmds = append(cmds, loadNav(m.svc))
		}

	case contentLoadedMsg:
		m.contentFiles = msg.files
		m.contentLines = msg.lines
		m.contentOffsets = msg.offsets
		m.contentHas = msg.has
		m.contentScroll = 0
		m.contentLoading = false

	case tea.KeyMsg:
		cmds = append(cmds, m.handleKey(msg))

	case tea.MouseMsg:
		cmds = append(cmds, m.handleMouse(msg))

	}

	return m, tea.Batch(cmds...)
}

// handleKey routes key events based on active focus pane.
func (m *Model) handleKey(msg tea.KeyMsg) tea.Cmd {
	// Global keys — always active
	switch {
	case msg.String() == "ctrl+\\":
		m.selectionMode = true
		// One final redraw shows the status message, then screen freezes.
		return tea.DisableMouse
	case key.Matches(msg, keys.Quit):
		return tea.Quit
	case key.Matches(msg, keys.Back):
		m.cmdComplete = nil
		m.cmdCompleteIdx = -1
		m.paramItems = nil
		m.paramIdx = -1
		m.statusMsg = ""
		m.statusLines = nil
		m.pendingConfirm = nil
		m.pendingConfirmMsg = ""
		m.inputValue = ""
		m.inputCursor = 0
		m.focus = paneCommand
		m.cursorVisible = true
		return nil
	case key.Matches(msg, keys.PaneNext):
		// If param picker active, Tab fills selected param into input.
		if len(m.paramItems) > 0 && m.paramIdx >= 0 {
			m.acceptParam()
			return nil
		}
		// If completions active, Tab accepts the selected command.
		if len(m.cmdComplete) > 0 {
			m.acceptCompletion()
			return nil
		}
		m.focus = (m.focus + 1) % 3
		if m.focus == paneCommand {
			m.cursorVisible = true
		}
		return nil
	case key.Matches(msg, keys.PanePrev):
		m.focus = (m.focus + 2) % 3 // +2 mod 3 = -1 mod 3
		if m.focus == paneCommand {
			m.cursorVisible = true
		}
		return nil
	}

	// Pane-specific keys
	switch m.focus {
	case paneNav:
		return m.handleNavKey(msg)
	case paneContent:
		return m.handleContentKey(msg)
	case paneCommand:
		return m.handleCommandKey(msg)
	}

	return nil
}

func (m *Model) handleNavKey(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, keys.ContentTabPrev):
		m.activeTab = (m.activeTab - 1 + tabCount) % tabCount
		return nil
	case key.Matches(msg, keys.ContentTabNext):
		m.activeTab = (m.activeTab + 1) % tabCount
		return nil
	case key.Matches(msg, keys.NavUp):
		if m.navCursor > 0 {
			m.navCursor--
			m.clampNavScroll()
			return m.triggerContentLoad()
		}
	case key.Matches(msg, keys.NavDown):
		if m.navCursor < len(m.navItems)-1 {
			m.navCursor++
			m.clampNavScroll()
			return m.triggerContentLoad()
		}
	case key.Matches(msg, keys.Select):
		if len(m.navItems) > 0 {
			m.focus = paneContent
		}
	case key.Matches(msg, keys.MarkRead):
		return m.cmdMarkRead()
	case key.Matches(msg, keys.MarkUnread):
		return m.cmdMarkUnread()
	case key.Matches(msg, keys.Delete):
		return m.cmdDeleteArticle()
	case key.Matches(msg, keys.Open):
		return m.openCurrentURL()
	case key.Matches(msg, keys.Command):
		m.focus = paneCommand
		m.cursorVisible = true
		m.inputValue = "/"
		m.inputCursor = 1
		m.updateCompletions()
	case key.Matches(msg, keys.Help):
		// help overlay in future phases
	}
	return nil
}

func (m *Model) handleContentKey(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, keys.NavUp):
		if m.contentScroll > 0 {
			m.contentScroll--
		}
	case key.Matches(msg, keys.NavDown):
		maxScroll := len(m.contentLines) - m.contentViewHeight()
		if maxScroll < 0 {
			maxScroll = 0
		}
		if m.contentScroll < maxScroll {
			m.contentScroll++
		}
	case key.Matches(msg, keys.ContentTabNext):
		return m.cycleContentTab(1)
	case key.Matches(msg, keys.ContentTabPrev):
		return m.cycleContentTab(-1)
	case key.Matches(msg, keys.Open):
		return m.openCurrentURL()
	}
	return nil
}

func (m *Model) handleCommandKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.Type {
	case tea.KeyRunes:
		m.inputExitHistory()
		m.inputInsert(msg.Runes)
		m.updateCompletions()
	case tea.KeySpace:
		m.inputExitHistory()
		m.inputInsert([]rune{' '})
		m.updateCompletions()
	case tea.KeyBackspace, tea.KeyCtrlH:
		m.inputExitHistory()
		m.inputDeleteBefore()
		m.updateCompletions()
	case tea.KeyDelete:
		m.inputExitHistory()
		m.inputDeleteAt()
		m.updateCompletions()
	case tea.KeyLeft, tea.KeyCtrlB:
		if m.inputCursor > 0 {
			m.inputCursor--
		}
	case tea.KeyRight, tea.KeyCtrlF:
		runes := []rune(m.inputValue)
		if m.inputCursor < len(runes) {
			m.inputCursor++
		}
	case tea.KeyHome, tea.KeyCtrlA:
		m.inputCursor = 0
	case tea.KeyEnd, tea.KeyCtrlE:
		m.inputCursor = len([]rune(m.inputValue))
	case tea.KeyCtrlU:
		m.inputExitHistory()
		m.inputValue = ""
		m.inputCursor = 0
		m.updateCompletions()
	case tea.KeyCtrlK:
		m.inputExitHistory()
		runes := []rune(m.inputValue)
		m.inputValue = string(runes[:m.inputCursor])
		m.updateCompletions()
	case tea.KeyTab:
		m.acceptCompletion()
	case tea.KeyUp:
		if len(m.paramItems) > 0 {
			if m.paramIdx > 0 {
				m.paramIdx--
			}
		} else if len(m.cmdComplete) > 0 {
			if m.cmdCompleteIdx > 0 {
				m.cmdCompleteIdx--
			}
		} else {
			m.inputHistoryPrev()
		}
	case tea.KeyDown:
		if len(m.paramItems) > 0 {
			if m.paramIdx < len(m.paramItems)-1 {
				m.paramIdx++
			}
		} else if len(m.cmdComplete) > 0 {
			if m.cmdCompleteIdx < len(m.cmdComplete)-1 {
				m.cmdCompleteIdx++
			}
		} else {
			m.inputHistoryNext()
		}
	case tea.KeyEnter:
		// Param picker: Enter fills selected value into input but does not execute.
		if len(m.paramItems) > 0 && m.paramIdx >= 0 {
			m.acceptParam()
			return nil
		}
		// Completion list: Enter on a no-arg command executes; on commands with args, fills like Tab.
		if len(m.cmdComplete) > 0 && m.cmdCompleteIdx >= 0 {
			c := m.cmdComplete[m.cmdCompleteIdx]
			if c.arg == "" {
				// No arg needed — execute immediately.
				m.cmdComplete = nil
				m.cmdCompleteIdx = -1
				m.inputValue = ""
				m.inputCursor = 0
				return m.dispatchCommand(c.cmd)
			}
			// Has arg — fill + space and show param picker, same as Tab.
			m.acceptCompletion()
			return nil
		}
		val := strings.TrimSpace(m.inputValue)
		m.inputSubmit()
		m.cmdComplete = nil
		m.cmdCompleteIdx = -1
		// Confirmation flow
		if m.pendingConfirm != nil {
			if val == "yes" {
				fn := m.pendingConfirm
				m.pendingConfirm = nil
				m.pendingConfirmMsg = ""
				return fn()
			}
			m.pendingConfirm = nil
			m.pendingConfirmMsg = ""
			m.statusMsg = "cancelled"
			return nil
		}
		if val != "" {
			return m.dispatchCommand(val)
		}
	}
	return nil
}

// inputInsert inserts runes at the cursor position.
func (m *Model) inputInsert(runes []rune) {
	r := []rune(m.inputValue)
	r = append(r[:m.inputCursor], append(runes, r[m.inputCursor:]...)...)
	m.inputValue = string(r)
	m.inputCursor += len(runes)
}

// inputDeleteBefore deletes the rune immediately before the cursor (backspace).
func (m *Model) inputDeleteBefore() {
	if m.inputCursor == 0 {
		return
	}
	r := []rune(m.inputValue)
	r = append(r[:m.inputCursor-1], r[m.inputCursor:]...)
	m.inputValue = string(r)
	m.inputCursor--
}

// inputDeleteAt deletes the rune at the cursor position (delete key).
func (m *Model) inputDeleteAt() {
	r := []rune(m.inputValue)
	if m.inputCursor >= len(r) {
		return
	}
	r = append(r[:m.inputCursor], r[m.inputCursor+1:]...)
	m.inputValue = string(r)
}

// inputExitHistory exits history browsing mode, keeping the current value.
func (m *Model) inputExitHistory() {
	m.inputHistoryIdx = -1
	m.inputHistorySaved = ""
}

// inputHistoryPrev navigates to the previous (older) history entry.
func (m *Model) inputHistoryPrev() {
	if len(m.inputHistory) == 0 {
		return
	}
	if m.inputHistoryIdx == -1 {
		m.inputHistorySaved = m.inputValue
		m.inputHistoryIdx = len(m.inputHistory) - 1
	} else if m.inputHistoryIdx > 0 {
		m.inputHistoryIdx--
	} else {
		return
	}
	m.inputValue = m.inputHistory[m.inputHistoryIdx]
	m.inputCursor = len([]rune(m.inputValue))
}

// inputHistoryNext navigates to the next (newer) history entry, or restores draft.
func (m *Model) inputHistoryNext() {
	if m.inputHistoryIdx == -1 {
		return
	}
	if m.inputHistoryIdx < len(m.inputHistory)-1 {
		m.inputHistoryIdx++
		m.inputValue = m.inputHistory[m.inputHistoryIdx]
	} else {
		m.inputValue = m.inputHistorySaved
		m.inputHistoryIdx = -1
		m.inputHistorySaved = ""
	}
	m.inputCursor = len([]rune(m.inputValue))
}

// inputSubmit pushes the current value to history and clears the input.
func (m *Model) inputSubmit() {
	val := strings.TrimSpace(m.inputValue)
	if val != "" {
		m.pushHistory(val)
	}
	m.inputValue = ""
	m.inputCursor = 0
	m.inputHistoryIdx = -1
	m.inputHistorySaved = ""
}

// pushHistory appends val to history, deduplicating consecutive identical entries.
// Caps entries at 64 runes. Max 128 entries total.
func (m *Model) pushHistory(val string) {
	runes := []rune(val)
	if len(runes) > 64 {
		val = string(runes[:60]) + " ..."
	}
	if len(m.inputHistory) > 0 && m.inputHistory[len(m.inputHistory)-1] == val {
		return
	}
	m.inputHistory = append(m.inputHistory, val)
	if len(m.inputHistory) > 128 {
		m.inputHistory = m.inputHistory[1:]
	}
}

// cycleContentTab jumps contentScroll to the next/prev present section.
func (m *Model) cycleContentTab(delta int) tea.Cmd {
	cur := m.activeSection()
	// Collect present sections in display order
	order := []contentTab{ctFlash, ctSummary, ctBody, ctCards}
	var present []contentTab
	for _, ct := range order {
		if m.contentHas[ct] {
			present = append(present, ct)
		}
	}
	if len(present) == 0 {
		return nil
	}
	// Find index of current section
	idx := 0
	for i, ct := range present {
		if ct == cur {
			idx = i
			break
		}
	}
	next := present[(idx+delta+len(present))%len(present)]
	if m.contentOffsets[next] >= 0 {
		m.contentScroll = m.contentOffsets[next]
	}
	return nil
}

// triggerContentLoad fires loadContent for the current nav cursor item.
func (m *Model) triggerContentLoad() tea.Cmd {
	if m.navCursor < 0 || m.navCursor >= len(m.navItems) {
		return nil
	}
	root := m.navItems[m.navCursor].root
	if root == "" {
		return nil
	}
	m.contentLoading = true
	m.contentLines = nil
	return loadContent(root, m.cfg.PreferredStyles, m.cfg.PreferredModels)
}

// openCurrentURL opens the source URL of the current nav item in a new Chrome window.
func (m *Model) openCurrentURL() tea.Cmd {
	if m.navCursor < 0 || m.navCursor >= len(m.navItems) {
		return nil
	}
	url := m.navItems[m.navCursor].url
	if url == "" {
		return nil
	}
	return openInChrome(url)
}

// activeSection returns the content tab whose section is currently visible at the top
// of the content scroll position. Walks offsets in display order, returns the last
// section whose offset is ≤ contentScroll.
func (m *Model) activeSection() contentTab {
	order := []contentTab{ctFlash, ctSummary, ctBody, ctCards}
	active := ctBody // fallback
	for _, ct := range order {
		if m.contentHas[ct] {
			active = ct
			break
		}
	}
	for _, ct := range order {
		if m.contentHas[ct] && m.contentOffsets[ct] >= 0 && m.contentOffsets[ct] <= m.contentScroll {
			active = ct
		}
	}
	return active
}

// clampNavScroll adjusts navScroll so navCursor stays within the visible window.
func (m *Model) clampNavScroll() {
	visibleHeight := m.navPaneHeight()
	if visibleHeight < 1 {
		return
	}
	if m.navCursor < m.navScroll {
		m.navScroll = m.navCursor
	} else if m.navCursor >= m.navScroll+visibleHeight {
		m.navScroll = m.navCursor - visibleHeight + 1
	}
}

// updateCompletions recomputes the completion list based on the current input.
// Shows completions when input starts with "/" and contains no space.
// Shows param suggestions when input is a known command followed by a space.
func (m *Model) updateCompletions() {
	val := m.inputValue
	m.statusLines = nil

	if !strings.HasPrefix(val, "/") {
		m.cmdComplete = nil
		m.cmdCompleteIdx = -1
		return
	}

	// Param suggestion mode: "/cmd " with optional partial arg
	if strings.Contains(val, " ") {
		m.cmdComplete = nil
		m.cmdCompleteIdx = -1
		parts := strings.SplitN(val, " ", 2)
		cmd := strings.ToLower(parts[0])
		arg := strings.ToLower(parts[1])
		all := m.paramSuggestions(cmd)
		// Filter by partial arg
		var filtered []string
		for _, s := range all {
			if arg == "" || strings.HasPrefix(strings.ToLower(s), arg) {
				filtered = append(filtered, s)
			}
		}
		m.paramItems = filtered
		if len(filtered) > 0 {
			m.paramIdx = 0
		} else {
			m.paramIdx = -1
		}
		return
	}

	// Completion mode: "/prefix" with no space
	prefix := strings.ToLower(val)
	var filtered []cmdCompletion
	for _, c := range m.allCommands() {
		if strings.HasPrefix(c.cmd, prefix) {
			filtered = append(filtered, c)
		}
	}
	m.cmdComplete = filtered
	if m.cmdCompleteIdx >= len(filtered) {
		m.cmdCompleteIdx = len(filtered) - 1
	}
	if len(filtered) > 0 && m.cmdCompleteIdx < 0 {
		m.cmdCompleteIdx = 0
	}
}

// paramSuggestions returns candidate values for commands that take a known arg.
func (m *Model) paramSuggestions(cmd string) []string {
	switch cmd {
	case "/filter":
		seen := map[string]bool{}
		var tags []string
		for _, item := range m.navItemsAll {
			for _, tag := range item.tags {
				if !seen[tag] {
					seen[tag] = true
					tags = append(tags, tag)
				}
			}
		}
		return tags

	case "/collection":
		return nil // user can run /collections to see list

	case "/help":
		return []string{"filter", "article", "library"}
	}
	return nil
}

// acceptParam fills the selected param value into the input, replacing any partial arg.
func (m *Model) acceptParam() {
	if m.paramIdx < 0 || m.paramIdx >= len(m.paramItems) {
		return
	}
	val := m.paramItems[m.paramIdx]
	// Replace everything after the first space with the selected value.
	input := m.inputValue
	if idx := strings.Index(input, " "); idx >= 0 {
		input = input[:idx]
	}
	m.inputValue = input + " " + val
	m.inputCursor = len([]rune(m.inputValue))
	m.paramItems = nil
	m.paramIdx = -1
}

// acceptCompletion fills the input with the selected command + space (if it takes an arg).
// Then immediately populates paramItems if the command has param suggestions.
func (m *Model) acceptCompletion() {
	if m.cmdCompleteIdx < 0 || m.cmdCompleteIdx >= len(m.cmdComplete) {
		return
	}
	c := m.cmdComplete[m.cmdCompleteIdx]
	if c.arg != "" {
		m.inputValue = c.cmd + " "
	} else {
		m.inputValue = c.cmd
	}
	m.inputCursor = len([]rune(m.inputValue))
	m.cmdComplete = nil
	m.cmdCompleteIdx = -1
	// Immediately show param picker if this command has suggestions.
	params := m.paramSuggestions(c.cmd)
	m.paramItems = params
	if len(params) > 0 {
		m.paramIdx = 0
	} else {
		m.paramIdx = -1
	}
}

// dispatchCommand parses and executes a submitted command string.
func (m *Model) dispatchCommand(val string) tea.Cmd {
	m.statusLines = nil
	m.statusMsg = ""
	m.pendingConfirm = nil
	m.pendingConfirmMsg = ""

	parts := strings.Fields(val)
	if len(parts) == 0 {
		return nil
	}
	cmd := strings.ToLower(parts[0])
	arg := ""
	if len(parts) > 1 {
		arg = strings.Join(parts[1:], " ")
	}

	switch cmd {
	case "/search":
		if arg == "" {
			m.statusMsg = "usage: /search <query> [--limit N]"
			return nil
		}
		query, limit := parseSearchArg(arg)
		if m.svc == nil {
			m.applyNavFilter("search", query)
			return nil
		}
		m.statusMsg = "searching…"
		return cmdFTSSearch(m.svc, query, limit)

	case "/filter":
		if arg == "" {
			m.statusMsg = "usage: /filter <tag>"
			return nil
		}
		m.applyNavFilter("tag", arg)
		return nil

	case "/collection":
		if arg == "" {
			m.statusMsg = "usage: /collection <name>"
			return nil
		}
		return m.filterByCollection(arg)

	case "/clear":
		m.navItems = m.navItemsAll
		m.navFilter = ""
		m.navCursor = 0
		m.navScroll = 0
		m.focus = paneNav
		m.statusMsg = "✓ filter cleared"
		return m.triggerContentLoad()

	case "/tags":
		return m.cmdTags()

	case "/collections":
		return m.cmdCollections()

	case "/open":
		return m.openCurrentURL()

	case "/read":
		return m.cmdMarkRead()

	case "/unread":
		return m.cmdMarkUnread()

	case "/delete":
		return m.cmdDeleteArticle()

	case "/reprocess":
		return m.cmdReprocess()

	case "/ingest":
		if arg == "" {
			m.statusMsg = "usage: /ingest <url>"
			return nil
		}
		return m.cmdIngest(arg)

	case "/stats":
		return m.cmdStats()

	case "/log", "/logs":
		return m.cmdLog()

	case "/help":
		m.setStatusLines(m.helpLines(arg))
		return nil

	default:
		m.statusMsg = "✗ unknown command: " + parts[0]
		return nil
	}
}

// applyNavFilter filters navItems from navItemsAll by mode ("search" or "tag") and query.
func (m *Model) applyNavFilter(mode, query string) {
	q := strings.ToLower(query)
	var filtered []navItem
	for _, item := range m.navItemsAll {
		switch mode {
		case "search":
			if strings.Contains(strings.ToLower(item.title), q) ||
				strings.Contains(strings.ToLower(item.url), q) {
				filtered = append(filtered, item)
			}
		case "tag":
			for _, tag := range item.tags {
				if strings.Contains(strings.ToLower(tag), q) {
					filtered = append(filtered, item)
					break
				}
			}
		}
	}
	m.navItems = filtered
	m.navCursor = 0
	m.navScroll = 0
	n := len(filtered)
	if n == 0 {
		m.statusMsg = fmt.Sprintf("no results for %q", query)
		m.navFilter = ""
	} else {
		m.navFilter = mode + ": " + query + " · " + fmt.Sprintf("%d", n) + " results  ·  /clear to reset"
		m.statusMsg = ""
	}
}

// cmdMarkRead marks the current article as read in-memory and persists to DB.
func (m *Model) cmdMarkRead() tea.Cmd {
	if m.navCursor < 0 || m.navCursor >= len(m.navItems) {
		m.statusMsg = "✗ no article selected"
		return nil
	}
	id := m.navItems[m.navCursor].id
	m.navItems[m.navCursor].read = true
	for i, item := range m.navItemsAll {
		if item.id == id {
			m.navItemsAll[i].read = true
			break
		}
	}
	m.statusMsg = "✓ marked as read"
	if m.svc == nil {
		return nil
	}
	svc := m.svc
	return func() tea.Msg {
		_ = svc.MarkRead(context.Background(), id)
		return nil
	}
}

// cmdMarkUnread marks the current article as unread in-memory and persists to DB.
func (m *Model) cmdMarkUnread() tea.Cmd {
	if m.navCursor < 0 || m.navCursor >= len(m.navItems) {
		m.statusMsg = "✗ no article selected"
		return nil
	}
	id := m.navItems[m.navCursor].id
	m.navItems[m.navCursor].read = false
	for i, item := range m.navItemsAll {
		if item.id == id {
			m.navItemsAll[i].read = false
			break
		}
	}
	m.statusMsg = "✓ marked as unread"
	if m.svc == nil {
		return nil
	}
	svc := m.svc
	return func() tea.Msg {
		_ = svc.MarkUnread(context.Background(), id)
		return nil
	}
}

// cmdDeleteArticle prompts for confirmation then deletes the current article.
func (m *Model) cmdDeleteArticle() tea.Cmd {
	if m.navCursor < 0 || m.navCursor >= len(m.navItems) {
		m.statusMsg = "✗ no article selected"
		return nil
	}
	item := m.navItems[m.navCursor]
	m.pendingConfirmMsg = fmt.Sprintf("delete %q? type yes + Enter to confirm, Esc to cancel", item.title)
	m.pendingConfirm = func() tea.Cmd {
		id := item.id
		svc := m.svc
		// Remove from in-memory lists immediately
		m.navItems = removeNavItem(m.navItems, id)
		m.navItemsAll = removeNavItem(m.navItemsAll, id)
		if m.navCursor >= len(m.navItems) {
			m.navCursor = len(m.navItems) - 1
		}
		m.statusMsg = "✓ deleted"
		m.pendingConfirm = nil
		m.pendingConfirmMsg = ""
		if svc == nil {
			return nil
		}
		return func() tea.Msg {
			_ = svc.DeleteArticle(context.Background(), id)
			return nil
		}
	}
	return nil
}

// cmdTags shows all tags from navItemsAll in the status area.
func (m *Model) cmdTags() tea.Cmd {
	seen := map[string]bool{}
	var tags []string
	for _, item := range m.navItemsAll {
		for _, tag := range item.tags {
			if !seen[tag] {
				seen[tag] = true
				tags = append(tags, tag)
			}
		}
	}
	if len(tags) == 0 {
		m.statusMsg = "(no tags found)"
		return nil
	}
	lines := make([]string, 0, len(tags)+1)
	lines = append(lines, fmt.Sprintf("tags (%d):", len(tags)))
	for _, t := range tags {
		lines = append(lines, "  "+t)
	}
	m.setStatusLines(lines)
	return nil
}

// cmdCollections shows all collections in the status area.
func (m *Model) cmdCollections() tea.Cmd {
	if m.svc == nil {
		m.statusMsg = "✗ service unavailable"
		return nil
	}
	svc := m.svc
	return func() tea.Msg {
		cols, err := svc.ListCollections(context.Background())
		if err != nil {
			return cmdDoneMsg{err: err.Error()}
		}
		if len(cols) == 0 {
			return cmdDoneMsg{statusMsg: "(no collections)"}
		}
		lines := make([]string, 0, len(cols)+1)
		lines = append(lines, fmt.Sprintf("collections (%d):", len(cols)))
		for _, c := range cols {
			line := "  " + c.Slug
			if c.Description != "" {
				line += "  — " + c.Description
			}
			lines = append(lines, line)
		}
		return cmdDoneMsg{statusLines: lines}
	}
}

// filterByCollection filters nav pane to articles in the given collection.
func (m *Model) filterByCollection(name string) tea.Cmd {
	if m.svc == nil {
		m.statusMsg = "✗ service unavailable"
		return nil
	}
	svc := m.svc
	all := m.navItemsAll
	return func() tea.Msg {
		slugs, err := svc.ListCollectionArticles(context.Background(), name)
		if err != nil {
			return cmdDoneMsg{err: fmt.Sprintf("collection %q: %v", name, err)}
		}
		slugSet := map[string]bool{}
		for _, s := range slugs {
			slugSet[s] = true
		}
		var filtered []navItem
		for _, item := range all {
			if slugSet[item.id] {
				filtered = append(filtered, item)
			}
		}
		if len(filtered) == 0 {
			return cmdDoneMsg{err: fmt.Sprintf("collection %q: no articles found", name)}
		}
		return cmdDoneMsg{
			navItems:  filtered,
			navFilter: fmt.Sprintf("collection: %s · %d articles  ·  /clear to reset", name, len(filtered)),
		}
	}
}

// parseSearchArg splits a /search arg string into query and optional --limit value.
// e.g. "go concurrency --limit 50" → ("go concurrency", 50)
func parseSearchArg(arg string) (query string, limit int) {
	const flag = "--limit"
	if idx := strings.Index(arg, flag); idx != -1 {
		rest := strings.TrimSpace(arg[idx+len(flag):])
		before := strings.TrimSpace(arg[:idx])
		var n int
		if _, err := fmt.Sscanf(rest, "%d", &n); err == nil && n > 0 {
			return before, n
		}
	}
	return arg, 0
}

// cmdFTSSearch runs a full-text search via the FTS5 index and replaces nav with results.
// limit=0 uses the service default (20).
func cmdFTSSearch(svc *service.Service, query string, limit int) tea.Cmd {
	return func() tea.Msg {
		results, err := svc.Search(context.Background(), service.SearchRequest{Query: query, Limit: limit})
		if err != nil {
			return cmdDoneMsg{err: fmt.Sprintf("search: %v", err)}
		}
		if len(results) == 0 {
			return cmdDoneMsg{
				statusMsg: fmt.Sprintf("no results for %q", query),
				navItems:  []navItem{},
				navFilter: fmt.Sprintf("search: %q · 0 results  ·  /clear to reset", query),
			}
		}
		items := make([]navItem, len(results))
		for i, r := range results {
			a := r.Article
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
		return cmdDoneMsg{
			navItems:  items,
			navFilter: fmt.Sprintf("search: %q · %d results  ·  /clear to reset", query, len(items)),
		}
	}
}

// cmdStats shows library stats in the status area.
func (m *Model) cmdStats() tea.Cmd {
	if !m.statsLoaded {
		m.statusMsg = "stats not loaded yet"
		return nil
	}
	s := m.stats
	lines := []string{
		fmt.Sprintf("articles: %d  ·  unread: %d  ·  collections: %d", s.TotalArticles, s.Unread, s.TotalCollections),
		fmt.Sprintf("cost this month: %s  ·  total: %s", formatUSD(s.CostThisMonth), formatUSD(s.CostTotal)),
	}
	m.setStatusLines(lines)
	return nil
}

// cmdLog opens or closes a tail of the arc log file in a new terminal window.
// Calling it a second time writes a sentinel file that signals the tail script to exit.
func (m *Model) cmdLog() tea.Cmd {
	pid := os.Getpid()
	sentinelPath := fmt.Sprintf("%s/arc-log-stop-%d", os.TempDir(), pid)

	if m.logViewerOpen {
		_ = os.WriteFile(sentinelPath, nil, 0o644)
		m.logViewerOpen = false
		m.statusMsg = "log viewer closed"
		return nil
	}

	logPath := m.cfg.LogPath
	scriptPath := fmt.Sprintf("%s/arc-log-viewer-%d.sh", os.TempDir(), pid)

	script := fmt.Sprintf(
		"#!/bin/bash\ntrap 'rm -f %q %q' EXIT\ntail -n 200 -f %q & __t=$!\nwhile kill -0 %d 2>/dev/null && [ ! -f %q ]; do sleep 1; done\nkill $__t 2>/dev/null\n",
		scriptPath, sentinelPath, logPath, pid, sentinelPath,
	)
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		m.statusMsg = fmt.Sprintf("✗ /log: could not write script: %v", err)
		return nil
	}

	var appleScript string
	switch ActiveTerminal {
	case TermITerm2:
		appleScript = fmt.Sprintf(
			`tell application "iTerm2" to create window with default profile command %q`,
			scriptPath,
		)
	default:
		appleScript = fmt.Sprintf(
			`tell application "Terminal" to do script %q`,
			scriptPath,
		)
	}
	go exec.Command("osascript", "-e", appleScript).Run() //nolint:errcheck
	m.logViewerOpen = true
	m.statusMsg = "log viewer opened — /log again to close"
	return nil
}

// cmdReprocess regenerates summary/flash for the current article.
func (m *Model) cmdReprocess() tea.Cmd {
	if m.navCursor < 0 || m.navCursor >= len(m.navItems) {
		m.statusMsg = "✗ no article selected"
		return nil
	}
	if m.svc == nil {
		m.statusMsg = "✗ service unavailable"
		return nil
	}
	item := m.navItems[m.navCursor]
	svc := m.svc
	cfg := m.cfg
	m.statusMsg = "⠸ reprocessing " + item.id + "…"
	return func() tea.Msg {
		req := service.ReprocessRequest{
			Slug: item.id,
		}
		_ = cfg
		_, err := svc.Reprocess(context.Background(), req)
		if err != nil {
			return cmdDoneMsg{err: err.Error()}
		}
		return cmdDoneMsg{statusMsg: "✓ reprocessed " + item.id, reloadNav: false}
	}
}

// cmdIngest ingests a new article from a URL.
func (m *Model) cmdIngest(url string) tea.Cmd {
	if m.svc == nil {
		m.statusMsg = "✗ service unavailable"
		return nil
	}
	svc := m.svc
	cfg := m.cfg
	m.statusMsg = "⠸ ingesting " + url + "…"
	return func() tea.Msg {
		req := service.IngestRequest{
			URL:      url,
			Progress: func(msg string) {},
		}
		_ = cfg
		result, err := svc.Ingest(context.Background(), req)
		if err != nil {
			return cmdDoneMsg{err: err.Error()}
		}
		return cmdDoneMsg{
			statusMsg: fmt.Sprintf("✓ ingested %s", result.Slug),
			reloadNav: true,
		}
	}
}

// helpGroups defines the command groups for the Library tab.
var helpGroups = []struct {
	name     string
	commands []cmdCompletion
}{
	{"filter", []cmdCompletion{
		{"/search", "<query> [--limit N]", "full-text search (FTS5)"},
		{"/filter", "<tag>", "filter articles by tag"},
		{"/collection", "<name>", "filter articles by collection"},
		{"/clear", "", "clear active filter"},
		{"/tags", "", "list all tags"},
		{"/collections", "", "list all collections"},
	}},
	{"article", []cmdCompletion{
		{"/open", "", "open source URL in Chrome"},
		{"/read", "", "mark current article as read"},
		{"/unread", "", "mark current article as unread"},
		{"/delete", "", "delete current article"},
		{"/reprocess", "", "regenerate summary/flash for current article"},
	}},
	{"library", []cmdCompletion{
		{"/ingest", "<url>", "add a new article"},
		{"/stats", "", "show library stats"},
		{"/log", "", "open/close debug log tail in a new terminal window"},
	}},
}

// helpLines returns context-sensitive help for the active tab.
// group="" shows all groups; group="filter"|"article"|"library" shows that group only.
func (m *Model) helpLines(group string) []string {
	if m.activeTab != tabLibrary {
		return []string{"no commands available for this tab"}
	}

	renderGroup := func(g struct {
		name     string
		commands []cmdCompletion
	}) []string {
		lines := []string{g.name + ":"}
		for _, c := range g.commands {
			synopsis := c.cmd
			if c.arg != "" {
				synopsis += " " + c.arg
			}
			lines = append(lines, fmt.Sprintf("  %-24s  %s", synopsis, c.desc))
		}
		return lines
	}

	if group == "" {
		var lines []string
		for i, g := range helpGroups {
			if i > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, renderGroup(g)...)
		}
		return lines
	}

	for _, g := range helpGroups {
		if g.name == group {
			return renderGroup(g)
		}
	}
	return []string{fmt.Sprintf("unknown group %q — available: filter, article, library", group)}
}

// removeNavItem removes the item with the given id from a slice.
func removeNavItem(items []navItem, id string) []navItem {
	out := items[:0]
	for _, item := range items {
		if item.id != id {
			out = append(out, item)
		}
	}
	return out
}

// handleMouse handles mouse press, release, and motion events.
func (m *Model) handleMouse(msg tea.MouseMsg) tea.Cmd {
	divCol := m.dividerCol()

	// Mouse wheel scrolling.
	if msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown {
		delta := 1
		if msg.Button == tea.MouseButtonWheelUp {
			delta = -1
		}

		// Status area — scrolls when statusLines visible, regardless of position.
		if len(m.statusLines) > 0 {
			maxVisible := m.height * 30 / 100
			if maxVisible < 3 {
				maxVisible = 3
			}
			m.statusScroll += delta
			if m.statusScroll < 0 {
				m.statusScroll = 0
			}
			maxScroll := len(m.statusLines) - maxVisible
			if maxScroll < 0 {
				maxScroll = 0
			}
			if m.statusScroll > maxScroll {
				m.statusScroll = maxScroll
			}
			return nil
		}

		// Nav pane wheel (left of divider).
		if msg.X < m.dividerCol() {
			m.navScroll += delta
			if m.navScroll < 0 {
				m.navScroll = 0
			}
			max := len(m.navItems) - m.navPaneHeight()
			if max < 0 {
				max = 0
			}
			if m.navScroll > max {
				m.navScroll = max
			}
			// Keep cursor within visible window.
			if m.navCursor < m.navScroll {
				m.navCursor = m.navScroll
			} else if m.navCursor >= m.navScroll+m.navPaneHeight() {
				m.navCursor = m.navScroll + m.navPaneHeight() - 1
			}
			return m.triggerContentLoad()
		}

		// Content pane wheel (right of divider).
		if msg.X > m.dividerCol() {
			m.contentScroll += delta
			if m.contentScroll < 0 {
				m.contentScroll = 0
			}
			maxScroll := len(m.contentLines) - m.contentViewHeight()
			if maxScroll < 0 {
				maxScroll = 0
			}
			if m.contentScroll > maxScroll {
				m.contentScroll = maxScroll
			}
			return nil
		}
	}

	switch msg.Action {
	case tea.MouseActionPress:
		if msg.Button == tea.MouseButtonLeft {
			// Click on tab bar (row 0).
			if msg.Y == 0 {
				if t := tabBarHitTest(msg.X); t >= 0 {
					m.activeTab = t
				}
				return nil
			}
			// Check command input row first — it spans full width.
			cmdRow := m.height - hintBarHeight - m.completionCount() - statusSepHeight - cmdBarHeight
			if msg.Y == cmdRow {
				m.focus = paneCommand
				m.cursorVisible = true
				return nil
			}
			// Clicking on or within 1 col of the divider starts a drag.
			if msg.X >= divCol-1 && msg.X <= divCol+1 {
				m.dragging = true
				m.dragCol = msg.X
				return nil
			}
			// Click in nav pane (left of divider) — focus nav, update cursor row.
			if msg.X < divCol {
				m.focus = paneNav
				if cmd := m.clickNavRow(msg.Y); cmd != nil {
					return cmd
				}
				return nil
			}
			// Click in content pane.
			m.focus = paneContent
		}

	case tea.MouseActionMotion:
		if m.dragging {
			newW := msg.X
			if newW < 10 {
				newW = 10
			}
			if newW > m.width-10 {
				newW = m.width - 10
			}
			m.navWidthOverride = newW
		}

	case tea.MouseActionRelease:
		m.dragging = false
	}

	return nil
}

// clickNavRow moves navCursor to the item at the given terminal row (0-based Y).
func (m *Model) clickNavRow(y int) tea.Cmd {
	// Nav content starts at row: topBarHeight (2) + 2 header lines = row 4
	contentStartRow := topBarHeight + 2
	idx := m.navScroll + (y - contentStartRow)
	if idx >= 0 && idx < len(m.navItems) {
		m.navCursor = idx
		return m.triggerContentLoad()
	}
	return nil
}

// navPaneHeight returns usable item lines in the nav pane.
func (m *Model) navPaneHeight() int {
	// fixed: top bar (2) + split sep (1) + cmd (1) + status sep (1) + status bar (1) = 6
	// plus completions expanding upward, plus 2 nav header rows
	h := m.height - 6 - m.completionCount() - 2
	if h < 1 {
		h = 1
	}
	return h
}

// contentViewHeight returns the number of lines available for scrollable content.
// Layout: header (4 lines) + sep + tab strip + sep = 7 fixed lines in content pane.
func (m *Model) contentViewHeight() int {
	mainH := m.height - 6 - m.completionCount()
	h := mainH - contentHeaderLines
	if h < 1 {
		h = 1
	}
	return h
}

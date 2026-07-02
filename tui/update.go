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
	storefs "github.com/jrniemiec/arc/store/fs"
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
		m.spinnerFrame++
		// Blink cursor at ~400ms (every 4 ticks of 100ms), only when command pane focused.
		if m.spinnerFrame%4 == 0 {
			if m.focus == paneCommand {
				m.cursorVisible = !m.cursorVisible
			} else {
				m.cursorVisible = false
			}
		}
		// During streaming, rebuild chat lines on each tick to animate wave + show new content.
		if m.chatMode && m.chatStreaming {
			m.rebuildChatLines(m.chatBuildWidth())
			chatViewH := m.height - 6 - m.completionCount() - 2
			m.chatAutoScrollToBottom(chatViewH)
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
			// Rebuild wsRows now that article titles are available.
			if m.workspacesLoaded {
				m.wsRows = m.buildWsRows()
			}
		}
		m.navLoaded = true
		// Trigger content load for the first item.
		if len(m.navItems) > 0 && m.navItems[0].root != "" {
			m.contentLoading = true
			cmds = append(cmds, loadContent(m.navItems[0].root, m.cfg.PreferredStyles, m.cfg.PreferredModels))
		}

	case collectionsLoadedMsg:
		if msg.err != "" {
			m.collectionsErr = msg.err
		} else {
			rows := make([]navRow, 0, len(msg.collections))
			for _, c := range msg.collections {
				rows = append(rows, navRow{
					kind:          rowCollection,
					colSlug:       c.Slug,
					colName:       c.Name,
					colDesc:       c.Description,
					colCount:      c.ArticleCount,
					colCreatedAt:  c.CreatedAt,
					colHasSummary: c.HasSummary,
					colHasSystem:  c.HasSystem,
				})
			}
			m.navRows = rows
			m.navRowsAll = rows
			m.navRowCursor = 0
			m.navRowScroll = 0
		}
		m.collectionsLoaded = true

	case collectionArticlesLoadedMsg:
		if msg.err != "" {
			m.statusMsg = "✗ " + msg.err
		} else {
			// Find header by slug (index may have shifted from concurrent expands).
			headerIdx := -1
			for i, r := range m.navRows {
				if r.kind == rowCollection && r.colSlug == msg.slug {
					headerIdx = i
					break
				}
			}
			if headerIdx >= 0 {
				m.navRows[headerIdx].expanded = true
				children := make([]navRow, 0, len(msg.items))
				for i := range msg.items {
					item := msg.items[i]
					children = append(children, navRow{
						kind:     rowArticle,
						item:     &item,
						indented: true,
					})
				}
				before := make([]navRow, headerIdx+1)
				copy(before, m.navRows[:headerIdx+1])
				after := make([]navRow, len(m.navRows)-(headerIdx+1))
				copy(after, m.navRows[headerIdx+1:])
				m.navRows = append(append(before, children...), after...)
				m.clampNavRowScroll()
				m.statusMsg = ""
			}
		}

	case workspacesLoadedMsg:
		if msg.err != "" {
			m.workspacesErr = msg.err
		} else {
			m.workspaceItemsAll = msg.items
			m.workspaceItems = msg.items
			m.wsRows = m.buildWsRows()
			m.wsCursor = 0
			m.wsScroll = 0
		}
		m.workspacesLoaded = true
		// Auto-load history for first workspace if on Workspaces sub-tab.
		if m.navSubTab == navSubTabWorkspaces {
			cmds = append(cmds, m.triggerWorkspaceChatLoad())
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
		if msg.reloadCollections && m.svc != nil {
			m.collectionsLoaded = false
			m.focus = paneNav
			cmds = append(cmds, loadCollectionsTree(m.svc))
		}
		if msg.reloadWorkspaces && m.svc != nil {
			m.workspacesLoaded = false
			m.focus = paneNav
			cmds = append(cmds, loadWorkspaces(m.svc))
		}
		if msg.resetChatEngine && msg.resetChatWorkspace != "" &&
			m.chatMode && m.chatWorkspace == msg.resetChatWorkspace {
			m.chatEngine = nil
			if m.statusMsg == "" {
				m.statusMsg = "✓ context reloaded — engine will reinitialise on next message"
			}
		}

	case contentLoadedMsg:
		m.contentFiles = msg.files
		m.contentLines = msg.lines
		m.contentOffsets = msg.offsets
		m.contentHas = msg.has
		m.contentScroll = 0
		m.contentLoading = false

	case chatHistoryLoadedMsg:
		if msg.err != "" {
			m.statusMsg = "✗ " + msg.err
		} else {
			// Cancel any in-flight stream from a previous workspace.
			if m.chatCancelStream != nil {
				m.chatCancelStream()
				m.chatCancelStream = nil
			}
			m.chatMode = true
			m.chatEngine = nil       // lazy — engine init deferred to first message
			m.chatPendingPrompt = "" // clear any pending prompt from previous workspace
			m.chatWorkspace = msg.workspace
			m.chatRawMsgs = msg.msgs
			m.chatArticleCount = msg.articleCount
			m.chatRagMode = msg.ragMode
			m.chatAutoScroll = true
			m.chatStreaming = false
			m.chatStreamBuf = ""
			m.chatLastUsage = nil
			m.chatLastElapsed = 0
			if msg.focus {
				m.focus = paneCommand
				m.cursorVisible = true
			}
			m.rebuildChatLines(m.chatBuildWidth())
			chatViewH := m.height - 6 - m.completionCount() - 2
			m.chatAutoScrollToBottom(chatViewH)
			m.statusMsg = ""
		}

	case chatReadyMsg:
		if msg.err != "" {
			// Only show error if still on the same workspace.
			if m.chatWorkspace == msg.workspace {
				m.statusMsg = "✗ chat: " + msg.err
				m.setStatusLines([]string{"Chat initialization failed:", msg.err})
			}
		} else if m.chatMode && m.chatWorkspace == msg.workspace {
			// Only apply if user hasn't navigated away.
			m.chatEngine = msg.engine
			// Sync raw msgs from engine history.
			m.chatRawMsgs = msg.engine.History().Msgs
			m.rebuildChatLines(m.chatBuildWidth())
			m.statusMsg = ""
			// If a prompt was queued for this workspace, send it now.
			if m.chatPendingPrompt != "" {
				prompt := m.chatPendingPrompt
				m.chatPendingPrompt = ""
				cmds = append(cmds, m.sendChatMsg(prompt))
			}
		}

	case chatStreamDeltaMsg:
		m.chatStreamBuf += string(msg)
		m.rebuildChatLines(m.chatBuildWidth())
		chatViewH := m.height - 6 - m.completionCount() - 2 // -2 for chat header+sep
		m.chatAutoScrollToBottom(chatViewH)

	case chatStreamDoneMsg:
		m.chatStreaming = false
		m.chatStreamBuf = ""
		if m.chatCancelStream != nil {
			m.chatCancelStream = nil
		}
		if msg.err != "" {
			m.statusMsg = "✗ " + msg.err
		} else {
			usage := msg.usage
			m.chatLastUsage = &usage
			m.chatLastElapsed = msg.elapsed
		}
		m.rebuildChatLines(m.chatBuildWidth())
		chatViewH := m.height - 6 - m.completionCount() - 2
		m.chatAutoScrollToBottom(chatViewH)

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
	case key.Matches(msg, keys.Quit) && !(m.focus == paneCommand && msg.String() == "q"):
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
		// In chat mode, Esc always returns focus to command input — never exits chat.
		// Use /exit or q to leave chat.
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
		m.focus = (m.focus + 1) % 4 // TabBar → Nav → Content → Command → TabBar
		if m.focus == paneCommand {
			m.cursorVisible = true
		}
		if m.chatMode {
			m.rebuildChatLines(m.chatBuildWidth())
		}
		return nil
	case key.Matches(msg, keys.PanePrev):
		m.focus = (m.focus + 3) % 4 // +3 mod 4 = -1 mod 4
		if m.focus == paneCommand {
			m.cursorVisible = true
		}
		if m.chatMode {
			m.rebuildChatLines(m.chatBuildWidth())
		}
		return nil
	}

	// Pane-specific keys
	switch m.focus {
	case paneTabBar:
		return m.handleTabBarKey(msg)
	case paneNav:
		return m.handleNavKey(msg)
	case paneContent:
		return m.handleContentKey(msg)
	case paneCommand:
		return m.handleCommandKey(msg)
	}

	return nil
}

// handleTabBarKey handles keys when the top tab bar has focus.
// ←/→ cycle top-level tabs; ↓ or Enter drops focus to nav pane.
// j/k and all other nav keys are intentionally ignored.
func (m *Model) handleTabBarKey(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, keys.ContentTabPrev):
		if m.chatMode {
			m.exitChatMode()
		}
		m.activeTab = (m.activeTab - 1 + tabCount) % tabCount
	case key.Matches(msg, keys.ContentTabNext):
		if m.chatMode {
			m.exitChatMode()
		}
		m.activeTab = (m.activeTab + 1) % tabCount
	case key.Matches(msg, keys.NavDown), key.Matches(msg, keys.Select):
		m.focus = paneNav
	}
	return nil
}

func (m *Model) handleNavKey(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, keys.ContentTabPrev):
		return m.navLeft()
	case key.Matches(msg, keys.ContentTabNext):
		return m.navRight()
	case key.Matches(msg, keys.NavUp):
		return m.navCursorUp()
	case key.Matches(msg, keys.NavDown):
		return m.navCursorDown()
	case key.Matches(msg, keys.PageUp):
		return m.navPageUp()
	case key.Matches(msg, keys.PageDown):
		return m.navPageDown()
	case key.Matches(msg, keys.Home):
		return m.navHome()
	case key.Matches(msg, keys.End):
		return m.navEnd()
	case key.Matches(msg, keys.Expand):
		return m.navToggleExpand()
	case key.Matches(msg, keys.Select):
		return m.navSelect()
	case key.Matches(msg, keys.MarkRead):
		return m.cmdMarkRead()
	case key.Matches(msg, keys.MarkUnread):
		return m.cmdMarkUnread()
	case key.Matches(msg, keys.ToggleFav):
		return m.cmdToggleFavorite()
	case key.Matches(msg, keys.Delete):
		switch m.navSubTab {
		case navSubTabWorkspaces:
			return m.cmdDeleteWorkspace()
		case navSubTabCollections:
			return m.cmdDeleteCollection()
		default:
			return m.cmdDeleteArticle()
		}
	case key.Matches(msg, keys.Open):
		return m.openCurrentURL()
	case key.Matches(msg, keys.View):
		return m.cmdViewArticle()
	case key.Matches(msg, keys.Command):
		m.focus = paneCommand
		m.cursorVisible = true
		m.inputValue = "/"
		m.inputCursor = 1
		m.updateCompletions()
	case key.Matches(msg, keys.Help):
		m.setStatusLines(m.helpLines("keys"))
	}
	return nil
}

// navLeft handles ← in the nav pane — cycles sub-tabs.
func (m *Model) navLeft() tea.Cmd {
	if m.activeTab != tabLibrary {
		m.activeTab = (m.activeTab - 1 + tabCount) % tabCount
		return nil
	}
	return m.switchNavSubTab((m.navSubTab - 1 + navSubTabCount) % navSubTabCount)
}

// navRight handles → in the nav pane — cycles sub-tabs.
func (m *Model) navRight() tea.Cmd {
	if m.activeTab != tabLibrary {
		m.activeTab = (m.activeTab + 1) % tabCount
		return nil
	}
	return m.switchNavSubTab((m.navSubTab + 1) % navSubTabCount)
}

// navCursorUp moves the cursor up in the active sub-tab.
func (m *Model) navCursorUp() tea.Cmd {
	switch m.navSubTab {
	case navSubTabArticles:
		if m.navCursor > 0 {
			m.navCursor--
			m.clampNavScroll()
			return m.triggerContentLoad()
		}
	case navSubTabCollections:
		if m.navRowCursor > 0 {
			m.navRowCursor--
			m.clampNavRowScroll()
			return m.triggerCollectionContentLoad()
		}
	case navSubTabWorkspaces:
		if m.wsCursor > 0 {
			m.wsCursor--
			m.clampWsScroll()
			return m.triggerWorkspaceChatLoad()
		}
	}
	return nil
}

// navCursorDown moves the cursor down in the active sub-tab.
func (m *Model) navCursorDown() tea.Cmd {
	switch m.navSubTab {
	case navSubTabArticles:
		if m.navCursor < len(m.navItems)-1 {
			m.navCursor++
			m.clampNavScroll()
			return m.triggerContentLoad()
		}
	case navSubTabCollections:
		if m.navRowCursor < len(m.navRows)-1 {
			m.navRowCursor++
			m.clampNavRowScroll()
			return m.triggerCollectionContentLoad()
		}
	case navSubTabWorkspaces:
		if m.wsCursor < len(m.wsRows)-1 {
			m.wsCursor++
			m.clampWsScroll()
			return m.triggerWorkspaceChatLoad()
		}
	}
	return nil
}

// navPageUp scrolls the nav pane up by one page.
func (m *Model) navPageUp() tea.Cmd {
	h := m.navPaneHeight()
	switch m.navSubTab {
	case navSubTabArticles:
		m.navCursor -= h
		if m.navCursor < 0 {
			m.navCursor = 0
		}
		m.clampNavScroll()
		return m.triggerContentLoad()
	case navSubTabCollections:
		m.navRowCursor -= h
		if m.navRowCursor < 0 {
			m.navRowCursor = 0
		}
		m.clampNavRowScroll()
		return m.triggerCollectionContentLoad()
	case navSubTabWorkspaces:
		m.wsCursor -= h
		if m.wsCursor < 0 {
			m.wsCursor = 0
		}
		m.clampWsScroll()
	}
	return nil
}

// navPageDown scrolls the nav pane down by one page.
func (m *Model) navPageDown() tea.Cmd {
	h := m.navPaneHeight()
	switch m.navSubTab {
	case navSubTabArticles:
		m.navCursor += h
		if m.navCursor >= len(m.navItems) {
			m.navCursor = len(m.navItems) - 1
		}
		if m.navCursor < 0 {
			m.navCursor = 0
		}
		m.clampNavScroll()
		return m.triggerContentLoad()
	case navSubTabCollections:
		m.navRowCursor += h
		if m.navRowCursor >= len(m.navRows) {
			m.navRowCursor = len(m.navRows) - 1
		}
		if m.navRowCursor < 0 {
			m.navRowCursor = 0
		}
		m.clampNavRowScroll()
		return m.triggerCollectionContentLoad()
	case navSubTabWorkspaces:
		m.wsCursor += h
		if m.wsCursor >= len(m.wsRows) {
			m.wsCursor = len(m.wsRows) - 1
		}
		if m.wsCursor < 0 {
			m.wsCursor = 0
		}
		m.clampWsScroll()
	}
	return nil
}

// navHome jumps the nav cursor to the first item.
func (m *Model) navHome() tea.Cmd {
	switch m.navSubTab {
	case navSubTabArticles:
		m.navCursor = 0
		m.clampNavScroll()
		return m.triggerContentLoad()
	case navSubTabCollections:
		m.navRowCursor = 0
		m.clampNavRowScroll()
		return m.triggerCollectionContentLoad()
	case navSubTabWorkspaces:
		m.wsCursor = 0
		m.clampWsScroll()
	}
	return nil
}

// navEnd jumps the nav cursor to the last item.
func (m *Model) navEnd() tea.Cmd {
	switch m.navSubTab {
	case navSubTabArticles:
		if len(m.navItems) > 0 {
			m.navCursor = len(m.navItems) - 1
			m.clampNavScroll()
			return m.triggerContentLoad()
		}
	case navSubTabCollections:
		if len(m.navRows) > 0 {
			m.navRowCursor = len(m.navRows) - 1
			m.clampNavRowScroll()
			return m.triggerCollectionContentLoad()
		}
	case navSubTabWorkspaces:
		if len(m.wsRows) > 0 {
			m.wsCursor = len(m.wsRows) - 1
			m.clampWsScroll()
		}
	}
	return nil
}

// navToggleExpand toggles expand/collapse on a collection header (Space key).
func (m *Model) navToggleExpand() tea.Cmd {
	if m.navSubTab == navSubTabWorkspaces {
		m.wsToggleExpand()
		return nil
	}
	if m.navSubTab != navSubTabCollections || m.navRowCursor >= len(m.navRows) {
		return nil
	}
	row := m.navRows[m.navRowCursor]
	if row.kind != rowCollection {
		return nil
	}
	if row.expanded {
		return m.collapseCollection(m.navRowCursor)
	}
	return m.expandCollection(m.navRowCursor)
}

// navSelect handles Enter in the nav pane.
func (m *Model) navSelect() tea.Cmd {
	switch m.navSubTab {
	case navSubTabArticles:
		if len(m.navItems) > 0 {
			m.focus = paneContent
		}
	case navSubTabCollections:
		if m.navRowCursor >= len(m.navRows) {
			return nil
		}
		row := m.navRows[m.navRowCursor]
		if row.kind == rowCollection {
			return m.navToggleExpand()
		}
		if row.kind == rowArticle {
			m.focus = paneContent
		}
	case navSubTabWorkspaces:
		if m.wsCursor < 0 || m.wsCursor >= len(m.wsRows) {
			return nil
		}
		row := m.wsRows[m.wsCursor]
		switch row.kind {
		case wsRowWorkspace:
			// Enter on workspace → load history (engine init deferred to first message).
			if row.wsIdx >= 0 && row.wsIdx < len(m.workspaceItems) {
				ws := m.workspaceItems[row.wsIdx]
				return m.loadChatHistoryCmd(ws.name, true)
			}
		case wsRowArticle:
			return m.cmdViewArticle()
		case wsRowCollection:
			m.wsToggleExpand()
		}
	}
	return nil
}

// switchNavSubTab switches to the given Library nav sub-tab.
func (m *Model) switchNavSubTab(sub navSubTab) tea.Cmd {
	if m.chatMode && sub != navSubTabWorkspaces {
		m.exitChatMode()
	}
	m.navSubTab = sub
	m.navRowCursor = 0
	m.navRowScroll = 0
	m.navCursor = 0
	m.navScroll = 0
	if sub == navSubTabCollections && !m.collectionsLoaded && m.svc != nil {
		return loadCollectionsTree(m.svc)
	}
	if sub == navSubTabWorkspaces && m.svc != nil {
		if !m.workspacesLoaded {
			return loadWorkspaces(m.svc)
		}
		// Already loaded — trigger history load for first workspace immediately.
		return m.triggerWorkspaceChatLoad()
	}
	return nil
}

// expandCollection starts an async load of articles for a collapsed collection header.
func (m *Model) expandCollection(rowIdx int) tea.Cmd {
	if rowIdx < 0 || rowIdx >= len(m.navRows) {
		return nil
	}
	row := m.navRows[rowIdx]
	if row.kind != rowCollection || row.expanded || m.svc == nil {
		return nil
	}
	m.statusMsg = "loading " + row.colSlug + "…"
	return loadCollectionArticlesCmd(m.svc, m.navItemsAll, row.colSlug, rowIdx)
}

// collapseCollection removes child article rows from an expanded collection.
func (m *Model) collapseCollection(rowIdx int) tea.Cmd {
	if rowIdx < 0 || rowIdx >= len(m.navRows) || m.navRows[rowIdx].kind != rowCollection {
		return nil
	}
	m.navRows[rowIdx].expanded = false
	// Remove consecutive indented article children after the header.
	i := rowIdx + 1
	for i < len(m.navRows) && m.navRows[i].kind == rowArticle && m.navRows[i].indented {
		i++
	}
	m.navRows = append(m.navRows[:rowIdx+1], m.navRows[i:]...)
	if m.navRowCursor > rowIdx {
		m.navRowCursor = rowIdx
	}
	m.clampNavRowScroll()
	return nil
}


// triggerCollectionContentLoad loads content for the article under navRowCursor.
// triggerWorkspaceChatLoad loads chat history if cursor is on a workspace row.
func (m *Model) triggerWorkspaceChatLoad() tea.Cmd {
	if m.wsCursor < 0 || m.wsCursor >= len(m.wsRows) {
		return nil
	}
	row := m.wsRows[m.wsCursor]
	if row.kind != wsRowWorkspace {
		return nil
	}
	if row.wsIdx < 0 || row.wsIdx >= len(m.workspaceItems) {
		return nil
	}
	return m.loadChatHistoryCmd(m.workspaceItems[row.wsIdx].name, false)
}

func (m *Model) triggerCollectionContentLoad() tea.Cmd {
	if m.navRowCursor < 0 || m.navRowCursor >= len(m.navRows) {
		return nil
	}
	row := m.navRows[m.navRowCursor]
	if row.kind != rowArticle || row.item == nil || row.item.root == "" {
		return nil
	}
	m.contentLoading = true
	m.contentLines = nil
	return loadContent(row.item.root, m.cfg.PreferredStyles, m.cfg.PreferredModels)
}

// clampNavRowScroll keeps navRowCursor visible within the scroll window.
func (m *Model) clampNavRowScroll() {
	h := m.navPaneHeight()
	if h < 1 {
		h = 1
	}
	if m.navRowCursor < m.navRowScroll {
		m.navRowScroll = m.navRowCursor
	} else if m.navRowCursor >= m.navRowScroll+h {
		m.navRowScroll = m.navRowCursor - h + 1
	}
}

func (m *Model) clampWsScroll() {
	h := m.navPaneHeight()
	if h < 1 {
		h = 1
	}
	if m.wsCursor < m.wsScroll {
		m.wsScroll = m.wsCursor
	} else if m.wsCursor >= m.wsScroll+h {
		m.wsScroll = m.wsCursor - h + 1
	}
}

// wsToggleExpand toggles expand/collapse for the workspace tree row at wsCursor.
func (m *Model) wsToggleExpand() {
	if m.wsCursor < 0 || m.wsCursor >= len(m.wsRows) {
		return
	}
	row := m.wsRows[m.wsCursor]
	if row.wsIdx < 0 || row.wsIdx >= len(m.workspaceItems) {
		return
	}
	ws := &m.workspaceItems[row.wsIdx]
	switch row.kind {
	case wsRowWorkspace:
		ws.expanded = !ws.expanded
	case wsRowCollection:
		if ws.expandedCols == nil {
			ws.expandedCols = make(map[string]bool)
		}
		ws.expandedCols[row.colSlug] = !ws.expandedCols[row.colSlug]
	}
	m.wsRows = m.buildWsRows()
	m.clampWsScroll()
}

func (m *Model) handleContentKey(msg tea.KeyMsg) tea.Cmd {
	if m.chatMode {
		return m.handleChatContentKey(msg)
	}
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
	case key.Matches(msg, keys.PageUp):
		m.contentScroll -= m.contentViewHeight()
		if m.contentScroll < 0 {
			m.contentScroll = 0
		}
	case key.Matches(msg, keys.PageDown):
		maxScroll := len(m.contentLines) - m.contentViewHeight()
		if maxScroll < 0 {
			maxScroll = 0
		}
		m.contentScroll += m.contentViewHeight()
		if m.contentScroll > maxScroll {
			m.contentScroll = maxScroll
		}
	case key.Matches(msg, keys.Home):
		m.contentScroll = 0
	case key.Matches(msg, keys.End):
		maxScroll := len(m.contentLines) - m.contentViewHeight()
		if maxScroll < 0 {
			maxScroll = 0
		}
		m.contentScroll = maxScroll
	case key.Matches(msg, keys.ContentTabNext):
		return m.cycleContentTab(1)
	case key.Matches(msg, keys.ContentTabPrev):
		return m.cycleContentTab(-1)
	case key.Matches(msg, keys.Open):
		return m.openCurrentURL()
	case key.Matches(msg, keys.ToggleFav):
		return m.cmdToggleFavorite()
	}
	return nil
}

// handleChatContentKey handles j/k/PgUp/PgDn in the content pane during chat mode.
func (m *Model) handleChatContentKey(msg tea.KeyMsg) tea.Cmd {
	chatViewH := m.height - 6 - m.completionCount() - 2
	if chatViewH < 1 {
		chatViewH = 1
	}
	maxScroll := m.chatTotalLines() - chatViewH
	if maxScroll < 0 {
		maxScroll = 0
	}
	switch {
	case key.Matches(msg, keys.NavUp):
		if m.chatScroll > 0 {
			m.chatScroll--
			m.chatAutoScroll = false
		}
	case key.Matches(msg, keys.NavDown):
		if m.chatScroll < maxScroll {
			m.chatScroll++
		}
		if m.chatScroll >= maxScroll {
			m.chatAutoScroll = true
		}
	case key.Matches(msg, keys.PageUp):
		m.chatScroll -= chatViewH
		if m.chatScroll < 0 {
			m.chatScroll = 0
		}
		m.chatAutoScroll = false
	case key.Matches(msg, keys.PageDown):
		m.chatScroll += chatViewH
		if m.chatScroll > maxScroll {
			m.chatScroll = maxScroll
		}
		if m.chatScroll >= maxScroll {
			m.chatAutoScroll = true
		}
	case key.Matches(msg, keys.Home):
		m.chatScroll = 0
		m.chatAutoScroll = false
	case key.Matches(msg, keys.End):
		m.chatScroll = maxScroll
		m.chatAutoScroll = true
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
		} else if m.chatMode {
			// Scroll chat up from command pane.
			if m.chatScroll > 0 {
				m.chatScroll--
				m.chatAutoScroll = false
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
		} else if m.chatMode {
			// Scroll chat down from command pane.
			chatViewH := m.height - 6 - m.completionCount() - 2
			maxScroll := m.chatTotalLines() - chatViewH
			if maxScroll < 0 {
				maxScroll = 0
			}
			if m.chatScroll < maxScroll {
				m.chatScroll++
			}
			if m.chatScroll >= maxScroll {
				m.chatAutoScroll = true
			}
		} else {
			m.inputHistoryNext()
		}
	case tea.KeyPgUp:
		if m.chatMode {
			chatViewH := m.height - 6 - m.completionCount() - 2
			if chatViewH < 1 {
				chatViewH = 1
			}
			m.chatScroll -= chatViewH
			if m.chatScroll < 0 {
				m.chatScroll = 0
			}
			m.chatAutoScroll = false
		}
	case tea.KeyPgDown:
		if m.chatMode {
			chatViewH := m.height - 6 - m.completionCount() - 2
			if chatViewH < 1 {
				chatViewH = 1
			}
			maxScroll := m.chatTotalLines() - chatViewH
			if maxScroll < 0 {
				maxScroll = 0
			}
			m.chatScroll += chatViewH
			if m.chatScroll > maxScroll {
				m.chatScroll = maxScroll
			}
			m.chatAutoScroll = true
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
			if val == "y" || val == "yes" {
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
			if m.chatMode {
				if strings.HasPrefix(val, "/") {
					return m.dispatchChatCommand(val)
				}
				if m.chatStreaming {
					m.statusMsg = "waiting for response…"
					return nil
				}
				if m.chatEngine == nil {
					// Lazy init: queue prompt, start engine.
					m.chatPendingPrompt = val
					m.statusMsg = "initializing…"
					return m.startChatCmd(m.chatWorkspace)
				}
				return m.sendChatMsg(val)
			}
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
	item := m.selectedNavItem()
	if item == nil || item.root == "" {
		return nil
	}
	m.contentLoading = true
	m.contentLines = nil
	return loadContent(item.root, m.cfg.PreferredStyles, m.cfg.PreferredModels)
}

// openCurrentURL opens the source URL of the current nav item in a new Chrome window.
func (m *Model) openCurrentURL() tea.Cmd {
	item := m.selectedNavItem()
	if item == nil || item.url == "" {
		return nil
	}
	return openInChrome(item.url)
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
		arg := parts[1] // preserve case for display; lowercase when filtering
		all := m.paramSuggestions(cmd, arg)
		// Filter by partial last token
		partial := strings.ToLower(arg)
		// For multi-word args (e.g. "/help article "), filter on the last word
		if idx := strings.LastIndex(partial, " "); idx >= 0 {
			partial = partial[idx+1:]
		}
		var filtered []cmdCompletion
		for _, c := range all {
			if partial == "" || strings.HasPrefix(strings.ToLower(c.cmd), partial) {
				filtered = append(filtered, c)
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
// arg is the partial text already typed after the command (may include spaces for /help).
func (m *Model) paramSuggestions(cmd, arg string) []cmdCompletion {
	switch cmd {
	case "/filter":
		seen := map[string]bool{}
		var items []cmdCompletion
		for _, item := range m.navItemsAll {
			for _, tag := range item.tags {
				if !seen[tag] {
					seen[tag] = true
					items = append(items, cmdCompletion{cmd: tag})
				}
			}
		}
		return items

	case "/delete":
		sub := m.navSubTab
		if m.activeTab != tabLibrary {
			sub = navSubTabArticles
		}
		switch sub {
		case navSubTabArticles:
			items := make([]cmdCompletion, 0, len(m.navItems))
			for _, item := range m.navItems {
				items = append(items, cmdCompletion{cmd: item.id, desc: truncate(oneLine(item.title), 40)})
			}
			return items
		case navSubTabCollections:
			var items []cmdCompletion
			for _, r := range m.navRows {
				if r.kind == rowCollection {
					items = append(items, cmdCompletion{cmd: r.colSlug, desc: fmt.Sprintf("%d articles", r.colCount)})
				}
			}
			return items
		case navSubTabWorkspaces:
			items := make([]cmdCompletion, 0, len(m.workspaceItems))
			for _, ws := range m.workspaceItems {
				items = append(items, cmdCompletion{cmd: ws.name, desc: fmt.Sprintf("%da %dc", ws.articleCount, ws.collectionCount)})
			}
			return items
		}

	case "/article":
		return []cmdCompletion{
			{cmd: "list", desc: "go to Articles sub-tab"},
			{cmd: "search", desc: "<query>  full-text search"},
			{cmd: "ingest", desc: "<url>  add a new article"},
		}

	case "/collection":
		return []cmdCompletion{
			{cmd: "list", desc: "go to Collections sub-tab"},
			{cmd: "search", desc: "<query>  filter by name/slug"},
		}

	case "/workspace":
		return []cmdCompletion{
			{cmd: "list", desc: "go to Workspaces sub-tab"},
			{cmd: "new", arg: "<name>", desc: "create a new workspace"},
			{cmd: "delete", desc: "delete selected workspace"},
			{cmd: "rename", arg: "<name>", desc: "rename selected workspace"},
			{cmd: "describe", arg: "<text>", desc: "set workspace description"},
			{cmd: "add", arg: "article|collection <slug>", desc: "add article or collection; resets chat engine"},
			{cmd: "remove", arg: "article|collection <slug>", desc: "remove article or collection; resets chat engine"},
			{cmd: "reload", desc: "reset chat engine to pick up corpus changes"},
		}

	case "/workspace add":
		return []cmdCompletion{
			{cmd: "article", arg: "<slug>", desc: "add article to selected workspace"},
			{cmd: "collection", arg: "<slug>", desc: "add collection to selected workspace"},
		}

	case "/workspace remove":
		return []cmdCompletion{
			{cmd: "article", arg: "<slug>", desc: "remove article from selected workspace"},
			{cmd: "collection", arg: "<slug>", desc: "remove collection from selected workspace"},
		}

	case "/help":
		// Second level: "/help article " → return command entries for that group.
		trimmed := strings.TrimSpace(strings.ToLower(arg))
		for _, g := range helpGroups {
			if trimmed == g.name || strings.HasPrefix(trimmed, g.name+" ") {
				items := make([]cmdCompletion, len(g.commands))
				for i, c := range g.commands {
					name := c.cmd
					// For CLI-only entries like "arc workspace new", show just the subcommand.
					if parts := strings.Fields(name); len(parts) == 3 && parts[0] == "arc" {
						name = parts[2]
					}
					items[i] = cmdCompletion{cmd: name, desc: c.desc}
				}
				return items
			}
		}
		// First level: return group names.
		items := make([]cmdCompletion, len(helpGroups))
		for i, g := range helpGroups {
			items[i] = cmdCompletion{cmd: g.name}
		}
		return items
	}
	return nil
}

// acceptParam fills the selected param value into the input, replacing any partial last token.
func (m *Model) acceptParam() {
	if m.paramIdx < 0 || m.paramIdx >= len(m.paramItems) {
		return
	}
	val := m.paramItems[m.paramIdx].cmd
	// Find the last space in the input and replace everything after it with val.
	input := m.inputValue
	if idx := strings.LastIndex(input, " "); idx >= 0 {
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
	params := m.paramSuggestions(c.cmd, "")
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

	// ── Global commands (available in any context) ──────────────────────────
	switch cmd {
	case "/stats":
		return m.cmdStats()
	case "/log", "/logs":
		return m.cmdLog()
	case "/help":
		m.setStatusLines(m.helpLines(arg))
		return nil
	case "/article":
		return m.dispatchQualified(navSubTabArticles, arg)
	case "/collection":
		return m.dispatchQualified(navSubTabCollections, arg)
	case "/workspace":
		return m.dispatchQualified(navSubTabWorkspaces, arg)
	}

	// ── Context-sensitive commands ──────────────────────────────────────────
	sub := m.navSubTab
	if m.activeTab != tabLibrary {
		sub = navSubTabArticles // default context outside Library
	}

	switch cmd {
	// ── Shared (multi-context) ──────────────────────────────────────────
	case "/search":
		if arg == "" {
			m.statusMsg = "usage: /search <query>"
			return nil
		}
		switch sub {
		case navSubTabWorkspaces:
			m.filterWorkspaces(arg)
			return nil
		case navSubTabCollections:
			m.filterCollections(arg)
			return nil
		default: // articles
			query, limit := parseSearchArg(arg)
			if m.svc == nil {
				m.applyNavFilter("search", query)
				return nil
			}
			m.statusMsg = "searching…"
			return cmdFTSSearch(m.svc, query, limit)
		}

	case "/clear":
		switch sub {
		case navSubTabWorkspaces:
			m.workspaceItems = m.workspaceItemsAll
			m.wsRows = m.buildWsRows()
			m.wsCursor = 0
			m.wsScroll = 0
			m.navFilter = ""
			m.focus = paneNav
			m.statusMsg = "✓ filter cleared"
			return nil
		case navSubTabCollections:
			m.navRows = m.navRowsAll
			m.navRowCursor = 0
			m.navRowScroll = 0
			m.navFilter = ""
			m.focus = paneNav
			m.statusMsg = "✓ filter cleared"
			return nil
		default: // articles
			m.navItems = m.navItemsAll
			m.navFilter = ""
			m.navCursor = 0
			m.navScroll = 0
			m.focus = paneNav
			m.statusMsg = "✓ filter cleared"
			return m.triggerContentLoad()
		}

	case "/delete":
		switch sub {
		case navSubTabWorkspaces:
			if arg != "" {
				return m.cmdDeleteWorkspaceByName(arg)
			}
			return m.cmdDeleteWorkspace()
		case navSubTabCollections:
			if arg != "" {
				return m.cmdDeleteCollectionByName(arg)
			}
			return m.cmdDeleteCollection()
		default: // articles
			if arg != "" {
				return m.cmdDeleteArticleBySlug(arg)
			}
			return m.cmdDeleteArticle()
		}

	// ── Article-only ────────────────────────────────────────────────────
	case "/filter":
		if sub != navSubTabArticles {
			m.statusMsg = "✗ /filter is only available in Articles context"
			return nil
		}
		if arg == "" {
			m.statusMsg = "usage: /filter <tag>"
			return nil
		}
		m.applyNavFilter("tag", arg)
		return nil

	case "/tags":
		return m.cmdTags()

	case "/collections":
		return m.cmdCollections()

	case "/favorites":
		if sub != navSubTabArticles {
			m.statusMsg = "✗ /favorites is only available in Articles context"
			return nil
		}
		m.applyNavFilter("favorite", "")
		return nil

	case "/favorite":
		if sub != navSubTabArticles {
			m.statusMsg = "✗ /favorite is only available in Articles context"
			return nil
		}
		return m.cmdToggleFavorite()

	case "/open":
		if sub != navSubTabArticles {
			m.statusMsg = "✗ /open is only available in Articles context"
			return nil
		}
		return m.openCurrentURL()

	case "/read":
		if sub != navSubTabArticles {
			m.statusMsg = "✗ /read is only available in Articles context"
			return nil
		}
		return m.cmdMarkRead()

	case "/unread":
		if sub != navSubTabArticles {
			m.statusMsg = "✗ /unread is only available in Articles context"
			return nil
		}
		return m.cmdMarkUnread()

	case "/reprocess":
		if sub != navSubTabArticles {
			m.statusMsg = "✗ /reprocess is only available in Articles context"
			return nil
		}
		return m.cmdReprocess()

	case "/ingest":
		if arg == "" {
			m.statusMsg = "usage: /ingest <url>"
			return nil
		}
		return m.cmdIngest(arg)

	// ── Workspace-only ──────────────────────────────────────────────────
	case "/new":
		if sub != navSubTabWorkspaces {
			m.statusMsg = "✗ /new is only available in Workspaces context"
			return nil
		}
		if arg == "" {
			m.statusMsg = "usage: /new <name>"
			return nil
		}
		return m.cmdNewWorkspace(arg)

	case "/rename":
		if sub != navSubTabWorkspaces {
			m.statusMsg = "✗ /rename is only available in Workspaces context"
			return nil
		}
		if arg == "" {
			m.statusMsg = "usage: /rename <new-name>"
			return nil
		}
		return m.cmdRenameWorkspace(arg)

	case "/describe":
		if sub != navSubTabWorkspaces {
			m.statusMsg = "✗ /describe is only available in Workspaces context"
			return nil
		}
		if arg == "" {
			m.statusMsg = "usage: /describe <text>"
			return nil
		}
		return m.cmdDescribeWorkspace(arg)

	default:
		m.statusMsg = "✗ unknown command: " + parts[0]
		return nil
	}
}

// dispatchQualified switches to the given sub-tab then executes the subcommand.
// subCmd examples: "list", "search foo", "new my-workspace".
func (m *Model) dispatchQualified(sub navSubTab, subCmd string) tea.Cmd {
	// Switch to Library tab and the right sub-tab first.
	m.activeTab = tabLibrary
	switchCmd := m.switchNavSubTab(sub)

	subCmd = strings.TrimSpace(strings.ToLower(subCmd))
	subParts := strings.Fields(subCmd)
	verb := ""
	if len(subParts) > 0 {
		verb = subParts[0]
	}
	arg := ""
	if len(subParts) > 1 {
		arg = strings.Join(subParts[1:], " ")
	}

	// After switching context, move focus to nav pane.
	m.focus = paneNav

	switch sub {
	case navSubTabArticles:
		switch verb {
		case "", "list":
			// just switching is enough
		case "search":
			if arg == "" {
				m.statusMsg = "usage: /article search <query>"
			} else {
				query, limit := parseSearchArg(arg)
				if m.svc != nil {
					m.statusMsg = "searching…"
					return tea.Batch(switchCmd, cmdFTSSearch(m.svc, query, limit))
				}
				m.applyNavFilter("search", query)
			}
		case "ingest":
			if arg == "" {
				m.statusMsg = "usage: /article ingest <url>"
			} else {
				return tea.Batch(switchCmd, m.cmdIngest(arg))
			}
		default:
			m.statusMsg = "✗ unknown article command: " + verb
		}

	case navSubTabCollections:
		switch verb {
		case "", "list":
			// switching is enough
		case "search":
			if arg == "" {
				m.statusMsg = "usage: /collection search <query>"
			} else {
				m.filterCollections(arg)
			}
		default:
			m.statusMsg = "✗ unknown collection command: " + verb
		}

	case navSubTabWorkspaces:
		switch verb {
		case "", "list":
			// switching is enough
		case "new":
			if arg == "" {
				m.statusMsg = "usage: /workspace new <name>"
			} else {
				return tea.Batch(switchCmd, m.cmdNewWorkspace(arg))
			}
		case "delete":
			m.cmdDeleteWorkspace()
		case "rename":
			if arg == "" {
				m.statusMsg = "usage: /workspace rename <new-name>"
			} else {
				return tea.Batch(switchCmd, m.cmdRenameWorkspace(arg))
			}
		case "describe":
			if arg == "" {
				m.statusMsg = "usage: /workspace describe <text>"
			} else {
				return tea.Batch(switchCmd, m.cmdDescribeWorkspace(arg))
			}
		case "add", "remove":
			return tea.Batch(switchCmd, m.cmdWorkspaceMembership(verb, arg))
		case "reload":
			return tea.Batch(switchCmd, m.cmdWorkspaceReload())
		default:
			m.statusMsg = "✗ unknown workspace command: " + verb
		}
	}

	return switchCmd
}

// filterCollections filters navRowsAll to collections matching query (slug/name/description).
func (m *Model) filterWorkspaces(query string) {
	q := strings.ToLower(query)
	var filtered []workspaceItem
	for _, ws := range m.workspaceItemsAll {
		// Build searchable text: name, description, collection slugs, article slugs (split by -).
		searchable := strings.ToLower(ws.name + " " + ws.description)
		for _, col := range ws.collectionSlugs {
			searchable += " " + strings.ToLower(strings.ReplaceAll(col, "-", " "))
		}
		for _, slug := range ws.articles {
			searchable += " " + strings.ToLower(strings.ReplaceAll(slug, "-", " "))
		}
		if strings.Contains(searchable, q) {
			filtered = append(filtered, ws)
		}
	}
	m.workspaceItems = filtered
	m.wsRows = m.buildWsRows()
	m.wsCursor = 0
	m.wsScroll = 0
	m.focus = paneNav
	n := len(filtered)
	if n == 0 {
		m.statusMsg = fmt.Sprintf("no workspaces matching %q", query)
		m.navFilter = ""
	} else {
		m.navFilter = fmt.Sprintf("workspaces: %q · %d results  ·  /clear to reset", query, n)
		m.statusMsg = ""
	}
}

func (m *Model) filterCollections(query string) {
	q := strings.ToLower(query)
	var filtered []navRow
	for _, row := range m.navRowsAll {
		if row.kind != rowCollection {
			continue
		}
		if strings.Contains(strings.ToLower(row.colSlug), q) ||
			strings.Contains(strings.ToLower(row.colName), q) ||
			strings.Contains(strings.ToLower(row.colDesc), q) {
			filtered = append(filtered, row)
		}
	}
	m.navRows = filtered
	m.navRowCursor = 0
	m.navRowScroll = 0
	m.focus = paneNav
	n := len(filtered)
	if n == 0 {
		m.statusMsg = fmt.Sprintf("no collections matching %q", query)
		m.navFilter = ""
	} else {
		m.navFilter = fmt.Sprintf("collections: %q · %d results  ·  /clear to reset", query, n)
		m.statusMsg = ""
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
		case "favorite":
			if item.favorite {
				filtered = append(filtered, item)
			}
		}
	}
	m.navItems = filtered
	m.navCursor = 0
	m.navScroll = 0
	n := len(filtered)
	if n == 0 {
		if mode == "favorite" {
			m.statusMsg = "no favorites yet — press f to mark an article"
		} else {
			m.statusMsg = fmt.Sprintf("no results for %q", query)
		}
		m.navFilter = ""
	} else {
		if mode == "favorite" {
			m.navFilter = fmt.Sprintf("★ favorites · %d articles  ·  /clear to reset", n)
		} else {
			m.navFilter = mode + ": " + query + " · " + fmt.Sprintf("%d", n) + " results  ·  /clear to reset"
		}
		m.statusMsg = ""
	}
}

// cmdMarkRead marks the current article as read in-memory and persists to DB.
func (m *Model) cmdMarkRead() tea.Cmd {
	item := m.selectedNavItem()
	if item == nil {
		m.statusMsg = "✗ no article selected"
		return nil
	}
	id := item.id
	item.read = true
	for i, ni := range m.navItemsAll {
		if ni.id == id {
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
	item := m.selectedNavItem()
	if item == nil {
		m.statusMsg = "✗ no article selected"
		return nil
	}
	id := item.id
	item.read = false
	for i, ni := range m.navItemsAll {
		if ni.id == id {
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

// cmdToggleFavorite toggles the favorite flag on the current article.
func (m *Model) cmdToggleFavorite() tea.Cmd {
	item := m.selectedNavItem()
	if item == nil {
		m.statusMsg = "✗ no article selected"
		return nil
	}
	id := item.id
	nowFav := !item.favorite
	// Update in-memory lists.
	item.favorite = nowFav
	for i, ni := range m.navItemsAll {
		if ni.id == id {
			m.navItemsAll[i].favorite = nowFav
			break
		}
	}
	if nowFav {
		m.statusMsg = "★ marked as favorite"
	} else {
		m.statusMsg = "✓ removed from favorites"
	}
	if m.svc == nil {
		return nil
	}
	svc := m.svc
	return func() tea.Msg {
		if nowFav {
			_ = svc.MarkFavorite(context.Background(), id)
		} else {
			_ = svc.UnmarkFavorite(context.Background(), id)
		}
		return nil
	}
}

// cmdDeleteArticle prompts for confirmation then deletes the current article.
func (m *Model) cmdDeleteArticle() tea.Cmd {
	sel := m.selectedNavItem()
	if sel == nil {
		m.statusMsg = "✗ no article selected"
		return nil
	}
	item := *sel
	m.pendingConfirmMsg = fmt.Sprintf("delete %q? (y/n)", item.title)
	m.focus = paneCommand
	m.cursorVisible = true
	m.inputValue = ""
	m.inputCursor = 0
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
		m.focus = paneNav
		contentCmd := m.triggerContentLoad()
		if svc == nil {
			return contentCmd
		}
		return tea.Batch(contentCmd, func() tea.Msg {
			_ = svc.DeleteArticle(context.Background(), id)
			return nil
		})
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

// cmdViewArticle opens the selected article's flash/summary/body in an external
// terminal window using less. The viewer auto-exits when arc exits (PID poll).
func (m *Model) cmdViewArticle() tea.Cmd {
	item := m.selectedNavItem()
	if item == nil {
		m.statusMsg = "✗ no article selected"
		return nil
	}
	if item.root == "" {
		m.statusMsg = "✗ article has no local files"
		return nil
	}

	// Resolve file paths.
	files := storefs.ProbeFiles(item.root)
	files.Summary = storefs.ResolveSummary(item.root, m.cfg.PreferredStyles, m.cfg.PreferredModels)
	files.Flash = storefs.ResolveFlash(item.root, m.cfg.PreferredModels)

	// Collect files in display order: Flash → Summary → Body.
	type viewPart struct {
		label string
		path  string
	}
	var parts []viewPart
	if files.Flash != "" {
		parts = append(parts, viewPart{"Flash", files.Flash})
	}
	if files.Summary != "" {
		parts = append(parts, viewPart{"Summary", files.Summary})
	}
	if files.Body != "" {
		parts = append(parts, viewPart{"Body", files.Body})
	}
	if len(parts) == 0 {
		m.statusMsg = "✗ no content files available"
		return nil
	}

	pid := os.Getpid()
	scriptPath := fmt.Sprintf("%s/arc-view-%d-%s.sh", os.TempDir(), pid, item.id)

	// Build a script that concatenates files with labeled dividers, pipes to less,
	// and exits when the parent arc process dies.
	var catParts string
	for i, p := range parts {
		if i > 0 {
			catParts += "echo ''; "
		}
		// ── Label ────────────────────────────────
		pad := 60 - 4 - len(p.label) - 1 // 4 = "── ", 1 = " "
		if pad < 3 {
			pad = 3
		}
		catParts += fmt.Sprintf("echo '── %s %s'; echo ''; ", p.label, strings.Repeat("─", pad))
		catParts += fmt.Sprintf("cat %q; ", p.path)
	}

	script := fmt.Sprintf(
		"#!/bin/bash\ntrap 'rm -f %q' EXIT\n"+
			"# Background watcher: exit when parent arc process dies.\n"+
			"(while kill -0 %d 2>/dev/null; do sleep 1; done; kill $$ 2>/dev/null) &\n"+
			"{ %s } | less -R\n",
		scriptPath, pid, catParts,
	)
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		m.statusMsg = fmt.Sprintf("✗ view: could not write script: %v", err)
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
	m.statusMsg = "opened viewer for " + item.id
	return nil
}

// cmdReprocess regenerates summary/flash for the current article.
func (m *Model) cmdReprocess() tea.Cmd {
	sel := m.selectedNavItem()
	if sel == nil {
		m.statusMsg = "✗ no article selected"
		return nil
	}
	if m.svc == nil {
		m.statusMsg = "✗ service unavailable"
		return nil
	}
	item := *sel
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

// ── Collection commands ──────────────────────────────────────────────────────

// cmdDeleteCollection deletes the selected collection after confirmation.
func (m *Model) cmdDeleteCollection() tea.Cmd {
	col := m.selectedCollection()
	if col == nil {
		m.statusMsg = "✗ no collection selected — cursor must be on a collection header"
		return nil
	}
	if m.svc == nil {
		m.statusMsg = "✗ service unavailable"
		return nil
	}
	slug := col.colSlug
	svc := m.svc
	m.pendingConfirmMsg = fmt.Sprintf("delete collection %q? (y/n)", slug)
	m.pendingConfirm = func() tea.Cmd {
		return func() tea.Msg {
			_, err := svc.DeleteCollection(context.Background(), slug, false)
			if err != nil {
				return cmdDoneMsg{err: err.Error()}
			}
			return cmdDoneMsg{statusMsg: "✓ deleted collection " + slug, reloadCollections: true}
		}
	}
	return nil
}

// cmdDeleteArticleBySlug deletes an article by slug (from /delete <slug>).
func (m *Model) cmdDeleteArticleBySlug(slug string) tea.Cmd {
	if m.svc == nil {
		m.statusMsg = "✗ service unavailable"
		return nil
	}
	svc := m.svc
	m.pendingConfirmMsg = fmt.Sprintf("delete article %q? (y/n)", slug)
	m.pendingConfirm = func() tea.Cmd {
		m.navItems = removeNavItem(m.navItems, slug)
		m.navItemsAll = removeNavItem(m.navItemsAll, slug)
		if m.navCursor >= len(m.navItems) {
			m.navCursor = len(m.navItems) - 1
		}
		if m.navCursor < 0 {
			m.navCursor = 0
		}
		m.clampNavScroll()
		contentCmd := m.triggerContentLoad()
		return tea.Batch(contentCmd, func() tea.Msg {
			_ = svc.DeleteArticle(context.Background(), slug)
			return nil
		})
	}
	return nil
}

// cmdDeleteCollectionByName deletes a collection by slug (from /delete <slug>).
func (m *Model) cmdDeleteCollectionByName(slug string) tea.Cmd {
	if m.svc == nil {
		m.statusMsg = "✗ service unavailable"
		return nil
	}
	svc := m.svc
	m.pendingConfirmMsg = fmt.Sprintf("delete collection %q? (y/n)", slug)
	m.pendingConfirm = func() tea.Cmd {
		return func() tea.Msg {
			_, err := svc.DeleteCollection(context.Background(), slug, false)
			if err != nil {
				return cmdDoneMsg{err: err.Error()}
			}
			return cmdDoneMsg{statusMsg: "✓ deleted collection " + slug, reloadCollections: true}
		}
	}
	return nil
}

// cmdDeleteWorkspaceByName deletes a workspace by name (from /delete <name>).
func (m *Model) cmdDeleteWorkspaceByName(name string) tea.Cmd {
	if m.svc == nil {
		m.statusMsg = "✗ service unavailable"
		return nil
	}
	svc := m.svc
	m.pendingConfirmMsg = fmt.Sprintf("delete workspace %q? (y/n)", name)
	m.pendingConfirm = func() tea.Cmd {
		return func() tea.Msg {
			if err := svc.DeleteWorkspace(context.Background(), name); err != nil {
				return cmdDoneMsg{err: err.Error()}
			}
			return cmdDoneMsg{statusMsg: "✓ deleted workspace " + name, reloadWorkspaces: true}
		}
	}
	return nil
}

// ── Workspace commands ───────────────────────────────────────────────────────

// selectedWorkspace returns the workspaceItem under the cursor, or nil.
func (m *Model) selectedWorkspace() *workspaceItem {
	if m.wsCursor < 0 || m.wsCursor >= len(m.wsRows) {
		return nil
	}
	idx := m.wsRows[m.wsCursor].wsIdx
	if idx < 0 || idx >= len(m.workspaceItems) {
		return nil
	}
	return &m.workspaceItems[idx]
}

// cmdNewWorkspace creates a new workspace.
func (m *Model) cmdNewWorkspace(name string) tea.Cmd {
	if m.svc == nil {
		m.statusMsg = "✗ service unavailable"
		return nil
	}
	svc := m.svc
	m.statusMsg = "⠸ creating workspace " + name + "…"
	return func() tea.Msg {
		if err := svc.CreateWorkspace(context.Background(), name, ""); err != nil {
			return cmdDoneMsg{err: err.Error()}
		}
		return cmdDoneMsg{statusMsg: "✓ created workspace " + name, reloadWorkspaces: true}
	}
}

// cmdDeleteWorkspace deletes the selected workspace after confirmation.
func (m *Model) cmdDeleteWorkspace() tea.Cmd {
	ws := m.selectedWorkspace()
	if ws == nil {
		m.statusMsg = "✗ no workspace selected"
		return nil
	}
	if m.svc == nil {
		m.statusMsg = "✗ service unavailable"
		return nil
	}
	name := ws.name
	svc := m.svc
	m.pendingConfirmMsg = fmt.Sprintf("delete workspace %q? (y/n)", name)
	m.pendingConfirm = func() tea.Cmd {
		return func() tea.Msg {
			if err := svc.DeleteWorkspace(context.Background(), name); err != nil {
				return cmdDoneMsg{err: err.Error()}
			}
			return cmdDoneMsg{statusMsg: "✓ deleted workspace " + name, reloadWorkspaces: true}
		}
	}
	return nil
}

// cmdRenameWorkspace renames the selected workspace.
func (m *Model) cmdRenameWorkspace(newName string) tea.Cmd {
	ws := m.selectedWorkspace()
	if ws == nil {
		m.statusMsg = "✗ no workspace selected"
		return nil
	}
	if m.svc == nil {
		m.statusMsg = "✗ service unavailable"
		return nil
	}
	oldName := ws.name
	svc := m.svc
	m.statusMsg = "⠸ renaming workspace…"
	return func() tea.Msg {
		if err := svc.RenameWorkspace(context.Background(), oldName, newName); err != nil {
			return cmdDoneMsg{err: err.Error()}
		}
		return cmdDoneMsg{statusMsg: fmt.Sprintf("✓ renamed %s → %s", oldName, newName), reloadWorkspaces: true}
	}
}

// cmdDescribeWorkspace sets the description of the selected workspace.
func (m *Model) cmdDescribeWorkspace(text string) tea.Cmd {
	ws := m.selectedWorkspace()
	if ws == nil {
		m.statusMsg = "✗ no workspace selected"
		return nil
	}
	if m.svc == nil {
		m.statusMsg = "✗ service unavailable"
		return nil
	}
	name := ws.name
	svc := m.svc
	return func() tea.Msg {
		if err := svc.SetWorkspaceDescription(context.Background(), name, text); err != nil {
			return cmdDoneMsg{err: err.Error()}
		}
		return cmdDoneMsg{statusMsg: "✓ description updated for " + name, reloadWorkspaces: true}
	}
}

// cmdWorkspaceMembership handles /workspace add|remove article|collection <slug>.
// On success it resets the chat engine for the affected workspace so the next
// message picks up the updated corpus. See local/CHAT_ARCHITECTURE.md.
func (m *Model) cmdWorkspaceMembership(verb, arg string) tea.Cmd {
	ws := m.selectedWorkspace()
	if ws == nil {
		m.statusMsg = "✗ no workspace selected"
		return nil
	}
	if m.svc == nil {
		m.statusMsg = "✗ service unavailable"
		return nil
	}
	parts := strings.Fields(arg)
	if len(parts) < 2 {
		m.statusMsg = "usage: /workspace " + verb + " article|collection <slug>"
		return nil
	}
	kind := strings.ToLower(parts[0])
	slug := parts[1]
	if kind != "article" && kind != "collection" {
		m.statusMsg = "✗ specify 'article' or 'collection'"
		return nil
	}

	wsName := ws.name
	svc := m.svc
	adding := verb == "add"

	return func() tea.Msg {
		var err error
		var statusMsg string
		switch {
		case kind == "article" && adding:
			err = svc.AddArticlesToWorkspace(context.Background(), wsName, []string{slug})
			statusMsg = "✓ added article " + slug + " → " + wsName
		case kind == "article" && !adding:
			err = svc.RemoveArticlesFromWorkspace(context.Background(), wsName, []string{slug})
			statusMsg = "✓ removed article " + slug + " from " + wsName
		case kind == "collection" && adding:
			err = svc.AddCollectionsToWorkspace(context.Background(), wsName, []string{slug})
			statusMsg = "✓ added collection " + slug + " → " + wsName
		case kind == "collection" && !adding:
			err = svc.RemoveCollectionsFromWorkspace(context.Background(), wsName, []string{slug})
			statusMsg = "✓ removed collection " + slug + " from " + wsName
		}
		if err != nil {
			return cmdDoneMsg{err: err.Error()}
		}
		return cmdDoneMsg{
			statusMsg:          statusMsg,
			reloadWorkspaces:   true,
			resetChatEngine:    true,
			resetChatWorkspace: wsName,
		}
	}
}

// cmdWorkspaceReload drops the chat engine for the selected workspace so the
// next message triggers a fresh engine init (rebuilding the RAG context).
func (m *Model) cmdWorkspaceReload() tea.Cmd {
	ws := m.selectedWorkspace()
	if ws == nil {
		// In chat mode, fall back to the active chat workspace.
		if m.chatMode && m.chatWorkspace != "" {
			m.chatEngine = nil
			m.statusMsg = "✓ engine reset — will reinitialise on next message"
			return nil
		}
		m.statusMsg = "✗ no workspace selected"
		return nil
	}
	wsName := ws.name
	// Apply immediately if this is the active chat workspace.
	if m.chatMode && m.chatWorkspace == wsName {
		m.chatEngine = nil
	}
	m.statusMsg = "✓ engine reset for " + wsName + " — will reinitialise on next message"
	return nil
}

// helpGroups defines the command groups shown by /help.
// Names match the context qualifier: article, collection, workspace, keys, system.
var helpGroups = []struct {
	name     string
	commands []cmdCompletion
}{
	{"article", []cmdCompletion{
		{"/search", "<query> [--limit N]", "full-text search — use /article search from any tab"},
		{"/filter", "<tag>", "filter by tag"},
		{"/favorites", "", "show only favorited articles"},
		{"/clear", "", "clear active filter"},
		{"/open", "", "open source URL in browser"},
		{"/read", "", "mark as read"},
		{"/unread", "", "mark as unread"},
		{"/favorite", "", "toggle favorite"},
		{"/delete", "", "delete current article"},
		{"/reprocess", "", "regenerate summary/flash"},
		{"/ingest", "<url>", "add a new article — use /article ingest from any tab"},
	}},
	{"collection", []cmdCompletion{
		{"/search", "<query>", "filter collections by name/slug"},
		{"/clear", "", "clear active filter"},
		{"/delete", "", "delete current collection"},
		{"arc collections create", "<slug>", "create a new collection  (CLI only)"},
		{"arc collections add", "<article> <slug>", "add article to collection  (CLI only)"},
		{"arc collections remove", "<article> <slug>", "remove article  (CLI only)"},
		{"arc collections rename", "<old> <new>", "rename  (CLI only)"},
		{"arc collections describe", "<slug> <desc>", "set description  (CLI only)"},
		{"arc collections suggest", "[--apply]", "AI-suggest collections  (CLI only)"},
		{"arc collections read", "<slug>", "read flash/summary across collection  (CLI only)"},
	}},
	{"workspace", []cmdCompletion{
		{"/search", "<query>", "filter workspaces by name/description"},
		{"/clear", "", "clear active filter"},
		{"/new", "<name>", "create a new workspace"},
		{"/delete", "", "delete current workspace"},
		{"/rename", "<new-name>", "rename current workspace"},
		{"/describe", "<text>", "set workspace description"},
		{"arc workspace add", "<slug>", "add articles/collections/resources  (CLI only)"},
		{"arc workspace remove", "<slug>", "remove articles/collections/resources  (CLI only)"},
		{"arc workspace chat", "<slug>", "start interactive chat session  (CLI only)"},
		{"arc workspace archive", "<slug>", "archive a workspace  (CLI only)"},
		{"arc workspace outcomes", "<slug>", "list or read outcomes  (CLI only)"},
		{"arc workspace system", "<slug>", "get/set system prompt  (CLI only)"},
	}},
	{"keys", []cmdCompletion{
		{"j / ↓", "", "move down"},
		{"k / ↑", "", "move up"},
		{"PgDn / ctrl+d", "", "page down"},
		{"PgUp / ctrl+u", "", "page up"},
		{"g / Home", "", "go to top"},
		{"G / End", "", "go to bottom"},
		{"enter", "", "select / expand / collapse"},
		{"space", "", "expand / collapse"},
		{"esc", "", "back / dismiss"},
		{"tab", "", "next pane"},
		{"shift+tab", "", "previous pane"},
		{"l / →", "", "next content tab (Body/Summary/Flash/Cards)"},
		{"h / ←", "", "previous content tab"},
		{"r", "", "mark article as read"},
		{"u", "", "mark article as unread"},
		{"f", "", "toggle favorite"},
		{"o", "", "open source URL in browser"},
		{"v", "", "view article in external terminal"},
		{"D", "", "delete current item"},
		{"/", "", "open command input"},
		{"?", "", "show key bindings"},
		{"q / ctrl+c", "", "quit"},
	}},
	{"system", []cmdCompletion{
		{"/tags", "", "list all tags"},
		{"/stats", "", "show library stats"},
		{"/log", "", "open/close debug log tail"},
	}},
}

// helpLines returns context-sensitive help for the active tab.
// arg="" shows all groups; "article" shows article commands;
// "article /read" shows just the /read entry.
func (m *Model) helpLines(arg string) []string {
	if m.activeTab != tabLibrary {
		return []string{"no commands available for this tab"}
	}

	renderGroup := func(g struct {
		name     string
		commands []cmdCompletion
	}) []string {
		// Check if this group has both TUI and CLI-only entries.
		hasTUI, hasCLI := false, false
		for _, c := range g.commands {
			if strings.HasPrefix(c.cmd, "arc ") {
				hasCLI = true
			} else {
				hasTUI = true
			}
		}
		showLegend := hasTUI && hasCLI

		header := g.name + ":"
		if showLegend {
			header += "  (/ = TUI command · no slash = CLI only: arc " + g.name + " <cmd>)"
		}
		lines := []string{header}
		for _, c := range g.commands {
			displayCmd := c.cmd
			// For CLI-only entries like "arc collections create", show just the subcommand.
			if parts := strings.Fields(displayCmd); len(parts) >= 3 && parts[0] == "arc" {
				displayCmd = parts[2]
			}
			synopsis := displayCmd
			if c.arg != "" {
				synopsis += " " + c.arg
			}
			// Strip "(CLI only)" from desc when legend is shown — it's redundant.
			desc := c.desc
			if showLegend {
				desc = strings.TrimSuffix(strings.TrimSpace(strings.Replace(desc, "  (CLI only)", "", 1)), "(CLI only)")
			}
			lines = append(lines, fmt.Sprintf("  %-24s  %s", synopsis, desc))
		}
		return lines
	}

	arg = strings.TrimSpace(strings.ToLower(arg))

	// No arg — show all groups.
	if arg == "" {
		var lines []string
		for i, g := range helpGroups {
			if i > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, renderGroup(g)...)
		}
		return lines
	}

	// Two-level: "article /read" — show just that command entry.
	parts := strings.SplitN(arg, " ", 2)
	groupName := parts[0]
	cmdFilter := ""
	if len(parts) == 2 {
		cmdFilter = strings.TrimSpace(parts[1])
	}

	for _, g := range helpGroups {
		if g.name == groupName {
			if cmdFilter == "" {
				return renderGroup(g)
			}
			// Filter to matching command(s).
			var lines []string
			for _, c := range g.commands {
				displayCmd := c.cmd
				if parts := strings.Fields(displayCmd); len(parts) >= 3 && parts[0] == "arc" {
					displayCmd = parts[2]
				}
				if strings.HasPrefix(displayCmd, cmdFilter) {
					synopsis := displayCmd
					if c.arg != "" {
						synopsis += " " + c.arg
					}
					lines = append(lines, fmt.Sprintf("  %-24s  %s", synopsis, c.desc))
				}
			}
			if len(lines) == 0 {
				return []string{fmt.Sprintf("no commands matching %q in %q", cmdFilter, groupName)}
			}
			return lines
		}
	}

	groupNames := make([]string, len(helpGroups))
	for i, g := range helpGroups {
		groupNames[i] = g.name
	}
	return []string{fmt.Sprintf("unknown group %q — available: %s", groupName, strings.Join(groupNames, ", "))}
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
			if m.navSubTab == navSubTabCollections {
				m.navRowScroll += delta
				if m.navRowScroll < 0 {
					m.navRowScroll = 0
				}
				max := len(m.navRows) - m.navPaneHeight()
				if max < 0 {
					max = 0
				}
				if m.navRowScroll > max {
					m.navRowScroll = max
				}
				if m.navRowCursor < m.navRowScroll {
					m.navRowCursor = m.navRowScroll
				} else if m.navRowCursor >= m.navRowScroll+m.navPaneHeight() {
					m.navRowCursor = m.navRowScroll + m.navPaneHeight() - 1
				}
				return m.triggerCollectionContentLoad()
			}
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
			if m.chatMode {
				chatViewH := m.height - 6 - m.completionCount() - 2
				if chatViewH < 1 {
					chatViewH = 1
				}
				m.chatScroll += delta
				if m.chatScroll < 0 {
					m.chatScroll = 0
				}
				maxScroll := m.chatTotalLines() - chatViewH
				if maxScroll < 0 {
					maxScroll = 0
				}
				if m.chatScroll > maxScroll {
					m.chatScroll = maxScroll
				}
				m.chatAutoScroll = m.chatScroll >= maxScroll
				return nil
			}
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
					m.focus = paneTabBar
				}
				return nil
			}
			// Click on nav sub-tab bar (first row of main area = topBarHeight).
			subTabRow := topBarHeight
			if msg.Y == subTabRow && msg.X < m.dividerCol() && m.activeTab == tabLibrary {
				if sub := navSubTabHitTest(msg.X); sub >= 0 {
					m.focus = paneNav
					return m.switchNavSubTab(sub)
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
			if m.chatMode {
				m.rebuildChatLines(m.chatBuildWidth())
			}
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
// Library: content starts at topBarHeight + 3 (top bar + sub-tab bar + blank).
// Other tabs: topBarHeight + 2 (top bar + label).
func (m *Model) clickNavRow(y int) tea.Cmd {
	contentStartRow := topBarHeight + 2
	if m.activeTab == tabLibrary {
		contentStartRow = topBarHeight + 3
	}
	switch m.navSubTab {
	case navSubTabArticles:
		idx := m.navScroll + (y - contentStartRow)
		if idx >= 0 && idx < len(m.navItems) {
			m.navCursor = idx
			return m.triggerContentLoad()
		}
	case navSubTabCollections:
		idx := m.navRowScroll + (y - contentStartRow)
		if idx >= 0 && idx < len(m.navRows) {
			m.navRowCursor = idx
			return m.triggerCollectionContentLoad()
		}
	case navSubTabWorkspaces:
		idx := m.wsScroll + (y - contentStartRow)
		if idx >= 0 && idx < len(m.wsRows) {
			m.wsCursor = idx
			row := m.wsRows[idx]
			switch row.kind {
			case wsRowWorkspace:
				// Click on workspace → load history (engine init deferred to first message).
				if row.wsIdx >= 0 && row.wsIdx < len(m.workspaceItems) {
					ws := m.workspaceItems[row.wsIdx]
					return m.loadChatHistoryCmd(ws.name, true)
				}
			case wsRowCollection:
				m.wsToggleExpand()
			}
		}
	}
	return nil
}

// navPaneHeight returns usable item lines in the nav pane.
func (m *Model) navPaneHeight() int {
	// fixed: top bar (2) + split sep (1) + cmd (1) + status sep (1) + status bar (1) = 6
	// plus completions expanding upward
	// Library tab: 2 header rows (sub-tab bar + blank)
	// Other tabs: 1 header row (label)
	overhead := 1
	if m.activeTab == tabLibrary {
		overhead = 2
	}
	h := m.height - 6 - m.completionCount() - overhead
	if h < 1 {
		h = 1
	}
	return h
}

// contentViewHeight returns the number of lines available for scrollable content.
// Layout: header (4 lines) + sep + tab strip + sep = 7 fixed lines in content pane.
func (m *Model) contentViewHeight() int {
	mainH := m.height - 6 - m.completionCount()
	h := mainH - contentHeaderLines(m.selectedNavItem())
	if h < 1 {
		h = 1
	}
	return h
}

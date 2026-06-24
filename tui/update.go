package tui

import (
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
			m.navCursor = 0
			m.navScroll = 0
		}
		m.navLoaded = true
		// Trigger content load for the first item.
		if len(m.navItems) > 0 && m.navItems[0].root != "" {
			m.contentLoading = true
			cmds = append(cmds, loadContent(m.navItems[0].root, m.contentTab, m.cfg.PreferredStyles, m.cfg.PreferredModels))
		}

	case statsLoadedMsg:
		if msg.err == "" {
			m.stats = msg.stats
			m.statsLoaded = true
		}

	case contentLoadedMsg:
		m.contentFiles = msg.files
		m.contentLines = msg.lines
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
	case key.Matches(msg, keys.Quit):
		return tea.Quit
	case key.Matches(msg, keys.Tab1):
		m.activeTab = tabLibrary
		return nil
	case key.Matches(msg, keys.Tab2):
		m.activeTab = tabAgent
		return nil
	case key.Matches(msg, keys.Tab3):
		m.activeTab = tabStats
		return nil
	case key.Matches(msg, keys.TabPrev):
		m.activeTab = (m.activeTab - 1 + tabCount) % tabCount
		return nil
	case key.Matches(msg, keys.TabNext):
		m.activeTab = (m.activeTab + 1) % tabCount
		return nil
	case key.Matches(msg, keys.PaneNext):
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
	case key.Matches(msg, keys.Command):
		m.focus = paneCommand
		m.cursorVisible = true
	case key.Matches(msg, keys.Help):
		// help overlay in future phases
	}
	return nil
}

func (m *Model) handleContentKey(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, keys.Back):
		m.focus = paneNav
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
	}
	return nil
}

func (m *Model) handleCommandKey(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, keys.Back):
		m.focus = paneNav
	}
	return nil
}

// cycleContentTab moves to the next/prev content tab unconditionally.
func (m *Model) cycleContentTab(delta int) tea.Cmd {
	m.contentTab = contentTab((int(m.contentTab) + delta + int(ctCount)) % int(ctCount))
	m.contentScroll = 0
	if m.navCursor >= 0 && m.navCursor < len(m.navItems) && m.navItems[m.navCursor].root != "" {
		m.contentLoading = true
		return loadContent(m.navItems[m.navCursor].root, m.contentTab, m.cfg.PreferredStyles, m.cfg.PreferredModels)
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
	return loadContent(root, m.contentTab, m.cfg.PreferredStyles, m.cfg.PreferredModels)
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

// handleMouse handles mouse press, release, and motion events.
func (m *Model) handleMouse(msg tea.MouseMsg) tea.Cmd {
	divCol := m.dividerCol()

	switch msg.Action {
	case tea.MouseActionPress:
		if msg.Button == tea.MouseButtonLeft {
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
			// Click in command input area (bottom rows).
			cmdRow := m.height - hintBarHeight - 1 // separator + input line
			if msg.Y >= cmdRow {
				m.focus = paneCommand
				m.cursorVisible = true
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
	// 5 fixed rows (top bar 2 + sep 1 + cmd 1 + hints 1) + 2 nav header rows
	h := m.height - 5 - 2
	if h < 1 {
		h = 1
	}
	return h
}

// contentViewHeight returns the number of lines available for scrollable content.
// Layout: header (4 lines) + sep + tab strip + sep = 7 fixed lines in content pane.
func (m *Model) contentViewHeight() int {
	mainH := m.height - 5 // total minus 5 fixed rows
	h := mainH - contentHeaderLines
	if h < 1 {
		h = 1
	}
	return h
}

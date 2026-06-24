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

	case statsLoadedMsg:
		if msg.err == "" {
			m.stats = msg.stats
			m.statsLoaded = true
		}

	case tea.KeyMsg:
		cmds = append(cmds, m.handleKey(msg))

	case tea.MouseMsg:
		// handled in future phases

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
		}
	case key.Matches(msg, keys.NavDown):
		if m.navCursor < len(m.navItems)-1 {
			m.navCursor++
			m.clampNavScroll()
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

// navPaneHeight returns usable lines in the nav pane.
func (m *Model) navPaneHeight() int {
	// matches renderMainArea: height - topBarHeight - cmdBarHeight - 1 - hintBarHeight
	h := m.height - topBarHeight - cmdBarHeight - 1 - hintBarHeight
	if h < 1 {
		h = 1
	}
	// subtract 2 header lines used by renderNavPane
	h -= 2
	if h < 1 {
		h = 1
	}
	return h
}

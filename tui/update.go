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
		cmds = append(cmds, spinnerTick())

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
	case key.Matches(msg,keys.Quit):
		return tea.Quit
	case key.Matches(msg,keys.Tab1):
		m.activeTab = tabLibrary
		return nil
	case key.Matches(msg,keys.Tab2):
		m.activeTab = tabAgent
		return nil
	case key.Matches(msg,keys.Tab3):
		m.activeTab = tabStats
		return nil
	case key.Matches(msg,keys.TabPrev):
		m.activeTab = (m.activeTab - 1 + tabCount) % tabCount
		return nil
	case key.Matches(msg,keys.TabNext):
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
	case key.Matches(msg,keys.NavUp):
		// navigation handled in future phases
	case key.Matches(msg,keys.NavDown):
		// navigation handled in future phases
	case key.Matches(msg,keys.Select):
		// selection handled in future phases
	case key.Matches(msg,keys.Command):
		m.focus = paneCommand
	case key.Matches(msg,keys.Help):
		// help overlay in future phases
	}
	return nil
}

func (m *Model) handleContentKey(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg,keys.Back):
		m.focus = paneNav
	}
	return nil
}

func (m *Model) handleCommandKey(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg,keys.Back):
		m.focus = paneNav
	}
	return nil
}

package tui

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
)

const stateFile = "tui_state.json"

// tuiState holds UI state that persists between TUI invocations.
type tuiState struct {
	ActiveTab  string `json:"active_tab,omitempty"`
	SubTab     string `json:"sub_tab,omitempty"`
	Workspace  string `json:"workspace,omitempty"`
	Article    string `json:"article,omitempty"`
	Collection string `json:"collection,omitempty"`
}

func statePath(dataRoot string) string {
	return filepath.Join(dataRoot, stateFile)
}

func loadTUIState(dataRoot string) tuiState {
	data, err := os.ReadFile(statePath(dataRoot))
	if err != nil {
		return tuiState{}
	}
	var s tuiState
	if json.Unmarshal(data, &s) != nil {
		return tuiState{}
	}
	return s
}

func saveTUIState(dataRoot string, s tuiState) {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		slog.Error("tui state marshal failed", "err", err)
		return
	}
	path := statePath(dataRoot)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		slog.Error("tui state write failed", "path", path, "err", err)
		return
	}
	slog.Debug("tui state saved", "path", path, "state", string(data))
}

func tabFromString(s string) tab {
	switch s {
	case "library":
		return tabLibrary
	case "agent":
		return tabAgent
	case "stats":
		return tabStats
	default:
		return tabLibrary
	}
}

func tabToString(t tab) string {
	switch t {
	case tabLibrary:
		return "library"
	case tabAgent:
		return "agent"
	case tabStats:
		return "stats"
	default:
		return "library"
	}
}

func subTabFromString(s string) navSubTab {
	switch s {
	case "workspaces":
		return navSubTabWorkspaces
	case "collections":
		return navSubTabCollections
	case "articles":
		return navSubTabArticles
	default:
		return navSubTabArticles
	}
}

func subTabToString(t navSubTab) string {
	switch t {
	case navSubTabWorkspaces:
		return "workspaces"
	case navSubTabCollections:
		return "collections"
	case navSubTabArticles:
		return "articles"
	default:
		return "articles"
	}
}

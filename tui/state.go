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
	ActiveTab          string `json:"active_tab,omitempty"`
	SubTab             string `json:"sub_tab,omitempty"`
	AgentSubTab        string `json:"agent_sub_tab,omitempty"`
	StatsSubTab        string `json:"stats_sub_tab,omitempty"`
	AgentRunID         string `json:"agent_run_id,omitempty"`
	AgentContentCursor int    `json:"agent_content_cursor,omitempty"`
	Workspace          string `json:"workspace,omitempty"`
	Article            string `json:"article,omitempty"`
	Collection         string `json:"collection,omitempty"`
	ExpandedCollection string `json:"expanded_collection,omitempty"` // collection that was unfolded
	NavArticle         string `json:"nav_article,omitempty"`         // selected article inside expanded collection
	WsFocus            string `json:"ws_focus,omitempty"`            // solo-mode workspace name
	WsExpanded         bool   `json:"ws_expanded,omitempty"`         // selected workspace was expanded
	WsExpandedCol      string `json:"ws_expanded_col,omitempty"`     // collection slug expanded within workspace
	WsArticle          string `json:"ws_article,omitempty"`          // selected article slug within workspace
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

func agentSubTabFromString(s string) agentSubTab {
	switch s {
	case "feeds":
		return agentSubTabFeeds
	default:
		return agentSubTabRuns
	}
}

func agentSubTabToString(t agentSubTab) string {
	switch t {
	case agentSubTabFeeds:
		return "feeds"
	default:
		return "runs"
	}
}

func statsSubTabFromString(s string) statsSubTab {
	switch s {
	case "cost":
		return statsSubTabCost
	case "tokens":
		return statsSubTabTokens
	case "requests":
		return statsSubTabRequests
	default:
		return statsSubTabOverview
	}
}

func statsSubTabToString(t statsSubTab) string {
	switch t {
	case statsSubTabCost:
		return "cost"
	case statsSubTabTokens:
		return "tokens"
	case statsSubTabRequests:
		return "requests"
	default:
		return "overview"
	}
}

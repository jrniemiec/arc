package chat

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ChatStore persists chat state for a single workspace.
//
// Layout:
//
//	<dataRoot>/workspaces/<name>/system.txt         ← workspace system prompt
//	<dataRoot>/workspaces/<name>/chat/history.json  ← conversation history
//	<dataRoot>/workspaces/<name>/chat/summary.txt   ← summarize strategy cache
type ChatStore struct {
	dataRoot      string
	workspaceName string
}

// NewChatStore creates a ChatStore for the given workspace.
func NewChatStore(dataRoot, workspaceName string) *ChatStore {
	return &ChatStore{dataRoot: dataRoot, workspaceName: workspaceName}
}

func (s *ChatStore) workspaceDir() string {
	return filepath.Join(s.dataRoot, "workspaces", s.workspaceName)
}

func (s *ChatStore) chatDir() string {
	return filepath.Join(s.workspaceDir(), "chat")
}

func (s *ChatStore) historyPath() string {
	return filepath.Join(s.chatDir(), "history.json")
}

func (s *ChatStore) systemPath() string {
	return filepath.Join(s.workspaceDir(), "system.txt")
}

func (s *ChatStore) summaryPath() string {
	return filepath.Join(s.chatDir(), "summary.txt")
}

// LoadSystem reads the workspace system prompt (empty string if missing).
func (s *ChatStore) LoadSystem() (string, error) {
	b, err := os.ReadFile(s.systemPath())
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(b), nil
}

// LoadHistory loads chat/history.json (returns empty history if missing).
func (s *ChatStore) LoadHistory() (*History, error) {
	if err := os.MkdirAll(s.chatDir(), 0755); err != nil {
		return nil, err
	}
	return loadHistoryFile(s.historyPath())
}

// SaveHistory writes chat/history.json atomically.
func (s *ChatStore) SaveHistory(h *History) error {
	if err := os.MkdirAll(s.chatDir(), 0755); err != nil {
		return err
	}
	return saveHistoryFile(s.historyPath(), h)
}

// ClearHistory resets history and removes any cached summary.
func (s *ChatStore) ClearHistory() error {
	_ = os.Remove(s.summaryPath())
	return s.SaveHistory(NewHistory())
}

// LoadSummary loads the cached summary from chat/summary.txt.
// Returns ("", -1, nil) if no summary exists.
func (s *ChatStore) LoadSummary() (text string, coversThrough int, err error) {
	b, err := os.ReadFile(s.summaryPath())
	if err != nil {
		if os.IsNotExist(err) {
			return "", -1, nil
		}
		return "", -1, err
	}
	return parseSummaryFile(string(b))
}

// SaveSummary persists the summary atomically to chat/summary.txt.
func (s *ChatStore) SaveSummary(text string, coversThrough int) error {
	if err := os.MkdirAll(s.chatDir(), 0755); err != nil {
		return err
	}
	content := fmt.Sprintf("covers_through: %d\n---\n%s", coversThrough, text)
	tmp := s.summaryPath() + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0644); err != nil {
		return err
	}
	return os.Rename(tmp, s.summaryPath())
}

// --- helpers -----------------------------------------------------------------

func parseSummaryFile(data string) (text string, coversThrough int, err error) {
	const sep = "\n---\n"
	idx := strings.Index(data, sep)
	if idx < 0 {
		return "", 0, fmt.Errorf("summary file: missing '---' separator")
	}
	header := strings.TrimSpace(data[:idx])
	body := data[idx+len(sep):]

	const prefix = "covers_through: "
	if !strings.HasPrefix(header, prefix) {
		return "", 0, fmt.Errorf("summary file: missing 'covers_through:' header")
	}
	n, err := strconv.Atoi(strings.TrimSpace(header[len(prefix):]))
	if err != nil {
		return "", 0, fmt.Errorf("summary file: bad covers_through value: %w", err)
	}
	return body, n, nil
}

func loadHistoryFile(path string) (*History, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return NewHistory(), nil
		}
		return nil, err
	}
	if len(b) == 0 {
		return NewHistory(), nil
	}
	var h History
	if err := json.Unmarshal(b, &h); err != nil {
		return nil, err
	}
	if h.Msgs == nil {
		h.Msgs = []Message{}
	}
	return &h, nil
}

func saveHistoryFile(path string, h *History) error {
	b, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

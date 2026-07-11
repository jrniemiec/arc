package tui

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

const historyFile = "command_history"

// historyPath returns the path to the command history file.
func historyPath(dataRoot string) string {
	return filepath.Join(dataRoot, historyFile)
}

// loadCommandHistory reads the history file and returns entries, oldest first.
// Returns nil (not an error) if the file doesn't exist yet.
func loadCommandHistory(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		slog.Debug("loadCommandHistory: read failed", "path", path, "err", err)
		return nil
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	var out []string
	for _, l := range lines {
		if strings.HasPrefix(l, "/") {
			out = append(out, l)
		}
	}
	result := dedupeHistory(out)
	slog.Debug("loadCommandHistory", "path", path, "rawLines", len(lines), "slashCmds", len(out), "afterDedup", len(result))
	return result
}

// saveCommandHistory filters to /commands, deduplicates, and writes to path.
// Errors are silently ignored — history is best-effort.
func saveCommandHistory(path string, history []string) {
	slog.Debug("saveCommandHistory", "path", path, "totalEntries", len(history))
	var cmds []string
	for _, h := range history {
		if strings.HasPrefix(h, "/") {
			cmds = append(cmds, h)
		}
	}
	cmds = dedupeHistory(cmds)
	slog.Debug("saveCommandHistory: writing", "slashCmds", len(cmds))
	_ = os.WriteFile(path, []byte(strings.Join(cmds, "\n")+"\n"), 0o644)
}

// dedupeHistory removes duplicate entries keeping the last occurrence of each,
// preserving relative order of the survivors (oldest first).
func dedupeHistory(entries []string) []string {
	seen := make(map[string]bool, len(entries))
	// Walk backwards to identify which occurrence to keep (the last one).
	keep := make([]bool, len(entries))
	for i := len(entries) - 1; i >= 0; i-- {
		if !seen[entries[i]] {
			seen[entries[i]] = true
			keep[i] = true
		}
	}
	out := entries[:0]
	for i, e := range entries {
		if keep[i] {
			out = append(out, e)
		}
	}
	return out
}

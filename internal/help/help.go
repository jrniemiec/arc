// Package help provides bundled and cached help content for arc.
// Documentation files are embedded at build time from docs/.
// Fresh versions can be fetched from GitHub and cached locally.
package help

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/jrniemiec/arc/docs"
)

// Section identifies a help document.
type Section int

const (
	Readme Section = iota
	Tutorial
	TUICommands
	TUIKeys
	CLICommands
	SectionCount
)

var sectionFiles = [SectionCount]string{
	Readme:      "readme.md",
	Tutorial:    "tutorial.md",
	TUICommands: "tui-commands.md",
	TUIKeys:     "tui-keys.md",
	CLICommands: "cli-commands.md",
}

var sectionLabels = [SectionCount]string{
	Readme:      "Readme",
	Tutorial:    "Tutorial",
	TUICommands: "TUI Cmds",
	TUIKeys:     "TUI Keys",
	CLICommands: "CLI Cmds",
}

// String returns the display label for a section.
func (s Section) String() string {
	if s >= 0 && s < SectionCount {
		return sectionLabels[s]
	}
	return "?"
}

// Filename returns the filename for a section.
func (s Section) Filename() string {
	if s >= 0 && s < SectionCount {
		return sectionFiles[s]
	}
	return ""
}

// ParseSection returns the section matching a CLI argument name.
func ParseSection(name string) (Section, bool) {
	aliases := map[string]Section{
		"readme":       Readme,
		"tutorial":     Tutorial,
		"tui-commands": TUICommands,
		"tui-cmds":     TUICommands,
		"tui-keys":     TUIKeys,
		"cli-commands": CLICommands,
		"cli-cmds":     CLICommands,
		"cli":          CLICommands,
	}
	s, ok := aliases[name]
	return s, ok
}

const (
	cacheTTL = 24 * time.Hour
	repoRaw  = "https://raw.githubusercontent.com/jrniemiec/arc/main/docs/"
)

// Content returns the help text for a section.
// It checks the local cache first, then falls back to the embedded copy.
func Content(section Section, cacheDir string) string {
	// Try cache first.
	if cacheDir != "" {
		if data, ok := readCache(cacheDir, section); ok {
			return data
		}
	}
	// Fall back to embedded.
	return Embedded(section)
}

// Embedded returns the bundled content for a section.
func Embedded(section Section) string {
	data, err := docs.FS.ReadFile(section.Filename())
	if err != nil {
		return fmt.Sprintf("(help content not available: %s)", section.Filename())
	}
	return string(data)
}

// FetchAndCache downloads the latest version of a section from GitHub
// and writes it to the cache directory. Returns the content and any error.
// Intended to be called in a background goroutine.
func FetchAndCache(section Section, cacheDir string) (string, error) {
	url := repoRaw + section.Filename()
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "", fmt.Errorf("fetch %s: %w", section.Filename(), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch %s: HTTP %d", section.Filename(), resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", section.Filename(), err)
	}
	content := string(body)

	// Write to cache.
	if cacheDir != "" {
		dir := filepath.Join(cacheDir, "help")
		_ = os.MkdirAll(dir, 0o755)
		_ = os.WriteFile(filepath.Join(dir, section.Filename()), body, 0o644)
	}
	return content, nil
}

// CacheDir returns the help cache directory under the given arc data root.
func CacheDir(dataRoot string) string {
	return filepath.Join(dataRoot, "cache")
}

// readCache reads cached content if it exists and is not stale.
func readCache(cacheDir string, section Section) (string, bool) {
	path := filepath.Join(cacheDir, "help", section.Filename())
	info, err := os.Stat(path)
	if err != nil {
		return "", false
	}
	if time.Since(info.ModTime()) > cacheTTL {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(data), true
}

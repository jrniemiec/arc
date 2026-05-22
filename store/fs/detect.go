package fs

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/jrniemiec/arc/store"
)

// ProbeFiles walks an article directory and resolves which files are present,
// returning a Files struct with absolute paths. Empty string means absent.
func ProbeFiles(dir string) store.Files {
	return store.Files{
		Root:       dir,
		Body:       firstExisting(dir, "body.txt"),
		SourceURL:  firstExisting(dir, "source.url"),
		SourcePDF:  firstExisting(dir, "source.pdf"),
		SourceHTML: firstExisting(dir, "source.html"),
		Meta:       firstExisting(dir, "meta.json"),
		// Summary, Flash, Flashcards resolved separately via config preferences
	}
}

// ResolveSummary returns the best summary file given ordered style and model preferences.
// Pattern: summary.<style>.<model>.txt
func ResolveSummary(dir string, styles, models []string) string {
	for _, style := range styles {
		for _, model := range models {
			path := filepath.Join(dir, summaryName(style, model))
			if fileExists(path) {
				return path
			}
		}
	}
	// fallback: first glob match
	return firstGlob(dir, "summary.*.*.txt")
}

// ResolveFlash returns the best flash file given ordered model preferences.
// Pattern: flash.<model>.txt
func ResolveFlash(dir string, models []string) string {
	for _, model := range models {
		path := filepath.Join(dir, flashName(model))
		if fileExists(path) {
			return path
		}
	}
	return firstGlob(dir, "flash.*.txt")
}

// ResolveFlashcards returns the best flashcards file given ordered style and model preferences.
// Pattern: flashcards.<style>.<model>.json
func ResolveFlashcards(dir string, styles, models []string) string {
	for _, style := range styles {
		for _, model := range models {
			path := filepath.Join(dir, flashcardsName(style, model))
			if fileExists(path) {
				return path
			}
		}
	}
	return firstGlob(dir, "flashcards.*.*.json")
}

// ListSummaries returns all summary variants in an article directory.
func ListSummaries(dir string) []string {
	return globFiles(dir, "summary.*.*.txt")
}

// ListFlashes returns all flash variants in an article directory.
func ListFlashes(dir string) []string {
	return globFiles(dir, "flash.*.txt")
}

// ListFlashcards returns all flashcard variants in an article directory.
func ListFlashcards(dir string) []string {
	return globFiles(dir, "flashcards.*.*.json")
}

// ParseSummaryName extracts style and model from a summary filename.
// "summary.study-notes.claude-opus-4-6.txt" → ("study-notes", "claude-opus-4-6")
func ParseSummaryName(filename string) (style, model string, ok bool) {
	base := filepath.Base(filename)
	base = strings.TrimSuffix(base, ".txt")
	parts := strings.SplitN(base, ".", 3)
	if len(parts) != 3 || parts[0] != "summary" {
		return "", "", false
	}
	return parts[1], parts[2], true
}

// ParseFlashName extracts model from a flash filename.
// "flash.claude-haiku-4-5.txt" → "claude-haiku-4-5"
func ParseFlashName(filename string) (model string, ok bool) {
	base := filepath.Base(filename)
	base = strings.TrimSuffix(base, ".txt")
	parts := strings.SplitN(base, ".", 2)
	if len(parts) != 2 || parts[0] != "flash" {
		return "", false
	}
	return parts[1], true
}

// ParseFlashcardsName extracts style and model from a flashcards filename.
func ParseFlashcardsName(filename string) (style, model string, ok bool) {
	base := filepath.Base(filename)
	base = strings.TrimSuffix(base, ".json")
	parts := strings.SplitN(base, ".", 3)
	if len(parts) != 3 || parts[0] != "flashcards" {
		return "", "", false
	}
	return parts[1], parts[2], true
}

// SummaryName returns the canonical filename for a summary variant.
func summaryName(style, model string) string {
	return "summary." + style + "." + model + ".txt"
}

// flashName returns the canonical filename for a flash variant.
func flashName(model string) string {
	return "flash." + model + ".txt"
}

// flashcardsName returns the canonical filename for a flashcards variant.
func flashcardsName(style, model string) string {
	return "flashcards." + style + "." + model + ".json"
}

func firstExisting(dir string, names ...string) string {
	for _, name := range names {
		p := filepath.Join(dir, name)
		if fileExists(p) {
			return p
		}
	}
	return ""
}

func firstGlob(dir, pattern string) string {
	matches := globFiles(dir, pattern)
	if len(matches) == 0 {
		return ""
	}
	return matches[0]
}

func globFiles(dir, pattern string) []string {
	matches, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil || len(matches) == 0 {
		return nil
	}
	sort.Strings(matches)
	return matches
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	info, err := filepath.Abs(path)
	_ = info
	if err != nil {
		return false
	}
	_, err = filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	// use os.Stat via the standard approach
	return pathExists(path)
}

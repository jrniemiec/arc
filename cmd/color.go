package cmd

import (
	"os"
	"regexp"
	"strings"

	"github.com/mattn/go-isatty"
)

// isTTY reports whether f is connected to a terminal.
func isTTY(f *os.File) bool {
	return isatty.IsTerminal(f.Fd())
}

const ansiReset = "\033[0m"

// ansiCode returns the ANSI escape sequence for a named color.
// Returns empty string for unrecognised names (safe to concatenate).
func ansiCode(name string) string {
	switch name {
	case "red":
		return "\033[31m"
	case "green":
		return "\033[32m"
	case "yellow":
		return "\033[33m"
	case "blue":
		return "\033[34m"
	case "magenta":
		return "\033[35m"
	case "cyan":
		return "\033[36m"
	case "bold":
		return "\033[1m"
	case "dim":
		return "\033[2m"
	default:
		return ""
	}
}

// colorFor returns the ANSI color name for a cost tier.
func colorFor(costTier string) string {
	switch costTier {
	case "local":
		return "green"
	case "very_low":
		return "cyan"
	case "low":
		return "cyan"
	case "medium":
		return "yellow"
	case "high":
		return "magenta"
	case "premium":
		return "red"
	default:
		return ""
	}
}

// colorize wraps s with the ANSI color for the given cost tier, if tty is true.
func colorize(s, costTier string, tty bool) string {
	if !tty {
		return s
	}
	c := ansiCode(colorFor(costTier))
	if c == "" {
		return s
	}
	return c + s + ansiReset
}

// header builds a "=== label ===" line, colored by cost tier when tty.
// tierByModel maps model ID → cost tier (from config profiles).
func header(label, model string, tierByModel map[string]string, tty bool) string {
	line := "=== " + label + " ==="
	if !tty {
		return line
	}
	tier := tierByModel[model]
	c := ansiCode(colorFor(tier))
	if c == "" {
		return line
	}
	return c + line + ansiReset
}

// costColor returns a cost tier name based on a USD amount, for coloring cost figures.
func costColor(usd float64) string {
	switch {
	case usd >= 1.0:
		return "red"
	case usd >= 0.1:
		return "magenta"
	case usd >= 0.01:
		return "yellow"
	default:
		return "green"
	}
}

// dim wraps s in dim ANSI, if tty is true.
func dim(s string, tty bool) string {
	if !tty {
		return s
	}
	return ansiCode("dim") + s + ansiReset
}

// bold wraps s in bold ANSI, if tty is true.
func bold(s string, tty bool) string {
	if !tty {
		return s
	}
	return ansiCode("bold") + s + ansiReset
}

var mdBoldRe = regexp.MustCompile(`\*\*(.+?)\*\*`)

// renderMarkdown applies basic ANSI styling to markdown text when tty is true.
// Handles: # headers (bold), **inline bold**, --- separators (dim).
// No base color is applied — only structural formatting.
func renderMarkdown(text, _ string, tty bool) string {
	if !tty {
		return text
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "# ") || strings.HasPrefix(trimmed, "## ") || strings.HasPrefix(trimmed, "### "):
			lines[i] = ansiCode("bold") + line + ansiReset
		case trimmed == "---" || trimmed == "***" || trimmed == "___":
			lines[i] = ansiCode("dim") + line + ansiReset
		default:
			lines[i] = mdBoldRe.ReplaceAllString(line, ansiCode("bold")+"$1"+ansiReset)
		}
	}
	return strings.Join(lines, "\n")
}

var (
	jsonKeyRe    = regexp.MustCompile(`("(?:[^"\\]|\\.)*")\s*:`)
	jsonStrValRe = regexp.MustCompile(`:\s*("(?:[^"\\]|\\.)*")`)
	jsonNumRe    = regexp.MustCompile(`:\s*(-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)`)
	jsonBoolRe   = regexp.MustCompile(`:\s*(true|false|null)`)
)

// renderJSON applies ANSI syntax highlighting to JSON text when tty is true.
// The base text (punctuation, whitespace) is rendered in the cost tier color.
// Keys → cyan, string values → green, numbers → yellow, booleans/null → magenta.
func renderJSON(data []byte, costTier string, tty bool) string {
	text := string(data)
	if !tty {
		return text
	}
	tierC := ansiCode(colorFor(costTier))
	restore := ansiReset
	if tierC != "" {
		restore = ansiReset + tierC
	}

	lines := strings.Split(text, "\n")
	for i, line := range lines {
		// Apply in order: booleans, numbers, string values, then keys.
		line = jsonBoolRe.ReplaceAllStringFunc(line, func(m string) string {
			idx := strings.IndexAny(m, "tfn")
			return m[:idx] + ansiCode("magenta") + m[idx:] + restore
		})
		line = jsonNumRe.ReplaceAllStringFunc(line, func(m string) string {
			idx := strings.IndexAny(m, "-0123456789")
			return m[:idx] + ansiCode("yellow") + m[idx:] + restore
		})
		line = jsonStrValRe.ReplaceAllStringFunc(line, func(m string) string {
			idx := strings.Index(m, `"`)
			return m[:idx] + ansiCode("green") + m[idx:] + restore
		})
		line = jsonKeyRe.ReplaceAllStringFunc(line, func(m string) string {
			colon := strings.LastIndex(m, ":")
			return ansiCode("cyan") + m[:colon] + restore + m[colon:]
		})
		lines[i] = line
	}
	result := strings.Join(lines, "\n")
	if tierC != "" {
		result = tierC + result + ansiReset
	}
	return result
}

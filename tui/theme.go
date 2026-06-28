package tui

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Theme holds foreground colors only. No background colors are set —
// the terminal's own background is used throughout.
type Theme struct {
	// Top bar and tab bar
	TopBarText  lipgloss.Color
	TabActive   lipgloss.Color
	TabInactive lipgloss.Color

	// Left navigator
	NavText    lipgloss.Color // article/item text
	NavGroup   lipgloss.Color // group headers (Collections, Workspaces)
	NavDimmed  lipgloss.Color // counts, secondary info
	NavMark    lipgloss.Color // ✓ read / • unread indicators

	// Right content pane
	ContentTitle  lipgloss.Color
	ContentText   lipgloss.Color
	ContentDimmed lipgloss.Color // metadata, timestamps

	// Tab strip in content pane (Body / Summary / Flash / Cards)
	ContentTabActive   lipgloss.Color
	ContentTabInactive lipgloss.Color

	// Command input
	InputPrompt lipgloss.Color
	InputText   lipgloss.Color

	// Status / hints bar
	StatusText lipgloss.Color

	// Spinner / progress
	Spinner lipgloss.Color

	// Accents, borders, separators
	Accent    lipgloss.Color
	BoxBorder lipgloss.Color

	// Dimmed (general purpose)
	Dimmed lipgloss.Color

	// Favorite star
	Favorite lipgloss.Color
}

// Nord is a cool-blues dark theme based on the Nord palette.
var Nord = Theme{
	TopBarText:  "#C79BFF", // purple
	TabActive:   "#C79BFF", // purple — active tab
	TabInactive: "#4C566A", // nord3  — muted gray

	NavText:   "#D8DEE9", // nord4  — soft white
	NavGroup:  "#81A1C1", // nord9  — steel blue
	NavDimmed: "#4C566A", // nord3  — dark gray
	NavMark:   "#A3BE8C", // nord14 — green

	ContentTitle:  "#C79BFF", // purple
	ContentText:   "#D8DEE9", // nord4  — soft white
	ContentDimmed: "#4C566A", // nord3  — dark gray

	ContentTabActive:   "#D8DEE9", // soft white
	ContentTabInactive: "#4C566A", // dark gray

	InputPrompt: "#81A1C1", // nord9
	InputText:   "#D8DEE9", // nord4

	StatusText: "#4C566A", // nord3
	Spinner:    "#88C0D0", // nord8  — light cyan
	Accent:     "#88C0D0", // nord8
	BoxBorder:  "#88C0D0", // nord8
	Dimmed:     "#4C566A", // nord3
	Favorite:   "#F5A623", // amber
}

// ClaudeCode approximates the color palette used by Claude Code's TUI.
var ClaudeCode = Theme{
	TopBarText:  "#D7D7D7",
	TabActive:   "#C79BFF", // purple
	TabInactive: "#505050",

	NavText:   "#D7D7D7",
	NavGroup:  "#7AB4E8", // steel blue
	NavDimmed: "#505050",
	NavMark:   "#88C0D0", // cyan

	ContentTitle:  "#C79BFF",
	ContentText:   "#D7D7D7",
	ContentDimmed: "#505050",

	ContentTabActive:   "#D7D7D7",
	ContentTabInactive: "#505050",

	InputPrompt: "#7AB4E8",
	InputText:   "#D7D7D7",

	StatusText: "#505050",
	Spinner:    "#6598FF",
	Accent:     "#6598FF",
	BoxBorder:  "#6598FF",
	Dimmed:     "#505050",
	Favorite:   "#F5A623", // amber
}

// Light is for terminals with a light background.
var Light = Theme{
	TopBarText:  "#5A2D9A",
	TabActive:   "#5A2D9A",
	TabInactive: "#9A9A9A",

	NavText:   "#2E2E2E",
	NavGroup:  "#1A5E8A",
	NavDimmed: "#9A9A9A",
	NavMark:   "#2D7A2D",

	ContentTitle:  "#5A2D9A",
	ContentText:   "#2E2E2E",
	ContentDimmed: "#9A9A9A",

	ContentTabActive:   "#2E2E2E",
	ContentTabInactive: "#9A9A9A",

	InputPrompt: "#1A5E8A",
	InputText:   "#2E2E2E",

	StatusText: "#9A9A9A",
	Spinner:    "#1A7AB0",
	Accent:     "#1A7AB0",
	BoxBorder:  "#1A7AB0",
	Dimmed:     "#9A9A9A",
	Favorite:   "#B8860B", // dark goldenrod for light bg
}

// ActiveTheme is the theme used by all view functions.
var ActiveTheme = Nord

// ApplyTheme sets ActiveTheme from a mode string: "light", "dark", or "auto".
func ApplyTheme(mode string) {
	switch strings.ToLower(mode) {
	case "light":
		ActiveTheme = Light
	case "dark":
		ActiveTheme = Nord
	default:
		DetectTheme()
	}
}

// DetectTheme sets ActiveTheme based on terminal-specific heuristics.
func DetectTheme() {
	switch ActiveTerminal {
	case TermITerm2:
		fgbg := os.Getenv("COLORFGBG")
		if fgbg == "" {
			return
		}
		parts := strings.SplitN(fgbg, ";", 2)
		if len(parts) != 2 {
			return
		}
		var bg int
		fmt.Sscanf(parts[1], "%d", &bg)
		if bg >= 8 {
			ActiveTheme = Light
		}
	case TermApple:
		if queryBackgroundLight() {
			ActiveTheme = Light
		}
	}
}

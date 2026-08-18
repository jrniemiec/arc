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
	NavText   lipgloss.Color // article/item text
	NavGroup  lipgloss.Color // group headers (Collections, Workspaces)
	NavDimmed lipgloss.Color // counts, secondary info
	NavMark   lipgloss.Color // ✓ read / • unread indicators

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

	// Pinned workspace star
	Pinned lipgloss.Color

	// Status
	StatusError lipgloss.Color // error messages in status bar

	// Multi-client xlock banner.
	// Held is deliberately distinct from StatusError: an unpushed commit is not
	// an error — the work is safe, it just has not left the machine — while a
	// diverged clone stops the app entirely. Sharing one colour would train the
	// eye to ignore both.
	XLockHeld    lipgloss.Color // this machine may write
	XLockBlocked lipgloss.Color // another machine holds it, or work is unpushed

	// Streaming / chat activity
	StreamingText lipgloss.Color

	// Chat
	ChatUser      lipgloss.Color // "you:" prefix
	ChatAssistant lipgloss.Color // assistant message text
	ChatHeader    lipgloss.Color // markdown heading text
	ChatQuote     lipgloss.Color // blockquote text
	ChatCode      lipgloss.Color // inline/block code text

	// Focus / cursor indicators (only colors that need contrast on both
	// dark and light backgrounds — everything else is theme-neutral gold
	// elsewhere in the codebase).
	FocusMark lipgloss.Color // nav/list selection indicator (▌)
	CursorBg  lipgloss.Color // input text-cursor block background
	CursorFg  lipgloss.Color // input text-cursor block foreground
}

// Nord is a cool-blues dark theme based on the Nord palette.
var Nord = Theme{
	TopBarText:  "#C79BFF", // purple
	TabActive:   "#C79BFF", // purple — active tab
	TabInactive: "#4C566A", // nord3  — muted gray

	NavText:   "#D8DEE9", // nord4  — soft white
	NavGroup:  "#81A1C1", // nord9  — steel blue
	NavDimmed: "#8890A0", // mid-gray (was #4C566A nord3)
	NavMark:   "#A3BE8C", // nord14 — green

	ContentTitle:  "#C79BFF", // purple
	ContentText:   "#D8DEE9", // nord4  — soft white
	ContentDimmed: "#8890A0", // mid-gray (was #4C566A nord3)

	ContentTabActive:   "#D8DEE9", // soft white
	ContentTabInactive: "#8890A0", // mid-gray (was #4C566A nord3)

	InputPrompt: "#81A1C1", // nord9
	InputText:   "#D8DEE9", // nord4

	StatusText: "#8890A0", // mid-gray (was #4C566A nord3)
	Spinner:    "#88C0D0", // nord8  — light cyan
	Accent:     "#88C0D0", // nord8
	BoxBorder:  "#88C0D0", // nord8
	Dimmed:     "#8890A0", // mid-gray (was #4C566A nord3)
	Favorite:   "#F5A623", // amber
	Pinned:     "#C79BFF", // purple

	StatusError: "#BF616A", // nord11 — red

	XLockHeld:    "#A3BE8C", // nord14 — green
	XLockBlocked: "#EBCB8B", // nord13 — amber

	StreamingText: "#88C0D0", // nord8 — light cyan
	ChatUser:      "#A3BE8C", // nord14 — green
	ChatAssistant: "#D8DEE9", // nord4 — soft white
	ChatHeader:    "#88C0D0", // nord8 — cyan for headings
	ChatQuote:     "#8890A0", // mid-gray (was #4C566A nord3) — dimmed for blockquotes
	ChatCode:      "#EBCB8B", // nord13 — yellow for code

	FocusMark: "#FFD700", // gold
	CursorBg:  "#FFD700", // gold
	CursorFg:  "#000000", // black
}

// ClaudeCode approximates the color palette used by Claude Code's TUI.
var ClaudeCode = Theme{
	TopBarText:  "#D7D7D7",
	TabActive:   "#C79BFF", // purple
	TabInactive: "#505050",

	NavText:   "#D7D7D7",
	NavGroup:  "#7AB4E8", // steel blue
	NavDimmed: "#8890A0", // mid-gray (was #505050)
	NavMark:   "#88C0D0", // cyan

	ContentTitle:  "#C79BFF",
	ContentText:   "#D7D7D7",
	ContentDimmed: "#8890A0", // mid-gray (was #505050)

	ContentTabActive:   "#D7D7D7",
	ContentTabInactive: "#8890A0", // mid-gray (was #505050)

	InputPrompt: "#7AB4E8",
	InputText:   "#D7D7D7",

	StatusText: "#8890A0", // mid-gray (was #505050)
	Spinner:    "#6598FF",
	Accent:     "#6598FF",
	BoxBorder:  "#6598FF",
	Dimmed:     "#8890A0", // mid-gray (was #505050)
	Favorite:   "#F5A623", // amber
	Pinned:     "#C79BFF", // purple

	StatusError: "#E06C75", // red

	XLockHeld:    "#98C379", // green
	XLockBlocked: "#E5C07B", // amber

	StreamingText: "#6598FF",
	ChatUser:      "#88C0D0",
	ChatAssistant: "#D7D7D7",
	ChatHeader:    "#7AB4E8", // steel blue for headings
	ChatQuote:     "#8890A0", // mid-gray (was #505050) — dimmed for blockquotes
	ChatCode:      "#E5C07B", // warm yellow for code
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
	Pinned:     "#5A2D9A", // purple for light bg

	StatusError: "#CC0000", // red for light bg

	XLockHeld:    "#2D7A2D", // green for light bg
	XLockBlocked: "#9A6700", // dark amber for light bg

	StreamingText: "#1A7AB0",
	ChatUser:      "#2D7A2D",
	ChatAssistant: "#2E2E2E",
	ChatHeader:    "#1A5E8A", // dark blue for headings
	ChatQuote:     "#9A9A9A", // muted for blockquotes
	ChatCode:      "#8A5E00", // dark amber for code

	FocusMark: "#5A2D9A", // purple
	CursorBg:  "#5A2D9A", // purple
	CursorFg:  "#FFFFFF", // white
}

// ActiveTheme is the theme used by all view functions.
var ActiveTheme = Nord

// ActiveThemeName is the resolved theme mode ("light" or "dark") behind
// ActiveTheme. Tracked separately because AdjustThemeForTerminal mutates
// ActiveTheme's fields in place, so the struct itself can no longer be
// compared against Light/Nord to recover which mode is active.
var ActiveThemeName = "dark"

// ApplyTheme sets ActiveTheme from a mode string: "light", "dark", or "auto".
func ApplyTheme(mode string) {
	switch strings.ToLower(mode) {
	case "light":
		ActiveTheme = Light
		ActiveThemeName = "light"
	case "dark":
		ActiveTheme = Nord
		ActiveThemeName = "dark"
	default:
		DetectTheme()
	}
}

// DetectTheme sets ActiveTheme by querying the terminal background.
// Tries OSC 11 first regardless of terminal — most emulators answer it —
// and falls back to COLORFGBG only when the query goes unanswered.
// Defaults to dark otherwise. Idempotent: always resets ActiveTheme before
// detecting, so repeated calls (e.g. /theme auto after /theme light) work.
func DetectTheme() {
	ActiveTheme = Nord
	ActiveThemeName = "dark"
	if light, ok := queryBackgroundLight(); ok {
		if light {
			ActiveTheme = Light
			ActiveThemeName = "light"
		}
		return
	}
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
		ActiveThemeName = "light"
	}
}

package tui

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"

	"github.com/charmbracelet/lipgloss"
)

// Terminal identifies the host terminal emulator.
type Terminal int

const (
	TermUnknown Terminal = iota
	TermITerm2           // iTerm2 on macOS
	TermApple            // macOS Terminal.app
	TermKitty            // Kitty terminal
	TermVSCode           // VS Code integrated terminal
	TermAlacritty        // Alacritty
	TermWezTerm          // WezTerm
)

// ActiveTerminal is detected once at startup by DetectTerminal.
var ActiveTerminal = TermUnknown

// DetectTerminal identifies the running terminal from environment variables
// and sets ActiveTerminal.
func DetectTerminal() {
	switch {
	case os.Getenv("TERM_PROGRAM") == "iTerm.app":
		ActiveTerminal = TermITerm2
	case os.Getenv("TERM_PROGRAM") == "Apple_Terminal":
		ActiveTerminal = TermApple
	case os.Getenv("KITTY_WINDOW_ID") != "":
		ActiveTerminal = TermKitty
	case os.Getenv("TERM_PROGRAM") == "vscode":
		ActiveTerminal = TermVSCode
	case os.Getenv("ALACRITTY_SOCKET") != "" || os.Getenv("ALACRITTY_LOG") != "":
		ActiveTerminal = TermAlacritty
	case os.Getenv("WEZTERM_EXECUTABLE") != "":
		ActiveTerminal = TermWezTerm
	default:
		ActiveTerminal = TermUnknown
	}
}

// queryBackgroundLight sends an OSC 11 query to the terminal and returns true
// if the background color luminance indicates a light theme.
// Reads synchronously using VTIME so the response is always fully consumed
// before bubbletea starts — no leakage into keyboard input.
func queryBackgroundLight() bool {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	defer tty.Close()
	fd := int(tty.Fd())

	// Save terminal state.
	var oldState syscall.Termios
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd),
		syscall.TIOCGETA, uintptr(unsafe.Pointer(&oldState))); errno != 0 {
		return false
	}
	// Raw mode: no echo, no canonical, VMIN=0, VTIME=2 (200ms timeout per read).
	raw := oldState
	raw.Lflag &^= syscall.ICANON | syscall.ECHO | syscall.ISIG
	raw.Iflag &^= syscall.IXON | syscall.ICRNL
	raw.Cc[syscall.VMIN] = 0
	raw.Cc[syscall.VTIME] = 2 // 200ms
	syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd),
		syscall.TIOCSETA, uintptr(unsafe.Pointer(&raw)))
	defer syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd),
		syscall.TIOCSETA, uintptr(unsafe.Pointer(&oldState)))

	// Send OSC 11 query.
	fmt.Fprintf(tty, "\033]11;?\007")

	// Read until we see the terminator or timeout.
	buf := make([]byte, 1)
	var resp strings.Builder
	for resp.Len() < 128 {
		n, _ := tty.Read(buf)
		if n == 0 {
			break // VTIME expired
		}
		resp.WriteByte(buf[0])
		s := resp.String()
		if strings.HasSuffix(s, "\007") || strings.HasSuffix(s, "\033\\") {
			break
		}
	}

	// Drain any remaining bytes (safety — ensures nothing leaks to bubbletea).
	drain := make([]byte, 32)
	for {
		n, _ := tty.Read(drain)
		if n == 0 {
			break
		}
	}

	// Parse: \033]11;rgb:RRRR/GGGG/BBBB\007
	s := resp.String()
	idx := strings.Index(s, "rgb:")
	if idx < 0 {
		return false
	}
	rgb := s[idx+4:]
	rgb = strings.TrimRight(rgb, "\007\033\\")
	parts := strings.Split(strings.TrimSpace(rgb), "/")
	if len(parts) != 3 || len(parts[0]) < 2 {
		return false
	}
	// Components are 4 hex digits (0000–FFFF). High byte gives 0–255.
	var r, g, b int
	fmt.Sscanf(parts[0][:2], "%x", &r)
	fmt.Sscanf(parts[1][:2], "%x", &g)
	fmt.Sscanf(parts[2][:2], "%x", &b)

	// Perceived luminance — light if > 127.
	luminance := (r*299 + g*587 + b*114) / 1000
	return luminance > 127
}

// AdjustThemeForTerminal overrides theme colors that don't render correctly
// on the active terminal. Called after ApplyTheme().
func AdjustThemeForTerminal() {
	switch ActiveTerminal {
	case TermApple:
		// Terminal.app does not reliably support 24-bit RGB escape sequences.
		// Replace all theme colors with ANSI 256-color palette indices.
		switch ActiveTheme.TopBarText {
		case Light.TopBarText:
			ActiveTheme.TopBarText         = lipgloss.Color("55")  // dark purple
			ActiveTheme.TabActive          = lipgloss.Color("55")
			ActiveTheme.TabInactive        = lipgloss.Color("245")
			ActiveTheme.NavText            = lipgloss.Color("235") // near-black
			ActiveTheme.NavGroup           = lipgloss.Color("24")  // dark blue
			ActiveTheme.NavDimmed          = lipgloss.Color("245")
			ActiveTheme.NavMark            = lipgloss.Color("28")  // dark green
			ActiveTheme.ContentTitle       = lipgloss.Color("55")
			ActiveTheme.ContentText        = lipgloss.Color("235")
			ActiveTheme.ContentDimmed      = lipgloss.Color("245")
			ActiveTheme.ContentTabActive   = lipgloss.Color("235")
			ActiveTheme.ContentTabInactive = lipgloss.Color("245")
			ActiveTheme.InputPrompt        = lipgloss.Color("24")
			ActiveTheme.InputText          = lipgloss.Color("235")
			ActiveTheme.StatusText         = lipgloss.Color("245")
			ActiveTheme.Spinner            = lipgloss.Color("31")  // dark cyan
			ActiveTheme.Accent             = lipgloss.Color("31")
			ActiveTheme.BoxBorder          = lipgloss.Color("31")
			ActiveTheme.Dimmed             = lipgloss.Color("245")
		default: // Nord
			ActiveTheme.TopBarText         = lipgloss.Color("183") // light purple
			ActiveTheme.TabActive          = lipgloss.Color("183")
			ActiveTheme.TabInactive        = lipgloss.Color("241")
			ActiveTheme.NavText            = lipgloss.Color("253") // soft white
			ActiveTheme.NavGroup           = lipgloss.Color("110") // steel blue
			ActiveTheme.NavDimmed          = lipgloss.Color("241")
			ActiveTheme.NavMark            = lipgloss.Color("114") // green
			ActiveTheme.ContentTitle       = lipgloss.Color("183")
			ActiveTheme.ContentText        = lipgloss.Color("253")
			ActiveTheme.ContentDimmed      = lipgloss.Color("241")
			ActiveTheme.ContentTabActive   = lipgloss.Color("253")
			ActiveTheme.ContentTabInactive = lipgloss.Color("241")
			ActiveTheme.InputPrompt        = lipgloss.Color("110")
			ActiveTheme.InputText          = lipgloss.Color("253")
			ActiveTheme.StatusText         = lipgloss.Color("241")
			ActiveTheme.Spinner            = lipgloss.Color("116") // light cyan
			ActiveTheme.Accent             = lipgloss.Color("116")
			ActiveTheme.BoxBorder          = lipgloss.Color("116")
			ActiveTheme.Dimmed             = lipgloss.Color("241")
		}
	}
}

// TerminalName returns a human-readable name for the active terminal.
func TerminalName() string {
	switch ActiveTerminal {
	case TermITerm2:
		return "iTerm2"
	case TermApple:
		return "Terminal.app"
	case TermKitty:
		return "Kitty"
	case TermVSCode:
		return "VS Code"
	case TermAlacritty:
		return "Alacritty"
	case TermWezTerm:
		return "WezTerm"
	default:
		return "unknown"
	}
}

// SetupTerminal applies terminal-specific escape sequences before p.Run().
// Returns a cleanup function that must be deferred by the caller.
// On iTerm2: enables alternate scroll mode (\033[?1007h) so mouse wheel
// events are delivered as cursor-key sequences, and downgrades to basic
// mouse mode (\033[?1002l + \033[?1000h) so clicks are reported but drag
// motion is not — allowing native text selection while keeping click events.
func SetupTerminal() func() {
	if ActiveTerminal == TermITerm2 {
		fmt.Fprint(os.Stdout, "\033[?1007h")
		return func() {
			fmt.Fprint(os.Stdout, "\033[?1007l")
			fmt.Fprint(os.Stdout, "\033[?1000l")
		}
	}
	return func() {}
}

// DowngradeMouseMode downgrades from cell-motion tracking to basic click-only
// mode on iTerm2. Called from Init() after bubbletea enables 1002h, so we
// downgrade to 1000h — clicks are still reported, drag motion is not (allowing
// native text selection), and 1007h handles wheel as cursor keys.
func DowngradeMouseMode() {
	if ActiveTerminal == TermITerm2 {
		// Disable motion tracking, enable basic click reporting + SGR coords.
		fmt.Fprint(os.Stdout, "\033[?1002l\033[?1000h\033[?1006h")
	}
}

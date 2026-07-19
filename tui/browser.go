package tui

import (
	"fmt"
	"os/exec"
	"strings"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
)

// chromeOpenedMsg carries the Chrome window ID returned by AppleScript.
type chromeOpenedMsg struct {
	windowID string
	err      error
}

// openInChrome fires an async cmd that opens a new Chrome window for url
// and returns the window ID so it can be closed on exit.
func openInChrome(url string) tea.Cmd {
	return func() tea.Msg {
		script := fmt.Sprintf(`
tell application "Google Chrome"
  make new window
  set URL of active tab of front window to "%s"
  return id of front window as text
end tell
`, escapeAppleScript(url))

		cmd := exec.Command("osascript", "-e", script)
		// Detach from arc's controlling terminal so that osascript's Apple Events
		// connection to Chrome does not corrupt the terminal's raw mode when Chrome
		// closes its window.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		out, err := cmd.Output()
		if err != nil {
			return chromeOpenedMsg{err: err}
		}
		return chromeOpenedMsg{windowID: strings.TrimSpace(string(out))}
	}
}

// CloseChromeWindow closes the Chrome window with the given window ID.
// Called from cmd/tui.go after p.Run() returns. No-op if windowID is empty.
func CloseChromeWindow(windowID string) {
	if windowID == "" {
		return
	}
	script := fmt.Sprintf(`
tell application "Google Chrome"
  repeat with w in windows
    try
      if (id of w as text) is "%s" then
        close w
        return
      end if
    end try
  end repeat
end tell
`, windowID)
	cmd := exec.Command("osascript", "-e", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	_ = cmd.Run()
}

// escapeAppleScript escapes a string for safe inclusion in an AppleScript string literal.
func escapeAppleScript(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}

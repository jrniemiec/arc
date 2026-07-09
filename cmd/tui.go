package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	arctui "github.com/jrniemiec/arc/tui"
)

func init() {
	rootCmd.AddCommand(tuiCmd)
	tuiCmd.Flags().String("theme", "auto", "color theme: auto|light|dark")
}

var tuiCmd = &cobra.Command{
	Use:   "tui",
	Short: "Launch the arc terminal UI",
	Long:  `Open arc's full terminal interface — browse, search, chat, and manage your knowledge base.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTUI(cmd)
	},
}

// runTUI launches the TUI. Used by both rootCmd (bare "arc") and tuiCmd ("arc tui").
func runTUI(cmd *cobra.Command) error {
	themeMode, _ := cmd.Flags().GetString("theme")
	svc := svcFrom(cmd)
	cfg := cfgFrom(cmd)

	// Intercept SIGINT so the Go runtime doesn't terminate the process
	// before p.Run() returns. Bubbletea captures ctrl+c as a keystroke
	// in raw mode, but a race with the OS signal can kill us first.
	// Drain the channel so the signal is consumed harmlessly.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT)
	go func() {
		for range sig {
		}
	}()
	defer signal.Stop(sig)

	m := arctui.New(svc, cfg, themeMode)
	cleanup := arctui.SetupTerminal()
	defer cleanup()
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	m.SetProgramSend(p.Send)

	final, err := p.Run()
	if fm, ok := final.(arctui.Model); ok {
		fm.Cleanup()
		arctui.CloseChromeWindow(fm.ChromeWindowID())
		fm.SaveHistory()
		fm.SaveState()
	}
	if err != nil && !errors.Is(err, tea.ErrInterrupted) {
		return fmt.Errorf("tui: %w", err)
	}
	return nil
}

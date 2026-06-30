package cmd

import (
	"fmt"

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
		themeMode, _ := cmd.Flags().GetString("theme")
		svc := svcFrom(cmd)
		cfg := cfgFrom(cmd)

		m := arctui.New(svc, cfg, themeMode)
		cleanup := arctui.SetupTerminal()
		defer cleanup()
		p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
		m.SetProgramSend(p.Send)

		final, err := p.Run()
		if fm, ok := final.(arctui.Model); ok {
			arctui.CloseChromeWindow(fm.ChromeWindowID())
			fm.SaveHistory()
		}
		if err != nil {
			return fmt.Errorf("tui: %w", err)
		}
		return nil
	},
}

package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jrniemiec/arc/internal/help"
)

func init() {
	rootCmd.AddCommand(helpCmd)
}

var helpCmd = &cobra.Command{
	Use:   "help [section]",
	Short: "Show arc documentation",
	Long: `Show arc documentation. Available sections:

  readme        Project overview and architecture
  tutorial      Getting started guide
  tui-commands  TUI slash commands reference
  tui-keys      TUI keyboard shortcuts
  cli-commands  CLI subcommands and flags

Without a section argument, lists available sections.`,
	DisableFlagParsing: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			fmt.Println("Available help sections:")
			fmt.Println()
			for i := help.Section(0); i < help.SectionCount; i++ {
				fmt.Printf("  %-14s %s\n", strings.ToLower(i.Filename()[:len(i.Filename())-3]), i.String())
			}
			fmt.Println()
			fmt.Println("Usage: arc help <section>")
			return nil
		}
		sec, ok := help.ParseSection(args[0])
		if !ok {
			return fmt.Errorf("unknown help section %q — run 'arc help' to list sections", args[0])
		}

		// Try cache, then embedded.
		cacheDir := help.CacheDir(arcHomeDir())
		content := help.Content(sec, cacheDir)
		fmt.Println(content)
		return nil
	},
}

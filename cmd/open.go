package cmd

import (
	"fmt"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/jrniemiec/arc/service"
)

func init() {
	rootCmd.AddCommand(openCmd)
}

var openCmd = &cobra.Command{
	Use:   "open <slug>",
	Short: "Open the article's source in the default viewer",
	Long: `Open the article's source using the system 'open' command.

  url  — opens in the default browser
  pdf  — opens in the default PDF viewer
  text — prints a message directing you to 'arc read'

Examples:
  arc open 20260522-my-article`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		slug, err := resolveSlug(cmd, args[0])
		if err != nil {
			return err
		}

		svc := svcFrom(cmd)
		a, err := svc.GetArticle(cmd.Context(), slug)
		if err != nil {
			return fmt.Errorf("open: %w", err)
		}

		switch a.SourceType {
		case "url", "rss":
			if a.URL == "" {
				return fmt.Errorf("no URL recorded for %s", slug)
			}
			return exec.Command("open", a.URL).Run()

		case "pdf":
			if a.Files.SourcePDF == "" {
				return fmt.Errorf("source PDF not found for %s", slug)
			}
			return exec.Command("open", a.Files.SourcePDF).Run()

		default:
			text, err := svc.Read(cmd.Context(), service.ReadRequest{ID: slug, Part: service.PartBody})
			if err != nil {
				return fmt.Errorf("read: %w", err)
			}
			fmt.Println(text)
			return nil
		}
	},
}

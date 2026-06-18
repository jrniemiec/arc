package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/jrniemiec/arc/store"
)

var (
	deleteAgentRun string
	deleteDryRun   bool
)

func init() {
	deleteCmd.Flags().StringVar(&deleteAgentRun, "agent-run", "", "delete all articles from a specific agent run ID")
	deleteCmd.Flags().BoolVar(&deleteDryRun, "dry-run", false, "print what would be deleted without deleting")
	rootCmd.AddCommand(deleteCmd)
}

var deleteCmd = &cobra.Command{
	Use:   "delete [<slug>]",
	Short: "Delete an article from the library",
	Long: `Permanently removes an article from the filesystem, SQLite index, and all collection symlinks.

This operation is irreversible. Use --dry-run to preview what would be deleted.

Examples:
  arc delete 20260617-my-article
  arc delete --agent-run agent-20260617-181418
  arc delete --agent-run agent-20260617-181418 --dry-run`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 && deleteAgentRun == "" {
			return fmt.Errorf("specify a slug or --agent-run <run-id>")
		}
		if len(args) > 0 && deleteAgentRun != "" {
			return fmt.Errorf("cannot specify a slug together with --agent-run")
		}

		svc := svcFrom(cmd)
		ctx := cmd.Context()

		var slugs []string

		if deleteAgentRun != "" {
			articles, err := svc.List(ctx, store.Filter{AgentRunID: deleteAgentRun, Limit: 1000})
			if err != nil {
				return fmt.Errorf("list articles for run: %w", err)
			}
			if len(articles) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "no articles found for run %s\n", deleteAgentRun)
				return nil
			}
			for _, a := range articles {
				slugs = append(slugs, a.ID)
			}
		} else {
			slug, err := resolveSlug(cmd, args[0])
			if err != nil {
				return err
			}
			slugs = []string{slug}
		}

		deleted := 0
		for _, slug := range slugs {
			if deleteDryRun {
				fmt.Fprintf(cmd.OutOrStdout(), "would delete: %s\n", slug)
				continue
			}
			if err := svc.DeleteArticle(ctx, slug); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "error deleting %s: %v\n", slug, err)
				continue
			}
			fmt.Fprintf(cmd.OutOrStdout(), "deleted: %s\n", slug)
			deleted++
		}

		if !deleteDryRun {
			fmt.Fprintf(cmd.OutOrStdout(), "deleted %d article(s)\n", deleted)
		}
		return nil
	},
}

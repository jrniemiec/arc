package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var reindexNoEmbed bool

func init() {
	reindexCmd.Flags().BoolVar(&reindexNoEmbed, "no-embed", false, "skip vector embedding generation")
	rootCmd.AddCommand(reindexCmd)
}

var reindexCmd = &cobra.Command{
	Use:   "reindex",
	Short: "Rebuild the search index from the filesystem",
	Long:  `Walk the articles directory and rebuild the SQLite metadata and FTS5 search index.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		svc := svcFrom(cmd)

		var last int
		err := svc.Reindex(cmd.Context(), func(indexed, total int) {
			last = indexed
			fmt.Fprintf(cmd.ErrOrStderr(), "\r  indexing %d/%d", indexed, total)
		})
		if last > 0 {
			fmt.Fprintln(cmd.ErrOrStderr())
		}
		if err != nil {
			return fmt.Errorf("reindex: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "reindexed %d articles\n", last)

		if reindexNoEmbed {
			return nil
		}

		var lastEmbed int
		embedded, err := svc.ReindexEmbed(cmd.Context(), func(done, total int) {
			lastEmbed = done
			fmt.Fprintf(cmd.ErrOrStderr(), "\r  embedding %d/%d", done, total)
		})
		if lastEmbed > 0 {
			fmt.Fprintln(cmd.ErrOrStderr())
		}
		if err != nil {
			// Non-fatal: vector store may not be configured.
			fmt.Fprintf(cmd.ErrOrStderr(), "embed: %v\n", err)
			return nil
		}
		if embedded > 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "embedded %d articles\n", embedded)
		}
		return nil
	},
}

package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func init() {
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
			fmt.Printf("\r  indexing %d/%d", indexed, total)
		})
		if last > 0 {
			fmt.Println() // newline after progress
		}
		if err != nil {
			return fmt.Errorf("reindex: %w", err)
		}

		fmt.Printf("reindexed %d articles\n", last)
		return nil
	},
}

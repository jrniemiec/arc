package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var reindexNoEmbed bool

func init() {
	reindexCmd.Flags().BoolVar(&reindexNoEmbed, "no-embed", false, "deprecated: no-op, reindex no longer embeds")
	_ = reindexCmd.Flags().MarkHidden("no-embed")
	rootCmd.AddCommand(reindexCmd)
}

var reindexCmd = &cobra.Command{
	Use:   "reindex",
	Short: "Rebuild the SQLite and full-text indexes from the filesystem",
	Long: `Rebuild the SQLite metadata and FTS5 full-text indexes by walking ~/.arc/articles/.

Reads every meta.json and preferred summary file from disk and re-populates
the database from scratch. Records for articles that no longer exist on disk
are removed (full rebuild, not incremental). Updates article metadata, the
full-text search index (summary text + flashcard questions), and collection
memberships.

Runs entirely offline: no API key, no network, no cost.

The vector index used for semantic search is NOT touched — rebuild it
separately with 'arc embed'. The two are split because they have very
different costs: reindex is free and local, embedding spends API calls.

When to run arc reindex:
  - After manually editing or deleting article files
  - After changing preferred_models or preferred_styles in config
    (to re-select which variant is indexed)
  - After arc reprocess (called automatically at end of reprocess)
  - To repair a corrupted or out-of-date database

Examples:
  arc reindex
  arc reindex && arc embed      # rebuild both indexes`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if reindexNoEmbed {
			fmt.Fprintln(cmd.ErrOrStderr(),
				"warning: --no-embed is deprecated and does nothing; reindex no longer embeds (see 'arc embed')")
		}

		svc := svcFrom(cmd)

		var last int
		result, err := svc.Reindex(cmd.Context(), func(indexed, total int) {
			last = indexed
			fmt.Fprintf(cmd.ErrOrStderr(), "\r  indexing %d/%d", indexed, total)
		})
		if last > 0 {
			fmt.Fprintln(cmd.ErrOrStderr())
		}
		if err != nil {
			return fmt.Errorf("reindex: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "reindexed %d articles, %d collections\n", result.Articles, result.Collections)
		return nil
	},
}

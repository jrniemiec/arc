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
	Long: `Rebuild the search indexes by walking ~/.arc/articles/.

Two indexes are rebuilt in sequence:

  SQLite / FTS5
    Reads every meta.json and preferred summary file from disk and
    re-populates the database from scratch. Records for articles that
    no longer exist on disk are removed (full rebuild, not incremental).
    Updates: article metadata, full-text search index (summary text +
    flashcard questions), collection memberships.

  Vector index (chromem-go)
    Generates embeddings for articles that have a summary but no
    embed_model recorded in meta.json (i.e. not yet embedded).
    Does NOT remove stale vectors for articles deleted from disk —
    orphaned vectors are harmless but waste space; a future --clean-vector
    flag will address this.
    Requires OPENAI_API_KEY (or ARC_OPENAI_API_KEY) to be set.
    Skip with --no-embed.

When to run arc reindex:
  - After manually editing or deleting article files
  - After changing preferred_models or preferred_styles in config
    (to re-select which variant is indexed)
  - After arc reprocess (called automatically at end of reprocess)
  - To backfill embeddings for articles ingested before semantic search
    was added

Examples:
  arc reindex
  arc reindex --no-embed`,
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

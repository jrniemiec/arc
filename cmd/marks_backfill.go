package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/jrniemiec/arc/store"
	"github.com/jrniemiec/arc/store/fs"
)

func init() {
	configCmd.AddCommand(backfillMarksCmd)
}

var backfillMarksCmd = &cobra.Command{
	Use:   "backfill-marks",
	Short: "Write read/played/favorite marks from the database to meta.json",
	Long: `Copy read, played, and favorite marks from arc.db into each article's meta.json.

These marks used to be stored only in SQLite. That made three things untrue at
once: that the filesystem is the source of truth, that arc.db can be rebuilt from
disk, and that your state travels with the tree. Rebuilding the database
destroyed them, and they never reached a second machine.

Marks are now written to meta.json as they happen. This moves the ones that
predate that change. Run it once per machine.

Only marks missing from disk are written — one already in meta.json is left
alone, so this is safe to re-run and cannot overwrite a newer value.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		out := cmd.OutOrStdout()
		cfg := cfgFrom(cmd)

		articles, err := svcFrom(cmd).List(cmd.Context(), store.Filter{})
		if err != nil {
			return fmt.Errorf("list articles: %w", err)
		}

		var written, skipped int
		for _, a := range articles {
			for _, m := range []struct {
				kind fs.Mark
				at   *time.Time
			}{
				{fs.MarkRead, a.ReadAt},
				{fs.MarkPlayed, a.PlayedAt},
				{fs.MarkFavorite, a.FavoritedAt},
			} {
				if m.at == nil {
					continue
				}
				onDisk, err := fs.GetMark(cfg.ArticlesRoot, a.ID, m.kind)
				if err != nil {
					return fmt.Errorf("read meta %s: %w", a.ID, err)
				}
				if onDisk != nil {
					skipped++
					continue // disk already has it; never overwrite
				}
				if err := fs.SetMark(cfg.ArticlesRoot, a.ID, m.kind, m.at); err != nil {
					return fmt.Errorf("write %s for %s: %w", m.kind, a.ID, err)
				}
				written++
			}
		}

		fmt.Fprintf(out, "wrote %d mark(s) to meta.json\n", written)
		if skipped > 0 {
			fmt.Fprintf(out, "left %d already on disk untouched\n", skipped)
		}
		if written > 0 {
			fmt.Fprintln(out, "\nThese now survive a database rebuild and travel with the tree.")
		}
		return nil
	},
}

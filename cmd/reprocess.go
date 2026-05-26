package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/jrniemiec/arc/service"
)

var (
	reprocessAll          bool
	reprocessClean        bool
	reprocessRefetch      bool
	reprocessBody         string
	reprocessNoSummary    bool
	reprocessNoFlash      bool
	reprocessNoFlashcards bool
	reprocessNoEmbed      bool
)

func init() {
	reprocessCmd.Flags().BoolVar(&reprocessAll, "all", false, "process all articles")
	reprocessCmd.Flags().BoolVar(&reprocessClean, "clean", false, "delete existing variant files before regenerating")
	reprocessCmd.Flags().BoolVar(&reprocessRefetch, "refetch", false, "re-fetch body from source URL or PDF")
	reprocessCmd.Flags().StringVar(&reprocessBody, "body", "", "replace body.txt from file or stdin (\"-\")")
	reprocessCmd.Flags().BoolVar(&reprocessNoSummary, "no-summary", false, "skip summary generation")
	reprocessCmd.Flags().BoolVar(&reprocessNoFlash, "no-flash", false, "skip flash generation")
	reprocessCmd.Flags().BoolVar(&reprocessNoFlashcards, "no-flashcards", false, "skip flashcard generation")
	reprocessCmd.Flags().BoolVar(&reprocessNoEmbed, "no-embed", false, "skip embedding")
	rootCmd.AddCommand(reprocessCmd)
}

var reprocessCmd = &cobra.Command{
	Use:   "reprocess [<slug>]",
	Short: "Re-run generation steps on existing articles",
	Long: `Re-run generation steps on existing articles without re-fetching.

Model and style selection is driven entirely by config profiles — to switch
models, update config then run arc reprocess (no --profile flag by design).

Pipeline (each step skippable with --no-*):
  1. Replace body.txt      (--body <file> or --body -)
  2. Refetch from source   (--refetch: re-fetches URL or re-extracts PDF)
  3. Clean variants        (--clean: delete existing files + clear vector)
  4. Summarize             → writes summary.<style>.<model>.txt
  5. Flash                 → writes flash.<model>.txt
  6. Flashcards            → writes flashcards.<style>.<model>.json
  7. Embed                 → upserts summary embedding into vector index
  8. Update meta.json      with new model/style fields
  After all articles: arc reindex rebuilds SQLite from the updated filesystem.

What --clean deletes:
  summary.*.*.txt, flash.*.txt, flashcards.*.*.json
  vector index entry for the article
  summary_model, flash_model, flashcard_model, embed_model in meta.json

What --clean never touches:
  body.txt, source.url, source.pdf, source.html, meta.json identity fields
  (title, author, ingested_at, tags, collections)

What --refetch requires:
  URL sources: source.url must exist; cookie jars from config are applied.
  PDF sources: source.pdf must exist in the article directory.
  Text-only sources: error — use --body to replace body.txt manually.

Examples:
  arc reprocess my-article
  arc reprocess my-article --clean
  arc reprocess my-article --refetch --clean
  arc reprocess my-article --no-flash --no-flashcards
  arc reprocess --all --no-embed
  cat new-body.txt | arc reprocess my-article --body -`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 && !reprocessAll {
			return fmt.Errorf("specify a slug or use --all")
		}
		if len(args) > 0 && reprocessAll {
			return fmt.Errorf("cannot specify a slug and --all together")
		}

		svc := svcFrom(cmd)

		req := service.ReprocessRequest{
			All:          reprocessAll,
			Clean:        reprocessClean,
			Refetch:      reprocessRefetch,
			BodyFile:     reprocessBody,
			NoSummary:    reprocessNoSummary,
			NoFlash:      reprocessNoFlash,
			NoFlashcards: reprocessNoFlashcards,
			NoEmbed:      reprocessNoEmbed,
		}
		if len(args) > 0 {
			req.Slug = args[0]
		}

		if !isJSON(cmd) {
			req.Progress = func(msg string) {
				fmt.Fprintf(cmd.ErrOrStderr(), "  %s\n", msg)
			}
		}

		result, err := svc.Reprocess(cmd.Context(), req)
		if err != nil {
			return fmt.Errorf("reprocess: %w", err)
		}

		if isJSON(cmd) {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		}

		w := cmd.OutOrStdout()
		if result.Skipped > 0 {
			fmt.Fprintf(w, "processed %d, skipped %d", result.Processed, result.Skipped)
		} else {
			fmt.Fprintf(w, "processed %d", result.Processed)
		}
		if result.CostUSD > 0 {
			fmt.Fprintf(w, "  ($%.4f)", result.CostUSD)
		}
		fmt.Fprintln(w)
		return nil
	},
}

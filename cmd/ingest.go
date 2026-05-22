package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jrniemiec/arc/service"
)

var (
	ingestTitle          string
	ingestCollection     string
	ingestSummaryStyle   string
	ingestProfile        string
	ingestNoFlashcards   bool
	ingestDryRun         bool
)

func init() {
	ingestCmd.Flags().StringVar(&ingestTitle, "title", "", "article title (default: auto-generated)")
	ingestCmd.Flags().StringVar(&ingestCollection, "collection", "", "add to this collection ID")
	ingestCmd.Flags().StringVar(&ingestSummaryStyle, "summary-style", "", "summary style: study-notes|bullets|technical|executive (default: config)")
	ingestCmd.Flags().StringVar(&ingestProfile, "profile", "", "override profile for all generation steps (e.g. oai-mini, opus)")
	ingestCmd.Flags().BoolVar(&ingestNoFlashcards, "no-flashcards", false, "skip flashcard generation")
	ingestCmd.Flags().BoolVar(&ingestDryRun, "dry-run", false, "extract only, do not write files or call LLM")
	rootCmd.AddCommand(ingestCmd)
}

var ingestCmd = &cobra.Command{
	Use:   "ingest <url|file|->",
	Short: "Ingest an article from a URL, file, or stdin",
	Long: `Ingest fetches and processes an article through the full pipeline:
  extract → summarize → flash → flashcards → index

Examples:
  arc ingest https://example.com/article
  arc ingest paper.pdf --no-flashcards
  arc ingest notes.txt --title "My Notes" --collection my-collection
  cat article.txt | arc ingest -`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		svc := svcFrom(cmd)

		input := args[0]
		req := service.IngestRequest{
			Title:            ingestTitle,
			Collection:       ingestCollection,
			SummaryStyle:     ingestSummaryStyle,
			SummaryProfile:   ingestProfile,
			FlashProfile:     ingestProfile,
			FlashcardProfile: ingestProfile,
			NoFlashcards:     ingestNoFlashcards,
			DryRun:           ingestDryRun,
		}

		if strings.HasPrefix(input, "http://") || strings.HasPrefix(input, "https://") {
			req.URL = input
		} else {
			req.File = input
		}

		if !isJSON(cmd) {
			req.Progress = func(msg string) {
				fmt.Fprintf(cmd.ErrOrStderr(), "  %s\n", msg)
			}
		}

		result, err := svc.Ingest(cmd.Context(), req)
		if err != nil {
			return fmt.Errorf("ingest: %w", err)
		}

		if result.DryRun {
			fmt.Fprintln(cmd.OutOrStdout(), "dry-run: extraction successful, no files written")
			return nil
		}

		if isJSON(cmd) {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		}

		fmt.Fprintln(cmd.OutOrStdout(), result.Slug)
		if result.Cost.TotalUSD > 0 {
			fmt.Fprintf(cmd.ErrOrStderr(), "cost: $%.4f\n", result.Cost.TotalUSD)
		}
		return nil
	},
}

package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jrniemiec/arc/service"
)

var (
	ingestTitle        string
	ingestCollection   string
	ingestSummaryStyle string
	ingestProfile      string
	ingestFlashcards   bool
	ingestNoFlashcards bool
	ingestNoEmbed      bool
	ingestDryRun       bool
	ingestForce        bool
	ingestFile         string
	ingestShowFlash    bool
	ingestShowSummary  bool
	ingestQuiet        bool
)

func init() {
	ingestCmd.Flags().StringVar(&ingestTitle, "title", "", "article title (default: auto-generated)")
	ingestCmd.Flags().StringVar(&ingestCollection, "collection", "", "add to this collection ID")
	ingestCmd.Flags().StringVar(&ingestSummaryStyle, "summary-style", "", "summary style: study-notes|bullets|technical|executive (default: config)")
	ingestCmd.Flags().StringVar(&ingestProfile, "profile", "", "override profile for all generation steps (e.g. oai-mini, opus)")
	ingestCmd.Flags().BoolVar(&ingestFlashcards, "flashcards", false, "generate flashcards (overrides config default)")
	ingestCmd.Flags().BoolVar(&ingestNoFlashcards, "no-flashcards", false, "skip flashcard generation (overrides config default)")
	ingestCmd.Flags().BoolVar(&ingestNoEmbed, "no-embed", false, "skip vector embedding generation")
	ingestCmd.Flags().BoolVar(&ingestDryRun, "dry-run", false, "extract only, do not write files or call LLM")
	ingestCmd.Flags().BoolVar(&ingestForce, "force", false, "ingest even if URL was already ingested")
	ingestCmd.Flags().StringVar(&ingestFile, "file", "", "batch mode: file with one URL/file per line (\"-\" for stdin)")
	ingestCmd.Flags().BoolVar(&ingestShowFlash, "show-flash", false, "print flash summary to stdout after ingest")
	ingestCmd.Flags().BoolVar(&ingestShowSummary, "show-summary", false, "print full summary to stdout after ingest")
	ingestCmd.Flags().BoolVarP(&ingestQuiet, "quiet", "q", false, "suppress progress output; print slug only")
	rootCmd.AddCommand(ingestCmd)
}

var ingestCmd = &cobra.Command{
	Use:   "ingest <url|file|->",
	Short: "Ingest an article from a URL, file, or stdin",
	Long: `Ingest fetches and processes an article through the full pipeline:
  extract → summarize → flash → flashcards → embed → index

Files written to ~/.arc/articles/<slug>/:
  body.txt                          extracted plain text
  source.url / source.pdf           original source reference
  source.html                       raw HTML (URL sources only)
  meta.json                         title, author, model, style, tags
  summary.<style>.<model>.txt       generated summary
  flash.<model>.txt                 generated flash summary
  flashcards.<style>.<model>.json   generated flashcards

Databases updated:
  SQLite  — article metadata and FTS5 full-text index (summary + flashcard questions)
  Vector  — embedding of the summary text (skip with --no-embed)

Multiple variants can coexist (different models or styles). The preferred variant
for reading/search is determined by preferred_models and preferred_styles in config.

Cookie jars from config (cookie_jars) are applied automatically for URL sources.
URL deduplication: if the URL was already ingested, arc errors with the existing
slug. Use --force to ingest again (e.g. to regenerate with a different model).

Use --dry-run to extract and report stats without writing any files or calling LLMs.

Examples:
  arc ingest https://example.com/article
  arc ingest paper.pdf --no-flashcards
  arc ingest notes.txt --title "My Notes" --collection my-collection
  arc ingest https://example.com/article --dry-run
  arc ingest https://example.com/article --show-flash
  arc ingest https://example.com/article --show-summary
  cat article.txt | arc ingest -

Batch mode (--file):
  arc ingest --file urls.txt
  arc ingest --file urls.txt --no-flashcards --dry-run
  cat urls.txt | arc ingest --file -

  The file format is one URL or file path per line. Blank lines and lines
  starting with '#' are ignored. Duplicates are skipped (not errors).
  Errors are logged per-item and do not abort the batch.
  Output: one slug per line to stdout; progress and summary to stderr.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 && ingestFile == "" {
			return fmt.Errorf("provide a URL/file argument or use --file for batch mode")
		}
		if len(args) > 0 && ingestFile != "" {
			return fmt.Errorf("cannot specify a URL and --file together")
		}

		svc := svcFrom(cmd)

		// ── Batch mode ────────────────────────────────────────────────────────
		if ingestFile != "" {
			req := service.BatchIngestRequest{
				File:             ingestFile,
				Collection:       ingestCollection,
				SummaryStyle:     ingestSummaryStyle,
				SummaryProfile:   ingestProfile,
				FlashProfile:     ingestProfile,
				FlashcardProfile: ingestProfile,
				Flashcards:       ingestFlashcards,
				NoFlashcards:     ingestNoFlashcards,
				NoEmbed:          ingestNoEmbed,
				DryRun:           ingestDryRun,
				Force:            ingestForce,
			}
			if !isJSON(cmd) && !ingestQuiet {
				req.Progress = func(msg string) {
					fmt.Fprintf(cmd.ErrOrStderr(), "  %s\n", msg)
				}
			}

			result, err := svc.BatchIngest(cmd.Context(), req)
			if err != nil {
				return fmt.Errorf("batch ingest: %w", err)
			}

			if isJSON(cmd) {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
			}

			for _, slug := range result.Slugs {
				fmt.Fprintln(cmd.OutOrStdout(), slug)
			}
			if !ingestQuiet {
				fmt.Fprintf(cmd.ErrOrStderr(), "ingested %d", result.Ingested)
				if result.Teasers > 0 {
					fmt.Fprintf(cmd.ErrOrStderr(), " (%d teasers)", result.Teasers)
				}
				if result.Skipped > 0 {
					fmt.Fprintf(cmd.ErrOrStderr(), ", skipped %d (duplicates)", result.Skipped)
				}
				if result.Errors > 0 {
					fmt.Fprintf(cmd.ErrOrStderr(), ", errors %d", result.Errors)
				}
				if result.CostUSD > 0 {
					fmt.Fprintf(cmd.ErrOrStderr(), "  ($%.4f)", result.CostUSD)
				}
				fmt.Fprintln(cmd.ErrOrStderr())
			}
			return nil
		}

		// ── Single mode ───────────────────────────────────────────────────────
		input := args[0]
		req := service.IngestRequest{
			Title:            ingestTitle,
			Collection:       ingestCollection,
			SummaryStyle:     ingestSummaryStyle,
			SummaryProfile:   ingestProfile,
			FlashProfile:     ingestProfile,
			FlashcardProfile: ingestProfile,
			Flashcards:       ingestFlashcards,
			NoFlashcards:     ingestNoFlashcards,
			NoEmbed:          ingestNoEmbed,
			DryRun:           ingestDryRun,
			Force:            ingestForce,
		}

		if strings.HasPrefix(input, "http://") || strings.HasPrefix(input, "https://") {
			req.URL = input
		} else {
			req.File = input
		}

		if !isJSON(cmd) && !ingestQuiet {
			req.Progress = func(msg string) {
				fmt.Fprintf(cmd.ErrOrStderr(), "  %s\n", msg)
			}
		}

		result, err := svc.Ingest(cmd.Context(), req)
		if err != nil {
			return fmt.Errorf("ingest: %w", err)
		}

		if result.DryRun {
			fmt.Fprintln(cmd.OutOrStdout(), "dry-run: no files written")
			return nil
		}

		if isJSON(cmd) {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		}

		if result.Teaser && !ingestQuiet {
			fmt.Fprintf(cmd.OutOrStdout(), "%s  [teaser]\n", result.Slug)
		} else {
			fmt.Fprintln(cmd.OutOrStdout(), result.Slug)
		}
		if result.Cost.TotalUSD > 0 && !ingestQuiet {
			fmt.Fprintf(cmd.ErrOrStderr(), "cost: $%.4f\n", result.Cost.TotalUSD)
		}

		if !result.Teaser && !ingestQuiet {
			tty := isTTY(os.Stdout)
			if ingestShowFlash {
				if flash, err := svc.Read(cmd.Context(), service.ReadRequest{
					ID:   result.Slug,
					Part: service.PartFlash,
				}); err == nil && flash != "" {
					fmt.Fprintln(cmd.OutOrStdout())
					fmt.Fprintln(cmd.OutOrStdout(), renderMarkdown(flash, "", tty))
				}
			}
			if ingestShowSummary {
				if summary, err := svc.Read(cmd.Context(), service.ReadRequest{
					ID:   result.Slug,
					Part: service.PartSummary,
				}); err == nil && summary != "" {
					fmt.Fprintln(cmd.OutOrStdout())
					fmt.Fprintln(cmd.OutOrStdout(), renderMarkdown(summary, "", tty))
				}
			}
		}

		return nil
	},
}

package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jrniemiec/arc/config"
	"github.com/jrniemiec/arc/service"
)

var (
	flashcardsStyle    string
	flashcardsProfile  string
	flashcardsModel    string
	flashcardsCount    int
	flashcardsWrite    bool
	flashcardsFromBody bool
	flashcardsDelete   bool
	flashcardsDryRun   bool
)

func init() {
	flashcardsCmd.Flags().StringVar(&flashcardsStyle, "style", "", "flashcard style: socratic|cloze (default: config)")
	flashcardsCmd.Flags().StringVar(&flashcardsProfile, "profile", "", "LLM profile to use (default: config)")
	flashcardsCmd.Flags().IntVar(&flashcardsCount, "count", 0, "target number of cards (default: scaled to article length)")
	flashcardsCmd.Flags().BoolVar(&flashcardsDelete, "delete", false, "delete flashcards instead of generating them")
	flashcardsCmd.Flags().StringVar(&flashcardsModel, "model", "", "with --delete: only remove this model's variant")
	flashcardsCmd.Flags().BoolVar(&flashcardsDryRun, "dry-run", false, "with --delete: show what would be removed")
	flashcardsCmd.Flags().BoolVar(&flashcardsWrite, "write", false, "write flashcard file into the article directory (slug mode only)")
	flashcardsCmd.Flags().BoolVar(&flashcardsFromBody, "from-body", false, "use article body instead of summary as input (slug mode only)")
	rootCmd.AddCommand(flashcardsCmd)
}

var flashcardsCmd = &cobra.Command{
	Use:   "flashcards [slug]",
	Short: "Generate flashcards from an article or piped text",
	Long: `Generate flashcards as a JSON array from an article summary or piped text.

Reads the preferred summary (or body with --from-body) from disk and calls
the configured flashcard LLM profile. Does not modify SQLite or the vector index.

With --write, saves the result as flashcards.<style>.<model>.json in the article
directory. Existing flashcard files for the same style+model are overwritten.

Styles:
  socratic  question-and-answer pairs that test understanding
  cloze     fill-in-the-blank sentences

Card count:
  By default the number of cards scales with the article's body length, using
  the ingest.flashcard_counts buckets in config. --count overrides it.

Each card: {"type":"concept|fact|insight","front":"...","back":"...","tags":["..."]}

Input (slug mode):
  default     reads preferred summary.<style>.<model>.txt
  --from-body reads body.txt instead

Input (stdin mode):
  pipe text directly; --write is not available

Deleting:
  --delete removes flashcard files. With no --style or --model it removes every
  variant for the article; either flag narrows it to one. --dry-run previews.
  Deleting the last variant also clears flashcard_model/flashcard_style in
  meta.json and drops the card questions from the search index.

Output:
  stdout  JSON array (syntax-highlighted on terminal)
  stderr  progress, model header, cost

Examples:
  arc flashcards 20260522-my-article
  arc flashcards --style cloze 20260522-my-article
  arc flashcards --write 20260522-my-article
  arc flashcards --count 20 --write 20260522-my-article
  arc summarize 20260522-my-article | arc flashcards
  arc extract https://example.com/article | arc summarize | arc flashcards
  arc flashcards --delete 20260522-my-article
  arc flashcards --delete --dry-run 20260522-my-article
  arc flashcards --delete --model gpt-4o-mini 20260522-my-article`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if flashcardsDelete {
			return runFlashcardsDelete(cmd, args)
		}

		req := service.FlashcardsRequest{
			Style:    flashcardsStyle,
			Profile:  flashcardsProfile,
			Count:    flashcardsCount,
			Write:    flashcardsWrite,
			FromBody: flashcardsFromBody,
		}

		if len(args) == 0 || args[0] == "-" {
			stat, _ := os.Stdin.Stat()
			if len(args) == 0 && (stat.Mode()&os.ModeCharDevice) != 0 {
				return fmt.Errorf("provide a slug or pipe text to stdin")
			}
			data, err := io.ReadAll(os.Stdin)
			if err != nil {
				return fmt.Errorf("read stdin: %w", err)
			}
			req.Text = strings.TrimSpace(string(data))
			if req.Text == "" {
				return fmt.Errorf("no text on stdin")
			}
		} else {
			slug, err := resolveSlug(cmd, args[0])
			if err != nil {
				return err
			}
			req.Slug = slug
		}

		cfg := cmd.Context().Value(keyConfig).(config.Config)
		tiers := make(map[string]string)
		for _, p := range cfg.Profiles {
			tiers[p.Model] = p.Info.CostTier
		}
		tty := isTTY(os.Stdout)
		errTTY := isTTY(os.Stderr)

		effectiveProfile := flashcardsProfile
		if effectiveProfile == "" {
			effectiveProfile = cfg.Ingest.FlashcardProfile
		}
		progressTier := cfg.Profiles[effectiveProfile].Info.CostTier

		if !isJSON(cmd) {
			req.Progress = func(msg string) {
				fmt.Fprintf(cmd.ErrOrStderr(), "  %s\n", colorize(msg, progressTier, errTTY))
			}
		}

		svc := svcFrom(cmd)
		result, err := svc.Flashcards(cmd.Context(), req)
		if err != nil {
			return fmt.Errorf("flashcards: %w", err)
		}

		label := "Flashcards"
		if result.Count > 0 {
			label += fmt.Sprintf(" · %d", result.Count)
		}
		if result.Style != "" {
			label += " · " + result.Style
		}
		if result.Model != "" {
			label += " · " + result.Model
		}
		if tty {
			fmt.Fprintln(cmd.OutOrStdout(), header(label, result.Model, tiers, tty))
			fmt.Fprintln(cmd.OutOrStdout(), renderJSON(result.JSON, tiers[result.Model], tty))
		} else {
			fmt.Fprintln(cmd.ErrOrStderr(), header(label, result.Model, tiers, errTTY))
			fmt.Fprintln(cmd.OutOrStdout(), string(result.JSON))
		}

		if result.CostUSD > 0 {
			fmt.Fprintf(cmd.ErrOrStderr(), "cost: $%.4f\n", result.CostUSD)
		}
		if result.Written {
			fmt.Fprintf(cmd.ErrOrStderr(), "written: %s\n", result.WritePath)
		}

		return nil
	},
}

// runFlashcardsDelete handles `arc flashcards --delete <slug>`.
//
// There is no interactive prompt: `arc delete` — the existing precedent for a
// destructive CLI operation — does not prompt either, it offers --dry-run.
func runFlashcardsDelete(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("--delete requires a slug")
	}
	slug, err := resolveSlug(cmd, args[0])
	if err != nil {
		return err
	}

	svc := svcFrom(cmd)
	result, err := svc.DeleteFlashcards(cmd.Context(), service.DeleteFlashcardsRequest{
		Slug:   slug,
		Style:  flashcardsStyle,
		Model:  flashcardsModel,
		DryRun: flashcardsDryRun,
	})
	if err != nil {
		return fmt.Errorf("delete flashcards: %w", err)
	}

	w := cmd.OutOrStdout()
	verb := "deleted"
	if flashcardsDryRun {
		verb = "would delete"
	}
	for _, p := range result.Deleted {
		fmt.Fprintf(w, "%s %s\n", verb, filepath.Base(p))
	}
	fmt.Fprintf(w, "%s %d file(s), %d card(s)", verb, len(result.Deleted), result.Cards)
	if result.Remaining > 0 {
		fmt.Fprintf(w, "; %d variant(s) left", result.Remaining)
	}
	fmt.Fprintln(w)
	return nil
}

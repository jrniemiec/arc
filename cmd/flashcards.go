package cmd

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jrniemiec/arc/config"
	"github.com/jrniemiec/arc/service"
)

var (
	flashcardsStyle    string
	flashcardsProfile  string
	flashcardsWrite    bool
	flashcardsFromBody bool
)

func init() {
	flashcardsCmd.Flags().StringVar(&flashcardsStyle, "style", "", "flashcard style: socratic|cloze (default: config)")
	flashcardsCmd.Flags().StringVar(&flashcardsProfile, "profile", "", "LLM profile to use (default: config)")
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

Each card: {"type":"concept|fact|insight","front":"...","back":"...","tags":["..."]}

Input (slug mode):
  default     reads preferred summary.<style>.<model>.txt
  --from-body reads body.txt instead

Input (stdin mode):
  pipe text directly; --write is not available

Output:
  stdout  JSON array (syntax-highlighted on terminal)
  stderr  progress, model header, cost

Examples:
  arc flashcards 20260522-my-article
  arc flashcards --style cloze 20260522-my-article
  arc flashcards --write 20260522-my-article
  arc summarize 20260522-my-article | arc flashcards
  arc extract https://example.com/article | arc summarize | arc flashcards`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		req := service.FlashcardsRequest{
			Style:    flashcardsStyle,
			Profile:  flashcardsProfile,
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

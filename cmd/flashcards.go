package cmd

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

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

Input is the article summary by default (slug mode). Use --from-body to use
the raw body instead. If no slug is given and stdin is a pipe, reads from
stdin automatically.

Each flashcard: {"type":"concept|fact|insight","front":"...","back":"...","tags":["..."]}

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

		if !isJSON(cmd) {
			req.Progress = func(msg string) {
				fmt.Fprintf(cmd.ErrOrStderr(), "  %s\n", msg)
			}
		}

		svc := svcFrom(cmd)
		result, err := svc.Flashcards(cmd.Context(), req)
		if err != nil {
			return fmt.Errorf("flashcards: %w", err)
		}

		fmt.Fprintln(cmd.OutOrStdout(), string(result.JSON))

		if result.CostUSD > 0 {
			fmt.Fprintf(cmd.ErrOrStderr(), "cost: $%.4f\n", result.CostUSD)
		}
		if result.Written {
			fmt.Fprintf(cmd.ErrOrStderr(), "written: %s\n", result.WritePath)
		}

		return nil
	},
}

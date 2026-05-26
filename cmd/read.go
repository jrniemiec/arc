package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/jrniemiec/arc/service"
)

var (
	readSummary    bool
	readFlash      bool
	readFlashcards bool
	readModel      string
	readStyle      string
)

func init() {
	readCmd.Flags().BoolVar(&readSummary, "summary", false, "read summary instead of body")
	readCmd.Flags().BoolVar(&readFlash, "flash", false, "read flash summary")
	readCmd.Flags().BoolVar(&readFlashcards, "flashcards", false, "read flashcards")
	readCmd.Flags().StringVar(&readModel, "model", "", "prefer this model variant")
	readCmd.Flags().StringVar(&readStyle, "style", "", "prefer this style variant")
	rootCmd.AddCommand(readCmd)
}

var readCmd = &cobra.Command{
	Use:   "read <slug>",
	Short: "Read an article or one of its generated outputs",
	Long: `Read the body, summary, flash, or flashcards of an article.

Reads directly from the filesystem — does not call any LLM or modify any database.

Default (no flag): body.txt — the raw extracted text.
--summary:         preferred summary.<style>.<model>.txt
--flash:           preferred flash.<model>.txt
--flashcards:      preferred flashcards.<style>.<model>.json

Variant selection uses preferred_models and preferred_styles from config,
trying each combination in order and returning the first file that exists.
Use --model and --style to prepend an override to those preference lists
for this read only.

The slug argument accepts a full article ID or a partial slug/title — arc
will error if the partial match is ambiguous.

Examples:
  arc read 20260522-my-article
  arc read --summary 20260522-my-article
  arc read --summary --style bullets 20260522-my-article
  arc read --flash my-article
  arc read --flashcards my-article`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		slug, err := resolveSlug(cmd, args[0])
		if err != nil {
			return err
		}

		svc := svcFrom(cmd)

		part := service.PartBody
		switch {
		case readSummary:
			part = service.PartSummary
		case readFlash:
			part = service.PartFlash
		case readFlashcards:
			part = service.PartFlashcards
		}

		text, err := svc.Read(cmd.Context(), service.ReadRequest{
			ID:    slug,
			Part:  part,
			Model: readModel,
			Style: readStyle,
		})
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}

		fmt.Println(text)
		return nil
	},
}

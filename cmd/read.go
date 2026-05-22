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

Variant selection follows preferred_models and preferred_styles from config.
Use --model and --style to override for a specific read.

Examples:
  arc read 20260522-my-article
  arc read --summary 20260522-my-article
  arc read --summary --style bullets 20260522-my-article
  arc read --flash 20260522-my-article
  arc read --flashcards 20260522-my-article`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
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
			ID:    args[0],
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

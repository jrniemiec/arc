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
	flashProfile  string
	flashWrite    bool
	flashFromBody bool
)

func init() {
	flashCmd.Flags().StringVar(&flashProfile, "profile", "", "LLM profile to use (default: config)")
	flashCmd.Flags().BoolVar(&flashWrite, "write", false, "write flash file into the article directory (slug mode only)")
	flashCmd.Flags().BoolVar(&flashFromBody, "from-body", false, "use article body instead of summary as input (slug mode only)")
	rootCmd.AddCommand(flashCmd)
}

var flashCmd = &cobra.Command{
	Use:   "flash [slug]",
	Short: "Generate a flash summary for audio playback",
	Long: `Generate a 3–5 sentence flash summary optimised for TTS playback.

Input is the article summary by default (slug mode). Use --from-body to use
the raw body instead. If no slug is given and stdin is a pipe, reads from
stdin automatically.

Examples:
  arc flash 20260522-my-article
  arc flash --write 20260522-my-article
  arc flash --from-body 20260522-my-article
  arc summarize 20260522-my-article | arc flash
  arc extract https://example.com/article | arc summarize | arc flash`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		req := service.FlashRequest{
			Profile:  flashProfile,
			Write:    flashWrite,
			FromBody: flashFromBody,
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
		result, err := svc.Flash(cmd.Context(), req)
		if err != nil {
			return fmt.Errorf("flash: %w", err)
		}

		fmt.Fprintln(cmd.OutOrStdout(), result.Text)

		if result.CostUSD > 0 {
			fmt.Fprintf(cmd.ErrOrStderr(), "cost: $%.4f\n", result.CostUSD)
		}
		if result.Written {
			fmt.Fprintf(cmd.ErrOrStderr(), "written: %s\n", result.WritePath)
		}

		return nil
	},
}

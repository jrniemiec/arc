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
	summarizeStyle   string
	summarizeProfile string
	summarizeWrite   bool
)

func init() {
	summarizeCmd.Flags().StringVar(&summarizeStyle, "style", "", "summary style: study-notes|bullets|technical|executive (default: config)")
	summarizeCmd.Flags().StringVar(&summarizeProfile, "profile", "", "LLM profile to use (default: config)")
	summarizeCmd.Flags().BoolVar(&summarizeWrite, "write", false, "write summary as a variant file in the article directory (slug mode only)")
	rootCmd.AddCommand(summarizeCmd)
}

var summarizeCmd = &cobra.Command{
	Use:   "summarize [slug]",
	Short: "Summarize an article or piped text",
	Long: `Summarize an existing article (by slug) or text from stdin.

If no slug is given and stdin is a pipe, reads from stdin automatically.

Examples:
  arc summarize 20260522-claude-s-character
  arc summarize --style bullets 20260522-claude-s-character
  arc summarize --style bullets --write 20260522-claude-s-character
  arc extract https://example.com/article | arc summarize
  cat article.txt | arc summarize --style technical`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		req := service.SummarizeRequest{
			Style:   summarizeStyle,
			Profile: summarizeProfile,
			Write:   summarizeWrite,
		}

		if len(args) == 0 || args[0] == "-" {
			// stdin mode: explicit "-" or no arg when piped
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
			req.Slug = args[0]
		}

		if !isJSON(cmd) {
			req.Progress = func(msg string) {
				fmt.Fprintf(cmd.ErrOrStderr(), "  %s\n", msg)
			}
		}

		svc := svcFrom(cmd)
		result, err := svc.Summarize(cmd.Context(), req)
		if err != nil {
			return fmt.Errorf("summarize: %w", err)
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

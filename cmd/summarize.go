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

Reads the article body from disk and calls the configured summary LLM profile.
Does not modify SQLite or the vector index — use arc reindex to sync after --write.

Modes:
  slug       reads body.txt from the article directory
  stdin      pipe text directly; --write is not available in this mode

With --write, saves the result as summary.<style>.<model>.txt alongside any
existing summary variants in the article directory. Existing variants with the
same style+model are overwritten; others are untouched.

Output:
  stdout  summary text (plain when piped, markdown-rendered on terminal)
  stderr  progress, model header, cost

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

		// Resolve the effective profile tier for progress message coloring.
		effectiveProfile := summarizeProfile
		if effectiveProfile == "" {
			effectiveProfile = cfg.Ingest.SummaryProfile
		}
		progressTier := cfg.Profiles[effectiveProfile].Info.CostTier

		if !isJSON(cmd) {
			req.Progress = func(msg string) {
				fmt.Fprintf(cmd.ErrOrStderr(), "  %s\n", colorize(msg, progressTier, errTTY))
			}
		}

		svc := svcFrom(cmd)
		result, err := svc.Summarize(cmd.Context(), req)
		if err != nil {
			return fmt.Errorf("summarize: %w", err)
		}

		label := "Summary"
		if result.Style != "" {
			label += " · " + result.Style
		}
		if result.Model != "" {
			label += " · " + result.Model
		}
		if tty {
			// Terminal: display header + formatted body on stdout.
			fmt.Fprintln(cmd.OutOrStdout(), header(label, result.Model, tiers, tty))
			fmt.Fprintln(cmd.OutOrStdout(), renderMarkdown(result.Text, tiers[result.Model], tty))
		} else {
			// Piped: display goes to stderr (visible on terminal), plain text to stdout for next command.
			fmt.Fprintln(cmd.ErrOrStderr(), header(label, result.Model, tiers, errTTY))
			fmt.Fprintln(cmd.OutOrStdout(), result.Text)
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

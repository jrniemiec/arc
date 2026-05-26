package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jrniemiec/arc/config"
	"github.com/jrniemiec/arc/ingest/extractor"
)

func init() {
	// Override PersistentPreRunE so extract loads config but does not open the library.
	extractCmd.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		ctx := context.WithValue(cmd.Context(), keyConfig, cfg)
		cmd.SetContext(ctx)
		return nil
	}
	rootCmd.AddCommand(extractCmd)
}

var extractCmd = &cobra.Command{
	Use:   "extract <url|file|->",
	Short: "Extract article text from a URL, file, or stdin",
	Long: `Extract fetches and extracts the main article text, writing it to stdout.

Does not write anything to disk or touch any database. No API keys required.
Useful for previewing what arc would ingest, or for piping into arc summarize.

Sources:
  url   — HTTP fetch with readability extraction; retries via Jina on bot-check
  pdf   — pdftotext extraction
  file  — plain text passthrough; html files are stripped to text
  -     — read from stdin

Cookie jars from config (cookie_jars) are applied automatically for matching domains.
Extraction stats (download speed, size, words, tokens) are printed to stderr.

Examples:
  arc extract https://example.com/article
  arc extract paper.pdf
  arc extract notes.txt
  cat raw.html | arc extract -
  arc extract https://example.com/article | arc summarize`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		input := args[0]

		var result extractor.Result
		var err error

		cfg := cmd.Context().Value(keyConfig).(config.Config)

		switch {
		case strings.HasPrefix(input, "http://") || strings.HasPrefix(input, "https://"):
			fmt.Fprintf(cmd.ErrOrStderr(), "fetching %s...\n", input)
			result, err = extractor.FromURLWithCookies(ctx, input, cfg.CookieJars)
		case strings.HasSuffix(strings.ToLower(input), ".pdf"):
			fmt.Fprintf(cmd.ErrOrStderr(), "extracting PDF...\n")
			result, err = extractor.FromPDF(ctx, input)
		default:
			result, err = extractor.FromFile(input)
		}

		if err != nil {
			return fmt.Errorf("extract: %w", err)
		}

		if strings.TrimSpace(result.Text) == "" {
			return fmt.Errorf("extraction produced no text")
		}

		fmt.Fprintf(cmd.ErrOrStderr(), "%s\n", result.Stats())

		if isJSON(cmd) {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
				Text     string `json:"text"`
				Title    string `json:"title,omitempty"`
				Author   string `json:"author,omitempty"`
				Language string `json:"language,omitempty"`
			}{
				Text:     result.Text,
				Title:    result.Title,
				Author:   result.Author,
				Language: result.Language,
			})
		}

		fmt.Fprintln(cmd.OutOrStdout(), result.Text)
		return nil
	},
}

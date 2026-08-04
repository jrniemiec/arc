package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jrniemiec/arc/config"
)

func init() {
	rootCmd.AddCommand(configCmd)
}

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Print the resolved configuration",
	Long: `Print the fully resolved configuration — defaults merged with ~/.arc/config.json.

This is what arc actually uses at runtime, regardless of what is explicitly
written in the config file.

Examples:
  arc config
  arc config --json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg := cmd.Context().Value(keyConfig).(config.Config)

		if isJSON(cmd) {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(cfg)
		}

		// Human-readable output
		w := cmd.OutOrStdout()

		fmt.Fprintf(w, "Paths:\n")
		fmt.Fprintf(w, "  data_root:     %s\n", cfg.DataRoot)
		fmt.Fprintf(w, "  articles_root: %s\n", cfg.ArticlesRoot)
		fmt.Fprintf(w, "  db_path:       %s\n", cfg.DBPath)
		fmt.Fprintf(w, "  vector_path:   %s\n", cfg.VectorPath)
		fmt.Fprintf(w, "  events_path:   %s\n", cfg.EventsPath)
		fmt.Fprintln(w)

		fmt.Fprintf(w, "Ingest:\n")
		fmt.Fprintf(w, "  summary_profile:   %s\n", cfg.Ingest.SummaryProfile)
		fmt.Fprintf(w, "  flash_profile:     %s\n", cfg.Ingest.FlashProfile)
		fmt.Fprintf(w, "  flashcard_profile: %s\n", cfg.Ingest.FlashcardProfile)
		fmt.Fprintf(w, "  summary_style:     %s\n", cfg.Ingest.SummaryStyle)
		fmt.Fprintf(w, "  flashcard_style:   %s\n", cfg.Ingest.FlashcardStyle)
		fmt.Fprintf(w, "  chunk_tokens:      %d\n", cfg.Ingest.ChunkTokens)
		fmt.Fprintf(w, "  summary_max_tokens:%d\n", cfg.Ingest.SummaryMaxTokens)
		fmt.Fprintf(w, "  flash_max_tokens:  %d\n", cfg.Ingest.FlashMaxTokens)
		fmt.Fprintf(w, "  flashcard_max_tokens: %d\n", cfg.Ingest.FlashcardMaxTokens)
		fmt.Fprintf(w, "  flashcard_counts:  %s\n", formatCardCounts(cfg))
		fmt.Fprintln(w)

		fmt.Fprintf(w, "Summary styles:\n")
		for name, sc := range cfg.Ingest.SummaryStyles {
			fmt.Fprintf(w, "  %-12s  %s\n", name, truncate(sc.SystemPrompt, 80))
		}
		fmt.Fprintln(w)

		fmt.Fprintf(w, "Flashcard styles:\n")
		for name, sc := range cfg.Ingest.FlashcardStyles {
			fmt.Fprintf(w, "  %-12s  %s\n", name, truncate(sc.SystemPrompt, 80))
		}
		fmt.Fprintln(w)

		fmt.Fprintf(w, "Profiles: %d defined (run `arc profiles` for details)\n", len(cfg.Profiles))
		fmt.Fprintln(w)

		fmt.Fprintf(w, "Preferred models: %v\n", cfg.PreferredModels)
		fmt.Fprintf(w, "Preferred styles: %v\n", cfg.PreferredStyles)

		return nil
	},
}

// formatCardCounts renders the flashcard count buckets as "<500:5 <1500:8 …".
// A max_words of 0 is the catch-all and prints as "rest".
func formatCardCounts(cfg config.Config) string {
	rules := cfg.Ingest.FlashcardCounts
	if len(rules) == 0 {
		return "(built-in defaults)"
	}
	parts := make([]string, 0, len(rules))
	for _, r := range rules {
		if r.MaxWords == 0 {
			parts = append(parts, fmt.Sprintf("rest:%d", r.Cards))
			continue
		}
		parts = append(parts, fmt.Sprintf("<%d:%d", r.MaxWords, r.Cards))
	}
	return strings.Join(parts, "  ")
}

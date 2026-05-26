package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/spf13/cobra"

	"github.com/jrniemiec/arc/config"
)

func init() {
	rootCmd.AddCommand(statsCmd)
}

var statsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Show knowledge base statistics",
	Long: `Show a summary of the knowledge base state.

Data is read from SQLite (article counts, models, styles) and events.jsonl
(cost totals). Does not read the filesystem or call any LLM.

Output:
  Articles      total count with unread, unplayed, and embed coverage
  Collections   number of defined collections
  Tags          number of unique tags
  By model      article count per summary model (colored by cost tier)
  By style      article count per summary style
  Cost          this month and all-time total, broken down by model

Examples:
  arc stats
  arc stats --json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		svc := svcFrom(cmd)

		stats, err := svc.Stats(cmd.Context())
		if err != nil {
			return fmt.Errorf("stats: %w", err)
		}

		if isJSON(cmd) {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(stats)
		}

		w := cmd.OutOrStdout()
		tty := isTTY(os.Stdout)

		cfg := cmd.Context().Value(keyConfig).(config.Config)
		tierByModel := make(map[string]string)
		for _, p := range cfg.Profiles {
			tierByModel[p.Model] = p.Info.CostTier
		}

		embedPct := 0
		if stats.TotalArticles > 0 {
			embedPct = stats.EmbedCoverage * 100 / stats.TotalArticles
		}
		fmt.Fprintf(w, "Articles:    %d  (unread: %d, unplayed: %d, embedded: %d/%d %d%%)\n",
			stats.TotalArticles, stats.Unread, stats.Unplayed,
			stats.EmbedCoverage, stats.TotalArticles, embedPct)
		fmt.Fprintf(w, "Collections: %d\n", stats.TotalCollections)
		fmt.Fprintf(w, "Tags:        %d\n", stats.TotalTags)

		// Articles by summary model
		if len(stats.ArticlesByModel) > 0 {
			fmt.Fprintln(w)
			fmt.Fprintf(w, "Articles by model:\n")
			models := sortedKeys(stats.ArticlesByModel)
			for _, m := range models {
				label := colorize(fmt.Sprintf("%-40s", m), tierByModel[m], tty)
				fmt.Fprintf(w, "  %s  %d\n", label, stats.ArticlesByModel[m])
			}
		}

		// Articles by summary style
		if len(stats.ArticlesByStyle) > 0 {
			fmt.Fprintln(w)
			fmt.Fprintf(w, "Articles by style:\n")
			styles := sortedKeys(stats.ArticlesByStyle)
			for _, s := range styles {
				fmt.Fprintf(w, "  %-20s  %d\n", s, stats.ArticlesByStyle[s])
			}
		}

		// Cost breakdown
		if stats.CostTotal > 0 || stats.CostThisMonth > 0 {
			fmt.Fprintln(w)
			month := colorize(fmt.Sprintf("$%.4f", stats.CostThisMonth), costColor(stats.CostThisMonth), tty)
			total := colorize(fmt.Sprintf("$%.4f", stats.CostTotal), costColor(stats.CostTotal), tty)
			fmt.Fprintf(w, "Cost:        %s this month  (%s total)\n", month, total)

			if len(stats.CostByModel) > 0 {
				fmt.Fprintf(w, "Cost by model:\n")
				type kv struct {
					model string
					usd   float64
				}
				var pairs []kv
				for m, u := range stats.CostByModel {
					pairs = append(pairs, kv{m, u})
				}
				sort.Slice(pairs, func(i, j int) bool { return pairs[i].usd > pairs[j].usd })
				for _, p := range pairs {
					label := colorize(fmt.Sprintf("%-40s", p.model), tierByModel[p.model], tty)
					fmt.Fprintf(w, "  %s  $%.4f\n", label, p.usd)
				}
			}
		}

		return nil
	},
}

// sortedKeys returns map keys sorted alphabetically.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

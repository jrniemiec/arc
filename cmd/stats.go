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
  Articles      total count with embed coverage
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
		fmt.Fprintf(w, "Articles:    %d  (embedded: %d/%d %d%%)\n",
			stats.TotalArticles, stats.EmbedCoverage, stats.TotalArticles, embedPct)
		fmt.Fprintf(w, "Collections: %d\n", stats.TotalCollections)
		fmt.Fprintf(w, "Tags:        %d\n", stats.TotalTags)

		// Articles by collection
		if len(stats.ArticlesByCollection) > 0 {
			fmt.Fprintln(w)
			fmt.Fprintf(w, "Articles by collection:\n")
			type colRow struct {
				name  string
				count int
				embed int
			}
			var rows []colRow
			var uncollected *colRow
			for name, count := range stats.ArticlesByCollection {
				row := colRow{name, count, stats.EmbedByCollection[name]}
				if name == "(uncollected)" {
					uncollected = &colRow{name, count, stats.EmbedByCollection[name]}
				} else {
					rows = append(rows, row)
				}
			}
			sort.Slice(rows, func(i, j int) bool { return rows[i].count > rows[j].count })
			if uncollected != nil {
				rows = append(rows, *uncollected)
			}
			for _, r := range rows {
				pct := 0
				if r.count > 0 {
					pct = r.embed * 100 / r.count
				}
				fmt.Fprintf(w, "  %-35s  %3d  (embedded: %d/%d %d%%)\n",
					r.name, r.count, r.embed, r.count, pct)
			}
		}

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
			fmt.Fprintf(w, "  today:     $%.4f\n", stats.CostToday)
			fmt.Fprintf(w, "  7d:        $%.4f\n", stats.CostThisWeek)

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

			if len(stats.CostByType) > 0 {
				fmt.Fprintf(w, "Cost by operation:\n")
				type kv struct {
					op  string
					usd float64
				}
				var pairs []kv
				for op, u := range stats.CostByType {
					pairs = append(pairs, kv{op, u})
				}
				sort.Slice(pairs, func(i, j int) bool { return pairs[i].usd > pairs[j].usd })
				for _, p := range pairs {
					fmt.Fprintf(w, "  %-40s  $%.4f\n", p.op, p.usd)
				}
			}

			if stats.AvgCostPerIngest > 0 || stats.AvgCostPerChatTurn > 0 || stats.AvgCostPerAskX > 0 {
				fmt.Fprintf(w, "Efficiency:\n")
				if stats.AvgCostPerIngest > 0 {
					fmt.Fprintf(w, "  avg per ingest:      $%.4f\n", stats.AvgCostPerIngest)
				}
				if stats.AvgCostPerChatTurn > 0 {
					fmt.Fprintf(w, "  avg per chat turn:   $%.4f\n", stats.AvgCostPerChatTurn)
				}
				if stats.AvgCostPerAskX > 0 {
					fmt.Fprintf(w, "  avg per askx:        $%.4f\n", stats.AvgCostPerAskX)
				}
			}
		}

		// Token usage
		if stats.TotalInputTokens > 0 || stats.TotalOutputTokens > 0 {
			fmt.Fprintln(w)
			fmt.Fprintf(w, "Tokens:      %s in  %s out\n",
				fmtTokens(stats.TotalInputTokens), fmtTokens(stats.TotalOutputTokens))
			if len(stats.TokensByModel) > 0 {
				fmt.Fprintf(w, "Tokens by model:\n")
				type kv struct {
					model  string
					in, out int
				}
				var pairs []kv
				for m, t := range stats.TokensByModel {
					pairs = append(pairs, kv{m, t[0], t[1]})
				}
				sort.Slice(pairs, func(i, j int) bool { return pairs[i].in+pairs[i].out > pairs[j].in+pairs[j].out })
				for _, p := range pairs {
					label := colorize(fmt.Sprintf("%-40s", p.model), tierByModel[p.model], tty)
					fmt.Fprintf(w, "  %s  %s in  %s out\n", label, fmtTokens(p.in), fmtTokens(p.out))
				}
			}
		}

		// Request counts
		if stats.TotalRequests > 0 {
			fmt.Fprintln(w)
			fmt.Fprintf(w, "Requests:    %d\n", stats.TotalRequests)
			if len(stats.RequestsByType) > 0 {
				fmt.Fprintf(w, "Requests by operation:\n")
				type kv struct {
					op    string
					count int
				}
				var pairs []kv
				for op, c := range stats.RequestsByType {
					pairs = append(pairs, kv{op, c})
				}
				sort.Slice(pairs, func(i, j int) bool { return pairs[i].count > pairs[j].count })
				for _, p := range pairs {
					fmt.Fprintf(w, "  %-40s  %d\n", p.op, p.count)
				}
			}
			if len(stats.RequestsByModel) > 0 {
				fmt.Fprintf(w, "Requests by model:\n")
				type kv struct {
					model string
					count int
				}
				var pairs []kv
				for m, c := range stats.RequestsByModel {
					pairs = append(pairs, kv{m, c})
				}
				sort.Slice(pairs, func(i, j int) bool { return pairs[i].count > pairs[j].count })
				for _, p := range pairs {
					label := colorize(fmt.Sprintf("%-40s", p.model), tierByModel[p.model], tty)
					fmt.Fprintf(w, "  %s  %d\n", label, p.count)
				}
			}
		}

		return nil
	},
}

// fmtTokens renders a token count with K/M suffix for CLI output.
func fmtTokens(n int) string {
	if n == 0 {
		return "0"
	}
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
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

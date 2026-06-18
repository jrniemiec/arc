package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"

	agentpkg "github.com/jrniemiec/arc/agent"
)

var agentStatsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Show per-feed signal/noise statistics across all agent runs",
	Long: `Aggregates per-feed counts from all runs in runs.jsonl and prints
ingest rate, maybe rate, and skip rate per feed, sorted by ingest rate descending.

Useful for identifying low-signal feeds worth disabling or tightening.`,
	RunE: runAgentStats,
}

func init() {
	agentCmd.AddCommand(agentStatsCmd)
}

type feedStats struct {
	Name    string
	Runs    int
	New     int
	Filter  int
	Ingest  int
	Maybe   int
	Skip    int
	CostUSD float64
}

func runAgentStats(cmd *cobra.Command, _ []string) error {
	cfg := cfgFrom(cmd)
	runsPath := filepath.Join(cfg.AgentPath, "runs.jsonl")

	f, err := os.Open(runsPath)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("No agent runs recorded yet.")
			return nil
		}
		return fmt.Errorf("open runs file: %w", err)
	}
	defer f.Close()

	var recs []agentpkg.RunRecord
	dec := json.NewDecoder(f)
	for dec.More() {
		var rec agentpkg.RunRecord
		if err := dec.Decode(&rec); err != nil {
			break
		}
		recs = append(recs, rec)
	}

	if len(recs) == 0 {
		fmt.Println("No agent runs recorded yet.")
		return nil
	}

	// Aggregate per feed name.
	statsMap := map[string]*feedStats{}
	for _, rec := range recs {
		seen := map[string]bool{}
		for _, fr := range rec.Feeds {
			name := fr.Name
			if name == "" {
				name = fr.URL
			}
			if _, ok := statsMap[name]; !ok {
				statsMap[name] = &feedStats{Name: name}
			}
			s := statsMap[name]
			if !seen[name] {
				s.Runs++
				seen[name] = true
			}
			s.New += fr.New
			s.Filter += fr.Filter
			s.Ingest += fr.Ingest
			s.Maybe += fr.Maybe
			s.Skip += fr.Skip
			s.CostUSD += fr.CostUSD
		}
	}

	// Sort by ingest rate descending.
	stats := make([]*feedStats, 0, len(statsMap))
	for _, s := range statsMap {
		stats = append(stats, s)
	}
	sort.Slice(stats, func(i, j int) bool {
		ri := ingestRate(stats[i])
		rj := ingestRate(stats[j])
		if ri != rj {
			return ri > rj
		}
		return stats[i].Name < stats[j].Name
	})

	if isJSON(cmd) {
		data, _ := json.MarshalIndent(stats, "", "  ")
		fmt.Println(string(data))
		return nil
	}

	fmt.Printf("%-42s  %5s  %6s  %6s  %6s  %6s  %7s  %7s\n",
		"Feed", "Runs", "New", "Filter", "Ingest", "Skip", "In%", "Cost")
	fmt.Printf("%-42s  %5s  %6s  %6s  %6s  %6s  %7s  %7s\n",
		"----", "----", "---", "------", "------", "----", "---", "----")

	for _, s := range stats {
		rate := ingestRate(s)
		costStr := ""
		if s.CostUSD > 0 {
			costStr = fmt.Sprintf("$%.3f", s.CostUSD)
		}
		name := s.Name
		if len(name) > 42 {
			name = name[:39] + "..."
		}
		fmt.Printf("%-42s  %5d  %6d  %6d  %6d  %6d  %6.0f%%  %7s\n",
			name, s.Runs, s.New, s.Filter, s.Ingest, s.Skip, rate, costStr)
	}

	fmt.Printf("\n%d feeds across %d runs\n", len(stats), len(recs))
	return nil
}

func ingestRate(s *feedStats) float64 {
	if s.Filter == 0 {
		return 0
	}
	return float64(s.Ingest+s.Maybe) / float64(s.Filter) * 100
}

package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	agentpkg "github.com/jrniemiec/arc/agent"
)

var agentLogCmd = &cobra.Command{
	Use:   "log",
	Short: "Show recent agent run records",
	Long:  `Reads ~/.arc/agent/runs.jsonl and prints a summary of recent agent runs.`,
	RunE:  runAgentLog,
}

var agentLogN int

func init() {
	agentCmd.AddCommand(agentLogCmd)
	agentLogCmd.Flags().IntVarP(&agentLogN, "number", "n", 10, "number of recent runs to show")
}

func runAgentLog(cmd *cobra.Command, _ []string) error {
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

	// Read all records, keep last N.
	// Uses json.NewDecoder which handles both compact JSONL and pretty-printed records.
	var recs []agentpkg.RunRecord
	dec := json.NewDecoder(f)
	for dec.More() {
		var rec agentpkg.RunRecord
		if err := dec.Decode(&rec); err != nil {
			break // stop on first malformed record
		}
		recs = append(recs, rec)
	}

	if len(recs) == 0 {
		fmt.Println("No agent runs recorded yet.")
		return nil
	}

	// Show last N.
	start := 0
	if len(recs) > agentLogN {
		start = len(recs) - agentLogN
	}
	recs = recs[start:]

	if isJSON(cmd) {
		data, _ := json.MarshalIndent(recs, "", "  ")
		fmt.Println(string(data))
		return nil
	}

	for _, rec := range recs {
		duration := rec.FinishedAt.Sub(rec.StartedAt).Round(1e9)
		status := "✓"
		if rec.Error != "" {
			status = "✗"
		}
		fmt.Printf("%s %s  (%s)  in:%d maybe:%d skip:%d  feeds:%d  %s\n",
			status,
			rec.StartedAt.Format("2006-01-02 15:04"),
			duration,
			rec.TotalIngest,
			rec.TotalMaybe,
			rec.TotalSkip,
			len(rec.Feeds),
			rec.RunID,
		)
		if rec.Error != "" {
			fmt.Printf("  error: %s\n", rec.Error)
		}
	}
	return nil
}

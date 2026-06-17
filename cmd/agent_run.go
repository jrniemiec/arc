package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	agentpkg "github.com/jrniemiec/arc/agent"
)

var agentRunCmd = &cobra.Command{
	Use:   "run",
	Short: "Run one agent feed cycle",
	Long: `Polls all configured feeds, filters new items via LLM, and ingests relevant articles.

When running in a TTY (interactive terminal), prints a summary report.
When running in the background (launchd, cron), stays silent — all output
goes to ~/.arc/agent/runs.jsonl and the arc log.`,
	RunE: runAgentRun,
}

var (
	agentDryRun   bool
	agentFocus    string
	agentJSON     bool
	agentVerbose  bool
	agentDecisions string
)

func init() {
	agentCmd.AddCommand(agentRunCmd)
	agentRunCmd.Flags().BoolVar(&agentDryRun, "dry-run", false, "filter items but do not ingest")
	agentRunCmd.Flags().StringVar(&agentFocus, "focus", "", "temporary emphasis for this run only")
	agentRunCmd.Flags().BoolVar(&agentJSON, "json", false, "print run record as JSON")
	agentRunCmd.Flags().BoolVarP(&agentVerbose, "verbose", "v", false, "list every article with its verdict")
	agentRunCmd.Flags().StringVar(&agentDecisions, "decisions", "", "path to a decisions file to process user overrides")
}

func runAgentRun(cmd *cobra.Command, _ []string) error {
	cfg := cfgFrom(cmd)
	svc := svcFrom(cmd)

	// Replace cobra context with one we can cancel on SIGINT/SIGTERM.
	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Fprintf(os.Stderr, "\narc agent: interrupted — saving partial results...\n")
		cancel()
	}()

	agentDir := cfg.AgentPath
	agentCfgPath := filepath.Join(agentDir, "config.json")
	stateDir := filepath.Join(agentDir, "state")
	runsPath := filepath.Join(agentDir, "runs.jsonl")

	// Ensure directories exist.
	for _, dir := range []string{agentDir, stateDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create agent dir %s: %w", dir, err)
		}
	}

	agentCfg, err := agentpkg.LoadAgentConfig(agentCfgPath)
	if err != nil {
		return fmt.Errorf("load agent config: %w", err)
	}

	if len(agentCfg.Feeds) == 0 {
		return fmt.Errorf("no feeds configured in %s", agentCfgPath)
	}

	db := svc.Library().DB()

	// Start spinner only when attached to a TTY.
	var spinnerStatus func(slot int, msg string)
	isTTY := term.IsTerminal(int(os.Stdout.Fd()))
	if isTTY {
		sp := NewSpinner(agentpkg.IngestConcurrency)
		defer sp.Stop()
		spinnerStatus = sp.SetSlot
	}

	runOpts := agentpkg.RunOptions{
		ArcConfig: cfg,
		AgentCfg:  agentCfg,
		DB:        db,
		RunsPath:  runsPath,
		DryRun:    agentDryRun,
		Status:    spinnerStatus,
	}

	tag := ""
	if agentDryRun {
		tag = " [dry-run]"
	}

	var rec agentpkg.RunRecord

	if agentDecisions != "" {
		// Decisions mode: ingest user-overridden articles from a decisions file.
		if isTTY {
			fmt.Printf("arc agent — processing decisions file%s\n", tag)
		}
		var derr error
		rec, derr = agentpkg.RunDecisions(ctx, runOpts, agentDecisions)
		if derr != nil {
			return fmt.Errorf("decisions run: %w", derr)
		}
	} else {
		// Normal mode: poll feeds and filter via LLM.
		if isTTY {
			activeFeeds := 0
			for _, f := range agentCfg.Feeds {
				if !f.Disabled {
					activeFeeds++
				}
			}
			fmt.Printf("arc agent — polling %d feeds, %d filter threads, %d ingest threads%s\n", activeFeeds, agentpkg.FilterConcurrency, agentpkg.IngestConcurrency, tag)
		}
		runOpts.FeedStateDir = stateDir
		runOpts.DecisionsDir = agentDir
		runOpts.Focus = agentFocus
		var rerr error
		rec, rerr = agentpkg.RunFeeds(ctx, runOpts)
		if rerr != nil {
			return fmt.Errorf("agent run: %w", rerr)
		}
	}

	// Index newly ingested articles into SQLite.
	// pipeline.Run writes files only; the service layer normally handles reindexing.
	if rec.TotalIngest > 0 && !agentDryRun {
		if isTTY {
			fmt.Print("indexing...")
		}
		if err := svc.Library().Reindex(ctx, nil); err != nil {
			slog.Warn("reindex after agent run failed", "err", err)
		}
		if isTTY {
			fmt.Print("\r\033[K") // clear "indexing..." line
		}
	}

	// Print report.
	if agentJSON {
		data, _ := json.MarshalIndent(rec, "", "  ")
		fmt.Println(string(data))
	} else if isTTY {
		if agentVerbose {
			printAgentRunVerbose(rec)
		} else {
			printAgentRunSummary(rec)
		}
	}

	return nil
}

func dryTag(isDry bool) string {
	if isDry {
		return " [dry-run]"
	}
	return ""
}

func printAgentRunVerbose(rec agentpkg.RunRecord) {
	duration := rec.FinishedAt.Sub(rec.StartedAt).Round(1e9)
	fmt.Printf("\n── Agent run %s (%s) ──\n", rec.RunID, duration)
	fmt.Printf("  new: %d  filtered: %d  ingested: %d  maybe: %d  skipped: %d",
		rec.TotalNew, rec.TotalFilter, rec.TotalIngest, rec.TotalMaybe, rec.TotalSkip)
	if rec.TotalCostUSD > 0 {
		fmt.Printf("  cost: $%.4f", rec.TotalCostUSD)
	}
	fmt.Println()

	for _, f := range rec.Feeds {
		name := f.Name
		if name == "" {
			name = f.URL
		}
		status := "✓"
		if f.Error != "" {
			status = "✗"
		}
		costStr := ""
		if f.CostUSD > 0 {
			costStr = fmt.Sprintf("  $%.4f", f.CostUSD)
		}
		fmt.Printf("\n  %s %s  (new:%d  in:%d  maybe:%d  skip:%d%s)\n",
			status, name, f.New, f.Ingest, f.Maybe, f.Skip, costStr)
		if f.Error != "" {
			fmt.Printf("    error: %s\n", f.Error)
		}
		for _, item := range f.Items {
			var marker, color, reset string
			switch item.Verdict {
			case "ingest":
				marker, color, reset = "✓", "\033[32m", "\033[0m"
			case "maybe":
				marker, color, reset = "⁇", "\033[33m", "\033[0m"
			case "skip":
				marker, color, reset = "✗", "\033[2m", "\033[0m" // dim
			}
			fmt.Printf("    %s%s %-70s%s\n", color, marker, truncate(item.Title, 70), reset)
		}
	}
	fmt.Println()
}


func printAgentRunSummary(rec agentpkg.RunRecord) {
	duration := rec.FinishedAt.Sub(rec.StartedAt).Round(1e9)
	fmt.Printf("\n── Agent run %s (%s) ──\n", rec.RunID, duration)
	fmt.Printf("  new: %d  filtered: %d  ingested: %d  maybe: %d  skipped: %d",
		rec.TotalNew, rec.TotalFilter, rec.TotalIngest, rec.TotalMaybe, rec.TotalSkip)
	if rec.TotalCostUSD > 0 {
		fmt.Printf("  cost: $%.4f", rec.TotalCostUSD)
	}
	fmt.Printf("\n\n")

	for _, f := range rec.Feeds {
		status := "✓"
		if f.Error != "" {
			status = "✗"
		}
		name := f.Name
		if name == "" {
			name = f.URL
		}
		fmt.Printf("  %s %-40s  new:%-3d  in:%-3d  maybe:%-3d  skip:%-3d\n",
			status, name, f.New, f.Ingest, f.Maybe, f.Skip)
		if f.Error != "" {
			fmt.Printf("    error: %s\n", f.Error)
		}
	}
	fmt.Println()
}

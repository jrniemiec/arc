package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	agentpkg "github.com/jrniemiec/arc/agent"
	"github.com/jrniemiec/arc/service"
	"github.com/jrniemiec/arc/store"
)

var agentBriefingCmd = &cobra.Command{
	Use:   "digest",
	Short: "Print a digest of articles ingested in the last agent run",
	Long: `Formats a human-readable digest of articles ingested by the feed agent.

By default reads the most recent run. Use --run to target a specific run ID.
Outputs to stdout — pipe to msmtp or any mailer to send as email.
Produces no output (exit 0) if the run ingested nothing, so callers can
check for empty output before sending.

Examples:
  arc agent digest
  arc agent digest --run agent-20260615-060000
  arc agent digest --summary
  arc agent digest --flash --summary`,
	RunE: runAgentBriefing,
}

var (
	briefingFlash   bool
	briefingSummary bool
	briefingRunID   string
	briefingTTS     bool
)

func init() {
	agentCmd.AddCommand(agentBriefingCmd)
	agentBriefingCmd.Flags().BoolVar(&briefingFlash, "flash", true, "include flash summaries (default true)")
	agentBriefingCmd.Flags().BoolVar(&briefingSummary, "summary", false, "include full summaries")
	agentBriefingCmd.Flags().StringVar(&briefingRunID, "run", "", "run ID to use (default: last run)")
	agentBriefingCmd.Flags().BoolVar(&briefingTTS, "tts", false, "TTS-friendly output: no URLs, no unicode separators")
}

func runAgentBriefing(cmd *cobra.Command, _ []string) error {
	cfg := cfgFrom(cmd)
	runsPath := filepath.Join(cfg.AgentPath, "runs.jsonl")

	// Load runs.jsonl.
	f, err := os.Open(runsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no agent runs recorded yet")
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
		return fmt.Errorf("no agent runs recorded yet")
	}

	// Find target run — must be a daily run (not a decisions rerun).
	var rec agentpkg.RunRecord
	if briefingRunID != "" {
		found := false
		for _, r := range recs {
			if r.RunID == briefingRunID {
				rec = r
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("run %q not found in %s", briefingRunID, runsPath)
		}
	} else {
		// Default: last daily run (skip decisions reruns).
		for i := len(recs) - 1; i >= 0; i-- {
			if recs[i].RunType != "decisions" {
				rec = recs[i]
				break
			}
		}
		if rec.RunID == "" {
			rec = recs[len(recs)-1]
		}
	}

	// Query SQLite for articles from this run.
	svc := svcFrom(cmd)
	articles, err := svc.List(cmd.Context(), store.Filter{
		AgentRunID: rec.RunID,
		Limit:      200,
	})
	if err != nil {
		return fmt.Errorf("list articles: %w", err)
	}

	// Nothing ingested — emit no output so the caller can detect and skip sending.
	if len(articles) == 0 {
		return nil
	}

	// Split into ingest vs maybe by agent verdict.
	var ingestArticles, maybeArticles []store.Article
	for _, a := range articles {
		if a.AgentVerdict == "maybe" {
			maybeArticles = append(maybeArticles, a)
		} else {
			ingestArticles = append(ingestArticles, a)
		}
	}
	// Sort by ingest time ascending (chronological order within the run).
	sort.Slice(ingestArticles, func(i, j int) bool {
		return ingestArticles[i].IngestedAt.Before(ingestArticles[j].IngestedAt)
	})
	sort.Slice(maybeArticles, func(i, j int) bool {
		return maybeArticles[i].IngestedAt.Before(maybeArticles[j].IngestedAt)
	})

	sep := strings.Repeat("━", 42)
	if briefingTTS {
		sep = ""
	}
	duration := rec.FinishedAt.Sub(rec.StartedAt).Round(time.Second)
	date := rec.StartedAt.Local().Format("Mon Jan 2, 2006")

	var sb strings.Builder

	// Header.
	if briefingTTS {
		fmt.Fprintf(&sb, "Arc Briefing: %s\n", date)
	} else {
		fmt.Fprintf(&sb, "── Arc Briefing: %s ──\n", date)
	}
	fmt.Fprintf(&sb, "%d ingested  %d maybe  %d skipped",
		rec.TotalIngest, rec.TotalMaybe, rec.TotalSkip)
	if rec.TotalCostUSD > 0 {
		fmt.Fprintf(&sb, "  $%.4f", rec.TotalCostUSD)
	}
	fmt.Fprintf(&sb, "  %s\n", duration)

	// Ingest articles.
	for i, a := range ingestArticles {
		if sep != "" {
			fmt.Fprintf(&sb, "\n%s\n\n", sep)
		} else {
			fmt.Fprintf(&sb, "\n\n")
		}
		fmt.Fprintf(&sb, "%d. %s\n", i+1, a.Title)
		if a.URL != "" && !briefingTTS {
			fmt.Fprintf(&sb, "   %s\n", a.URL)
		}

		if briefingFlash {
			flash, err := svc.Read(cmd.Context(), service.ReadRequest{
				ID:   a.ID,
				Part: service.PartFlash,
			})
			if err == nil && strings.TrimSpace(flash) != "" {
				fmt.Fprintln(&sb)
				for _, line := range strings.Split(strings.TrimSpace(flash), "\n") {
					if strings.TrimSpace(line) != "" {
						fmt.Fprintf(&sb, "   %s\n", line)
					}
				}
			}
		}

		if briefingSummary {
			summary, err := svc.Read(cmd.Context(), service.ReadRequest{
				ID:   a.ID,
				Part: service.PartSummary,
			})
			if err == nil && strings.TrimSpace(summary) != "" {
				if briefingTTS {
					fmt.Fprintf(&sb, "\n   Summary:\n")
				} else {
					fmt.Fprintf(&sb, "\n   ── summary ──\n")
				}
				for _, line := range strings.Split(strings.TrimSpace(summary), "\n") {
					fmt.Fprintf(&sb, "   %s\n", line)
				}
			}
		}
	}

	// Maybe section.
	if len(maybeArticles) > 0 {
		if sep != "" {
			fmt.Fprintf(&sb, "\n%s\n\n", sep)
		} else {
			fmt.Fprintf(&sb, "\n\n")
		}
		if briefingTTS {
			fmt.Fprintf(&sb, "Also ingested, lower confidence:\n")
		} else {
			fmt.Fprintf(&sb, "── Maybe (also ingested, lower confidence) ──\n")
		}
		for i, a := range maybeArticles {
			if sep != "" {
				fmt.Fprintf(&sb, "\n%s\n\n", sep)
			} else {
				fmt.Fprintf(&sb, "\n\n")
			}
			fmt.Fprintf(&sb, "%d. %s\n", i+1, a.Title)
			if a.URL != "" && !briefingTTS {
				fmt.Fprintf(&sb, "   %s\n", a.URL)
			}

			if briefingFlash {
				flash, err := svc.Read(cmd.Context(), service.ReadRequest{
					ID:   a.ID,
					Part: service.PartFlash,
				})
				if err == nil && strings.TrimSpace(flash) != "" {
					fmt.Fprintln(&sb)
					for _, line := range strings.Split(strings.TrimSpace(flash), "\n") {
						if strings.TrimSpace(line) != "" {
							fmt.Fprintf(&sb, "   %s\n", line)
						}
					}
				}
			}

			if briefingSummary {
				summary, err := svc.Read(cmd.Context(), service.ReadRequest{
					ID:   a.ID,
					Part: service.PartSummary,
				})
				if err == nil && strings.TrimSpace(summary) != "" {
					if briefingTTS {
						fmt.Fprintf(&sb, "\n   Summary:\n")
					} else {
						fmt.Fprintf(&sb, "\n   ── summary ──\n")
					}
					for _, line := range strings.Split(strings.TrimSpace(summary), "\n") {
						fmt.Fprintf(&sb, "   %s\n", line)
					}
				}
			}
		}
	}

	fmt.Fprintln(&sb)
	fmt.Print(sb.String())
	return nil
}

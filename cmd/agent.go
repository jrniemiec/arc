package cmd

import (
	"github.com/spf13/cobra"
)

var agentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Autonomous feed ingestion agent",
	Long: `arc agent polls configured RSS/Atom feeds, filters items using an LLM
against your interest profile and library context, and ingests relevant articles.

Configuration: ~/.arc/agent/config.json
State files:   ~/.arc/agent/state/
Run log:       ~/.arc/agent/runs.jsonl

Subcommands:
  arc agent run           run one feed cycle now
  arc agent log           show recent run records`,
}

func init() {
	rootCmd.AddCommand(agentCmd)
}

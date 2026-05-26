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
	rootCmd.AddCommand(profilesCmd)
}

var profilesCmd = &cobra.Command{
	Use:   "profiles",
	Short: "List available LLM profiles with pricing and tradeoff notes",
	Long: `List all available LLM profiles defined in config.

Profiles are sorted by cost tier. Active profiles for each ingest step
are marked with an arrow.

Examples:
  arc profiles
  arc profiles --json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg := cmd.Context().Value(keyConfig).(config.Config)

		type profileEntry struct {
			Name     string `json:"name"`
			Provider string `json:"provider"`
			Model    string `json:"model"`
			CostTier string `json:"cost_tier"`
			Active   []string `json:"active,omitempty"`
			config.ProfileInfo
		}

		tierOrder := map[string]int{
			"local": 0, "very_low": 1, "low": 2,
			"medium": 3, "high": 4, "premium": 5,
		}

		type namedProfile struct {
			name string
			p    config.Profile
		}
		sorted := make([]namedProfile, 0, len(cfg.Profiles))
		for name, p := range cfg.Profiles {
			sorted = append(sorted, namedProfile{name, p})
		}
		sort.Slice(sorted, func(i, j int) bool {
			ti := tierOrder[sorted[i].p.Info.CostTier]
			tj := tierOrder[sorted[j].p.Info.CostTier]
			if ti != tj {
				return ti < tj
			}
			return sorted[i].name < sorted[j].name
		})

		if isJSON(cmd) {
			entries := make([]profileEntry, 0, len(sorted))
			for _, np := range sorted {
				entries = append(entries, profileEntry{
					Name:        np.name,
					Provider:    np.p.Provider,
					Model:       np.p.Model,
					CostTier:    np.p.Info.CostTier,
					Active:      activeSteps(np.name, cfg),
					ProfileInfo: np.p.Info,
				})
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(entries)
		}

		// Active assignments header
		fmt.Fprintln(cmd.OutOrStdout(), "Active profiles:")
		fmt.Fprintf(cmd.OutOrStdout(), "  summary:   %s\n", cfg.Ingest.SummaryProfile)
		fmt.Fprintf(cmd.OutOrStdout(), "  flash:     %s\n", cfg.Ingest.FlashProfile)
		fmt.Fprintf(cmd.OutOrStdout(), "  flashcard: %s\n", cfg.Ingest.FlashcardProfile)
		fmt.Fprintf(cmd.OutOrStdout(), "  embed:     %s\n", cfg.Ingest.EmbedProfile)
		fmt.Fprintln(cmd.OutOrStdout())

		tty := isTTY(os.Stdout)
		for _, np := range sorted {
			p := np.p
			steps := activeSteps(np.name, cfg)

			active := ""
			if len(steps) > 0 {
				active = "  ←"
				for _, s := range steps {
					active += " " + s
				}
			}

			tier := p.Info.CostTier
			name := colorize(fmt.Sprintf("%-12s", np.name), tier, tty)
			tierBadge := colorize("["+tier+"]", tier, tty)

			fmt.Fprintf(cmd.OutOrStdout(), "%s  %-10s  %-36s  %s%s\n",
				name, p.Provider, p.Model, tierBadge, active)

			if p.Info.Pricing != nil {
				pr := p.Info.Pricing
				if pr.CachedInput > 0 {
					fmt.Fprintf(cmd.OutOrStdout(), "              pricing:  $%.2f / $%.2f / $%.3f per 1M tokens (in/out/cached)\n",
						pr.Input, pr.Output, pr.CachedInput)
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "              pricing:  $%.2f / $%.2f per 1M tokens (in/out)\n",
						pr.Input, pr.Output)
				}
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "              pricing:  free (local)")
			}

			if p.Info.CostVsValue != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "              note:     %s\n", p.Info.CostVsValue)
			}
			fmt.Fprintln(cmd.OutOrStdout())
		}

		return nil
	},
}

// activeSteps returns the ingest steps that use the given profile name.
func activeSteps(name string, cfg config.Config) []string {
	var steps []string
	if cfg.Ingest.SummaryProfile == name {
		steps = append(steps, "summary")
	}
	if cfg.Ingest.FlashProfile == name {
		steps = append(steps, "flash")
	}
	if cfg.Ingest.FlashcardProfile == name {
		steps = append(steps, "flashcard")
	}
	if cfg.Ingest.EmbedProfile == name {
		steps = append(steps, "embed")
	}
	return steps
}

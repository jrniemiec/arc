package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(statsCmd)
}

var statsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Show knowledge base statistics",
	RunE: func(cmd *cobra.Command, args []string) error {
		svc := svcFrom(cmd)

		stats, err := svc.Stats(cmd.Context())
		if err != nil {
			return fmt.Errorf("stats: %w", err)
		}

		if isJSON(cmd) {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(stats)
		}

		fmt.Printf("Articles:    %d  (unread: %d, unplayed: %d)\n",
			stats.TotalArticles, stats.Unread, stats.Unplayed)
		fmt.Printf("Collections: %d\n", stats.TotalCollections)
		fmt.Printf("Tags:        %d\n", stats.TotalTags)
		if stats.CostThisMonth > 0 || stats.CostTotal > 0 {
			fmt.Printf("Cost:        $%.3f this month  ($%.3f total)\n",
				stats.CostThisMonth, stats.CostTotal)
		}
		return nil
	},
}

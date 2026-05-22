package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/jrniemiec/arc/service"
	"github.com/jrniemiec/arc/store"
)

var (
	searchCollection string
	searchTag        string
	searchLimit      int
)

func init() {
	searchCmd.Flags().StringVar(&searchCollection, "collection", "", "filter by collection ID")
	searchCmd.Flags().StringVar(&searchTag, "tag", "", "filter by tag")
	searchCmd.Flags().IntVar(&searchLimit, "limit", 20, "maximum number of results")
	rootCmd.AddCommand(searchCmd)
}

var searchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search articles by keyword",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		svc := svcFrom(cmd)

		var tags []string
		if searchTag != "" {
			tags = []string{searchTag}
		}

		req := service.SearchRequest{
			Query:      args[0],
			Collection: searchCollection,
			Tags:       tags,
			Mode:       store.QueryKeyword,
			Limit:      searchLimit,
		}

		results, err := svc.Search(cmd.Context(), req)
		if err != nil {
			return fmt.Errorf("search: %w", err)
		}

		if isJSON(cmd) {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(results)
		}

		if len(results) == 0 {
			fmt.Printf("no results for %q\n", args[0])
			return nil
		}

		fmt.Printf("results for %q:\n\n", args[0])
		for i, r := range results {
			fmt.Printf("%d. %s\n", i+1, r.Article.ID)
			if r.Article.Title != "" {
				fmt.Printf("   %s\n", r.Article.Title)
			}
			if r.Excerpt != "" {
				fmt.Printf("   %s\n", r.Excerpt)
			}
			fmt.Println()
		}
		return nil
	},
}

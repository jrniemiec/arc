package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jrniemiec/arc/store"
)

var (
	listCollection string
	listTag        string
	listUnread     bool
	listUnplayed   bool
	listLimit      int
)

func init() {
	listCmd.Flags().StringVar(&listCollection, "collection", "", "filter by collection ID")
	listCmd.Flags().StringVar(&listTag, "tag", "", "filter by tag")
	listCmd.Flags().BoolVar(&listUnread, "unread", false, "show only unread articles")
	listCmd.Flags().BoolVar(&listUnplayed, "unplayed", false, "show only unplayed articles")
	listCmd.Flags().IntVar(&listLimit, "limit", 50, "maximum number of results")
	rootCmd.AddCommand(listCmd)
}

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List articles in the knowledge base",
	RunE: func(cmd *cobra.Command, args []string) error {
		svc := svcFrom(cmd)

		var tags []string
		if listTag != "" {
			tags = []string{listTag}
		}

		f := store.Filter{
			Collection: listCollection,
			Tags:       tags,
			Unread:     listUnread,
			Unplayed:   listUnplayed,
			Limit:      listLimit,
		}

		articles, err := svc.List(cmd.Context(), f)
		if err != nil {
			return fmt.Errorf("list: %w", err)
		}

		if isJSON(cmd) {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(articles)
		}

		if len(articles) == 0 {
			fmt.Println("no articles found")
			return nil
		}

		for _, a := range articles {
			tags := make([]string, 0, len(a.Tags))
			for _, t := range a.Tags {
				tags = append(tags, t.Value)
			}
			tagStr := ""
			if len(tags) > 0 {
				tagStr = "  [" + strings.Join(tags, ", ") + "]"
			}
			collections := strings.Join(a.Collections, ", ")
			read := " "
			if a.ReadAt != nil {
				read = "✓"
			}
			fmt.Printf("%s  %-45s  %-20s  %s%s\n",
				read,
				truncate(a.ID, 45),
				truncate(collections, 20),
				truncate(a.Title, 50),
				tagStr,
			)
		}
		return nil
	},
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

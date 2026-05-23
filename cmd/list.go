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
			read := " "
			if a.ReadAt != nil {
				read = "✓"
			}

			date := a.IngestedAt.Format("2006-01-02")
			collections := strings.Join(a.Collections, ", ")
			collStr := ""
			if collections != "" {
				collStr = "  [" + collections + "]"
			}

			// Line 1: read marker, date, slug, title
			fmt.Fprintf(cmd.OutOrStdout(), "%s  %s  %-50s  %s%s\n",
				read, date, truncate(a.ID, 50), truncate(a.Title, 50), collStr)

			// Line 2: variant indicators
			var variants []string
			if a.SummaryStyle != "" && a.SummaryModel != "" {
				variants = append(variants, fmt.Sprintf("summary:%s/%s", a.SummaryStyle, a.SummaryModel))
			}
			if a.FlashModel != "" {
				variants = append(variants, fmt.Sprintf("flash:%s", a.FlashModel))
			}
			if a.FlashcardStyle != "" && a.FlashcardModel != "" {
				variants = append(variants, fmt.Sprintf("flashcards:%s/%s", a.FlashcardStyle, a.FlashcardModel))
			}
			if len(variants) > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "             %s\n", strings.Join(variants, "  ·  "))
			}
			fmt.Fprintln(cmd.OutOrStdout())
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

package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jrniemiec/arc/config"
	"github.com/jrniemiec/arc/store"
)

var (
	listCollection string
	listTag        string
	listUnread     bool
	listUnplayed   bool
	listAgent      bool
	listAgentRun   string
	listSlugs      bool
	listLimit      int
)

func init() {
	listCmd.Flags().StringVar(&listCollection, "collection", "", "filter by collection ID")
	listCmd.Flags().StringVar(&listTag, "tag", "", "filter by tag")
	listCmd.Flags().BoolVar(&listUnread, "unread", false, "show only unread articles")
	listCmd.Flags().BoolVar(&listUnplayed, "unplayed", false, "show only unplayed articles")
	listCmd.Flags().BoolVar(&listSlugs, "slugs", false, "print one slug per line (for scripting)")
	listCmd.Flags().BoolVar(&listAgent, "agent", false, "show only articles ingested by the feed agent")
	listCmd.Flags().StringVar(&listAgentRun, "agent-run", "", "show only articles from a specific agent run ID")
	listCmd.Flags().IntVar(&listLimit, "limit", 50, "maximum number of results")
	rootCmd.AddCommand(listCmd)
}

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List articles in the knowledge base",
	Long: `List articles stored in the knowledge base with variant and status indicators.

Reads from SQLite — does not scan the filesystem directly.

Each article is shown on two lines:
  Line 1:  read marker (✓/space), ingested date, slug, title, collections
  Line 2:  variant indicators — summary:<style>/<model>, flash:<model>,
           flashcards:<style>/<model>, embed:<model> (dimmed, colored by cost tier)

Filtering:
  --collection  show only articles in the given collection
  --tag         show only articles with the given tag
  --unread      show only articles not yet marked as read
  --unplayed    show only articles not yet marked as played
  --limit       cap the number of results (default 50)

Examples:
  arc list
  arc list --unread
  arc list --collection my-collection
  arc list --tag ml --limit 20
  arc list --json`,
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
			AgentOnly:  listAgent,
			AgentRunID: listAgentRun,
			Limit:      listLimit,
		}

		articles, err := svc.List(cmd.Context(), f)
		if err != nil {
			return fmt.Errorf("list: %w", err)
		}

		if listSlugs {
			for _, a := range articles {
				fmt.Fprintln(cmd.OutOrStdout(), a.ID)
			}
			return nil
		}

		if isJSON(cmd) {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(articles)
		}

		if len(articles) == 0 {
			fmt.Println("no articles found")
			return nil
		}

		cfg := cmd.Context().Value(keyConfig).(config.Config)
		tty := isTTY(os.Stdout)

		// Build model → cost tier lookup
		tierByModel := make(map[string]string)
		for _, p := range cfg.Profiles {
			tierByModel[p.Model] = p.Info.CostTier
		}

		for _, a := range articles {
			read := " "
			if a.ReadAt != nil {
				read = "✓"
			}

			date := dim(a.IngestedAt.Format("2006-01-02"), tty)
			collections := strings.Join(a.Collections, ", ")
			collStr := ""
			if collections != "" {
				collStr = "  " + dim("["+collections+"]", tty)
			}

			// Line 1: read marker, date, slug, title
			fmt.Fprintf(cmd.OutOrStdout(), "%s  %s  %-50s  %s%s\n",
				read, date, truncate(a.ID, 50), truncate(a.Title, 50), collStr)

			// Line 2: variant indicators, model colored by cost tier
			var variants []string
			if a.SummaryStyle != "" && a.SummaryModel != "" {
				model := colorize(a.SummaryModel, tierByModel[a.SummaryModel], tty)
				variants = append(variants, fmt.Sprintf("summary:%s/%s", a.SummaryStyle, model))
			}
			if a.FlashModel != "" {
				model := colorize(a.FlashModel, tierByModel[a.FlashModel], tty)
				variants = append(variants, fmt.Sprintf("flash:%s", model))
			}
			if a.FlashcardStyle != "" && a.FlashcardModel != "" {
				model := colorize(a.FlashcardModel, tierByModel[a.FlashcardModel], tty)
				variants = append(variants, fmt.Sprintf("flashcards:%s/%s", a.FlashcardStyle, model))
			}
			if a.EmbedModel != "" {
				variants = append(variants, fmt.Sprintf("embed:%s", a.EmbedModel))
			}
			if len(variants) > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "             %s\n", dim(strings.Join(variants, "  ·  "), tty))
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

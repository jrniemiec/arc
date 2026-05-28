package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jrniemiec/arc/store/fs"
)

func init() {
	rootCmd.AddCommand(collectionsCmd)
	collectionsCmd.AddCommand(collectionsListCmd)
	collectionsCmd.AddCommand(collectionsShowCmd)
	collectionsCmd.AddCommand(collectionsCreateCmd)
	collectionsCmd.AddCommand(collectionsAddCmd)
	collectionsCmd.AddCommand(collectionsRemoveCmd)
	collectionsCmd.AddCommand(collectionsDescribeCmd)
}

var collectionsCmd = &cobra.Command{
	Use:   "collections",
	Short: "Manage article collections",
	Long: `Create and manage collections of articles.

Collections are directories under ~/.arc/collections/<slug>/ containing
symlinks to articles. An article can belong to multiple collections.
SQLite is kept in sync automatically.

Examples:
  arc collections list
  arc collections create ml
  arc collections add 20260115-attention-is-all-you-need ml
  arc collections show ml
  arc collections remove 20260115-attention-is-all-you-need ml
  arc collections describe ml "Machine learning papers and research"`,
}

var collectionsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all collections",
	RunE: func(cmd *cobra.Command, args []string) error {
		svc := svcFrom(cmd)
		cols, err := svc.ListCollections(cmd.Context())
		if err != nil {
			return fmt.Errorf("list collections: %w", err)
		}
		if len(cols) == 0 {
			fmt.Println("no collections — create one with: arc collections create <slug>")
			return nil
		}
		tty := isTTY(os.Stdout)
		for _, c := range cols {
			indicators := []string{}
			if c.HasSummary {
				indicators = append(indicators, "meta-summary")
			}
			if c.HasSystem {
				indicators = append(indicators, "system")
			}
			ind := ""
			if len(indicators) > 0 {
				ind = "  " + dim("["+strings.Join(indicators, ", ")+"]", tty)
			}
			desc := ""
			if c.Description != "" {
				desc = "  " + dim(truncate(c.Description, 60), tty)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%-20s  %s articles%s%s\n",
				bold(c.Slug, tty), fmt.Sprintf("%3d", c.ArticleCount), ind, desc)
		}
		return nil
	},
}

var collectionsShowCmd = &cobra.Command{
	Use:   "show <slug>",
	Short: "List articles in a collection",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		svc := svcFrom(cmd)
		slug := args[0]

		info, err := svc.GetCollection(cmd.Context(), slug)
		if err != nil {
			return err
		}

		tty := isTTY(os.Stdout)
		fmt.Fprintf(cmd.OutOrStdout(), "collection: %s  (%d articles)\n\n",
			bold(info.Slug, tty), info.ArticleCount)

		articles, err := svc.ListCollectionArticles(cmd.Context(), slug)
		if err != nil {
			return err
		}
		if len(articles) == 0 {
			fmt.Println("  no articles — add one with: arc collections add <article-slug> " + slug)
			return nil
		}
		for _, a := range articles {
			fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", a)
		}
		return nil
	},
}

var collectionsCreateCmd = &cobra.Command{
	Use:   "create <slug>",
	Short: "Create a new collection",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		slug := args[0]
		if err := validateSlug(slug); err != nil {
			return err
		}
		svc := svcFrom(cmd)
		if err := svc.CreateCollection(cmd.Context(), slug); err != nil {
			return fmt.Errorf("create collection: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "created collection: %s\n", slug)
		return nil
	},
}

var collectionsAddCmd = &cobra.Command{
	Use:   "add <article-slug> <collection-slug>",
	Short: "Add an article to a collection",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		articleSlug := args[0]
		collectionSlug := args[1]
		svc := svcFrom(cmd)

		// Resolve fuzzy article slug
		resolved, err := resolveSlug(cmd, articleSlug)
		if err != nil {
			return fmt.Errorf("article not found: %w", err)
		}

		err = svc.AddToCollection(cmd.Context(), resolved, collectionSlug)
		if err == fs.ErrAlreadyInCollection {
			fmt.Fprintf(cmd.OutOrStdout(), "%s is already in collection %q\n", resolved, collectionSlug)
			return nil
		}
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "added %s → %s\n", resolved, collectionSlug)
		return nil
	},
}

var collectionsRemoveCmd = &cobra.Command{
	Use:   "remove <article-slug> <collection-slug>",
	Short: "Remove an article from a collection",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		articleSlug := args[0]
		collectionSlug := args[1]
		svc := svcFrom(cmd)

		resolved, err := resolveSlug(cmd, articleSlug)
		if err != nil {
			return fmt.Errorf("article not found: %w", err)
		}

		if err := svc.RemoveFromCollection(cmd.Context(), resolved, collectionSlug); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "removed %s from %s\n", resolved, collectionSlug)
		return nil
	},
}

var collectionsDescribeCmd = &cobra.Command{
	Use:   "describe <slug> <text>",
	Short: "Set a description for a collection",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		svc := svcFrom(cmd)
		if err := svc.SetCollectionDescription(cmd.Context(), args[0], args[1]); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "description set for collection: %s\n", args[0])
		return nil
	},
}

// validateSlug ensures a collection slug is filesystem-safe.
func validateSlug(slug string) error {
	if slug == "" {
		return fmt.Errorf("slug cannot be empty")
	}
	if strings.ContainsAny(slug, "/ \\:*?\"<>|") {
		return fmt.Errorf("slug %q contains invalid characters — use letters, numbers, and hyphens only", slug)
	}
	if strings.HasPrefix(slug, ".") {
		return fmt.Errorf("slug cannot start with a dot")
	}
	return nil
}

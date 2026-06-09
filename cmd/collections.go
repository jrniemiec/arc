package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jrniemiec/arc/service"
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
	collectionsCmd.AddCommand(collectionsSuggestCmd)
	collectionsCmd.AddCommand(collectionsReadCmd)

	collectionsSuggestCmd.Flags().String("profile", "", "LLM profile override")
	collectionsSuggestCmd.Flags().Bool("apply", false, "interactively create collections and link articles")
	collectionsSuggestCmd.Flags().Bool("all", false, "with --apply: accept all without prompting")

	collectionsReadCmd.Flags().Bool("flash", false, "read flash summaries (default)")
	collectionsReadCmd.Flags().Bool("summary", false, "read full summaries")
	collectionsReadCmd.Flags().String("model", "", "prefer this model variant")
	collectionsReadCmd.Flags().String("style", "", "prefer this style variant")
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
		slug, err := resolveCollectionSlug(cmd, args[0])
		if err != nil {
			return err
		}

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

		collectionSlug, err = resolveCollectionSlug(cmd, collectionSlug)
		if err != nil {
			return fmt.Errorf("collection not found: %w", err)
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

		collectionSlug, err = resolveCollectionSlug(cmd, collectionSlug)
		if err != nil {
			return fmt.Errorf("collection not found: %w", err)
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
		slug, err := resolveCollectionSlug(cmd, args[0])
		if err != nil {
			return err
		}
		if err := svc.SetCollectionDescription(cmd.Context(), slug, args[1]); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "description set for collection: %s\n", slug)
		return nil
	},
}

var collectionsReadCmd = &cobra.Command{
	Use:   "read <slug> [<slug>...]",
	Short: "Read concatenated flash or summary across one or more collections",
	Long: `Concatenates flash summaries (default) or full summaries for every article
in the given collections, separated by article titles.

Multiple collections are concatenated in order. No deduplication — if an article
appears in more than one collection it will be shown multiple times, but flash
summaries are short enough that this is rarely an issue.

Default: flash summaries — concise 3-5 sentence briefs per article.
--summary: full summaries.

Articles missing the requested variant are skipped silently.

Examples:
  arc collections read software-architecture
  arc collections read ml systems
  arc collections read --summary ml
  arc collections read --style bullets ml systems`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		useSummary, _ := cmd.Flags().GetBool("summary")
		model, _ := cmd.Flags().GetString("model")
		style, _ := cmd.Flags().GetString("style")

		part := service.PartFlash
		if useSummary {
			part = service.PartSummary
		}

		req := service.ReadRequest{Part: part, Model: model, Style: style}
		svc := svcFrom(cmd)

		for i, arg := range args {
			slug, err := resolveCollectionSlug(cmd, arg)
			if err != nil {
				return err
			}
			text, err := svc.ReadCollection(cmd.Context(), slug, req)
			if err != nil {
				return fmt.Errorf("read collection %q: %w", slug, err)
			}
			if i > 0 {
				fmt.Println()
			}
			fmt.Println(text)
		}
		return nil
	},
}

var collectionsSuggestCmd = &cobra.Command{
	Use:   "suggest [<article-slug>]",
	Short: "Suggest collections using AI (read-only by default)",
	Long: `Without an argument: suggest a set of new collections for the whole library.
With an article slug: suggest which existing collections the article fits.

By default only prints suggestions and equivalent arc commands — nothing is created.
Use --apply to interactively create collections and link articles.

Examples:
  arc collections suggest                         # library-wide suggestions (print only)
  arc collections suggest 20260115-attention      # per-article suggestions (print only)
  arc collections suggest --apply                 # interactive: accept/skip each suggestion
  arc collections suggest --apply --all           # apply all without prompting`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		svc := svcFrom(cmd)
		profile, _ := cmd.Flags().GetString("profile")
		apply, _ := cmd.Flags().GetBool("apply")
		acceptAll, _ := cmd.Flags().GetBool("all")
		tty := isTTY(os.Stdout)

		progress := func(msg string) {
			fmt.Fprintf(cmd.ErrOrStderr(), "  %s\n", msg)
		}

		if len(args) == 0 {
			// Library-wide suggestion
			suggestions, err := svc.SuggestCollections(cmd.Context(), profile, progress)
			if err != nil {
				return fmt.Errorf("suggest collections: %w", err)
			}
			if len(suggestions) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no suggestions")
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout())
			for _, s := range suggestions {
				fmt.Fprintf(cmd.OutOrStdout(), "%s", bold(s.Slug, tty))
				if s.Description != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "  %s", dim(s.Description, tty))
				}
				fmt.Fprintf(cmd.OutOrStdout(), "  (%d articles)\n", len(s.Articles))
				for _, a := range s.Articles {
					fmt.Fprintf(cmd.OutOrStdout(), "    %s\n", a)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "  arc collections create %s\n", s.Slug)
				for _, a := range s.Articles {
					fmt.Fprintf(cmd.OutOrStdout(), "  arc collections add %s %s\n", a, s.Slug)
				}
				fmt.Fprintln(cmd.OutOrStdout())

				if !apply {
					continue
				}

				if acceptAll || promptYN(cmd, fmt.Sprintf("Create %q and add %d articles? [Y/n] ", s.Slug, len(s.Articles))) {
					if err := svc.CreateCollection(cmd.Context(), s.Slug); err != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "  skip (create): %v\n", err)
						continue
					}
					if s.Description != "" {
						_ = svc.SetCollectionDescription(cmd.Context(), s.Slug, s.Description)
					}
					added := 0
					for _, a := range s.Articles {
						if err := svc.AddToCollection(cmd.Context(), a, s.Slug); err != nil {
							fmt.Fprintf(cmd.ErrOrStderr(), "  skip %s: %v\n", a, err)
							continue
						}
						added++
					}
					fmt.Fprintf(cmd.OutOrStdout(), "  created %s, added %d articles\n", s.Slug, added)
				} else {
					fmt.Fprintln(cmd.OutOrStdout(), "  skipped")
				}
				fmt.Fprintln(cmd.OutOrStdout())
			}
			return nil
		}

		// Per-article suggestion
		articleSlug, err := resolveSlug(cmd, args[0])
		if err != nil {
			return fmt.Errorf("article not found: %w", err)
		}

		matches, err := svc.SuggestCollectionsForArticle(cmd.Context(), articleSlug, profile, progress)
		if err != nil {
			return fmt.Errorf("suggest collections for article: %w", err)
		}
		if len(matches) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "no matching collections found")
			return nil
		}
		fmt.Fprintln(cmd.OutOrStdout())
		for _, m := range matches {
			fmt.Fprintf(cmd.OutOrStdout(), "%s  %s\n", bold(m.Slug, tty), dim(m.Reason, tty))
			fmt.Fprintf(cmd.OutOrStdout(), "  arc collections add %s %s\n", articleSlug, m.Slug)
			fmt.Fprintln(cmd.OutOrStdout())

			if !apply {
				continue
			}

			if acceptAll || promptYN(cmd, fmt.Sprintf("Add %q to collection %q? [Y/n] ", articleSlug, m.Slug)) {
				if err := svc.AddToCollection(cmd.Context(), articleSlug, m.Slug); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "  skip: %v\n", err)
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "  added %s → %s\n", articleSlug, m.Slug)
				}
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "  skipped")
			}
			fmt.Fprintln(cmd.OutOrStdout())
		}
		return nil
	},
}

// promptYN reads a y/n response from stdin; returns true for y/Y/enter (default yes).
func promptYN(cmd *cobra.Command, prompt string) bool {
	fmt.Fprint(cmd.OutOrStdout(), prompt)
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return false
	}
	ans := strings.ToLower(strings.TrimSpace(scanner.Text()))
	return ans == "" || ans == "y" || ans == "yes"
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

package cmd

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jrniemiec/arc/service"
	"github.com/jrniemiec/arc/store"
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
	collectionsCmd.AddCommand(collectionsDescribeAllCmd)
	collectionsCmd.AddCommand(collectionsGenerateDescriptionCmd)
	collectionsCmd.AddCommand(collectionsGenerateDescriptionAllCmd)
	collectionsCmd.AddCommand(collectionsSuggestCmd)
	collectionsCmd.AddCommand(collectionsSearchCmd)
	collectionsCmd.AddCommand(collectionsReadCmd)
	collectionsCmd.AddCommand(collectionsDeleteCmd)
	collectionsCmd.AddCommand(collectionsRenameCmd)

	collectionsListCmd.Flags().BoolP("quiet", "q", false, "output slugs only, one per line")
	collectionsShowCmd.Flags().StringVar(&collectionsShowArticle, "article", "", "show collections that contain this article slug")
	collectionsDeleteCmd.Flags().Bool("force", false, "skip confirmation prompt")
	collectionsDeleteCmd.Flags().Bool("purge", false, "also delete articles that belong only to this collection")

	collectionsGenerateDescriptionCmd.Flags().Bool("dry-run", false, "print generated description without writing")
	collectionsGenerateDescriptionCmd.Flags().Bool("edit", false, "review and optionally modify before saving")
	collectionsGenerateDescriptionCmd.Flags().Bool("force", false, "overwrite existing description")
	collectionsGenerateDescriptionCmd.Flags().String("profile", "", "LLM profile override")
	collectionsGenerateDescriptionCmd.MarkFlagsMutuallyExclusive("dry-run", "edit")

	collectionsGenerateDescriptionAllCmd.Flags().Bool("dry-run", false, "print generated descriptions without writing")
	collectionsGenerateDescriptionAllCmd.Flags().Bool("edit", false, "review and optionally modify each before saving")
	collectionsGenerateDescriptionAllCmd.Flags().Bool("force", false, "overwrite existing descriptions")
	collectionsGenerateDescriptionAllCmd.Flags().Int("limit", 0, "process at most N collections (0 = no limit)")
	collectionsGenerateDescriptionAllCmd.Flags().String("profile", "", "LLM profile override")
	collectionsGenerateDescriptionAllCmd.MarkFlagsMutuallyExclusive("dry-run", "edit")

	collectionsSuggestCmd.Flags().String("profile", "", "LLM profile override")
	collectionsSuggestCmd.Flags().Bool("apply", false, "interactively create collections and link articles")
	collectionsSuggestCmd.Flags().Bool("all", false, "with --apply: accept all without prompting")
	collectionsSuggestCmd.Flags().Bool("uncollected", false, "suggest collections for all uncollected articles")
	collectionsSuggestCmd.Flags().Int("count", 0, "target number of collections (0 = let the model decide)")
	collectionsSuggestCmd.Flags().Int("min", 0, "minimum articles per collection (0 = no constraint)")
	collectionsSuggestCmd.Flags().Int("limit", 0, "with --uncollected: process at most N articles (0 = no limit)")

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
	Use:   "list [pattern]",
	Short: "List collections (optional glob pattern, e.g. ai-*)",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		svc := svcFrom(cmd)
		cols, err := svc.ListCollections(cmd.Context())
		if err != nil {
			return fmt.Errorf("list collections: %w", err)
		}
		if len(args) == 1 {
			pattern := args[0]
			filtered := cols[:0]
			for _, c := range cols {
				if matched, _ := filepath.Match(pattern, c.Slug); matched {
					filtered = append(filtered, c)
				}
			}
			cols = filtered
		}
		if len(cols) == 0 {
			fmt.Println("no collections — create one with: arc collections create <slug>")
			return nil
		}
		quiet, _ := cmd.Flags().GetBool("quiet")
		if quiet {
			for _, c := range cols {
				fmt.Fprintln(cmd.OutOrStdout(), c.Slug)
			}
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
			numIDStr := "   "
			if c.NumID > 0 {
				numIDStr = fmt.Sprintf("%3d", c.NumID)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s  %-20s  %s articles%s%s\n",
				dim(numIDStr, tty), bold(c.Slug, tty), fmt.Sprintf("%3d", c.ArticleCount), ind, desc)
		}
		return nil
	},
}

var collectionsShowArticle string

var collectionsShowCmd = &cobra.Command{
	Use:   "show <slug>",
	Short: "List articles in a collection, or collections for an article",
	Args:  cobra.RangeArgs(0, 1),
	RunE: func(cmd *cobra.Command, args []string) error {
		svc := svcFrom(cmd)

		// --article mode: show which collections contain a given article
		if collectionsShowArticle != "" {
			articleSlug, err := resolveSlug(cmd, collectionsShowArticle)
			if err != nil {
				return fmt.Errorf("article not found: %w", err)
			}
			a, err := svc.GetArticle(cmd.Context(), articleSlug)
			if err != nil {
				return err
			}
			tty := isTTY(os.Stdout)
			fmt.Fprintf(cmd.OutOrStdout(), "article: %s\n\n", bold(a.ID, tty))
			if len(a.Collections) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "  (uncollected)")
				return nil
			}
			for _, c := range a.Collections {
				fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", c)
			}
			return nil
		}

		if len(args) == 0 {
			return fmt.Errorf("specify a collection slug or use --article <slug>")
		}

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

		articles, err := svc.List(cmd.Context(), store.Filter{Collection: slug})
		if err != nil {
			return err
		}
		if len(articles) == 0 {
			fmt.Println("  no articles — add one with: arc collections add <article-slug> " + slug)
			return nil
		}
		for _, a := range articles {
			fmt.Fprintf(cmd.OutOrStdout(), "  %-45s  |  %s\n", a.ID, a.Title)
		}
		return nil
	},
}

var collectionsCreateCmd = &cobra.Command{
	Use:   "create <slug> [description]",
	Short: "Create a new collection",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		slug := args[0]
		if err := validateSlug(slug); err != nil {
			return err
		}
		description := ""
		if len(args) == 2 {
			description = args[1]
		}
		svc := svcFrom(cmd)
		if err := svc.CreateCollection(cmd.Context(), slug, description); err != nil {
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

var collectionsGenerateDescriptionCmd = &cobra.Command{
	Use:   "generate-description <slug>",
	Short: "Generate a description for a collection using AI",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		svc := svcFrom(cmd)
		slug, err := resolveCollectionSlug(cmd, args[0])
		if err != nil {
			return err
		}

		force, _ := cmd.Flags().GetBool("force")
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		edit, _ := cmd.Flags().GetBool("edit")
		profile, _ := cmd.Flags().GetString("profile")

		info, err := svc.GetCollection(cmd.Context(), slug)
		if err != nil {
			return err
		}
		if info.Description != "" && !force {
			fmt.Fprintf(cmd.OutOrStdout(), "%s already has a description: %s\n", slug, info.Description)
			fmt.Fprintln(cmd.OutOrStdout(), "use --force to overwrite")
			return nil
		}

		progress := func(msg string) {
			fmt.Fprintf(cmd.ErrOrStderr(), "  %s\n", msg)
		}

		desc, err := svc.GenerateCollectionDescription(cmd.Context(), slug, profile, progress)
		if err != nil {
			return fmt.Errorf("generate description: %w", err)
		}

		tty := isTTY(os.Stdout)
		fmt.Fprintf(cmd.OutOrStdout(), "%s → %s\n", bold(slug, tty), desc)

		if dryRun {
			return nil
		}

		if edit {
			fmt.Fprint(cmd.OutOrStdout(), "[Enter=accept, type replacement, n=skip]: ")
			scanner := bufio.NewScanner(os.Stdin)
			if !scanner.Scan() {
				return fmt.Errorf("aborted")
			}
			input := strings.TrimSpace(scanner.Text())
			if strings.ToLower(input) == "n" {
				fmt.Fprintln(cmd.OutOrStdout(), "skipped")
				return nil
			}
			if input != "" {
				desc = input
			}
		}

		if err := svc.SetCollectionDescription(cmd.Context(), slug, desc); err != nil {
			return fmt.Errorf("set description: %w", err)
		}
		fmt.Fprintln(cmd.OutOrStdout(), "description saved")
		return nil
	},
}

var collectionsGenerateDescriptionAllCmd = &cobra.Command{
	Use:   "generate-description-all",
	Short: "Generate descriptions for all collections missing one",
	RunE: func(cmd *cobra.Command, args []string) error {
		svc := svcFrom(cmd)
		force, _ := cmd.Flags().GetBool("force")
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		edit, _ := cmd.Flags().GetBool("edit")
		limit, _ := cmd.Flags().GetInt("limit")
		profile, _ := cmd.Flags().GetString("profile")

		cols, err := svc.ListCollections(cmd.Context())
		if err != nil {
			return fmt.Errorf("list collections: %w", err)
		}

		// Filter to collections needing descriptions.
		var pending []service.CollectionInfo
		for _, c := range cols {
			if c.Description == "" || force {
				pending = append(pending, c)
			}
		}
		if len(pending) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "all collections already have descriptions")
			return nil
		}
		if limit > 0 && len(pending) > limit {
			pending = pending[:limit]
		}

		tty := isTTY(os.Stdout)
		generated := 0
		for i, c := range pending {
			progress := func(msg string) {
				fmt.Fprintf(cmd.ErrOrStderr(), "  %s\n", msg)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "[%d/%d] ", i+1, len(pending))

			desc, err := svc.GenerateCollectionDescription(cmd.Context(), c.Slug, profile, progress)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "%s: error: %v\n", c.Slug, err)
				continue
			}

			fmt.Fprintf(cmd.OutOrStdout(), "%s → %s\n", bold(c.Slug, tty), desc)

			if dryRun {
				generated++
				continue
			}

			if edit {
				fmt.Fprint(cmd.OutOrStdout(), "[Enter=accept, type replacement, n=skip]: ")
				scanner := bufio.NewScanner(os.Stdin)
				if !scanner.Scan() {
					return fmt.Errorf("aborted")
				}
				input := strings.TrimSpace(scanner.Text())
				if strings.ToLower(input) == "n" {
					fmt.Fprintln(cmd.OutOrStdout(), "  skipped")
					continue
				}
				if input != "" {
					desc = input
				}
			}

			if err := svc.SetCollectionDescription(cmd.Context(), c.Slug, desc); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "  %s: write error: %v\n", c.Slug, err)
				continue
			}
			fmt.Fprintln(cmd.OutOrStdout(), "  description saved")
			generated++
		}

		action := "generated"
		if dryRun {
			action = "would generate"
		}
		fmt.Fprintf(cmd.OutOrStdout(), "\n%s descriptions for %d/%d collections\n", action, generated, len(pending))
		return nil
	},
}

var collectionsDescribeAllCmd = &cobra.Command{
	Use:   "describe-all",
	Short: "Show descriptions for all collections",
	RunE: func(cmd *cobra.Command, args []string) error {
		svc := svcFrom(cmd)
		cols, err := svc.ListCollections(cmd.Context())
		if err != nil {
			return fmt.Errorf("list collections: %w", err)
		}
		if len(cols) == 0 {
			fmt.Println("no collections")
			return nil
		}
		tty := isTTY(os.Stdout)
		for i, c := range cols {
			desc := c.Description
			if desc == "" {
				desc = "(no description)"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%3d. %s  %s\n", i+1, bold(c.Slug, tty), dim(desc, tty))
		}
		return nil
	},
}

var collectionsSearchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search collections by name or description",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		svc := svcFrom(cmd)
		query := strings.Join(args, " ")

		results, err := svc.SearchCollections(cmd.Context(), query)
		if err != nil {
			return fmt.Errorf("search collections: %w", err)
		}
		if len(results) == 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "no collections matching %q\n", query)
			return nil
		}

		tty := isTTY(os.Stdout)
		for i, c := range results {
			desc := c.Description
			if desc == "" {
				desc = "(no description)"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%3d. %s  %s articles  %s\n",
				i+1, bold(c.Slug, tty), fmt.Sprintf("%3d", c.ArticleCount), dim(desc, tty))
		}
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
With --uncollected: iterate all articles that don't belong to any collection
and suggest where each one should go (one LLM call per article).

By default only prints suggestions and equivalent arc commands — nothing is created.
Use --apply to interactively create collections and link articles.

Examples:
  arc collections suggest                         # library-wide suggestions (print only)
  arc collections suggest 20260115-attention      # per-article suggestions (print only)
  arc collections suggest --apply                 # interactive: accept/skip each suggestion
  arc collections suggest --apply --all           # apply all without prompting
  arc collections suggest --uncollected           # suggest for all uncollected articles
  arc collections suggest --uncollected --apply   # interactive batch organize`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		svc := svcFrom(cmd)
		profile, _ := cmd.Flags().GetString("profile")
		apply, _ := cmd.Flags().GetBool("apply")
		acceptAll, _ := cmd.Flags().GetBool("all")
		uncollected, _ := cmd.Flags().GetBool("uncollected")
		tty := isTTY(os.Stdout)

		progress := func(msg string) {
			fmt.Fprintf(cmd.ErrOrStderr(), "  %s\n", msg)
		}

		// --uncollected mode: iterate all articles with no collections
		if uncollected {
			if len(args) > 0 {
				return fmt.Errorf("cannot specify a slug together with --uncollected")
			}
			pending, err := svc.List(cmd.Context(), store.Filter{Uncollected: true})
			if err != nil {
				return err
			}
			if len(pending) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no uncollected articles")
				return nil
			}
			limit, _ := cmd.Flags().GetInt("limit")
			if limit > 0 && len(pending) > limit {
				pending = pending[:limit]
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "%d uncollected articles\n\n", len(pending))
			for i, a := range pending {
				prefix := fmt.Sprintf("[%d/%d] %s", i+1, len(pending), a.ID)
				fmt.Fprintf(cmd.OutOrStdout(), "%s\n", bold(prefix, tty))
				suggestForArticle(cmd, svc, a.ID, profile, apply, acceptAll, tty, progress)
				fmt.Fprintln(cmd.OutOrStdout())
			}
			return nil
		}

		if len(args) == 0 {
			// Library-wide suggestion (uncollected articles only)
			uncollectedArticles, err := svc.List(cmd.Context(), store.Filter{Uncollected: true})
			if err != nil {
				return fmt.Errorf("list uncollected: %w", err)
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "%d uncollected articles\n", len(uncollectedArticles))
			if len(uncollectedArticles) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no uncollected articles — nothing to suggest")
				return nil
			}
			count, _ := cmd.Flags().GetInt("count")
			minArticles, _ := cmd.Flags().GetInt("min")
			suggestions, err := svc.SuggestCollections(cmd.Context(), profile, count, minArticles, progress)
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
				if s.EstimatedCount > 0 {
					fmt.Fprintf(cmd.OutOrStdout(), "  ~%d articles", s.EstimatedCount)
				}
				if s.Description != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "  %s", dim(s.Description, tty))
				}
				fmt.Fprintln(cmd.OutOrStdout())
				fmt.Fprintf(cmd.OutOrStdout(), "  arc collections create %s %q\n", s.Slug, s.Description)
				fmt.Fprintln(cmd.OutOrStdout())

				if !apply {
					continue
				}

				if acceptAll || promptYN(cmd, fmt.Sprintf("Create %q? [Y/n] ", s.Slug)) {
					if err := svc.CreateCollection(cmd.Context(), s.Slug, s.Description); err != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "  skip (create): %v\n", err)
						continue
					}
					fmt.Fprintf(cmd.OutOrStdout(), "  created %s\n", s.Slug)
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
		return suggestForArticle(cmd, svc, articleSlug, profile, apply, acceptAll, tty, progress)
	},
}

// suggestForArticle runs per-article collection suggestion and optionally applies results.
func suggestForArticle(cmd *cobra.Command, svc *service.Service, articleSlug, profile string, apply, acceptAll, tty bool, progress func(string)) error {
	matches, err := svc.SuggestCollectionsForArticle(cmd.Context(), articleSlug, profile, progress)
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "  error: %v\n", err)
		return nil // non-fatal in batch mode
	}
	if len(matches) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "  no matching collections found")
		return nil
	}
	for _, m := range matches {
		if m.NewSlug != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "  %s  %s\n", bold("(new) "+m.NewSlug, tty), dim(m.Reason, tty))
			fmt.Fprintf(cmd.OutOrStdout(), "    arc collections create %s %q\n", m.NewSlug, m.NewDescription)
			fmt.Fprintf(cmd.OutOrStdout(), "    arc collections add %s %s\n", articleSlug, m.NewSlug)

			if !apply {
				continue
			}

			if acceptAll || promptYN(cmd, fmt.Sprintf("  Create collection %q and add %q? [Y/n] ", m.NewSlug, articleSlug)) {
				if err := svc.CreateCollection(cmd.Context(), m.NewSlug, m.NewDescription); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "    skip (create): %v\n", err)
					continue
				}
				if err := svc.AddToCollection(cmd.Context(), articleSlug, m.NewSlug); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "    skip (add): %v\n", err)
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "    created %s and added %s\n", m.NewSlug, articleSlug)
				}
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "    skipped")
			}
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "  %s  %s\n", bold(m.Slug, tty), dim(m.Reason, tty))
			fmt.Fprintf(cmd.OutOrStdout(), "    arc collections add %s %s\n", articleSlug, m.Slug)

			if !apply {
				continue
			}

			if acceptAll || promptYN(cmd, fmt.Sprintf("  Add %q to collection %q? [Y/n] ", articleSlug, m.Slug)) {
				if err := svc.AddToCollection(cmd.Context(), articleSlug, m.Slug); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "    skip: %v\n", err)
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "    added %s → %s\n", articleSlug, m.Slug)
				}
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "    skipped")
			}
		}
	}
	return nil
}

var collectionsDeleteCmd = &cobra.Command{
	Use:   "delete <slug>",
	Short: "Delete a collection",
	Long: `Delete a collection directory and remove all article symlinks inside it.
Article directories are never touched unless --purge is specified.

--purge: for each article in the collection, if it belongs to NO other collection,
         delete its article directory too. Articles shared with other collections
         are always left untouched.

Example with --purge:
  Collection "ml" contains article-a, article-b, article-c.
  Collection "transformers" also contains article-b.

  arc collections delete ml --purge
    → article-a deleted  (only in "ml")
    → article-b kept     (also in "transformers")
    → article-c deleted  (only in "ml")
    → collection "ml" deleted

Examples:
  arc collections delete old-collection
  arc collections delete old-collection --force
  arc collections delete old-collection --purge`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		slug, err := resolveCollectionSlug(cmd, args[0])
		if err != nil {
			return err
		}

		force, _ := cmd.Flags().GetBool("force")
		purge, _ := cmd.Flags().GetBool("purge")
		tty := isTTY(os.Stdout)

		if !force {
			svc := svcFrom(cmd)
			info, err := svc.GetCollection(cmd.Context(), slug)
			if err != nil {
				return err
			}
			prompt := fmt.Sprintf("Delete collection %q (%d articles)? [y/N] ", slug, info.ArticleCount)
			if purge {
				prompt = fmt.Sprintf("Delete collection %q and purge exclusively-owned articles? [y/N] ", slug)
			}
			fmt.Fprint(cmd.OutOrStdout(), bold(prompt, tty))
			scanner := bufio.NewScanner(os.Stdin)
			if !scanner.Scan() {
				return fmt.Errorf("aborted")
			}
			ans := strings.ToLower(strings.TrimSpace(scanner.Text()))
			if ans != "y" && ans != "yes" {
				fmt.Fprintln(cmd.OutOrStdout(), "aborted")
				return nil
			}
		}

		svc := svcFrom(cmd)
		purged, err := svc.DeleteCollection(cmd.Context(), slug, purge)
		if err != nil {
			return fmt.Errorf("delete collection: %w", err)
		}

		fmt.Fprintf(cmd.OutOrStdout(), "deleted collection: %s\n", slug)
		if len(purged) > 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "purged %d articles:\n", len(purged))
			for _, a := range purged {
				fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", a)
			}
		}
		return nil
	},
}

var collectionsRenameCmd = &cobra.Command{
	Use:   "rename <old-slug> <new-slug>",
	Short: "Rename a collection",
	Long: `Rename a collection by moving its directory to a new slug.

The old slug accepts fuzzy matching (partial name is enough).
The new slug must be an exact valid identifier: lowercase letters,
numbers, and hyphens only — no spaces or special characters.

All article symlinks inside the collection remain valid after the rename.
SQLite is updated atomically.

Examples:
  arc collections rename ml machine-learning
  arc collections rename software software-architecture`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		oldSlug, err := resolveCollectionSlug(cmd, args[0])
		if err != nil {
			return err
		}
		newSlug := args[1]
		if err := validateSlug(newSlug); err != nil {
			return err
		}
		svc := svcFrom(cmd)
		if err := svc.RenameCollection(cmd.Context(), oldSlug, newSlug); err != nil {
			return fmt.Errorf("rename collection: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "renamed: %s → %s\n", oldSlug, newSlug)
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

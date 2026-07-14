package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/spf13/cobra"

	chatengine "github.com/jrniemiec/arc/chat/engine"
	"github.com/jrniemiec/arc/internal/clog"
	"github.com/jrniemiec/arc/service"
	"github.com/jrniemiec/arc/store/fs"
)

func init() {
	rootCmd.AddCommand(workspaceCmd)
	workspaceCmd.AddCommand(workspaceNewCmd)
	workspaceCmd.AddCommand(workspaceListCmd)
	workspaceCmd.AddCommand(workspaceShowCmd)
	workspaceCmd.AddCommand(workspaceRenameCmd)
	workspaceCmd.AddCommand(workspaceDescribeCmd)
	workspaceCmd.AddCommand(workspaceSystemCmd)
	workspaceCmd.AddCommand(workspaceArchiveCmd)
	workspaceCmd.AddCommand(workspaceDeleteCmd)
	workspaceCmd.AddCommand(workspaceAddCmd)
	workspaceCmd.AddCommand(workspaceRemoveCmd)
	workspaceCmd.AddCommand(workspaceOutcomesCmd)
	workspaceCmd.AddCommand(workspacePopulateCmd)

	workspacePopulateCmd.Flags().Bool("dry-run", false, "show suggestions without linking")
	workspacePopulateCmd.Flags().Bool("edit", false, "review each suggestion interactively")
	workspacePopulateCmd.Flags().StringP("profile", "p", "", "LLM profile for selection (default: config or haiku)")
	workspacePopulateCmd.Flags().String("hint", "", "free-form guidance for the LLM (e.g. \"focus on introductory tutorials, max 10\")")
	workspacePopulateCmd.Flags().Bool("include-collections", false, "include collections in selection (default: articles only)")

	workspaceListCmd.Flags().Bool("all", false, "include archived workspaces")
	workspaceDeleteCmd.Flags().Bool("force", false, "skip confirmation prompt")

	workspaceAddCmd.Flags().StringSlice("article", nil, "article slug(s) to add (comma-separated or repeated)")
	workspaceAddCmd.Flags().StringSlice("collection", nil, "collection slug(s) to add")
	workspaceAddCmd.Flags().StringSlice("resource", nil, "file path(s) or URL(s) to add as resources")
	workspaceAddCmd.Flags().String("into", "", "target subdirectory within resources/ for --resource")
	workspaceAddCmd.Flags().String("comment", "", "comment to include in .url resource file")

	workspaceRemoveCmd.Flags().StringSlice("article", nil, "article slug(s) to remove")
	workspaceRemoveCmd.Flags().StringSlice("collection", nil, "collection slug(s) to remove")
	workspaceRemoveCmd.Flags().StringSlice("resource", nil, "resource basename(s) to remove")
	workspaceRemoveCmd.Flags().Bool("all-articles", false, "remove all articles from workspace")
	workspaceRemoveCmd.Flags().Bool("all-collections", false, "remove all collections from workspace")
	workspaceRemoveCmd.Flags().Bool("dry-run", false, "list items that would be removed without removing")

	workspaceOutcomesCmd.Flags().String("read", "", "print this outcome file to stdout")

	workspaceCmd.AddCommand(workspaceChatCmd)
	workspaceCmd.AddCommand(workspaceChatConfigCmd)

	workspaceChatCmd.Flags().StringP("profile", "p", "", "LLM profile to use (overrides chat config)")
	workspaceChatCmd.Flags().String("strategy", "", "context strategy: tail|token-budget|summarize")
	workspaceChatCmd.Flags().Int("context-limit", 0, "token budget override")
	workspaceChatCmd.Flags().Bool("no-stream", false, "disable streaming, print full response at once")
	workspaceChatCmd.Flags().Bool("clear", false, "clear history before starting")
	workspaceChatCmd.Flags().BoolP("debug", "D", false, "print debug info to stderr")

	workspaceChatConfigCmd.Flags().String("profile", "", "set chat profile")
	workspaceChatConfigCmd.Flags().String("strategy", "", "set context strategy: tail|token-budget|summarize")
	workspaceChatConfigCmd.Flags().Int("context-limit", 0, "set token budget (0 = unset)")
	workspaceChatConfigCmd.Flags().Int("max-output-tokens", 0, "cap response length (0 = provider default)")
	workspaceChatConfigCmd.Flags().Int("max-user-messages", 0, "tail strategy: past user turns to keep (0 = default 50)")
	workspaceChatConfigCmd.Flags().String("summarizer-profile", "", "profile for history compaction in summarize strategy")
	workspaceChatConfigCmd.Flags().Float64("verbatim-ratio", 0, "summarize strategy: fraction of budget kept verbatim (0 = default 0.4)")
	workspaceChatConfigCmd.Flags().String("grounding-mode", "", "set grounding mode: corpus-only|corpus-first|open")
	workspaceChatConfigCmd.Flags().Bool("list-modes", false, "list available grounding modes")
}

var workspaceCmd = &cobra.Command{
	Use:   "workspace",
	Short: "Manage workspaces",
	Long: `Create and manage workspaces — persistent research environments that
combine articles, collections, resources, and an LLM conversation.

Examples:
  arc workspace new transformer-research "Evolution of attention mechanisms"
  arc workspace add transformer-research --collection ml --article 20260115-attention
  arc workspace add transformer-research --resource ~/papers/paper.pdf
  arc workspace show transformer-research
  arc workspace list`,
}

// ── new ───────────────────────────────────────────────────────────────────────

var workspaceNewCmd = &cobra.Command{
	Use:   "new <name> [description]",
	Short: "Create a new workspace",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if err := validateWorkspaceName(name); err != nil {
			return err
		}
		description := ""
		if len(args) == 2 {
			description = args[1]
		}
		svc := svcFrom(cmd)
		if err := svc.CreateWorkspace(cmd.Context(), name, description); err != nil {
			return fmt.Errorf("create workspace: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "created workspace: %s\n", name)
		return nil
	},
}

// ── list ──────────────────────────────────────────────────────────────────────

var workspaceListCmd = &cobra.Command{
	Use:   "list",
	Short: "List workspaces",
	RunE: func(cmd *cobra.Command, args []string) error {
		all, _ := cmd.Flags().GetBool("all")
		svc := svcFrom(cmd)
		ws, err := svc.ListWorkspaces(cmd.Context(), all)
		if err != nil {
			return fmt.Errorf("list workspaces: %w", err)
		}
		if len(ws) == 0 {
			if all {
				fmt.Fprintln(cmd.OutOrStdout(), "no workspaces — create one with: arc workspace new <name>")
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "no active workspaces — use --all to include archived")
			}
			return nil
		}
		tty := isTTY(os.Stdout)
		for _, w := range ws {
			status := ""
			if w.Status == "archived" {
				status = "  " + dim("[archived]", tty)
			}
			desc := ""
			if w.Description != "" {
				desc = "  " + dim(truncate(w.Description, 60), tty)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%-30s  %2d articles  %2d collections  %2d resources%s%s\n",
				bold(w.Name, tty), w.ArticleCount, w.CollectionCount, w.ResourceCount, status, desc)
		}
		return nil
	},
}

// ── show ──────────────────────────────────────────────────────────────────────

var workspaceShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show workspace details",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, err := resolveWorkspaceName(cmd, args[0])
		if err != nil {
			return err
		}
		svc := svcFrom(cmd)
		w, err := svc.GetWorkspace(cmd.Context(), name)
		if err != nil {
			return err
		}
		tty := isTTY(os.Stdout)

		fmt.Fprintf(cmd.OutOrStdout(), "workspace: %s  (%s)\n", bold(w.Name, tty), w.Status)
		if w.Description != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "description: %s\n", w.Description)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "created: %s\n", w.CreatedAt.Format(time.DateOnly))
		fmt.Fprintln(cmd.OutOrStdout())

		cfg := cfgFrom(cmd)

		// Articles
		articles, _, _ := fs.ListWorkspaceArticles(cfg.DataRoot, name)
		fmt.Fprintf(cmd.OutOrStdout(), "articles (%d):\n", len(articles))
		if len(articles) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "  (none)")
		} else {
			for _, slug := range articles {
				a, err := svc.GetArticle(cmd.Context(), slug)
				title := slug
				if err == nil && a.Title != "" {
					title = a.Title
				}
				fmt.Fprintf(cmd.OutOrStdout(), "  %-45s  %s\n", slug, dim(truncate(title, 50), tty))
			}
		}
		fmt.Fprintln(cmd.OutOrStdout())

		// Collections
		cols, _ := fs.ListWorkspaceCollections(cfg.DataRoot, name)
		fmt.Fprintf(cmd.OutOrStdout(), "collections (%d):\n", len(cols))
		if len(cols) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "  (none)")
		} else {
			for _, c := range cols {
				fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", c)
			}
		}
		fmt.Fprintln(cmd.OutOrStdout())

		// Resources
		resources, _ := svc.ListWorkspaceResources(cmd.Context(), name)
		fmt.Fprintf(cmd.OutOrStdout(), "resources (%d):\n", len(resources))
		if len(resources) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "  (none)")
		} else {
			for _, r := range resources {
				if r.IsDir {
					fmt.Fprintf(cmd.OutOrStdout(), "  %-30s  %s\n", r.Name+"/", dim("dir", tty))
				} else if r.IsURL {
					fmt.Fprintf(cmd.OutOrStdout(), "  %-30s  %s\n", r.Name, dim("url: "+truncate(r.SrcURL, 60), tty))
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "  %-30s  %s\n", r.Name, dim(formatSize(r.Size), tty))
				}
			}
		}
		fmt.Fprintln(cmd.OutOrStdout())

		// Outcomes
		outcomes, _ := svc.ListWorkspaceOutcomes(cmd.Context(), name)
		fmt.Fprintf(cmd.OutOrStdout(), "outcomes (%d):\n", len(outcomes))
		if len(outcomes) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "  (none)")
		} else {
			for _, o := range outcomes {
				fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", o)
			}
		}
		fmt.Fprintln(cmd.OutOrStdout())

		// System prompt & chat config
		systemStatus := dim("(not set)", tty)
		if w.HasSystem {
			systemStatus = "yes"
		}
		fmt.Fprintf(cmd.OutOrStdout(), "system prompt: %s\n", systemStatus)

		if w.ChatConfig.Profile != "" || w.ChatConfig.Strategy != "" {
			profile := w.ChatConfig.Profile
			if profile == "" {
				profile = dim("(default)", tty)
			}
			strategy := w.ChatConfig.Strategy
			if strategy == "" {
				strategy = dim("(default)", tty)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "chat profile:  %s\n", profile)
			fmt.Fprintf(cmd.OutOrStdout(), "chat strategy: %s\n", strategy)
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "chat config:   %s\n", dim("(default)", tty))
		}

		return nil
	},
}

// ── rename ────────────────────────────────────────────────────────────────────

var workspaceRenameCmd = &cobra.Command{
	Use:   "rename <old-name> <new-name>",
	Short: "Rename a workspace",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		oldName, err := resolveWorkspaceName(cmd, args[0])
		if err != nil {
			return err
		}
		newName := args[1]
		if err := validateWorkspaceName(newName); err != nil {
			return err
		}
		svc := svcFrom(cmd)
		if err := svc.RenameWorkspace(cmd.Context(), oldName, newName); err != nil {
			return fmt.Errorf("rename workspace: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "renamed: %s → %s\n", oldName, newName)
		return nil
	},
}

// ── describe ──────────────────────────────────────────────────────────────────

var workspaceDescribeCmd = &cobra.Command{
	Use:   "describe <name> <text>",
	Short: "Set a description for a workspace",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, err := resolveWorkspaceName(cmd, args[0])
		if err != nil {
			return err
		}
		svc := svcFrom(cmd)
		if err := svc.SetWorkspaceDescription(cmd.Context(), name, args[1]); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "description set for workspace: %s\n", name)
		return nil
	},
}

// ── system ────────────────────────────────────────────────────────────────────

var workspaceSystemCmd = &cobra.Command{
	Use:   "system <name> [text]",
	Short: "Get or set the system prompt for a workspace",
	Long: `With no text argument: print the current system prompt.
With text argument: overwrite the system prompt.

The system prompt is used as the LLM persona/focus when chatting in this workspace.

Examples:
  arc workspace system transformer-research
  arc workspace system transformer-research "Focus on technical precision."`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, err := resolveWorkspaceName(cmd, args[0])
		if err != nil {
			return err
		}
		svc := svcFrom(cmd)

		if len(args) == 1 {
			text, err := svc.GetWorkspaceSystemPrompt(cmd.Context(), name)
			if err != nil {
				return err
			}
			if text == "" {
				fmt.Fprintln(cmd.OutOrStdout(), "(no system prompt)")
				return nil
			}
			fmt.Fprint(cmd.OutOrStdout(), text)
			return nil
		}

		if err := svc.SetWorkspaceSystemPrompt(cmd.Context(), name, args[1]); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "system prompt set for workspace: %s\n", name)
		return nil
	},
}

// ── archive ───────────────────────────────────────────────────────────────────

var workspaceArchiveCmd = &cobra.Command{
	Use:   "archive <name>",
	Short: "Archive a workspace (hide from default list)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, err := resolveWorkspaceName(cmd, args[0])
		if err != nil {
			return err
		}
		svc := svcFrom(cmd)
		if err := svc.ArchiveWorkspace(cmd.Context(), name); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "archived workspace: %s\n", name)
		return nil
	},
}

// ── delete ────────────────────────────────────────────────────────────────────

var workspaceDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a workspace",
	Long: `Delete a workspace directory and all its contents (articles symlinks,
collection symlinks, resources, outcomes, chat history).

Article and collection directories are never touched — only the workspace
membership symlinks and workspace-specific files are removed.

Examples:
  arc workspace delete old-research
  arc workspace delete old-research --force`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, err := resolveWorkspaceName(cmd, args[0])
		if err != nil {
			return err
		}
		force, _ := cmd.Flags().GetBool("force")
		tty := isTTY(os.Stdout)

		if !force {
			svc := svcFrom(cmd)
			w, err := svc.GetWorkspace(cmd.Context(), name)
			if err != nil {
				return err
			}
			prompt := fmt.Sprintf("Delete workspace %q (%d articles, %d resources, %d outcomes)? [y/N] ",
				name, w.ArticleCount, w.ResourceCount, w.OutcomeCount)
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
		if err := svc.DeleteWorkspace(cmd.Context(), name); err != nil {
			return fmt.Errorf("delete workspace: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "deleted workspace: %s\n", name)
		return nil
	},
}

// ── add ───────────────────────────────────────────────────────────────────────

var workspaceAddCmd = &cobra.Command{
	Use:   "add <name> [--article slug,...] [--collection col,...] [--resource path|url,...]",
	Short: "Add articles, collections, or resources to a workspace",
	Long: `Add one or more items to a workspace. Flags can be combined in any order
and accept comma-separated values or can be repeated.

Articles and collections are linked via symlinks. Resources are hard-copied
into the workspace (files) or stored as .url stubs (URLs).

Examples:
  arc workspace add myws --article slug1,slug2 --collection ml
  arc workspace add myws --resource ~/papers/paper.pdf
  arc workspace add myws --resource ~/papers/paper.pdf --into data/raw
  arc workspace add myws --resource https://youtube.com/watch?v=abc`,
	Args: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return fmt.Errorf("workspace name required")
		}
		if len(args) > 1 {
			return fmt.Errorf("unexpected extra arguments: %v\n\nHint: multiple slugs must be comma-separated with no spaces:\n  --article slug1,slug2,slug3\n  --collection col1,col2", args[1:])
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		name, err := resolveWorkspaceName(cmd, args[0])
		if err != nil {
			return err
		}

		articles, _ := cmd.Flags().GetStringSlice("article")
		cols, _ := cmd.Flags().GetStringSlice("collection")
		resources, _ := cmd.Flags().GetStringSlice("resource")

		if len(articles) == 0 && len(cols) == 0 && len(resources) == 0 {
			return fmt.Errorf("specify at least one of --article, --collection, or --resource")
		}

		svc := svcFrom(cmd)
		var errs []string

		if len(articles) > 0 {
			if err := svc.AddArticlesToWorkspace(cmd.Context(), name, articles); err != nil {
				errs = append(errs, err.Error())
			} else {
				for _, a := range articles {
					fmt.Fprintf(cmd.OutOrStdout(), "added article %s → %s\n", a, name)
				}
			}
		}

		if len(cols) > 0 {
			if err := svc.AddCollectionsToWorkspace(cmd.Context(), name, cols); err != nil {
				errs = append(errs, err.Error())
			} else {
				for _, c := range cols {
					fmt.Fprintf(cmd.OutOrStdout(), "added collection %s → %s\n", c, name)
				}
			}
		}

		if len(resources) > 0 {
			into, _ := cmd.Flags().GetString("into")
			comment, _ := cmd.Flags().GetString("comment")
			if err := svc.AddResourcesToWorkspace(cmd.Context(), name, resources, into, comment); err != nil {
				errs = append(errs, err.Error())
			} else {
				for _, r := range resources {
					dest := name
					if into != "" {
						dest = name + "/resources/" + into
					}
					fmt.Fprintf(cmd.OutOrStdout(), "added resource %s → %s\n", r, dest)
				}
			}
		}

		if len(errs) > 0 {
			return fmt.Errorf("%s", strings.Join(errs, "\n"))
		}
		return nil
	},
}

// ── remove ────────────────────────────────────────────────────────────────────

var workspaceRemoveCmd = &cobra.Command{
	Use:   "remove <name> [--article slug,...] [--collection col,...] [--resource basename,...]",
	Short: "Remove articles, collections, or resources from a workspace",
	Long: `Remove one or more items from a workspace. For resources, provide the
basename as shown by 'arc workspace show'.

Use --all-articles or --all-collections to remove all items of that type.
These require confirmation (or --force to skip).

Examples:
  arc workspace remove myws --article slug1
  arc workspace remove myws --collection ml --resource paper.pdf
  arc workspace remove myws --all-articles
  arc workspace remove myws --all-collections`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, err := resolveWorkspaceName(cmd, args[0])
		if err != nil {
			return err
		}
		cfg := cfgFrom(cmd)

		articles, _ := cmd.Flags().GetStringSlice("article")
		cols, _ := cmd.Flags().GetStringSlice("collection")
		resources, _ := cmd.Flags().GetStringSlice("resource")
		allArticles, _ := cmd.Flags().GetBool("all-articles")
		allCollections, _ := cmd.Flags().GetBool("all-collections")
		dryRun, _ := cmd.Flags().GetBool("dry-run")

		// Resolve --all-articles: list all, show, confirm.
		if allArticles {
			linked, _, _ := fs.ListWorkspaceArticles(cfg.DataRoot, name)
			if len(linked) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no articles linked to workspace")
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "Articles to remove from %s:\n", name)
				for _, a := range linked {
					fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", a)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "\nRemove all %d articles? (yes/N): ", len(linked))
				scanner := bufio.NewScanner(os.Stdin)
				if !scanner.Scan() || strings.ToLower(strings.TrimSpace(scanner.Text())) != "yes" {
					fmt.Fprintln(cmd.OutOrStdout(), "aborted")
					return nil
				}
				articles = append(articles, linked...)
			}
		}

		// Resolve --all-collections: list all, show, confirm.
		if allCollections {
			linked, _ := fs.ListWorkspaceCollections(cfg.DataRoot, name)
			if len(linked) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no collections linked to workspace")
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "Collections to remove from %s:\n", name)
				for _, c := range linked {
					fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", c)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "\nRemove all %d collections? (yes/N): ", len(linked))
				scanner := bufio.NewScanner(os.Stdin)
				if !scanner.Scan() || strings.ToLower(strings.TrimSpace(scanner.Text())) != "yes" {
					fmt.Fprintln(cmd.OutOrStdout(), "aborted")
					return nil
				}
				cols = append(cols, linked...)
			}
		}

		if len(articles) == 0 && len(cols) == 0 && len(resources) == 0 {
			return fmt.Errorf("specify at least one of --article, --collection, --resource, --all-articles, or --all-collections")
		}

		total := len(articles) + len(cols) + len(resources)
		clog.Info("workspace remove",
			"workspace", name,
			"articles", len(articles),
			"collections", len(cols),
			"resources", len(resources),
			"dry_run", dryRun,
		)

		if dryRun {
			fmt.Fprintf(cmd.OutOrStdout(), "\nwould remove %d items from workspace %s\n", total, name)
			clog.Info("workspace remove dry-run complete",
				"workspace", name,
				"would_remove", total,
			)
			return nil
		}

		svc := svcFrom(cmd)
		var errs []string
		removed := 0

		if len(articles) > 0 {
			if err := svc.RemoveArticlesFromWorkspace(cmd.Context(), name, articles); err != nil {
				errs = append(errs, err.Error())
			} else {
				removed += len(articles)
			}
		}

		if len(cols) > 0 {
			if err := svc.RemoveCollectionsFromWorkspace(cmd.Context(), name, cols); err != nil {
				errs = append(errs, err.Error())
			} else {
				removed += len(cols)
			}
		}

		if len(resources) > 0 {
			if err := svc.RemoveResourcesFromWorkspace(cmd.Context(), name, resources); err != nil {
				errs = append(errs, err.Error())
			} else {
				removed += len(resources)
			}
		}

		if len(errs) > 0 {
			return fmt.Errorf("%s", strings.Join(errs, "\n"))
		}

		fmt.Fprintf(cmd.OutOrStdout(), "✓ removed %d items from workspace %s\n", removed, name)
		clog.Info("workspace remove complete",
			"workspace", name,
			"removed", removed,
		)
		return nil
	},
}

// ── outcomes ──────────────────────────────────────────────────────────────────

var workspaceOutcomesCmd = &cobra.Command{
	Use:   "outcomes <name>",
	Short: "List or read outcomes for a workspace",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, err := resolveWorkspaceName(cmd, args[0])
		if err != nil {
			return err
		}
		readFile, _ := cmd.Flags().GetString("read")
		svc := svcFrom(cmd)

		if readFile != "" {
			text, err := svc.ReadWorkspaceOutcome(cmd.Context(), name, readFile)
			if err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), text)
			return nil
		}

		outcomes, err := svc.ListWorkspaceOutcomes(cmd.Context(), name)
		if err != nil {
			return err
		}
		if len(outcomes) == 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "no outcomes in workspace %q\n", name)
			return nil
		}
		for _, o := range outcomes {
			fmt.Fprintln(cmd.OutOrStdout(), o)
		}
		return nil
	},
}

// ── chat ──────────────────────────────────────────────────────────────────────

var workspaceChatCmd = &cobra.Command{
	Use:   "chat <name>",
	Short: "Start an interactive chat session in a workspace",
	Long: `Start an interactive LLM chat session grounded in the workspace corpus.

The system prompt is loaded from workspace system.txt. History persists across
sessions in chat/history.json. Use /help inside the session for available commands.

Grounding modes control how the LLM uses the workspace corpus:
  corpus-only    Answer only from workspace articles; no general knowledge
  corpus-first   Start with articles, extend with general knowledge (default)
  open           Use everything: articles, library, general knowledge

Set via: arc workspace chat-config <name> --grounding-mode <mode>
List all: arc workspace chat-config <name> --list-modes

Examples:
  arc workspace chat production-agents
  arc workspace chat myws --profile opus
  arc workspace chat myws --strategy token-budget --context-limit 8000`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, err := resolveWorkspaceName(cmd, args[0])
		if err != nil {
			return err
		}

		cfg := cfgFrom(cmd)
		profileFlag, _ := cmd.Flags().GetString("profile")
		stratFlag, _ := cmd.Flags().GetString("strategy")
		limitFlag, _ := cmd.Flags().GetInt("context-limit")
		noStream, _ := cmd.Flags().GetBool("no-stream")
		clearHist, _ := cmd.Flags().GetBool("clear")
		debug, _ := cmd.Flags().GetBool("debug")

		eng, err := chatengine.New(cfg, name, profileFlag)
		if err != nil {
			return fmt.Errorf("chat engine: %w", err)
		}

		if clearHist {
			if err := eng.ClearHistory(); err != nil {
				return fmt.Errorf("clear history: %w", err)
			}
		}

		// Print session header.
		profile := eng.Profile()
		chatCfg, _ := fs.ReadChatConfig(cfg.DataRoot, name)
		effectiveStrategy := stratFlag
		if effectiveStrategy == "" {
			effectiveStrategy = chatCfg.Strategy
		}
		if effectiveStrategy == "" {
			effectiveStrategy = "tail"
		}
		fmt.Fprintf(os.Stderr, "Workspace: %s  Profile: %s (%s/%s)  Strategy: %s\n",
			name, eng.ProfileName(), profile.Provider, profile.Model, effectiveStrategy)
		if sys := eng.SystemPrompt(); sys != "" {
			preview := strings.TrimSpace(sys)
			if len(preview) > 80 {
				preview = preview[:80] + "…"
			}
			fmt.Fprintf(os.Stderr, "System: %q\n", preview)
		}
		fmt.Fprintln(os.Stderr, "Type /help for commands, Ctrl+D or /exit to quit.")
		fmt.Fprintln(os.Stderr)

		scanner := bufio.NewScanner(os.Stdin)
		for {
			fmt.Fprint(os.Stderr, "You> ")
			if !scanner.Scan() {
				fmt.Fprintln(os.Stderr)
				break
			}
			input := strings.TrimSpace(scanner.Text())
			if input == "" {
				continue
			}

			// Slash commands.
			if strings.HasPrefix(input, "/") {
				parts := strings.SplitN(input, " ", 2)
				switch parts[0] {
				case "/exit", "/quit":
					fmt.Fprintln(os.Stderr, "Bye.")
					return nil
				case "/clear":
					if err := eng.ClearHistory(); err != nil {
						fmt.Fprintf(os.Stderr, "error: %v\n", err)
					} else {
						fmt.Fprintln(os.Stderr, "History cleared.")
					}
					continue
				case "/system":
					sys := eng.SystemPrompt()
					if sys == "" {
						fmt.Fprintln(os.Stderr, "(no system prompt)")
					} else {
						fmt.Fprintln(os.Stderr, sys)
					}
					continue
				case "/history":
					h := eng.History()
					if len(h.Msgs) == 0 {
						fmt.Fprintln(os.Stderr, "(no history)")
					} else {
						for _, m := range h.Msgs {
							fmt.Fprintf(os.Stderr, "[%s] %s\n", m.Role, m.Content)
						}
					}
					continue
				case "/stats":
					in, out, cost := eng.SessionStats()
					line := fmt.Sprintf("Session: %d in / %d out tokens", in, out)
					if cost > 0 {
						line += fmt.Sprintf(" | $%.4f", cost)
					}
					fmt.Fprintln(os.Stderr, line)
					continue
				case "/save":
					filename := ""
					if len(parts) > 1 {
						filename = strings.TrimSpace(parts[1])
					}
					if filename == "" {
						filename = time.Now().Format("2006-01-02-150405") + ".md"
					}
					if err := saveChatOutcome(cfg.DataRoot, name, filename, eng); err != nil {
						fmt.Fprintf(os.Stderr, "save: %v\n", err)
					} else {
						fmt.Fprintf(os.Stderr, "saved to outcomes/%s\n", filename)
					}
					continue
				case "/help":
					fmt.Fprint(os.Stderr, chatHelp)
					continue
				default:
					fmt.Fprintf(os.Stderr, "unknown command %q — type /help\n", parts[0])
					continue
				}
			}

			opts := chatengine.ChatOptions{
				NoStream:         noStream,
				StrategyOverride: stratFlag,
				BudgetOverride:   limitFlag,
				Out:              os.Stderr,
				Debug:            debug,
			}

			ctx, cancel := interruptCtx()
			var result chatengine.ChatResult
			var chatErr error

			if noStream {
				result, chatErr = eng.Chat(ctx, input, opts, nil)
				cancel()
				if chatErr == nil {
					h := eng.History()
					if len(h.Msgs) > 0 {
						last := h.Msgs[len(h.Msgs)-1]
						fmt.Println(last.Content)
					}
				}
			} else {
				fmt.Println() // blank line before response
				result, chatErr = eng.Chat(ctx, input, opts, func(delta string) error {
					fmt.Print(delta)
					return nil
				})
				cancel()
				fmt.Println() // newline after streamed response
			}

			if chatErr != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", chatErr)
				continue
			}

			// Per-turn stats.
			u := result.Usage
			line := fmt.Sprintf("[%s · %d in · %d out", eng.ProfileName(), u.InputTokens, u.OutputTokens)
			if cost := cfg.CalcCost(eng.Profile().Model, u.InputTokens, u.OutputTokens); cost > 0 {
				line += fmt.Sprintf(" · $%.4f", cost)
			}
			line += fmt.Sprintf(" · %dms]", result.Elapsed.Milliseconds())
			fmt.Fprintln(os.Stderr, line)
		}
		return nil
	},
}

const chatHelp = `Commands:
  /exit, /quit   end session
  /clear         clear conversation history
  /system        print system prompt (includes corpus map)
  /history       print conversation history
  /stats         show session token usage and cost
  /save [file]   save session to outcomes/<file>.md
  /help          show this help

Grounding modes (set via: arc workspace chat-config <name> --grounding-mode <mode>):
  corpus-only    answer only from workspace articles; no general knowledge
  corpus-first   start with articles, extend with general knowledge (default)
  open           use everything: articles, library, general knowledge
`

// saveChatOutcome writes the conversation history as a markdown file to outcomes/.
func saveChatOutcome(dataRoot, workspaceName, filename string, eng *chatengine.Engine) error {
	svc := &struct{ dataRoot, workspace string }{dataRoot, workspaceName}
	_ = svc // placeholder for direct fs write

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# Chat session — %s\n\n", time.Now().Format("2006-01-02")))
	sb.WriteString(fmt.Sprintf("**Workspace:** %s  \n", workspaceName))
	sb.WriteString(fmt.Sprintf("**Profile:** %s (%s)  \n\n", eng.ProfileName(), eng.Profile().Model))
	sb.WriteString("---\n\n")

	for _, m := range eng.History().Msgs {
		switch m.Role {
		case "user":
			sb.WriteString("**You:** ")
		case "assistant":
			sb.WriteString("**Assistant:** ")
		default:
			sb.WriteString(fmt.Sprintf("**%s:** ", m.Role))
		}
		sb.WriteString(m.Content)
		sb.WriteString("\n\n")
	}

	outcomesDir := fmt.Sprintf("%s/workspaces/%s/outcomes", dataRoot, workspaceName)
	if err := os.MkdirAll(outcomesDir, 0755); err != nil {
		return err
	}
	return os.WriteFile(fmt.Sprintf("%s/%s", outcomesDir, filename), []byte(sb.String()), 0644)
}

// interruptCtx returns a context cancelled on SIGINT.
func interruptCtx() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt)
		select {
		case <-c:
			cancel()
		case <-ctx.Done():
		}
		signal.Stop(c)
	}()
	return ctx, cancel
}

// ── chat-config ───────────────────────────────────────────────────────────────

var workspaceChatConfigCmd = &cobra.Command{
	Use:   "chat-config <name>",
	Short: "Show or set workspace chat configuration",
	Long: `Show the current chat configuration for a workspace, or set fields.

Examples:
  arc workspace chat-config myws
  arc workspace chat-config myws --profile opus
  arc workspace chat-config myws --strategy token-budget --context-limit 8000`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// Handle --list-modes before anything else (doesn't need a workspace).
		if listModes, _ := cmd.Flags().GetBool("list-modes"); listModes {
			w := cmd.OutOrStdout()
			fmt.Fprintln(w, "Available grounding modes:")
			fmt.Fprintln(w)
			fmt.Fprintf(w, "  %-16s %s\n", "corpus-only", "Answer only from workspace articles; no general knowledge")
			fmt.Fprintf(w, "  %-16s %s\n", "corpus-first", "Start with articles, extend with general knowledge (default)")
			fmt.Fprintf(w, "  %-16s %s\n", "open", "Use everything: articles, library, general knowledge, web")
			return nil
		}

		name, err := resolveWorkspaceName(cmd, args[0])
		if err != nil {
			return err
		}
		cfg := cfgFrom(cmd)

		profileFlag, _ := cmd.Flags().GetString("profile")
		stratFlag, _ := cmd.Flags().GetString("strategy")
		limitFlag, _ := cmd.Flags().GetInt("context-limit")
		maxOutputFlag, _ := cmd.Flags().GetInt("max-output-tokens")
		maxUserFlag, _ := cmd.Flags().GetInt("max-user-messages")
		summProfileFlag, _ := cmd.Flags().GetString("summarizer-profile")
		verbatimFlag, _ := cmd.Flags().GetFloat64("verbatim-ratio")
		groundingModeFlag, _ := cmd.Flags().GetString("grounding-mode")
		anySet := cmd.Flags().Changed("profile") || cmd.Flags().Changed("strategy") ||
			cmd.Flags().Changed("context-limit") || cmd.Flags().Changed("max-output-tokens") ||
			cmd.Flags().Changed("max-user-messages") || cmd.Flags().Changed("summarizer-profile") ||
			cmd.Flags().Changed("verbatim-ratio") || cmd.Flags().Changed("grounding-mode")

		if anySet {
			chatCfg, _ := fs.ReadChatConfig(cfg.DataRoot, name)
			if cmd.Flags().Changed("profile") {
				chatCfg.Profile = profileFlag
			}
			if cmd.Flags().Changed("strategy") {
				chatCfg.Strategy = stratFlag
			}
			if cmd.Flags().Changed("context-limit") {
				chatCfg.ContextLimit = limitFlag
			}
			if cmd.Flags().Changed("max-output-tokens") {
				chatCfg.MaxOutputTokens = maxOutputFlag
			}
			if cmd.Flags().Changed("max-user-messages") {
				chatCfg.MaxUserMessages = maxUserFlag
			}
			if cmd.Flags().Changed("summarizer-profile") {
				chatCfg.SummarizerProfile = summProfileFlag
			}
			if cmd.Flags().Changed("verbatim-ratio") {
				chatCfg.VerbatimRatio = verbatimFlag
			}
			if cmd.Flags().Changed("grounding-mode") {
				chatCfg.GroundingMode = groundingModeFlag
			}
			if err := fs.WriteChatConfig(cfg.DataRoot, name, chatCfg); err != nil {
				return fmt.Errorf("write chat config: %w", err)
			}
		}

		chatCfg, _ := fs.ReadChatConfig(cfg.DataRoot, name)

		// Resolve effective profile (same chain as engine.New).
		effectiveProfile := chatCfg.Profile
		profileSource := ""
		if effectiveProfile == "" {
			effectiveProfile = cfg.Ingest.FlashProfile
			profileSource = " (from ingest.flash_profile)"
		}
		if effectiveProfile == "" {
			for k := range cfg.Profiles {
				effectiveProfile = k
				break
			}
			profileSource = " (first available profile)"
		}
		profileLine := effectiveProfile
		if chatCfg.Profile == "" {
			profileLine = effectiveProfile + profileSource
		}
		if p, ok := cfg.Profiles[effectiveProfile]; ok {
			profileLine += fmt.Sprintf("  (%s/%s)", p.Provider, p.Model)
		}

		effectiveStrategy := chatCfg.Strategy
		if effectiveStrategy == "" {
			effectiveStrategy = "tail"
		}
		strategyLine := effectiveStrategy
		if chatCfg.Strategy == "" {
			strategyLine += "  (default)"
		}

		limitLine := "(only used with token-budget or summarize strategy)"
		if chatCfg.ContextLimit > 0 {
			limitLine = fmt.Sprintf("%d tokens", chatCfg.ContextLimit)
		}

		maxOutLine := "provider default (4096)"
		if chatCfg.MaxOutputTokens > 0 {
			maxOutLine = fmt.Sprintf("%d tokens", chatCfg.MaxOutputTokens)
		}

		maxUserLine := "50  (default)"
		if chatCfg.MaxUserMessages > 0 {
			maxUserLine = fmt.Sprintf("%d", chatCfg.MaxUserMessages)
		}

		summProfileLine := "(same as profile)"
		if chatCfg.SummarizerProfile != "" {
			summProfileLine = chatCfg.SummarizerProfile
		}

		verbatimLine := "0.40  (default)"
		if chatCfg.VerbatimRatio > 0 {
			verbatimLine = fmt.Sprintf("%.2f", chatCfg.VerbatimRatio)
		}

		w := cmd.OutOrStdout()
		fmt.Fprintf(w, "  %-22s %s\n", "profile", profileLine)
		fmt.Fprintf(w, "  %-22s %s\n", "", "LLM profile for this workspace (must match a key in profiles)")
		fmt.Fprintln(w)
		fmt.Fprintf(w, "  %-22s %s\n", "strategy", strategyLine)
		fmt.Fprintf(w, "  %-22s %s\n", "", "tail | token-budget | summarize")
		fmt.Fprintln(w)
		fmt.Fprintf(w, "  %-22s %s\n", "context_limit", limitLine)
		fmt.Fprintf(w, "  %-22s %s\n", "", "token budget for token-budget/summarize strategies; 0 = no limit")
		fmt.Fprintln(w)
		fmt.Fprintf(w, "  %-22s %s\n", "max_output_tokens", maxOutLine)
		fmt.Fprintf(w, "  %-22s %s\n", "", "cap on response length; 0 = provider default (4096)")
		fmt.Fprintln(w)
		fmt.Fprintf(w, "  %-22s %s\n", "max_user_messages", maxUserLine)
		fmt.Fprintf(w, "  %-22s %s\n", "", "tail strategy: number of past user turns to include")
		fmt.Fprintln(w)
		fmt.Fprintf(w, "  %-22s %s\n", "summarizer_profile", summProfileLine)
		fmt.Fprintf(w, "  %-22s %s\n", "", "profile used for history compaction (summarize strategy only)")
		fmt.Fprintln(w)
		fmt.Fprintf(w, "  %-22s %s\n", "verbatim_ratio", verbatimLine)
		fmt.Fprintf(w, "  %-22s %s\n", "", "summarize strategy: fraction of budget kept as verbatim recent turns")
		fmt.Fprintln(w)

		groundingModeLine := chatCfg.GroundingMode
		if groundingModeLine == "" {
			groundingModeLine = "corpus-first  (default)"
		}
		fmt.Fprintf(w, "  %-22s %s\n", "grounding_mode", groundingModeLine)
		fmt.Fprintf(w, "  %-22s %s\n", "", "corpus-only | corpus-first | open — use --list-modes to see descriptions")
		return nil
	},
}

// ── helpers ───────────────────────────────────────────────────────────────────

// validateWorkspaceName ensures a workspace name is filesystem-safe.
func validateWorkspaceName(name string) error {
	if name == "" {
		return fmt.Errorf("workspace name cannot be empty")
	}
	if strings.ContainsAny(name, "/ \\:*?\"<>|") {
		return fmt.Errorf("workspace name %q contains invalid characters — use letters, numbers, and hyphens only", name)
	}
	if strings.HasPrefix(name, ".") {
		return fmt.Errorf("workspace name cannot start with a dot")
	}
	return nil
}

// ── Populate ─────────────────────────────────────────────────────────────────

var workspacePopulateCmd = &cobra.Command{
	Use:   "populate <name>",
	Short: "LLM-assisted workspace population from library",
	Long: `Selects articles for a workspace using a two-pass LLM flow.

Pass 1 shortlists article candidates from titles.
Pass 2 refines candidates using flash summaries for precise final selection.

By default only articles are considered. Use --include-collections to also
select collections (useful for broad research workspaces).

The LLM infers the appropriate number of results from the workspace name and
description. Use --hint to provide additional guidance — for example, to focus
on specific topics or limit the number of results.

Re-runs automatically exclude items already linked to the workspace.
The workspace must have a description set (arc workspace describe <name> <text>).

Workflow: run with --dry-run first, refine with --hint, then run for real.

Examples:
  arc workspace populate myws --dry-run
  arc workspace populate myws --dry-run --hint "introductory only, max 10"
  arc workspace populate myws --hint "focus on practical tutorials"
  arc workspace populate myws --edit
  arc workspace populate myws --include-collections`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		svc := svcFrom(cmd)
		name := args[0]

		dryRun, _ := cmd.Flags().GetBool("dry-run")
		edit, _ := cmd.Flags().GetBool("edit")
		profile, _ := cmd.Flags().GetString("profile")
		hint, _ := cmd.Flags().GetString("hint")
		includeCols, _ := cmd.Flags().GetBool("include-collections")

		tty := isTTY(os.Stdout)
		var progressLog []string
		progress := func(msg string) {
			fmt.Fprintf(cmd.ErrOrStderr(), "  %s\n", msg)
			progressLog = append(progressLog, msg)
		}

		result, err := svc.PopulateWorkspace(cmd.Context(), service.PopulateRequest{
			Workspace:          name,
			Profile:            profile,
			Hint:               hint,
			IncludeCollections: includeCols,
			Progress:           progress,
		})
		if err != nil {
			return err
		}

		// Interactive edit: review each suggestion.
		if edit {
			scanner := bufio.NewScanner(os.Stdin)

			if len(result.Collections) > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "\n%s\n\n", bold("Collections:", tty))
				var accepted []service.PopulateSuggestion
				for _, c := range result.Collections {
					fmt.Fprintf(cmd.OutOrStdout(), "  %s (%d articles)\n", c.Slug, c.ArticleCount)
					if c.Display != "" {
						fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", c.Display)
					}
					fmt.Fprint(cmd.OutOrStdout(), "  [Enter=accept, n=skip]: ")
					if !scanner.Scan() {
						return fmt.Errorf("aborted")
					}
					if strings.ToLower(strings.TrimSpace(scanner.Text())) == "n" {
						fmt.Fprintln(cmd.OutOrStdout(), "– skipped")
					} else {
						fmt.Fprintln(cmd.OutOrStdout(), "✓ accepted")
						accepted = append(accepted, c)
					}
					fmt.Fprintln(cmd.OutOrStdout())
				}
				result.Collections = accepted
			}

			if len(result.Articles) > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "\n%s\n\n", bold("Articles:", tty))
				var accepted []service.PopulateSuggestion
				for _, a := range result.Articles {
					fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", a.Slug)
					if a.Display != "" {
						fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", a.Display)
					}
					fmt.Fprint(cmd.OutOrStdout(), "  [Enter=accept, n=skip]: ")
					if !scanner.Scan() {
						return fmt.Errorf("aborted")
					}
					if strings.ToLower(strings.TrimSpace(scanner.Text())) == "n" {
						fmt.Fprintln(cmd.OutOrStdout(), "– skipped")
					} else {
						fmt.Fprintln(cmd.OutOrStdout(), "✓ accepted")
						accepted = append(accepted, a)
					}
					fmt.Fprintln(cmd.OutOrStdout())
				}
				result.Articles = accepted
			}
		}

		// Build formatted output for display, scratch, and log.
		output := formatPopulateOutput(result, dryRun, edit, hint, progressLog)
		fmt.Fprint(cmd.OutOrStdout(), output)

		// Save to workspace scratch.
		cfg := cfgFrom(cmd)
		if err := fs.AppendScratch(cfg.DataRoot, name, output); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "  warning: write scratch: %v\n", err)
		}

		// Log the full output.
		clog.Raw("populate output", output)

		// Apply: link accepted items to workspace.
		if !dryRun {
			colSlugs := make([]string, len(result.Collections))
			for i, c := range result.Collections {
				colSlugs[i] = c.Slug
			}
			artSlugs := make([]string, len(result.Articles))
			for i, a := range result.Articles {
				artSlugs[i] = a.Slug
			}
			if len(colSlugs) > 0 {
				if err := svc.AddCollectionsToWorkspace(cmd.Context(), name, colSlugs); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "  warning: add collections: %v\n", err)
				}
			}
			if len(artSlugs) > 0 {
				if err := svc.AddArticlesToWorkspace(cmd.Context(), name, artSlugs); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "  warning: add articles: %v\n", err)
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Linked to workspace %s\n", bold(name, tty))
		}

		return nil
	},
}

func formatPopulateOutput(result service.PopulateResult, dryRun, edit bool, hint string, progressLog []string) string {
	var sb strings.Builder

	header := "### populate"
	if dryRun {
		header = "### populate dry-run"
	}
	sb.WriteString(header + "\n")
	if hint != "" {
		sb.WriteString(fmt.Sprintf("hint: %s\n", hint))
	}
	sb.WriteString("\n")

	for _, msg := range progressLog {
		sb.WriteString(fmt.Sprintf("  %s\n", msg))
	}
	if len(progressLog) > 0 {
		sb.WriteString("\n")
	}

	if len(result.Collections) > 0 {
		sb.WriteString("**Collections:**\n\n")
		for _, c := range result.Collections {
			if c.ArticleCount > 0 {
				sb.WriteString(fmt.Sprintf("  %s (%d articles)\n", c.Slug, c.ArticleCount))
			} else {
				sb.WriteString(fmt.Sprintf("  %s\n", c.Slug))
			}
			if c.Display != "" {
				sb.WriteString(fmt.Sprintf("  %s\n", c.Display))
			}
			sb.WriteString("\n")
		}
	}

	if len(result.Articles) > 0 {
		sb.WriteString("**Articles:**\n\n")
		for _, a := range result.Articles {
			sb.WriteString(fmt.Sprintf("  %s\n", a.Slug))
			if a.Display != "" {
				sb.WriteString(fmt.Sprintf("  %s\n", a.Display))
			}
			sb.WriteString("\n")
		}
	}

	label := "Suggested:"
	if edit {
		label = "Accepted:"
	}
	sb.WriteString(fmt.Sprintf("%s %d collections, %d articles (cost: $%.4f)\n", label, len(result.Collections), len(result.Articles), result.CostUSD))

	return sb.String()
}

// formatSize returns a human-readable file size string.
func formatSize(size int64) string {
	switch {
	case size >= 1024*1024:
		return fmt.Sprintf("%.1f MB", float64(size)/1024/1024)
	case size >= 1024:
		return fmt.Sprintf("%.1f KB", float64(size)/1024)
	default:
		return fmt.Sprintf("%d B", size)
	}
}

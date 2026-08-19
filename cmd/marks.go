package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(unreadCmd, favoriteCmd, unfavoriteCmd)
}

var unreadCmd = &cobra.Command{
	Use:   "unread <slug>",
	Short: "Mark an article as unread",
	Long: `Clear the read mark on an article.

'arc read' sets the mark as a side effect of printing the body or summary, so
opening something by mistake marked it read with no way to undo it.

The mark is written to the article's meta.json, so it survives a database
rebuild and travels with the tree to your other machines.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		slug, err := resolveSlug(cmd, args[0])
		if err != nil {
			return err
		}
		if err := svcFrom(cmd).MarkUnread(cmd.Context(), slug); err != nil {
			return fmt.Errorf("mark unread: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "marked unread: %s\n", slug)
		return nil
	},
}

var favoriteCmd = &cobra.Command{
	Use:     "favorite <slug>",
	Aliases: []string{"fav"},
	Short:   "Mark an article as a favorite",
	Long: `Mark an article as a favorite.

List them again with 'arc list --favorites', which pairs with --slugs for
scripting:

  arc list --favorites --slugs | xargs -n1 arc read --summary`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		slug, err := resolveSlug(cmd, args[0])
		if err != nil {
			return err
		}
		if err := svcFrom(cmd).MarkFavorite(cmd.Context(), slug); err != nil {
			return fmt.Errorf("mark favorite: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "favorited: %s\n", slug)
		return nil
	},
}

var unfavoriteCmd = &cobra.Command{
	Use:     "unfavorite <slug>",
	Aliases: []string{"unfav"},
	Short:   "Remove an article from favorites",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		slug, err := resolveSlug(cmd, args[0])
		if err != nil {
			return err
		}
		if err := svcFrom(cmd).UnmarkFavorite(cmd.Context(), slug); err != nil {
			return fmt.Errorf("unfavorite: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "unfavorited: %s\n", slug)
		return nil
	},
}

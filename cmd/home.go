package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(homeCmd)
}

var homeCmd = &cobra.Command{
	Use:   "home",
	Short: "Print the active arc data root",
	Long: `Print the active arc data root directory.

Reflects ARC_HOME env var and --data-root flag, so it always shows
what the running instance is actually using.

Examples:
  arc home
  ARC_HOME=~/.arc-exp arc home
  cd $(arc home)`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg := cfgFrom(cmd)
		fmt.Fprintln(cmd.OutOrStdout(), cfg.DataRoot)
		return nil
	},
}

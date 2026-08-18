package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/jrniemiec/arc/gitsync"
)

var xlockTakeForce bool

func init() {
	xlockTakeCmd.Flags().BoolVar(&xlockTakeForce, "force", false,
		"skip the confirmation when the holder's timer has not elapsed")
	xlockCmd.AddCommand(xlockTakeCmd, xlockReleaseCmd)
	rootCmd.AddCommand(xlockCmd)
}

var xlockCmd = &cobra.Command{
	Use:   "xlock",
	Short: "Manage write authority across machines",
	Long: `Manage write authority in multi-client mode.

The xlock records which machine may write. It is not a mutex — with a single user
nobody ever waits on it. It answers one question: whose copy is authoritative.

It is taken automatically on your first write and released when this machine has
been idle past its timeout, so these commands are rarely needed. Use 'take' to
grab it from a machine you know is off, and 'release' to hand over immediately
rather than waiting out the timer.

State is reported by 'arc sync status' and the TUI banner.`,
}

var xlockTakeCmd = &cobra.Command{
	Use:   "take",
	Short: "Seize the xlock from another machine",
	Long: `Seize the xlock from another machine.

Silent when the holder's idle timer has already elapsed — that machine is idle,
released without pushing, or dead, and all three mean it is not working.

While the timer is still live this confirms first, because seizing strands any
work the other machine had not pushed. arc cannot reach that machine to check.

--force skips the confirmation, for non-interactive use.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		svc := svcFrom(cmd)
		ctx := cmd.Context()
		out := cmd.OutOrStdout()

		if !svc.Sync().Enabled() {
			return errors.New("not in multi-client mode")
		}

		err := svc.Sync().Take(ctx, xlockTakeForce)
		var blocked *gitsync.Blocked
		if errors.As(err, &blocked) {
			fmt.Fprintf(out, "%s holds the xlock (seizable in %s).\n",
				blocked.Holder, blocked.SeizableIn.Round(time.Second))
			fmt.Fprintf(out, "Taking it strands any unpushed work on %s.\n", blocked.Holder)
			if !confirm(cmd, "Take it?") {
				fmt.Fprintln(out, "cancelled")
				return nil
			}
			err = svc.Sync().Take(ctx, true)
		}
		if err != nil {
			return err
		}
		fmt.Fprintln(out, "xlock: this machine")
		return nil
	},
}

var xlockReleaseCmd = &cobra.Command{
	Use:   "release",
	Short: "Hand the xlock over now",
	Long: `Push outstanding work, clear the holder, and push again.

Rarely needed — the idle timer releases the xlock on its own. This is for "I am
done here, hand over now" without waiting the timeout out.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		svc := svcFrom(cmd)
		if !svc.Sync().Enabled() {
			return errors.New("not in multi-client mode")
		}
		if err := svc.Sync().Release(cmd.Context()); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "xlock: free")
		return nil
	},
}

// confirm asks a yes/no question, defaulting to no.
func confirm(cmd *cobra.Command, prompt string) bool {
	fmt.Fprintf(cmd.OutOrStdout(), "%s [y/N] ", prompt)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}

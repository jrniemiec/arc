package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/jrniemiec/arc/service"
	arctui "github.com/jrniemiec/arc/tui"
)

func init() {
	rootCmd.AddCommand(tuiCmd)
	tuiCmd.Flags().String("theme", "auto", "color theme: auto|light|dark")
}

var tuiCmd = &cobra.Command{
	Use:   "tui",
	Short: "Launch the arc terminal UI",
	Long:  `Open arc's full terminal interface — browse, search, chat, and manage your knowledge base.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTUI(cmd)
	},
}

// requireCapableTerminal rejects launching the TUI on a terminal that can't
// support it: not a tty (piped/redirected), or a tty with no usable cursor
// addressing (TERM unset or "dumb" — e.g. Emacs' M-x shell, which allocates
// a real pty via comint but doesn't implement raw-mode escapes).
func requireCapableTerminal() error {
	if !isTTY(os.Stdout) || !isTTY(os.Stdin) {
		return fmt.Errorf("arc's TUI requires an interactive terminal (stdin/stdout is not a tty)")
	}
	if term := os.Getenv("TERM"); term == "" || term == "dumb" {
		return fmt.Errorf("arc's TUI requires a capable terminal (TERM=%q is not supported, e.g. Emacs shell-mode); run arc's CLI subcommands there instead", term)
	}
	return nil
}

// runTUI launches the TUI. Used by both rootCmd (bare "arc") and tuiCmd ("arc tui").
func runTUI(cmd *cobra.Command) error {
	if err := requireCapableTerminal(); err != nil {
		return err
	}
	svc := svcFrom(cmd)
	cfg := cfgFrom(cmd)
	themeMode := cfg.Theme
	if cmd.Flags().Changed("theme") {
		themeMode, _ = cmd.Flags().GetString("theme")
	}

	// Resolve the config file path so the TUI can patch fields in place.
	cfgPath := cfgFile
	if cfgPath == "" {
		cfgPath = filepath.Join(arcHomeDir(), "config.jsonc")
	}

	// Intercept SIGINT so the Go runtime doesn't terminate the process
	// before p.Run() returns. Bubbletea captures ctrl+c as a keystroke
	// in raw mode, but a race with the OS signal can kill us first.
	// Drain the channel so the signal is consumed harmlessly.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT)
	go func() {
		for range sig {
		}
	}()
	defer signal.Stop(sig)

	// Divergence is detected by the startup pull, which now runs in the
	// background — blocking every launch on a network round trip to catch a rare
	// condition made the app unusable. When the pull does find a fork, the banner
	// shows "xlock: diverged" and every write is refused, so the tree cannot be
	// made worse while the user repairs it.
	//
	// A pre-existing divergence is still caught before any damage: the first
	// write pulls before acquiring the xlock and refuses on a fork.
	if st, err := svc.Sync().Status(cmd.Context()); err == nil && st.Diverged {
		return reportDiverged(cmd, svc)
	}

	m := arctui.New(svc, cfg, cfgPath, themeMode)
	cleanup := arctui.SetupTerminal()
	defer cleanup()
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	m.SetProgramSend(p.Send)

	final, err := p.Run()
	if fm, ok := final.(arctui.Model); ok {
		fm.Cleanup()
		arctui.CloseChromeWindows(fm.ChromeWindowIDs())
		fm.SaveHistory()
		fm.SaveState()
	}
	if err != nil && !errors.Is(err, tea.ErrInterrupted) {
		return fmt.Errorf("tui: %w", err)
	}
	return nil
}

// reportDiverged prints the repair screen for a forked clone.
//
// Divergence means another machine almost certainly took the xlock while this
// one was working. It is the one case the protocol cannot resolve on its own, so
// it is surfaced rather than automated: only a human can judge which side of the
// fork to keep, and replaying can collide on num_id.
func reportDiverged(cmd *cobra.Command, svc *service.Service) error {
	out := cmd.ErrOrStderr()
	fmt.Fprint(out, "\n  arc cannot start: this tree has diverged.\n\n")
	fmt.Fprintln(out, "  Another machine wrote while this one had unpushed work.")
	fmt.Fprintln(out, "  Reads are blocked too — these files are a fork that is about to be")
	fmt.Fprint(out, "  replayed or dropped, so anything you found here might not survive.\n\n")

	if st, err := svc.Sync().Status(cmd.Context()); err == nil && st.Unpushed > 0 {
		fmt.Fprintf(out, "  %d local commit(s) are not on the remote.\n\n", st.Unpushed)
	}

	fmt.Fprintln(out, "  To repair, in the arc data directory:")
	fmt.Fprintln(out, "    git fetch origin                       # refresh origin/main first")
	fmt.Fprintln(out, "    git log --oneline origin/main..HEAD    # what only this machine has")
	fmt.Fprintln(out, "    git log --oneline HEAD..origin/main    # what only the remote has")
	fmt.Fprintln(out, "    git merge origin/main                  # keep both sides")
	fmt.Fprint(out, "    git push\n\n")
	fmt.Fprintln(out, "  Fetch first, always. Every command above reads origin/main, which is")
	fmt.Fprintln(out, "  only as current as the last fetch — arc may have detected this fork")
	fmt.Fprint(out, "  from a stale ref, and merging one pushes back a rejected non-fast-forward.\n\n")
	fmt.Fprintln(out, "  Merge keeps both histories, which is almost always what you want —")
	fmt.Fprintln(out, "  look at the two logs above before assuming either side is disposable.")
	fmt.Fprintln(out, "  Append-only files (events.jsonl) conflict on every fork; resolve them")
	fmt.Fprint(out, "  by keeping both sides, or set 'events.jsonl merge=union' in .gitattributes.\n\n")
	fmt.Fprintln(out, "  'git rebase origin/main' replays this machine's commits instead, but it")
	fmt.Fprint(out, "  rewrites them and can collide on num_id — check after, with 'arc reindex'.\n\n")
	fmt.Fprintln(out, "  'git reset --hard origin/main' DISCARDS everything this machine has that")
	fmt.Fprint(out, "  the remote does not. Only after reading the first log above.\n")
	return errors.New("diverged clone: manual repair required")
}

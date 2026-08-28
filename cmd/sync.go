package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/jrniemiec/arc/config"
	"github.com/jrniemiec/arc/gitsync"
)

func init() {
	syncRepairCmd.Flags().BoolVar(&repairDryRun, "dry-run", false, "show the plan, change nothing")
	syncRepairCmd.Flags().BoolVar(&repairYes, "yes", false, "skip the confirmation prompt")

	syncCmd.AddCommand(syncInitCmd, syncEnableCmd, syncDisableCmd,
		syncStatusCmd, syncPullCmd, syncPushCmd, syncRepairCmd)
	rootCmd.AddCommand(syncCmd)
}

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Share the arc tree across several machines",
	Long: `Share the arc tree across several of your own machines via a git remote.

arc has two modes. In standalone (the default) arc performs no git operations at
all. In multi-client the tree is a git repository, and exactly one machine holds
write authority at a time — recorded in xlock.json and reported by the TUI banner.

Standalone does not mean git is forbidden: you may version the tree yourself,
arc simply does not touch it.

Multi-client means one person's several machines. It is not multi-user: the xlock
cannot fence a machine it has lost contact with, so a forced takeover strands
whatever the other side had not pushed.

Setup:
  arc sync init git@github.com:you/arc-tree.git

Run the same command on every machine — it pushes the tree up on the first and
clones it down on the rest.`,
}

// -----------------------------------------------------------------------------
// init
// -----------------------------------------------------------------------------

var syncInitCmd = &cobra.Command{
	Use:   "init <repo-url>",
	Short: "Set up multi-client sync (run on every machine)",
	Long: `Set up multi-client sync. Symmetric: run the same command on every machine.

The repository must already exist and be empty. arc does not create it — that
needs a host API token, a different credential from the SSH key used to push.

  GitHub:      gh repo create arc-tree --private
  Self-hosted: ssh you@host 'git init --bare /srv/git/arc-tree.git'

Do not initialise the GitHub repo with a README, .gitignore, or licence. Any
initial commit makes the remote non-empty and the first push non-fast-forward.

What init does depends on both ends:

  local has content, remote empty   git init, .gitignore, commit, push
  local empty, remote has content   clone, then index this machine's database
  both empty                        git init, commit, push
  both have content                 refused — only you can decide which survives

It finishes by writing "mode": "multi-client" to config.jsonc.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		url := strings.TrimSpace(args[0])
		cfg := cfgFrom(cmd)
		ctx := cmd.Context()
		out := cmd.OutOrStdout()

		branch := cfg.Sync.Branch
		if branch == "" {
			branch = "main"
		}
		remote := cfg.Sync.Remote
		if remote == "" {
			remote = "origin"
		}
		g := &gitsync.Git{Dir: cfg.DataRoot, Remote: remote, Branch: branch}

		// 1. Already set up? Check real state, not just config — the two drift,
		//    and a config claiming initialisation over a tree with no .git
		//    should self-heal rather than block.
		if g.IsRepo(ctx) && g.RemoteURL(ctx) != "" {
			fmt.Fprintf(out, "arc is already set up for %s\n", g.RemoteURL(ctx))
			if !cfg.Sync.MultiClient() {
				fmt.Fprintln(out, "mode is standalone — run 'arc sync enable' to re-enter multi-client")
			}
			return nil
		}

		// 2. Probe the remote before touching anything. Pushing to a
		//    non-existent GitHub repo reports "Repository not found", which
		//    reads like an auth failure and sends people to debug SSH keys.
		fmt.Fprintf(out, "checking %s ...\n", url)
		exists, remoteHasContent, err := gitsync.LsRemote(ctx, url)
		if err != nil || !exists {
			return fmt.Errorf("cannot reach %s — create it first (gh repo create <name> --private): %w", url, err)
		}

		localHasContent, err := treeHasContent(cfg)
		if err != nil {
			return err
		}

		switch {
		case localHasContent && remoteHasContent:
			return fmt.Errorf(
				"both this machine and %s already contain an arc tree.\n"+
					"Only you can decide which one survives. Move or remove one, then run init again",
				url)

		case !localHasContent && remoteHasContent:
			if err := cloneInto(ctx, url, cfg, branch, out); err != nil {
				return err
			}
			// Build this machine's database from the cloned files.
			//
			// arc.db is per-machine and never tracked, so a clone arrives with
			// none: list and search would return nothing until the user noticed
			// an instruction telling them to run reindex. Setup should leave a
			// machine that works.
			//
			// Runs before the mode is written, so a machine that failed to index
			// is not also declared ready.
			if err := reindexAfterClone(cmd); err != nil {
				return err
			}

		default:
			if err := initAndPush(ctx, g, url, cfg, out); err != nil {
				return err
			}
		}

		// 4. Enable. One command leaves a machine ready — a clone that is not
		//    in multi-client mode is in a useless state.
		if err := writeSyncMode(config.SyncModeMultiClient, url, branch); err != nil {
			return err
		}
		fmt.Fprintf(out, "\nmode: multi-client (machine %q)\n", cfg.Sync.Machine)
		fmt.Fprintln(out, "the xlock is taken automatically on your first write")
		return nil
	},
}

// treeHasContent reports whether this machine already holds an arc tree worth
// preserving — any article directory.
func treeHasContent(cfg config.Config) (bool, error) {
	entries, err := os.ReadDir(cfg.ArticlesRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read articles root: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			return true, nil
		}
	}
	return false, nil
}

// initAndPush creates the repository and pushes this machine's tree.
func initAndPush(ctx context.Context, g *gitsync.Git, url string, cfg config.Config, out io.Writer) error {
	fmt.Fprintln(out, "initialising repository ...")
	if !g.IsRepo(ctx) {
		if err := g.Init(ctx); err != nil {
			return err
		}
	}
	if err := writeGitignore(cfg.DataRoot); err != nil {
		return err
	}
	if _, err := g.CommitAll(ctx, "arc: initial tree"); err != nil {
		return err
	}
	if err := g.AddRemote(ctx, url); err != nil {
		return err
	}
	fmt.Fprintln(out, "pushing ...")
	return g.PushSetUpstream(ctx)
}

// cloneInto clones the remote into the data root.
//
// git clone refuses a non-empty directory, so the tree is cloned to a sibling
// and its .git moved into place. This keeps whatever untracked per-machine files
// already exist (arc.db, config.jsonc, logs) rather than demanding an empty dir.
func cloneInto(ctx context.Context, url string, cfg config.Config, branch string, out io.Writer) error {
	fmt.Fprintln(out, "cloning ...")
	tmp := cfg.DataRoot + ".clone-tmp"
	if err := os.RemoveAll(tmp); err != nil {
		return fmt.Errorf("clear temp clone dir: %w", err)
	}
	if err := gitsync.Clone(ctx, url, tmp, branch); err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	if err := os.MkdirAll(cfg.DataRoot, 0o755); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(tmp, ".git"), filepath.Join(cfg.DataRoot, ".git")); err != nil {
		return fmt.Errorf("move .git into place: %w", err)
	}

	// Materialise the working tree. Moving .git into place leaves HEAD and the
	// index pointing at the cloned commit while the directory itself is still
	// empty, so the files have to be checked out explicitly. A merge is a no-op
	// here — there is nothing to fast-forward to.
	//
	// Only tracked paths are written; untracked per-machine files that already
	// exist (config.jsonc, arc.db, logs) are left alone.
	g := &gitsync.Git{Dir: cfg.DataRoot, Remote: "origin", Branch: branch}
	if err := g.CheckoutAll(ctx); err != nil {
		return fmt.Errorf("check out cloned tree: %w", err)
	}
	return nil
}

// reindexAfterClone builds the local database from the freshly cloned tree.
//
// The library was opened before the clone, which is fine: it holds the articles
// root as a path and walks it live, so it sees the new files without reopening.
func reindexAfterClone(cmd *cobra.Command) error {
	out := cmd.OutOrStdout()
	fmt.Fprintln(out, "indexing ...")

	var last int
	res, err := svcFrom(cmd).Reindex(cmd.Context(), func(indexed, total int) {
		last = indexed
		fmt.Fprintf(cmd.ErrOrStderr(), "\r  indexing %d/%d", indexed, total)
	})
	if last > 0 {
		fmt.Fprintln(cmd.ErrOrStderr())
	}
	if err != nil {
		// Name what worked, so the failure does not read as "setup is broken".
		return fmt.Errorf("the clone succeeded but indexing failed: %w\n"+
			"resolve the above, then run 'arc reindex'", err)
	}
	fmt.Fprintf(out, "indexed %d articles, %d collections\n", res.Articles, res.Collections)
	return nil
}

// gitignorePatterns are the per-machine files that must never be tracked.
//
// arc.db is binary and rewritten in place: git would store a fresh multi-MB blob
// per commit, and two machines writing it is an unresolvable merge. index/ is
// deliberately absent — it is committed, because its files are immutable and
// embedding is the only step that costs API money.
var gitignorePatterns = []string{
	"/arc.db",
	"/arc.db-shm",
	"/arc.db-wal",
	"/arc.log",
	"/arc.log.*",
	"/tui_state.json",
	"/command_history",
	"/cookies/",
	// Transient temp files from atomicfile.Write. Unanchored: they appear
	// wherever a file is replaced. CommitAll runs `git add -A`, so without this
	// a commit racing a write commits a temp file that is gone a moment later.
	".*.tmp*",
	// Anchored to the root. An unanchored "config.jsonc" matches at every depth,
	// which silently excluded agent/config.jsonc and every
	// workspaces/*/chat/config.jsonc — the per-workspace chat profile and
	// grounding mode, which are content and must sync. Only the root config is
	// per-machine, because it holds absolute paths and this machine's identity.
	"/config.jsonc",
	"/config.json",
	// The per-machine overlay: this machine's identity, mode, and paths. Never
	// tracked, so it survives a clone overwriting the shared config.
	"/config.local.jsonc",
	"/config.local.json",
}

func writeGitignore(dataRoot string) error {
	path := filepath.Join(dataRoot, ".gitignore")
	existing, _ := os.ReadFile(path)
	var b strings.Builder
	b.Write(existing)
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		b.WriteString("\n")
	}
	for _, p := range gitignorePatterns {
		if !strings.Contains(string(existing), p+"\n") {
			b.WriteString(p + "\n")
		}
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// -----------------------------------------------------------------------------
// enable / disable
// -----------------------------------------------------------------------------

var syncEnableCmd = &cobra.Command{
	Use:   "enable",
	Short: "Re-enter multi-client mode",
	Long: `Re-enter multi-client mode after 'arc sync disable'.

Only needed after a disable — 'arc sync init' already enables. The tree and
remote are still in place, so this commits any work done while standalone,
switches the mode, pulls, and pushes.

Committing before pulling is deliberate: it captures the offline work first, then
lets --ff-only reveal whether the tree diverged while this machine was away.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg := cfgFrom(cmd)
		ctx := cmd.Context()
		out := cmd.OutOrStdout()

		branch := cfg.Sync.Branch
		if branch == "" {
			branch = "main"
		}
		g := &gitsync.Git{Dir: cfg.DataRoot, Remote: "origin", Branch: branch}

		if !g.IsRepo(ctx) || g.RemoteURL(ctx) == "" {
			return fmt.Errorf("this tree is not set up for sync — run 'arc sync init <repo-url>' first")
		}

		// Capture work done while standalone before anything else.
		if committed, err := g.CommitAll(ctx, "arc: work done in standalone mode"); err != nil {
			return err
		} else if committed {
			fmt.Fprintln(out, "committed local changes made while standalone")
		}

		if err := writeSyncMode(config.SyncModeMultiClient, g.RemoteURL(ctx), branch); err != nil {
			return err
		}
		fmt.Fprintln(out, "mode: multi-client")

		if err := g.Fetch(ctx); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: fetch failed: %v\n", err)
			return nil
		}
		if err := g.MergeFFOnly(ctx); err != nil {
			if errors.Is(err, gitsync.ErrDiverged) {
				commits, _ := g.UnpushedCommits(ctx)
				errOut := cmd.ErrOrStderr()
				fmt.Fprintf(errOut,
					"\nDIVERGED: this machine has %d commit(s) the remote does not.\n"+
						"Another machine wrote while this one was standalone.\n"+
						"Resolve before continuing — arc will not operate until you do.\n", len(commits))
				for _, c := range commits {
					fmt.Fprintf(errOut, "  %s\n", c)
				}
				// This command diagnoses only. Saying so is the point: the message
				// it replaces pointed the user back at 'arc sync enable' itself.
				fmt.Fprintf(errOut,
					"\nThis command does not repair. Run:\n"+
						"  arc sync repair\n"+
						"It fetches, shows what each side has, and merges once you confirm.\n")
				return errors.New("diverged clone: manual repair required")
			}
			return err
		}
		fmt.Fprintln(out, "pulled; run 'arc reindex' if articles changed")
		return g.Push(ctx)
	},
}

var syncDisableCmd = &cobra.Command{
	Use:   "disable",
	Short: "Leave multi-client mode",
	Long: `Leave multi-client mode and return to standalone.

Order matters: outstanding work is pushed first, so nothing is stranded on a
machine that has stopped participating, and the xlock is released, so no other
machine waits out the idle timeout for nothing.

.git and the remote are left intact — this stops participation, it does not undo
'arc sync init'. Re-enabling later is instant.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg := cfgFrom(cmd)
		out := cmd.OutOrStdout()
		svc := svcFrom(cmd)

		if !cfg.Sync.MultiClient() {
			fmt.Fprintln(out, "already standalone")
			return nil
		}
		if err := svc.Sync().Release(cmd.Context()); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: release failed: %v\n", err)
		} else {
			fmt.Fprintln(out, "pushed outstanding work and released the xlock")
		}
		if err := writeSyncMode(config.SyncModeStandalone, cfg.Sync.Remote, cfg.Sync.Branch); err != nil {
			return err
		}
		fmt.Fprintln(out, "mode: standalone — arc will perform no git operations")
		return nil
	},
}

// -----------------------------------------------------------------------------
// status / pull / push
// -----------------------------------------------------------------------------

var syncStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show sync mode, xlock holder, and unpushed work",
	RunE: func(cmd *cobra.Command, args []string) error {
		out := cmd.OutOrStdout()
		coord := svcFrom(cmd).Sync()

		// Refresh the remote ref first so the holder shown is current. Fetch
		// only — never merge: reporting status must not swap working files.
		if coord.Enabled() {
			if err := coord.FetchOnly(cmd.Context()); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not reach the remote: %v\n", err)
			}
		}

		st, err := coord.Status(cmd.Context())
		if err != nil {
			return err
		}
		if !st.Enabled {
			fmt.Fprintln(out, "mode:     standalone (arc performs no git operations)")
			return nil
		}
		fmt.Fprintf(out, "mode:     multi-client\n")
		fmt.Fprintf(out, "machine:  %s\n", st.Machine)
		fmt.Fprintf(out, "remote:   %s/%s\n", st.Remote, st.Branch)
		switch {
		case st.Diverged:
			fmt.Fprintf(out, "xlock:    DIVERGED — repair required\n")
		case st.Holder == "":
			fmt.Fprintf(out, "xlock:    free\n")
		case st.IsSelf:
			fmt.Fprintf(out, "xlock:    this machine\n")
		case st.SeizableIn == 0:
			fmt.Fprintf(out, "xlock:    %s (idle — seizable now)\n", st.Holder)
		default:
			fmt.Fprintf(out, "xlock:    %s (seizable in %s)\n", st.Holder, st.SeizableIn.Round(time.Second))
		}
		fmt.Fprintf(out, "unpushed: %d\n", st.Unpushed)
		if !st.LastPush.IsZero() {
			fmt.Fprintf(out, "last push: %s\n", st.LastPush.Format(time.RFC3339))
		}
		return nil
	},
}

var syncPullCmd = &cobra.Command{
	Use:   "pull",
	Short: "Fetch, fast-forward, and index the changed articles",
	Long: `Fetch, fast-forward, and index only the articles that changed.

Rarely needed: startup pulls, and so does every mutating command. Its real use is
refreshing mid-session while you are only reading, when nothing else pulls.

Never a full reindex — git reports which files changed, so only those articles
are re-read.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		svc := svcFrom(cmd)
		if !svc.Sync().Enabled() {
			return errors.New("not in multi-client mode")
		}
		if err := svc.SyncPull(cmd.Context()); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "up to date")
		return nil
	},
}

var syncPushCmd = &cobra.Command{
	Use:   "push",
	Short: "Commit and push anything outstanding",
	RunE: func(cmd *cobra.Command, args []string) error {
		svc := svcFrom(cmd)
		if !svc.Sync().Enabled() {
			return errors.New("not in multi-client mode")
		}
		if err := svc.Sync().Push(cmd.Context()); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "pushed")
		return nil
	},
}

// -----------------------------------------------------------------------------
// repair
// -----------------------------------------------------------------------------

var (
	repairDryRun bool
	repairYes    bool
)

var syncRepairCmd = &cobra.Command{
	Use:   "repair",
	Short: "Merge a diverged tree back together",
	Long: `Rejoin a tree that has forked, by merging the remote into this machine.

A fork happens when another machine pushes while this one has unpushed work.
Until it is repaired arc refuses to open, so this command must stay usable in
exactly the state that stops everything else.

It only ever merges. Rebasing rewrites the auto-commits and can collide on
num_id; resetting discards whichever side you point it away from. Both remain
manual git commands, because only you can judge which side of a fork is
disposable — they are rarely the same size.

  arc sync repair --dry-run    show the plan, change nothing
  arc sync repair --yes        skip the confirmation`,
	RunE: func(cmd *cobra.Command, args []string) error { return runRepair(cmd) },
}

// runRepair automates this sequence:
//
//	cd <data root>
//
//	# 1. fetch — stop here if it fails
//	git fetch origin main
//
//	# 2. classify
//	git rev-list --left-right --count origin/main...HEAD   # behind<TAB>ahead
//
//	#    0 0  -> nothing to do
//	#    0 N  -> git merge --ff-only origin/main ; stop
//	#    N 0  -> git push ; stop
//	#    N M  -> real fork, continue
//
//	# 3. show both sides, then confirm
//	git log --oneline origin/main..HEAD    # only this machine
//	git log --oneline HEAD..origin/main    # only the remote
//
//	# 4. backup ref
//	git tag arc-pre-repair-$(date +%Y%m%d-%H%M%S)
//
//	# 5. union-merge the append-only log
//	grep -q 'events.jsonl merge=union' .gitattributes 2>/dev/null || \
//	  echo 'events.jsonl merge=union' >> .gitattributes
//	git add .gitattributes && git commit -m "Union-merge events.jsonl" || true
//
//	# 6. merge
//	git merge origin/main
//
//	# 7. on conflict
//	git diff --name-only --diff-filter=U   # list them
//	git merge --abort
//
//	# 8. reindex if articles moved
//	git diff --name-only HEAD@{1} HEAD -- articles/ && arc reindex
//
//	# 9. push
//	git push
func runRepair(cmd *cobra.Command) error {
	cfg := cfgFrom(cmd)
	ctx := cmd.Context()
	out := cmd.OutOrStdout()

	if !cfg.Sync.MultiClient() {
		return errors.New("not in multi-client mode — nothing to repair")
	}
	branch := cfg.Sync.Branch
	if branch == "" {
		branch = "main"
	}
	g := &gitsync.Git{Dir: cfg.DataRoot, Remote: "origin", Branch: branch}
	if !g.IsRepo(ctx) || g.RemoteURL(ctx) == "" {
		return fmt.Errorf("this tree is not set up for sync — run 'arc sync init <repo-url>' first")
	}
	remoteRef := "origin/" + branch

	// Uncommitted work would be swept into the merge. Capture it first, as every
	// other sync path does.
	if !repairDryRun {
		if committed, err := g.CommitAll(ctx, "arc: local changes"); err != nil {
			return err
		} else if committed {
			fmt.Fprintln(out, "committed uncommitted changes first")
		}
	}

	// 1. Fetch. Fatal on failure: every check below reads the remote-tracking
	// ref, and merging a stale one reproduces the non-fast-forward rejection
	// this command exists to clear.
	if err := g.Fetch(ctx); err != nil {
		return fmt.Errorf("fetch failed, cannot repair against a stale ref: %w", err)
	}

	// 2. Classify. Three of the four cases are not forks.
	ahead, behind, err := g.Divergence(ctx)
	if err != nil {
		return err
	}
	switch {
	case ahead == 0 && behind == 0:
		fmt.Fprintln(out, "already in sync — nothing to repair")
		return nil
	case ahead == 0:
		if repairDryRun {
			fmt.Fprintf(out, "would fast-forward %d commit(s)\n", behind)
			return nil
		}
		if err := g.MergeFFOnly(ctx); err != nil {
			return err
		}
		fmt.Fprintf(out, "fast-forwarded %d commit(s)\n", behind)
		return reindexIfArticlesChanged(cmd, g)
	case behind == 0:
		if repairDryRun {
			fmt.Fprintf(out, "would push %d commit(s)\n", ahead)
			return nil
		}
		if err := g.Push(ctx); err != nil {
			return err
		}
		fmt.Fprintf(out, "pushed %d commit(s)\n", ahead)
		return nil
	}

	// 3. A real fork. Show both sides before touching anything — they are rarely
	// the same size, and the asymmetry is the whole basis for the decision.
	mine, _ := g.LogRange(ctx, remoteRef+"..HEAD")
	theirs, _ := g.LogRange(ctx, "HEAD.."+remoteRef)
	fmt.Fprintf(out, "\nDIVERGED: %d commit(s) here, %d on the remote.\n\n", ahead, behind)
	printCommits(out, "only on this machine:", mine)
	printCommits(out, "only on the remote:", theirs)

	if repairDryRun {
		fmt.Fprintln(out, "would merge the remote into this machine, keeping both sides")
		return nil
	}
	if !repairYes {
		fmt.Fprint(out, "merge both sides? [y/N] ")
		var answer string
		fmt.Fscanln(cmd.InOrStdin(), &answer)
		if !strings.EqualFold(strings.TrimSpace(answer), "y") {
			fmt.Fprintln(out, "aborted — nothing changed")
			return nil
		}
	}

	// 4. Backup ref. Free: the objects stay reachable from the branch anyway.
	tag := "arc-pre-repair-" + time.Now().Format("20060102-150405")
	if err := g.Tag(ctx, tag); err != nil {
		return fmt.Errorf("could not tag the pre-repair state: %w", err)
	}

	// 5. events.jsonl is append-only, so both sides are always right and it
	// conflicts on every fork. A union merge is the correct resolution and
	// removes the only conflict most repairs would ever hit.
	if err := ensureUnionMerge(ctx, g, cfg.DataRoot); err != nil {
		return err
	}

	// 6/7. Merge, and on conflict restore rather than guess. arc cannot judge
	// which side of a summary or a workspace file is correct.
	if err := g.Merge(ctx, remoteRef); err != nil {
		conflicts, _ := g.ConflictedFiles(ctx)
		if abortErr := g.MergeAbort(ctx); abortErr != nil {
			return fmt.Errorf("merge conflicted and could not be undone: %v (abort: %w)", err, abortErr)
		}
		errOut := cmd.ErrOrStderr()
		fmt.Fprintf(errOut, "\nmerge conflicted — nothing changed. Resolve by hand:\n")
		for _, f := range conflicts {
			fmt.Fprintf(errOut, "  %s\n", f)
		}
		fmt.Fprintf(errOut, "\n  cd %s && git merge %s\n", cfg.DataRoot, remoteRef)
		fmt.Fprintf(errOut, "  # fix the files above, then: git add -A && git commit && git push\n")
		fmt.Fprintf(errOut, "  # to undo entirely: git reset --hard %s\n", tag)
		return errors.New("merge conflict: manual resolution required")
	}
	fmt.Fprintln(out, "merged")

	// 8. Reindex only if article files moved.
	if err := reindexIfArticlesChanged(cmd, g); err != nil {
		return err
	}

	// 9. Push, retrying once if the remote moved while we were merging.
	if err := g.Push(ctx); err != nil {
		fmt.Fprintln(out, "push rejected, remote moved — merging again")
		if err := g.Fetch(ctx); err != nil {
			return err
		}
		if err := g.Merge(ctx, remoteRef); err != nil {
			return fmt.Errorf("second merge failed: %w", err)
		}
		if err := g.Push(ctx); err != nil {
			return fmt.Errorf("push still rejected: %w", err)
		}
	}

	fmt.Fprintf(out, "pushed — repaired\n\nundo with: git reset --hard %s\ndelete the tag when satisfied: git tag -d %s\n", tag, tag)
	return nil
}

func printCommits(out io.Writer, heading string, commits []string) {
	fmt.Fprintf(out, "  %s\n", heading)
	if len(commits) == 0 {
		fmt.Fprintln(out, "    (none)")
	}
	for _, c := range commits {
		fmt.Fprintf(out, "    %s\n", c)
	}
	fmt.Fprintln(out)
}

// ensureUnionMerge adds the events.jsonl union rule to .gitattributes if absent,
// committing it so the merge that follows picks it up.
func ensureUnionMerge(ctx context.Context, g *gitsync.Git, dataRoot string) error {
	const rule = "events.jsonl merge=union"
	path := filepath.Join(dataRoot, ".gitattributes")

	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if strings.Contains(string(existing), rule) {
		return nil
	}
	body := string(existing)
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	body += rule + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return err
	}
	_, err = g.CommitAll(ctx, "Union-merge events.jsonl so append-only logs never conflict")
	return err
}

// reindexIfArticlesChanged re-reads only what a merge or fast-forward touched.
func reindexIfArticlesChanged(cmd *cobra.Command, g *gitsync.Git) error {
	changed, err := g.ChangedFiles(cmd.Context(), "HEAD@{1}", "HEAD")
	if err != nil {
		return nil // no reflog entry to compare against; nothing to do
	}
	touched := false
	for _, f := range changed {
		if strings.Contains(f, "articles/") {
			touched = true
			break
		}
	}
	if !touched {
		return nil
	}
	fmt.Fprintln(cmd.OutOrStdout(), "article files changed — reindexing")
	_, err = svcFrom(cmd).Reindex(cmd.Context(), nil)
	return err
}

// writeSyncMode persists the sync block to config.jsonc.
//
// Mode is explicit configuration and is never inferred from repository state: an
// incidental "git remote add" must not switch the app into a mode with a banner,
// a background fetch, and a divergence state that blocks startup. A command
// writing config is not inference — config stays the source of truth.
func writeSyncMode(mode, remoteURL, branch string) error {
	return updateConfigSync(func(s *config.SyncConfig) {
		s.Mode = mode
		if branch != "" {
			s.Branch = branch
		}
		if s.Remote == "" {
			s.Remote = "origin"
		}
		_ = remoteURL // the URL lives in .git/config; arc records only the name
	})
}

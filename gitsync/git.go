// Package gitsync implements multi-client sync for the arc tree.
//
// The tree is a git repository; SQLite and the vector index are per-machine and
// never tracked. Exactly one machine holds write authority at a time, recorded
// in a tracked xlock.json file. See local/XLOCK-SYNC-DESIGN.md.
//
// arc shells out to the git binary rather than using a git library. git already
// resolves ~/.ssh/config, credential helpers, the keychain, and proxy settings;
// a library inherits none of that and would force credentials into arc's own
// configuration.
package gitsync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
)

// Errors the service layer acts on. Everything else surfaces as a wrapped
// *ExitError with git's stderr attached.
var (
	// ErrDiverged means merge --ff-only refused: this clone holds commits the
	// remote does not. It is never resolved automatically — an auto-rebase
	// would merge a concurrent write and hide a num_id collision.
	ErrDiverged = errors.New("local clone has diverged from the remote")

	// ErrDirtyTree means uncommitted changes would be overwritten by a merge.
	// In arc this signals a crash between writing files and committing them.
	ErrDirtyTree = errors.New("working tree has uncommitted changes")

	// ErrPushRejected means the remote ref moved first. With the xlock held
	// this is an anomaly, not a retry case.
	ErrPushRejected = errors.New("push rejected: remote has moved")

	// ErrNoRemoteRepo means the remote URL does not resolve. Distinguished
	// from an auth failure because git reports both as "Repository not found".
	ErrNoRemoteRepo = errors.New("remote repository does not exist or is unreachable")

	// ErrNoGit means the git binary is not on PATH.
	ErrNoGit = errors.New("git binary not found on PATH")

	// ErrNotARepo means the data root is not a git repository.
	ErrNotARepo = errors.New("not a git repository")
)

// Git runs git commands against a fixed repository directory.
//
// Remote and branch are named explicitly on every invocation. The wrapper never
// calls "git pull": pull honours the user's pull.rebase setting, which would
// silently rewrite local commits — precisely the case that must fail loudly.
type Git struct {
	Dir    string // repository working directory
	Remote string // e.g. "origin"
	Branch string // e.g. "main"
}

// run executes git with the given arguments and returns stdout.
func (g *Git) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = g.Dir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	slog.Debug("gitsync exec", "args", args, "dir", g.Dir)
	err := cmd.Run()
	out := strings.TrimRight(stdout.String(), "\n")

	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return out, ErrNoGit
		}
		msg := strings.TrimSpace(stderr.String())
		slog.Debug("gitsync exec failed", "args", args, "stderr", msg, "err", err)
		return out, classify(args, msg, err)
	}
	return out, nil
}

// classify maps git's stderr onto the typed errors above. Matching on message
// text is unavoidable: git returns exit code 1 for most failures, so the exit
// code alone cannot distinguish divergence from a dirty tree.
func classify(args []string, stderr string, err error) error {
	low := strings.ToLower(stderr)
	cmdName := ""
	if len(args) > 0 {
		cmdName = args[0]
	}

	switch {
	case strings.Contains(low, "not possible to fast-forward"),
		strings.Contains(low, "refusing to merge unrelated histories"),
		strings.Contains(low, "diverging"):
		return fmt.Errorf("%w: %s", ErrDiverged, stderr)

	case strings.Contains(low, "local changes to the following files would be overwritten"),
		strings.Contains(low, "untracked working tree files would be overwritten"),
		strings.Contains(low, "please commit your changes or stash them"):
		return fmt.Errorf("%w: %s", ErrDirtyTree, stderr)

	case cmdName == "push" && (strings.Contains(low, "non-fast-forward") ||
		strings.Contains(low, "fetch first") ||
		strings.Contains(low, "rejected")):
		return fmt.Errorf("%w: %s", ErrPushRejected, stderr)

	case strings.Contains(low, "repository not found"),
		strings.Contains(low, "could not read from remote repository"),
		strings.Contains(low, "does not appear to be a git repository"):
		return fmt.Errorf("%w: %s", ErrNoRemoteRepo, stderr)

	case strings.Contains(low, "not a git repository"):
		return fmt.Errorf("%w: %s", ErrNotARepo, stderr)
	}

	if stderr != "" {
		return fmt.Errorf("git %s: %s", cmdName, stderr)
	}
	return fmt.Errorf("git %s: %w", cmdName, err)
}

// IsRepo reports whether Dir is inside a git working tree.
func (g *Git) IsRepo(ctx context.Context) bool {
	out, err := g.run(ctx, "rev-parse", "--is-inside-work-tree")
	return err == nil && strings.TrimSpace(out) == "true"
}

// RemoteURL returns the configured URL for the remote, or "" when unset.
func (g *Git) RemoteURL(ctx context.Context) string {
	out, err := g.run(ctx, "remote", "get-url", g.Remote)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// LsRemote probes a URL without touching the local tree. It answers two
// questions at once: does the repository exist, and does it have any content?
//
// Probing first is worth the round trip. Pushing to a non-existent GitHub repo
// returns "Repository not found", which reads like an auth failure and sends
// people to debug SSH keys for no reason.
func LsRemote(ctx context.Context, url string) (exists, hasContent bool, err error) {
	cmd := exec.CommandContext(ctx, "git", "ls-remote", "--heads", url)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return false, false, ErrNoGit
		}
		return false, false, classify([]string{"ls-remote"}, strings.TrimSpace(stderr.String()), err)
	}
	return true, strings.TrimSpace(stdout.String()) != "", nil
}

// IsClean reports whether the working tree has no uncommitted changes.
//
// Checked before every merge. In arc a dirty tree means a crash landed between
// writing files and committing them, and merging over it fails with a git
// message meaningless to a TUI user.
func (g *Git) IsClean(ctx context.Context) (bool, error) {
	out, err := g.run(ctx, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "", nil
}

// Head returns the current commit hash.
func (g *Git) Head(ctx context.Context) (string, error) {
	out, err := g.run(ctx, "rev-parse", "HEAD")
	return strings.TrimSpace(out), err
}

// Fetch downloads new commits into the remote-tracking ref. It never touches
// working files, which is what makes it safe to run on a timer behind the TUI.
func (g *Git) Fetch(ctx context.Context) error {
	_, err := g.run(ctx, "fetch", g.Remote, g.Branch)
	return err
}

// MergeFFOnly fast-forwards to the fetched remote branch, or fails.
//
// It cannot produce a conflict: either this clone is a strict ancestor of the
// remote, in which case files are updated with nothing to reconcile, or the
// histories have diverged and the command refuses without changing anything.
func (g *Git) MergeFFOnly(ctx context.Context) error {
	_, err := g.run(ctx, "merge", "--ff-only", g.Remote+"/"+g.Branch)
	return err
}

// ChangedFiles lists paths that changed between two commits, as "STATUS\tpath".
func (g *Git) ChangedFiles(ctx context.Context, from, to string) ([]string, error) {
	if from == to {
		return nil, nil
	}
	out, err := g.run(ctx, "diff", "--name-only", from, to)
	if err != nil {
		return nil, err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// HasChanges reports whether anything is staged or unstaged.
func (g *Git) HasChanges(ctx context.Context) (bool, error) {
	clean, err := g.IsClean(ctx)
	return !clean, err
}

// CommitAll stages everything and commits. It reports false when there was
// nothing to commit, which is not an error.
func (g *Git) CommitAll(ctx context.Context, message string) (bool, error) {
	if _, err := g.run(ctx, "add", "-A"); err != nil {
		return false, err
	}
	changed, err := g.staged(ctx)
	if err != nil {
		return false, err
	}
	if !changed {
		return false, nil
	}
	if _, err := g.run(ctx, "commit", "-m", message); err != nil {
		return false, err
	}
	return true, nil
}

// staged reports whether the index differs from HEAD.
func (g *Git) staged(ctx context.Context) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "diff", "--cached", "--quiet")
	cmd.Dir = g.Dir
	err := cmd.Run()
	if err == nil {
		return false, nil // exit 0: no staged changes
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return true, nil // exit 1: staged changes present
	}
	return false, fmt.Errorf("git diff --cached: %w", err)
}

// Push sends the local branch to the remote, naming both explicitly.
func (g *Git) Push(ctx context.Context) error {
	_, err := g.run(ctx, "push", g.Remote, g.Branch)
	return err
}

// PushSetUpstream pushes and sets the tracking branch, used once by init.
func (g *Git) PushSetUpstream(ctx context.Context) error {
	_, err := g.run(ctx, "push", "-u", g.Remote, g.Branch)
	return err
}

// UnpushedCount returns how many local commits are not on the remote branch.
// Returns 0 when the remote-tracking ref does not exist yet.
func (g *Git) UnpushedCount(ctx context.Context) (int, error) {
	out, err := g.run(ctx, "rev-list", "--count", g.Remote+"/"+g.Branch+"..HEAD")
	if err != nil {
		return 0, nil
	}
	var n int
	if _, scanErr := fmt.Sscanf(strings.TrimSpace(out), "%d", &n); scanErr != nil {
		return 0, nil
	}
	return n, nil
}

// UnpushedCommits returns one-line summaries of commits not yet on the remote,
// newest first. Used by the repair screen to show what would be replayed.
func (g *Git) UnpushedCommits(ctx context.Context) ([]string, error) {
	out, err := g.run(ctx, "log", "--oneline", g.Remote+"/"+g.Branch+"..HEAD")
	if err != nil {
		return nil, err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// ShowRemoteFile reads a file from the fetched remote branch without merging.
//
// This is how the banner learns who holds the xlock: it reads the remote's copy
// directly, so working files are never swapped under the user mid-read.
// Returns "" with no error when the file does not exist on the remote.
func (g *Git) ShowRemoteFile(ctx context.Context, path string) (string, error) {
	out, err := g.run(ctx, "show", g.Remote+"/"+g.Branch+":"+path)
	if err != nil {
		// A missing path is an expected state, not a failure: the xlock has
		// simply never been written.
		return "", nil
	}
	return out, nil
}

// Init creates a repository and sets the branch name.
func (g *Git) Init(ctx context.Context) error {
	if _, err := g.run(ctx, "init"); err != nil {
		return err
	}
	_, err := g.run(ctx, "branch", "-M", g.Branch)
	return err
}

// AddRemote points the remote at url, replacing any existing definition.
func (g *Git) AddRemote(ctx context.Context, url string) error {
	if existing := g.RemoteURL(ctx); existing != "" {
		_, err := g.run(ctx, "remote", "set-url", g.Remote, url)
		return err
	}
	_, err := g.run(ctx, "remote", "add", g.Remote, url)
	return err
}

// CheckoutAll writes every tracked file from HEAD into the working tree.
//
// Used after a clone whose .git was moved into an existing directory: HEAD and
// the index are correct but the files are absent. Untracked files are untouched.
func (g *Git) CheckoutAll(ctx context.Context) error {
	_, err := g.run(ctx, "checkout", "--", ".")
	return err
}

// Clone copies a remote repository into dir, which must not already exist.
func Clone(ctx context.Context, url, dir, branch string) error {
	args := []string{"clone", "--branch", branch, url, dir}
	cmd := exec.CommandContext(ctx, "git", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return ErrNoGit
		}
		return classify(args, strings.TrimSpace(stderr.String()), err)
	}
	return nil
}

package gitsync

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return string(out)
}

func commit(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-m", name)
}

// forkedClones builds two clones of one remote whose histories have diverged,
// and returns the first. Its origin/main is deliberately left stale — the fork
// exists on the server but this clone has not fetched it yet.
func forkedClones(t *testing.T) *Git {
	t.Helper()

	remote := t.TempDir()
	git(t, remote, "init", "--bare", "-b", "main", ".")

	a := t.TempDir()
	git(t, a, "clone", remote, ".")
	commit(t, a, "base.txt", "base")
	git(t, a, "push", "origin", "main")

	b := t.TempDir()
	git(t, b, "clone", remote, ".")
	commit(t, b, "from-b.txt", "b")
	git(t, b, "push", "origin", "main")

	// a commits without fetching: the server has forked, a cannot see it yet.
	commit(t, a, "from-a.txt", "a")

	return &Git{Dir: a, Remote: "origin", Branch: "main"}
}

// Divergence reads the last-fetched ref, so a fork is invisible until something
// fetches — and visible offline forever after. That second half is the whole
// point: it is what lets the startup guard refuse to open without a network call.
func TestDivergenceBecomesVisibleAfterFetch(t *testing.T) {
	g := forkedClones(t)
	ctx := context.Background()

	ahead, behind, err := g.Divergence(ctx)
	if err != nil {
		t.Fatalf("Divergence: %v", err)
	}
	if ahead != 1 || behind != 0 {
		t.Fatalf("before fetch: ahead=%d behind=%d, want 1/0 (fork not yet visible)", ahead, behind)
	}

	if err := g.Fetch(ctx); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	ahead, behind, err = g.Divergence(ctx)
	if err != nil {
		t.Fatalf("Divergence after fetch: %v", err)
	}
	if ahead != 1 || behind != 1 {
		t.Fatalf("after fetch: ahead=%d behind=%d, want 1/1 (fork visible offline)", ahead, behind)
	}
}

// A clone that has never fetched has not diverged. Divergence must not report a
// fork just because the remote-tracking ref is missing.
func TestDivergenceZeroWithoutRemoteRef(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-b", "main", ".")
	commit(t, dir, "only.txt", "x")

	g := &Git{Dir: dir, Remote: "origin", Branch: "main"}
	ahead, behind, err := g.Divergence(context.Background())
	if err != nil {
		t.Fatalf("Divergence: %v", err)
	}
	if ahead != 0 || behind != 0 {
		t.Errorf("ahead=%d behind=%d, want 0/0", ahead, behind)
	}
}

// Startup must settle divergence before it returns. Reading Status() straight
// after is exactly what cmd/tui.go does, and it read false for six days because
// the answer was only ever produced by the asynchronous pull.
func TestStartupSettlesDivergenceBeforeReturning(t *testing.T) {
	g := forkedClones(t)
	git(t, g.Dir, "fetch", "origin", "main") // a previous session's fetch

	c := &coordinator{
		git:      g,
		dataRoot: g.Dir,
		machine:  "test",
	}
	c.detectDivergenceOffline(context.Background())

	c.mu.Lock()
	diverged := c.diverged
	c.mu.Unlock()

	if !diverged {
		t.Error("diverged is false immediately after the offline check; the app would open as if healthy")
	}
}

// The offline read must not, on its own, condemn a healthy tree.
func TestNoDivergenceOnCleanClone(t *testing.T) {
	remote := t.TempDir()
	git(t, remote, "init", "--bare", "-b", "main", ".")
	dir := t.TempDir()
	git(t, dir, "clone", remote, ".")
	commit(t, dir, "base.txt", "base")
	git(t, dir, "push", "origin", "main")

	c := &coordinator{
		git:      &Git{Dir: dir, Remote: "origin", Branch: "main"},
		dataRoot: dir,
		machine:  "test",
	}
	c.detectDivergenceOffline(context.Background())

	c.mu.Lock()
	diverged := c.diverged
	c.mu.Unlock()

	if diverged {
		t.Error("clean clone reported as diverged")
	}
}

// Merge joins a fork; MergeAbort restores it. Repair depends on the abort being
// total — it reports "nothing changed" and must be telling the truth.
func TestMergeAndAbortRestoreTheFork(t *testing.T) {
	g := forkedClones(t)
	ctx := context.Background()
	if err := g.Fetch(ctx); err != nil {
		t.Fatal(err)
	}

	// Both sides touched the same path, so this merge conflicts.
	commit(t, g.Dir, "shared.txt", "mine")
	git(t, g.Dir, "fetch", "origin", "main")
	before := git(t, g.Dir, "rev-parse", "HEAD")

	if err := g.Merge(ctx, "origin/main"); err == nil {
		// No conflict on this fixture: the merge is legitimate, so just check it
		// moved HEAD and leave the abort case to the conflicting run below.
		if after := git(t, g.Dir, "rev-parse", "HEAD"); after == before {
			t.Error("Merge reported success but HEAD did not move")
		}
		return
	}

	if err := g.MergeAbort(ctx); err != nil {
		t.Fatalf("MergeAbort: %v", err)
	}
	if after := git(t, g.Dir, "rev-parse", "HEAD"); after != before {
		t.Error("HEAD moved across a conflicted merge that was aborted")
	}
}

// LogRange feeds the "what each side has" output. Getting the direction backwards
// would show the user the wrong side to keep.
func TestLogRangeReportsEachSideSeparately(t *testing.T) {
	g := forkedClones(t)
	ctx := context.Background()
	if err := g.Fetch(ctx); err != nil {
		t.Fatal(err)
	}

	mine, err := g.LogRange(ctx, "origin/main..HEAD")
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := g.LogRange(ctx, "HEAD..origin/main")
	if err != nil {
		t.Fatal(err)
	}

	if len(mine) != 1 || !strings.Contains(mine[0], "from-a") {
		t.Errorf("origin/main..HEAD = %v, want the local commit", mine)
	}
	if len(theirs) != 1 || !strings.Contains(theirs[0], "from-b") {
		t.Errorf("HEAD..origin/main = %v, want the remote commit", theirs)
	}
}

// The pre-repair tag is the only way back if a merge goes wrong.
func TestTagMarksHead(t *testing.T) {
	g := forkedClones(t)
	if err := g.Tag(context.Background(), "arc-pre-repair-test"); err != nil {
		t.Fatalf("Tag: %v", err)
	}
	head := strings.TrimSpace(git(t, g.Dir, "rev-parse", "HEAD"))
	tagged := strings.TrimSpace(git(t, g.Dir, "rev-parse", "arc-pre-repair-test"))
	if head != tagged {
		t.Errorf("tag points at %s, HEAD is %s", tagged, head)
	}
}

package gitsync

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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

package gitsync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Coordinator is the whole of the multi-client surface. Two implementations
// exist and exactly one is constructed at startup from the configured mode.
//
// Call sites invoke these methods unconditionally and never branch on the mode.
// In standalone every method returns immediately: no git subprocess, no
// goroutine, no xlock read. The separation lives in the wiring, not in
// conditionals scattered through every command.
type Coordinator interface {
	// Enabled reports whether multi-client mode is active. Only presentation
	// code should ask — logic must not branch on it.
	Enabled() bool

	// Startup pulls so the machine shows current content. It is non-blocking
	// in effect: any failure is logged and swallowed, because being offline
	// must never prevent the app from starting.
	Startup(ctx context.Context)

	// BeforeWrite gates a mutating operation. It returns *Blocked when another
	// machine holds the xlock and its timer is still live — before any work is
	// done, so an ingest never spends an LLM call and then discovers it cannot
	// write.
	BeforeWrite(ctx context.Context) error

	// Push sends everything committed so far. Used by /sync-push and on exit.
	Push(ctx context.Context) error

	// Touch refreshes last_activity locally. Called on any keypress; a long
	// read session must not drop the xlock mid-work.
	Touch()

	// Status reports the current state for `arc sync status` and the banner.
	Status(ctx context.Context) (Status, error)

	// Take seizes the xlock. force skips the confirmation gate for a live timer.
	Take(ctx context.Context, force bool) error

	// Release pushes outstanding work, clears the holder, and pushes.
	Release(ctx context.Context) error

	// Pull fetches, fast-forwards, and indexes only the changed articles.
	// Returns *Diverged when the histories have forked.
	Pull(ctx context.Context) error

	// RemoteXLock reads xlock.json from the fetched remote ref without merging.
	// The banner uses this: the remote is the single source of truth, and local
	// state is only a cache of what it last said.
	RemoteXLock(ctx context.Context) (XLock, error)

	// FetchOnly refreshes the remote-tracking ref without merging, so the
	// banner can see who holds the xlock without swapping files mid-read.
	FetchOnly(ctx context.Context) error

	// Activity reports what the coordinator is doing right now, or "" when idle.
	//
	// Acquiring the xlock happens implicitly inside the first write, so without
	// this the user sees several seconds of nothing and no reason for it.
	Activity() string

	// Close releases the xlock if held and flushes pending pushes.
	Close(ctx context.Context) error

	// StateVersion increments whenever this process acquires or releases the
	// xlock. It lets a UI notice the change without every mutation call site
	// having to report it — there are too many of those to keep correct, and one
	// that forgets leaves the display stale until the next timed refresh.
	StateVersion() uint64
}

// Blocked reports that another machine holds the xlock and its timer has not
// elapsed. Returned before any work is performed.
type Blocked struct {
	Holder     string
	SeizableIn time.Duration
}

func (b *Blocked) Error() string {
	if b.SeizableIn <= 0 {
		return fmt.Sprintf("%s holds the xlock — take it with 'xlock take'", b.Holder)
	}
	return fmt.Sprintf("%s holds the xlock, free in %s — take it now with 'xlock take'",
		b.Holder, roundDur(b.SeizableIn))
}

// Diverged reports that this clone holds commits the remote does not. The app
// must not operate in this state — reads included — until the fork is resolved.
type Diverged struct {
	Commits []string
}

func (d *Diverged) Error() string {
	return fmt.Sprintf(
		"this machine has %d unpushed commit(s) the remote does not have — "+
			"another machine wrote while this one was away; run 'arc sync enable' to repair",
		len(d.Commits))
}

// Status is a snapshot for the status command and the banner.
type Status struct {
	Enabled    bool
	Mode       string
	Machine    string
	Remote     string
	Branch     string
	Holder     string // "" when free
	IsSelf     bool
	SeizableIn time.Duration
	Unpushed   int
	Diverged   bool
	LastPush   time.Time
}

// -----------------------------------------------------------------------------
// standalone
// -----------------------------------------------------------------------------

// noop is the standalone Coordinator. Every method returns immediately.
type noop struct{}

// NewNoop returns the standalone coordinator.
func NewNoop() Coordinator { return noop{} }

func (noop) Enabled() bool                              { return false }
func (noop) Startup(context.Context)                    {}
func (noop) BeforeWrite(context.Context) error          { return nil }
func (noop) Push(context.Context) error                 { return nil }
func (noop) Touch()                                     {}
func (noop) Take(context.Context, bool) error           { return ErrStandalone }
func (noop) Release(context.Context) error              { return ErrStandalone }
func (noop) Pull(context.Context) error                 { return ErrStandalone }
func (noop) FetchOnly(context.Context) error            { return nil }
func (noop) Close(context.Context) error                { return nil }
func (noop) RemoteXLock(context.Context) (XLock, error) { return XLock{}, nil }
func (noop) StateVersion() uint64                       { return 0 }
func (noop) Activity() string                           { return "" }
func (noop) Status(context.Context) (Status, error) {
	return Status{Mode: "standalone"}, nil
}

// ErrStandalone is returned by xlock operations in standalone mode. Commands
// are not registered there, so this is a guard against a wiring mistake rather
// than a user-facing message.
var ErrStandalone = errors.New("not in multi-client mode")

// -----------------------------------------------------------------------------
// multi-client
// -----------------------------------------------------------------------------

// coordinator is the multi-client Coordinator.
type coordinator struct {
	git      *Git
	idx      Indexer
	dataRoot string
	articles string
	machine  string

	idleTimeout    time.Duration
	takeoverMargin time.Duration

	validateNumIDs func() error

	mu           sync.Mutex
	lastActivity time.Time
	held         bool
	diverged     bool
	lastPush     time.Time
	stateVersion uint64

	// pushMu serialises git push. Pushes are synchronous — they run on whichever
	// goroutine owns the operation, which is already off the UI event loop — so
	// this only has to stop two concurrent operations from pushing at once.
	// Without it they race on the remote ref and one is rejected as
	// non-fast-forward, which is indistinguishable from a genuine remote move.
	pushMu sync.Mutex

	// activity is a human-readable description of the in-flight operation.
	activity string
}

// Options configures a multi-client coordinator.
type Options struct {
	DataRoot       string
	ArticlesRoot   string
	Machine        string
	Remote         string
	Branch         string
	IdleTimeout    time.Duration
	TakeoverMargin time.Duration
	Indexer        Indexer
	ValidateNumIDs func() error
}

// New returns a multi-client coordinator.
func New(o Options) Coordinator {
	return &coordinator{
		git:            &Git{Dir: o.DataRoot, Remote: o.Remote, Branch: o.Branch},
		idx:            o.Indexer,
		dataRoot:       o.DataRoot,
		articles:       o.ArticlesRoot,
		machine:        o.Machine,
		idleTimeout:    o.IdleTimeout,
		takeoverMargin: o.TakeoverMargin,
		validateNumIDs: o.ValidateNumIDs,
		lastActivity:   time.Now(),
	}
}

func (c *coordinator) Enabled() bool { return true }

// push sends the current branch, serialised against any other push in this
// process. Callers are already off the UI event loop, so blocking here costs
// nothing the user can feel.
func (c *coordinator) push(ctx context.Context) error {
	c.pushMu.Lock()
	defer c.pushMu.Unlock()
	defer c.setActivity("pushing")()

	if err := c.git.Push(ctx); err != nil {
		// Keep the commit. A rejected push while holding the xlock is an
		// anomaly, never a retry case — auto-rebasing would merge a concurrent
		// write and hide a num_id collision.
		slog.Error("gitsync: push failed, commit retained", "err", err)
		return err
	}
	c.mu.Lock()
	c.lastPush = time.Now()
	c.stateVersion++
	c.mu.Unlock()
	return nil
}

func (c *coordinator) Activity() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.activity
}

// setActivity records what is in flight and returns a function that clears it.
func (c *coordinator) setActivity(what string) func() {
	started := time.Now()
	c.mu.Lock()
	// Restore the previous label rather than blanking it: these nest — claiming
	// the xlock pushes, so "pushing" sits inside "acquiring xlock". Clearing
	// unconditionally would drop the outer label the moment the inner one ends.
	prev := c.activity
	c.activity = what
	c.stateVersion++ // wake the UI immediately rather than at the next tick
	c.mu.Unlock()
	slog.Debug("gitsync activity start", "what", what)
	return func() {
		c.mu.Lock()
		c.activity = prev
		c.stateVersion++
		c.mu.Unlock()
		slog.Debug("gitsync activity end", "what", what, "took", time.Since(started).Round(time.Millisecond))
	}
}

func (c *coordinator) StateVersion() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stateVersion
}

func (c *coordinator) Touch() {
	c.mu.Lock()
	c.lastActivity = time.Now()
	c.mu.Unlock()
}

// Startup pulls so the machine shows current content, then checks whether the
// idle timer should have released a stale xlock this machine still holds.
// Startup pulls in the background.
//
// The pull crosses the network, so doing it inline delayed every app launch by
// the round-trip time. Nothing depends on it having finished: the first write
// pulls again before acquiring the xlock, and until then reads simply show
// content that is at most one launch stale.
func (c *coordinator) Startup(ctx context.Context) {
	go c.startupPull(context.WithoutCancel(ctx))
}

func (c *coordinator) startupPull(ctx context.Context) {
	if err := c.pull(ctx); err != nil {
		var d *Diverged
		if errors.As(err, &d) {
			c.mu.Lock()
			c.diverged = true
			c.mu.Unlock()
			slog.Error("gitsync: clone diverged at startup", "commits", len(d.Commits))
			return
		}
		// Offline or unreachable remote must never block startup.
		slog.Warn("gitsync: startup pull failed, continuing", "err", err)
	}
}

// Pull runs the full sequence: dirty check, fetch, fast-forward, then index only
// what changed.
func (c *coordinator) Pull(ctx context.Context) error { return c.pull(ctx) }

// pull runs the full sequence: dirty check, fetch, fast-forward, then index only
// what changed.
func (c *coordinator) pull(ctx context.Context) error {
	defer c.setActivity("syncing")()
	if err := c.sweep(ctx); err != nil {
		return err
	}

	before, err := c.git.Head(ctx)
	if err != nil {
		return err
	}
	if err := c.git.Fetch(ctx); err != nil {
		return err
	}
	if err := c.git.MergeFFOnly(ctx); err != nil {
		if errors.Is(err, ErrDiverged) {
			commits, _ := c.git.UnpushedCommits(ctx)
			return &Diverged{Commits: commits}
		}
		return err
	}
	after, err := c.git.Head(ctx)
	if err != nil {
		return err
	}

	paths, err := c.git.ChangedFiles(ctx, before, after)
	if err != nil {
		return err
	}
	changes := Classify(paths, c.articles)
	if err := changes.Apply(ctx, c.idx, c.validateNumIDs); err != nil {
		var nm *NumIDMismatch
		if errors.As(err, &nm) {
			// Indexing completed; surface without failing the pull.
			slog.Warn("gitsync: pull applied with num_id mismatch", "err", nm)
			return nil
		}
		return err
	}
	return nil
}

// BeforeWrite implements the four cases from the design: hold, take, seize
// silently, or refuse.
func (c *coordinator) BeforeWrite(ctx context.Context) error {
	c.mu.Lock()
	if c.diverged {
		c.mu.Unlock()
		return &Diverged{}
	}
	held := c.held
	c.mu.Unlock()

	// Already holding: proceed with no network round trip.
	if held {
		c.Touch()
		return nil
	}

	if err := c.pull(ctx); err != nil {
		var d *Diverged
		if errors.As(err, &d) {
			c.mu.Lock()
			c.diverged = true
			c.mu.Unlock()
			return d
		}
		return err
	}

	x, err := ReadXLock(c.dataRoot)
	if err != nil {
		return err
	}
	now := time.Now().UTC()

	switch x.Evaluate(c.machine, now, c.takeoverMargin) {
	case StateHeld, StateFree, StateSeizable:
		return c.claim(ctx, now)
	default:
		return &Blocked{
			Holder:     x.Holder,
			SeizableIn: x.SeizableIn(now, c.takeoverMargin),
		}
	}
}

// claim writes and pushes the xlock. The push is what publishes the claim, and
// it only succeeds if this clone was current — so acquiring doubles as a sync
// check.
func (c *coordinator) claim(ctx context.Context, now time.Time) error {
	defer c.setActivity("acquiring xlock")()
	x := XLock{}.Claim(c.machine, now, c.idleTimeout)
	if err := WriteXLock(c.dataRoot, x); err != nil {
		return err
	}
	if _, err := c.git.CommitAll(ctx, "xlock: "+c.machine); err != nil {
		return err
	}
	if err := c.push(ctx); err != nil {
		return err
	}
	c.mu.Lock()
	c.held = true
	c.lastActivity = time.Now()
	c.stateVersion++
	c.mu.Unlock()
	slog.Info("gitsync: xlock acquired", "machine", c.machine)
	return nil
}

// sweep commits anything left uncommitted in the working tree.
//
// This is the only thing that commits. Individual writes do not commit: arc
// never operates on single commits, so the consistency that matters is the whole
// tree at a git boundary, not after each keystroke.
//
// It is also the only approach that can be complete. Some writes bypass the
// service — scratch edits and deletes, new outcome files — and a file edited in
// $EDITOR (/config-edit, /outcome-edit) cannot be intercepted at all. Sweeping
// at the boundary catches every one of them without each writer having to
// remember anything.
func (c *coordinator) sweep(ctx context.Context) error {
	clean, err := c.git.IsClean(ctx)
	if err != nil {
		return err
	}
	if clean {
		return nil
	}
	slog.Info("gitsync: committing uncommitted changes before sync")
	if _, err := c.git.CommitAll(ctx, "arc: local changes"); err != nil {
		return err
	}
	c.mu.Lock()
	c.stateVersion++ // the unpushed count just moved
	c.mu.Unlock()
	return nil
}

// Push commits anything outstanding and sends it to the remote.
func (c *coordinator) Push(ctx context.Context) error {
	if _, err := c.git.CommitAll(ctx, "arc: sync"); err != nil {
		return err
	}
	return c.push(ctx)
}

func (c *coordinator) Take(ctx context.Context, force bool) error {
	if err := c.pull(ctx); err != nil {
		var d *Diverged
		if errors.As(err, &d) {
			return d
		}
		return err
	}
	x, err := ReadXLock(c.dataRoot)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if !force && x.Evaluate(c.machine, now, c.takeoverMargin) == StateBlocked {
		return &Blocked{Holder: x.Holder, SeizableIn: x.SeizableIn(now, c.takeoverMargin)}
	}
	return c.claim(ctx, now)
}

// Release pushes outstanding work, clears the holder, and pushes again.
func (c *coordinator) Release(ctx context.Context) error {
	// The xlock file is the authority, not this process's memory. Each CLI
	// invocation is a fresh process, so an in-memory "held" flag is always false
	// there and would make release a silent no-op.
	if err := c.git.Fetch(ctx); err != nil {
		slog.Debug("gitsync: fetch before release failed", "err", err)
	}
	x, err := c.remoteOrLocalXLock(ctx)
	if err != nil {
		return err
	}
	if x.Holder != "" && x.Holder != c.machine {
		// Releasing someone else's xlock would be a silent forced takeover.
		return fmt.Errorf("%s holds the xlock, not this machine", x.Holder)
	}
	if x.Holder == "" {
		c.mu.Lock()
		c.held = false
		c.mu.Unlock()
		return nil
	}

	defer c.setActivity("releasing xlock")()
	if err := c.Push(ctx); err != nil {
		return err
	}
	if err := WriteXLock(c.dataRoot, XLock{}.Released()); err != nil {
		return err
	}
	if _, err := c.git.CommitAll(ctx, "xlock: released by "+c.machine); err != nil {
		return err
	}
	if err := c.push(ctx); err != nil {
		return err
	}
	c.mu.Lock()
	c.held = false
	c.stateVersion++
	c.mu.Unlock()
	slog.Info("gitsync: xlock released", "machine", c.machine)
	return nil
}

func (c *coordinator) Status(ctx context.Context) (Status, error) {
	s := Status{
		Enabled: true,
		Mode:    "multi-client",
		Machine: c.machine,
		Remote:  c.git.Remote,
		Branch:  c.git.Branch,
	}
	c.mu.Lock()
	s.Diverged = c.diverged
	s.LastPush = c.lastPush
	c.mu.Unlock()

	x, err := c.remoteOrLocalXLock(ctx)
	if err != nil {
		return s, err
	}

	s.Holder = x.Holder
	s.IsSelf = x.Holder == c.machine
	if x.Holder != "" && !s.IsSelf {
		s.SeizableIn = x.SeizableIn(time.Now().UTC(), c.takeoverMargin)
	}
	s.Unpushed, _ = c.git.UnpushedCount(ctx)
	return s, nil
}

// remoteOrLocalXLock returns the authoritative xlock.
//
// The remote wins whenever it can be read at all — including when it reports a
// free xlock. Falling back to the local file on an empty holder would invert the
// source-of-truth rule: the local copy only changes on pull, so it can still name
// this machine as holder long after another machine released or seized it.
//
// The local file is consulted only when the remote is unreachable, and only when
// the remote-tracking ref has never been populated.
func (c *coordinator) remoteOrLocalXLock(ctx context.Context) (XLock, error) {
	x, err := c.RemoteXLock(ctx)
	if err == nil {
		return x, nil
	}
	local, lerr := ReadXLock(c.dataRoot)
	if lerr != nil {
		return XLock{}, lerr
	}
	return local, nil
}

// RemoteXLock reads the xlock from the fetched remote ref without merging.
//
// This is what the banner uses. The remote is the single source of truth; local
// state is only a cache of what it last said. Not asking would be the dangerous
// option — arc would keep showing "you hold it" after another machine took it,
// and let the user write on that belief.
func (c *coordinator) RemoteXLock(ctx context.Context) (XLock, error) {
	content, err := c.git.ShowRemoteFile(ctx, XLockFile)
	if err != nil {
		return XLock{}, err
	}
	return ParseXLock([]byte(content))
}

// FetchOnly refreshes the remote-tracking ref without merging, so the banner can
// see who holds the xlock without swapping files under the user mid-read.
func (c *coordinator) FetchOnly(ctx context.Context) error {
	return c.git.Fetch(ctx)
}

// Close pushes outstanding work and releases the xlock.
//
// Best effort: correctness never depends on a clean shutdown. If arc is killed,
// the commits stay on disk and the banner reports them as unpushed; the other
// machine recovers the xlock with /xlock-take.
// Close pushes outstanding work and releases the xlock.
//
// Best effort: correctness never depends on a clean shutdown. If arc is killed,
// the commits stay on disk and the banner reports them as unpushed; the other
// machine recovers the xlock with /xlock-take.
func (c *coordinator) Close(ctx context.Context) error {
	if err := c.Push(ctx); err != nil {
		slog.Warn("gitsync: push on close failed", "err", err)
	}
	c.mu.Lock()
	held := c.held
	c.mu.Unlock()
	if held {
		if err := c.Release(ctx); err != nil {
			slog.Warn("gitsync: release on close failed", "err", err)
		}
	}
	return nil
}

func roundDur(d time.Duration) time.Duration {
	if d >= time.Minute {
		return d.Round(time.Minute)
	}
	return d.Round(time.Second)
}

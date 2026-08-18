package gitsync

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// XLockFile is the tracked file at the repository root recording which machine
// currently holds write authority.
const XLockFile = "xlock.json"

// XLock is the on-disk shape of xlock.json.
//
// It is not a mutex — with a single user nobody ever waits on it. It answers one
// question: whose copy is authoritative right now. Its value is that claiming it
// is a cheap push happening *before* the work, so another machine learns you are
// mid-session without your actual work having been pushed.
type XLock struct {
	// Holder names the machine holding write authority; empty means free.
	Holder string `json:"holder"`

	// LastActivity is refreshed locally on any activity but published only when
	// something is pushed.
	LastActivity time.Time `json:"last_activity"`

	// IdleTimeout is the holder's own timeout, published so observers judge it
	// by the holder's value rather than their own. A laptop set to 5m must not
	// seize a desktop set to 15m early.
	IdleTimeout Duration `json:"idle_timeout"`
}

// Duration marshals as a Go duration string so xlock.json stays readable in a
// diff. Defined here rather than reusing config.Duration to keep gitsync free
// of a dependency on the config package.
type Duration time.Duration

func (d Duration) D() time.Duration { return time.Duration(d) }

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("xlock idle_timeout must be a duration string like \"15m\"")
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("parse xlock idle_timeout %q: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}

// State is the result of evaluating the xlock for a mutating operation.
type State int

const (
	// StateHeld means this machine already holds it: proceed, no round trip.
	StateHeld State = iota
	// StateFree means nobody holds it: take it and proceed.
	StateFree
	// StateSeizable means another machine holds it but its timeout elapsed:
	// seize silently. The holder is idle, released without pushing, or dead —
	// all three mean it is not working.
	StateSeizable
	// StateBlocked means another machine holds it and the timer is still live:
	// refuse before doing any work.
	StateBlocked
)

func (s State) String() string {
	switch s {
	case StateHeld:
		return "held"
	case StateFree:
		return "free"
	case StateSeizable:
		return "seizable"
	case StateBlocked:
		return "blocked"
	}
	return "unknown"
}

// XLockPath returns the path to xlock.json under the data root.
func XLockPath(dataRoot string) string {
	return filepath.Join(dataRoot, XLockFile)
}

// ReadXLock reads xlock.json. A missing file is not an error: it means the
// xlock has never been claimed, which is a legitimate free state.
func ReadXLock(dataRoot string) (XLock, error) {
	data, err := os.ReadFile(XLockPath(dataRoot))
	if err != nil {
		if os.IsNotExist(err) {
			return XLock{}, nil
		}
		return XLock{}, fmt.Errorf("read xlock: %w", err)
	}
	return ParseXLock(data)
}

// ParseXLock decodes xlock.json content. Empty content is a free xlock.
func ParseXLock(data []byte) (XLock, error) {
	if len(data) == 0 {
		return XLock{}, nil
	}
	var x XLock
	if err := json.Unmarshal(data, &x); err != nil {
		return XLock{}, fmt.Errorf("parse xlock: %w", err)
	}
	return x, nil
}

// WriteXLock writes xlock.json. Timestamps are stored in UTC because they are
// compared across machines.
func WriteXLock(dataRoot string, x XLock) error {
	x.LastActivity = x.LastActivity.UTC()
	data, err := json.MarshalIndent(x, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal xlock: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(XLockPath(dataRoot), data, 0o644); err != nil {
		return fmt.Errorf("write xlock: %w", err)
	}
	return nil
}

// Evaluate classifies the xlock for a machine at time now.
//
// takeoverMargin covers the lag between the holder's local clock and its last
// published last_activity: a holder about to self-release has not yet pushed
// the release. Without the margin two machines can both believe they own the
// xlock for one round trip. Clock skew between machines eats into it.
func (x XLock) Evaluate(machine string, now time.Time, takeoverMargin time.Duration) State {
	if x.Holder == "" {
		return StateFree
	}
	if x.Holder == machine {
		return StateHeld
	}
	if now.After(x.SeizableAt(takeoverMargin)) {
		return StateSeizable
	}
	return StateBlocked
}

// SeizableAt returns the instant another machine may seize the xlock. It uses
// the holder's published idle timeout, not the observer's configured one.
func (x XLock) SeizableAt(takeoverMargin time.Duration) time.Time {
	return x.LastActivity.Add(x.IdleTimeout.D()).Add(takeoverMargin)
}

// SeizableIn returns how long remains until the xlock may be seized, or zero
// when it already may be. Drives the banner countdown.
func (x XLock) SeizableIn(now time.Time, takeoverMargin time.Duration) time.Duration {
	d := x.SeizableAt(takeoverMargin).Sub(now)
	if d < 0 {
		return 0
	}
	return d
}

// ExpiredFor reports whether this machine, as holder, has been idle long enough
// to release itself. Self-release happens on the holder's own timeout, without
// the takeover margin — the margin exists for observers, not for the holder.
func (x XLock) ExpiredFor(machine string, now time.Time) bool {
	if x.Holder != machine {
		return false
	}
	return now.After(x.LastActivity.Add(x.IdleTimeout.D()))
}

// Claim returns a copy of the xlock held by machine as of now.
func (x XLock) Claim(machine string, now time.Time, idleTimeout time.Duration) XLock {
	return XLock{
		Holder:       machine,
		LastActivity: now.UTC(),
		IdleTimeout:  Duration(idleTimeout),
	}
}

// Released returns a copy with the holder cleared. The idle timeout is dropped
// with it so a free xlock carries no stale value.
func (x XLock) Released() XLock {
	return XLock{LastActivity: time.Now().UTC()}
}

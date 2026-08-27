// Package atomicfile replaces a file's contents in one step.
package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
)

// Write replaces path in one step: write a temp file alongside it, then rename.
//
// A plain write truncates first, so an interrupted one leaves a half-written
// file. The temp name is unique rather than a fixed "<path>.tmp": arc issues
// writes to the same file from several goroutines — the TUI runs each save on
// its own, and the sync gate releases them in a batch once it has the xlock —
// and a shared temp name makes them clobber each other's bytes, with the loser
// of the race failing its rename outright.
func Write(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	// Flush before the rename, so a crash cannot leave the new name pointing at
	// a file whose contents never reached the disk.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	// CreateTemp makes the file 0600; callers want the mode they asked for.
	if err := os.Chmod(tmpName, perm); err != nil {
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace %s: %w", filepath.Base(path), err)
	}
	return nil
}

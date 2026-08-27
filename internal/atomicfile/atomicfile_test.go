package atomicfile

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestWriteReplacesContents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.json")
	if err := Write(path, []byte("first"), 0644); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := Write(path, []byte("second"), 0644); err != nil {
		t.Fatalf("Write (replace): %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "second" {
		t.Errorf("contents = %q, want %q", got, "second")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0644 {
		t.Errorf("mode = %v, want 0644", info.Mode().Perm())
	}
}

// TestWriteConcurrent is the regression test for the askX save race: several
// goroutines replacing one file used to share a fixed "<path>.tmp", so they
// clobbered each other's bytes and the losers failed their rename with ENOENT.
// Every write must succeed, and the file must always be one writer's payload
// whole — never a blend of two.
func TestWriteConcurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")

	const writers = 16
	payloads := make([][]byte, writers)
	for i := range payloads {
		payloads[i] = bytes.Repeat([]byte{byte('a' + i)}, 64*1024)
	}

	var wg sync.WaitGroup
	errs := make([]error, writers)
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = Write(path, payloads[i], 0644)
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("writer %d: %v", i, err)
		}
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	for _, want := range payloads {
		if bytes.Equal(got, want) {
			return
		}
	}
	t.Errorf("file holds no writer's payload intact (len %d) — writes interleaved", len(got))
}

// TestWriteLeavesNoTempFiles guards the deferred cleanup: a temp file left
// behind would be swept into the next sync commit by `git add -A`.
func TestWriteLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := Write(path, []byte("x"), 0644); err != nil {
		t.Fatalf("Write: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "state.json" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("dir contains %v, want [state.json]", names)
	}
}

func TestWriteMissingDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope", "data.json")
	if err := Write(path, []byte("x"), 0644); err == nil {
		t.Error("Write into a missing directory returned nil, want error")
	}
}

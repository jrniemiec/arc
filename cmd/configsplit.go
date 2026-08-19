package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jrniemiec/arc/config"
)

func init() {
	configCmd.AddCommand(configSplitCmd)
}

// machineLocalKeys are the top-level keys that cannot be shared between machines.
//
// The paths are absolute, and every one of them already derives from data_root —
// so moving them out costs nothing, and a machine with a different home directory
// computes them correctly on its own.
//
// theme belongs to the terminal, not the corpus. cookie_jars maps domains to
// absolute file paths. sync carries this machine's identity and mode; see
// updateConfigSync for why sharing "machine" actively breaks the xlock.
var machineLocalKeys = []string{
	"data_root",
	"articles_root",
	"db_path",
	"vector_path",
	"events_path",
	"agent_path",
	"log_path",
	"theme",
	"cookie_jars",
	"sync",
}

var configSplitCmd = &cobra.Command{
	Use:   "split",
	Short: "Move machine-specific settings into config.local.jsonc",
	Long: `Move machine-specific settings out of config.jsonc and into config.local.jsonc.

config.jsonc holds things worth sharing between your machines — profiles,
preferred models, prompts, thresholds. Those only sync if the file is tracked,
and it cannot be tracked while it also holds absolute paths and this machine's
identity.

This moves the machine-specific keys into config.local.jsonc, which is never
tracked. arc reads the shared file first and then the overlay, so the result is
identical — verify with 'arc config' before and after.

Moved:

  data_root  articles_root  db_path  vector_path  events_path  agent_path
  log_path  theme  cookie_jars  sync

Run once per machine. Both files are left in place; nothing is deleted.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		out := cmd.OutOrStdout()
		sharedPath := configPath()
		localPath := config.LocalPath(sharedPath)

		raw, err := os.ReadFile(sharedPath)
		if err != nil {
			return fmt.Errorf("read %s: %w", sharedPath, err)
		}
		src := string(raw)

		// Snapshot the effective config so the move can be proved lossless.
		before, err := config.Load(sharedPath)
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}

		localSrc := "{\n}\n"
		if existing, err := os.ReadFile(localPath); err == nil {
			localSrc = string(existing)
		}

		var moved []string
		for _, key := range machineLocalKeys {
			start, end, found := findObjectValue(src, key)
			if !found {
				continue
			}
			// Take the key's own comment block with it. Leaving it behind orphans
			// a comment above nothing, and in a file that is mostly comments it
			// can strand a trailing comma the parser then rejects.
			lineStart := commentBlockStart(src, strings.LastIndexByte(src[:start], '\n')+1)
			block := strings.TrimRight(src[lineStart:end], " \t\n")

			localSrc, err = spliceJSONBlock(localSrc, key, block)
			if err != nil {
				return fmt.Errorf("write %s to overlay: %w", key, err)
			}
			src = src[:lineStart] + src[end:]
			moved = append(moved, key)
		}

		if len(moved) == 0 {
			fmt.Fprintln(out, "nothing to move — config.jsonc holds no machine-specific keys")
			return nil
		}

		src = dropDanglingComma(src)
		localSrc = dropDanglingComma(localSrc)

		// Verify before writing anything. Writing first and checking afterwards
		// leaves a broken config on disk when the check fails, which is the one
		// outcome this command must never produce.
		after, err := loadFromPair(src, localSrc, sharedPath)
		if err != nil {
			return fmt.Errorf("split would produce an unreadable config (%w) — nothing written", err)
		}
		if err := sameEffectiveConfig(before, after); err != nil {
			return fmt.Errorf("split would change the effective config (%w) — nothing written", err)
		}

		if err := os.WriteFile(localPath, []byte(localSrc), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", localPath, err)
		}
		if err := os.WriteFile(sharedPath, []byte(src), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", sharedPath, err)
		}

		fmt.Fprintf(out, "moved %d key(s) to %s:\n", len(moved), localPath)
		for _, k := range moved {
			fmt.Fprintf(out, "  %s\n", k)
		}
		fmt.Fprintln(out, "\neffective config unchanged — verified by reloading both files")
		fmt.Fprintln(out, "config.jsonc can now be tracked; remove /config.jsonc from .gitignore")
		return nil
	},
}

// sameEffectiveConfig compares the fields the split moves, so a mistake is caught
// before it can reach the remote rather than after.
func sameEffectiveConfig(a, b config.Config) error {
	checks := []struct {
		name string
		a, b string
	}{
		{"data_root", a.DataRoot, b.DataRoot},
		{"articles_root", a.ArticlesRoot, b.ArticlesRoot},
		{"db_path", a.DBPath, b.DBPath},
		{"vector_path", a.VectorPath, b.VectorPath},
		{"events_path", a.EventsPath, b.EventsPath},
		{"agent_path", a.AgentPath, b.AgentPath},
		{"log_path", a.LogPath, b.LogPath},
		{"theme", a.Theme, b.Theme},
		{"sync.mode", a.Sync.Mode, b.Sync.Mode},
		{"sync.machine", a.Sync.Machine, b.Sync.Machine},
		{"sync.remote", a.Sync.Remote, b.Sync.Remote},
		{"sync.branch", a.Sync.Branch, b.Sync.Branch},
	}
	for _, c := range checks {
		if c.a != c.b {
			return fmt.Errorf("%s: %q became %q", c.name, c.a, c.b)
		}
	}
	if len(a.CookieJars) != len(b.CookieJars) {
		return fmt.Errorf("cookie_jars: %d entries became %d", len(a.CookieJars), len(b.CookieJars))
	}
	return nil
}

// dropDanglingComma removes a comma left immediately before the closing brace.
//
// Removing the last key in an object takes its own trailing comma with it but
// leaves the preceding key's, producing `"theme": "auto",\n}` — which is invalid
// JSON even under a JSONC parser.
func dropDanglingComma(src string) string {
	closing := strings.LastIndexByte(src, '}')
	if closing < 0 {
		return src
	}
	head := strings.TrimRight(src[:closing], " \t\n")
	if !strings.HasSuffix(head, ",") {
		return src
	}
	return strings.TrimSuffix(head, ",") + "\n" + src[closing:]
}

// commentBlockStart walks back from a key's line over any comment lines
// immediately above it, so the comment travels with the key it documents.
func commentBlockStart(src string, lineStart int) int {
	for lineStart > 0 {
		prevEnd := lineStart - 1 // the newline before this line
		prevStart := strings.LastIndexByte(src[:prevEnd], '\n') + 1
		line := strings.TrimSpace(src[prevStart:prevEnd])
		if !strings.HasPrefix(line, "//") {
			return lineStart
		}
		lineStart = prevStart
	}
	return lineStart
}

// loadFromPair parses a candidate shared/overlay pair without touching the real
// files, by materialising both in a temporary directory under the same names.
func loadFromPair(shared, local, sharedPath string) (config.Config, error) {
	dir, err := os.MkdirTemp("", "arc-split")
	if err != nil {
		return config.Config{}, err
	}
	defer os.RemoveAll(dir)

	name := filepath.Base(sharedPath)
	tmpShared := filepath.Join(dir, name)
	if err := os.WriteFile(tmpShared, []byte(shared), 0o600); err != nil {
		return config.Config{}, err
	}
	if err := os.WriteFile(config.LocalPath(tmpShared), []byte(local), 0o600); err != nil {
		return config.Config{}, err
	}
	return config.Load(tmpShared)
}

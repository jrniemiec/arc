package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jrniemiec/arc/config"
)

// updateConfigSync rewrites the "sync" block in the per-machine overlay,
// config.local.jsonc.
//
// The whole sync block lives there rather than in config.jsonc, so the shared
// config can be tracked. Two of its keys make this necessary:
//
//   - machine: sharing it is not merely redundant, it breaks the xlock. Two
//     machines reading the same value each compare the holder to their own name,
//     both match, and both conclude they already hold it — so both write without
//     acquiring.
//   - mode: a third machine may legitimately stay standalone, and init/enable/
//     disable rewrite it at runtime, which shared would mean a commit that flips
//     the mode everywhere.
//
// remote and branch could be shared, but splitting one block across two files is
// harder to reason about than keeping it whole, and sync init writes them on each
// machine anyway.
//
// The whole Config struct is deliberately not re-marshalled: these files are
// JSONC and users keep comments in them, which a round trip through the struct
// would silently delete. This locates the existing block by brace matching and
// swaps just that span, leaving every other byte — comments included — intact.
func updateConfigSync(mutate func(*config.SyncConfig)) error {
	path := config.LocalPath(configPath())

	// Load through the normal path so the merged value is the starting point:
	// Load reads the shared config and then the overlay. Loading the overlay
	// directly would lose anything set only in the shared file.
	cfg, err := config.Load(configPath())
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	sync := cfg.Sync
	mutate(&sync)

	block, err := json.MarshalIndent(sync, "  ", "  ")
	if err != nil {
		return fmt.Errorf("marshal sync config: %w", err)
	}
	replacement := `  "sync": ` + string(block)

	raw, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("read config: %w", err)
		}
		// First write to the overlay — create it.
		raw = []byte("{\n}\n")
	}
	updated, err := spliceJSONBlock(string(raw), "sync", replacement)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// configPath resolves the active config file, mirroring root.go.
func configPath() string {
	if cfgFile != "" {
		return cfgFile
	}
	return filepath.Join(arcHomeDir(), "config.jsonc")
}

// spliceJSONBlock replaces the value of a top-level object key, or appends the
// key when absent. replacement must include the quoted key and its value.
func spliceJSONBlock(src, key, replacement string) (string, error) {
	start, end, found := findObjectValue(src, key)
	if found {
		// Extend backwards over the key's own indentation so the replacement's
		// leading spaces do not double up.
		lineStart := strings.LastIndexByte(src[:start], '\n') + 1
		return src[:lineStart] + replacement + src[end:], nil
	}

	// Append before the final closing brace of the document.
	closing := strings.LastIndexByte(src, '}')
	if closing < 0 {
		return "", fmt.Errorf("config is not a JSON object")
	}
	head := strings.TrimRight(src[:closing], " \t\n")
	if !strings.HasSuffix(head, ",") && !strings.HasSuffix(head, "{") {
		head += ","
	}
	return head + "\n" + replacement + "\n" + src[closing:], nil
}

// findObjectValue locates the byte range of `"key": <value>` at the top level,
// including a trailing comma. Returns found=false when the key is absent.
//
// Scanning by hand rather than parsing keeps comments and formatting untouched;
// it only needs to handle strings and nesting well enough to find one span.
func findObjectValue(src, key string) (start, end int, found bool) {
	needle := `"` + key + `"`
	depth := 0
	inString := false
	escaped := false

	for i := 0; i < len(src); i++ {
		ch := src[i]

		if inString {
			switch {
			case escaped:
				escaped = false
			case ch == '\\':
				escaped = true
			case ch == '"':
				inString = false
			}
			continue
		}

		switch ch {
		case '"':
			// Only match the key at the top level of the root object.
			if depth == 1 && strings.HasPrefix(src[i:], needle) {
				valueStart := i
				j := i + len(needle)
				for j < len(src) && src[j] != ':' {
					j++
				}
				j++ // past the colon
				valueEnd := skipValue(src, j)
				if valueEnd < 0 {
					return 0, 0, false
				}
				// Absorb a trailing comma so the document stays well-formed.
				k := valueEnd
				for k < len(src) && (src[k] == ' ' || src[k] == '\t') {
					k++
				}
				if k < len(src) && src[k] == ',' {
					valueEnd = k + 1
				}
				return valueStart, valueEnd, true
			}
			inString = true
		case '{', '[':
			depth++
		case '}', ']':
			depth--
		}
	}
	return 0, 0, false
}

// skipValue returns the index just past the JSON value beginning at or after i.
func skipValue(src string, i int) int {
	for i < len(src) && (src[i] == ' ' || src[i] == '\t' || src[i] == '\n' || src[i] == '\r') {
		i++
	}
	if i >= len(src) {
		return -1
	}

	switch src[i] {
	case '{', '[':
		open, close := src[i], byte('}')
		if open == '[' {
			close = ']'
		}
		depth := 0
		inString := false
		escaped := false
		for ; i < len(src); i++ {
			ch := src[i]
			if inString {
				switch {
				case escaped:
					escaped = false
				case ch == '\\':
					escaped = true
				case ch == '"':
					inString = false
				}
				continue
			}
			switch ch {
			case '"':
				inString = true
			case open:
				depth++
			case close:
				depth--
				if depth == 0 {
					return i + 1
				}
			}
		}
		return -1

	case '"':
		escaped := false
		for j := i + 1; j < len(src); j++ {
			switch {
			case escaped:
				escaped = false
			case src[j] == '\\':
				escaped = true
			case src[j] == '"':
				return j + 1
			}
		}
		return -1

	default: // number, true, false, null
		for j := i; j < len(src); j++ {
			if strings.IndexByte(",}\n\r", src[j]) >= 0 {
				return j
			}
		}
		return len(src)
	}
}

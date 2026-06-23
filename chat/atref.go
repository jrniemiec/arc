package chat

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// WorkspaceResourceDir returns the path to a workspace's resources/ directory.
func WorkspaceResourceDir(dataRoot, workspaceName string) string {
	return filepath.Join(dataRoot, "workspaces", workspaceName, "resources")
}

// atRefRe matches @ref tokens (@ followed by non-whitespace).
var atRefRe = regexp.MustCompile(`@(\S+)`)

// ResolveAtRefs scans input for @ref tokens, reads each referenced file, and
// returns the assembled message: instruction text followed by [file: name] blocks.
// resourceDir is the workspace's resources/ directory (for bare-name refs).
// Returns input unchanged if no @ref tokens are present.
// Returns an error (and empty string) if any ref cannot be resolved.
func ResolveAtRefs(input, resourceDir string) (string, error) {
	matches := atRefRe.FindAllStringSubmatch(input, -1)
	if len(matches) == 0 {
		return input, nil
	}

	type block struct {
		name    string
		content string
	}

	seen := map[string]bool{}
	var blocks []block

	for _, m := range matches {
		ref := m[1]
		if seen[ref] {
			continue
		}
		seen[ref] = true

		path := resolveRef(ref, resourceDir)
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("@%s: file not found: %s", ref, path)
		}
		blocks = append(blocks, block{
			name:    filepath.Base(path),
			content: strings.TrimRight(string(data), "\n"),
		})
	}

	// Strip all @ref tokens from the instruction, collapse whitespace.
	instruction := atRefRe.ReplaceAllString(input, "")
	instruction = strings.Join(strings.Fields(instruction), " ")

	var sb strings.Builder
	sb.WriteString(instruction)
	for _, b := range blocks {
		sb.WriteString("\n\n[file: ")
		sb.WriteString(b.name)
		sb.WriteString("]\n")
		sb.WriteString(b.content)
	}
	return sb.String(), nil
}

// resolveRef maps a ref string to a filesystem path.
//
//	@/abs/path      → absolute path
//	@./rel or @../  → relative to cwd
//	@~/path         → home-relative
//	@name           → resourceDir/name (workspace resource lookup)
func resolveRef(ref, resourceDir string) string {
	switch {
	case strings.HasPrefix(ref, "/"):
		return ref
	case strings.HasPrefix(ref, "./"), strings.HasPrefix(ref, "../"):
		return ref
	case strings.HasPrefix(ref, "~/"):
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ref[2:])
	default:
		return filepath.Join(resourceDir, ref)
	}
}

package tui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/bubbles/textarea"

	"github.com/jrniemiec/arc/config"
	storefs "github.com/jrniemiec/arc/store/fs"
)

// wsFileModel builds a model with one workspace holding the given outcome and
// resource files, written to a temp data root so sizes are readable.
func wsFileModel(t *testing.T, outcomes, resources []string, dirs []string) *Model {
	t.Helper()
	root := t.TempDir()
	for _, spec := range []struct {
		subdir string
		names  []string
	}{{"outcomes", outcomes}, {"resources", resources}} {
		dir := filepath.Join(storefs.WorkspaceDir(root, "transformers"), spec.subdir)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, n := range spec.names {
			if err := os.WriteFile(filepath.Join(dir, n), []byte("body\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	m := &Model{input: textarea.New(), cfg: config.Config{DataRoot: root}}
	m.workspaceItems = []workspaceItem{{
		name:      "transformers",
		outcomes:  outcomes,
		resources: resources,
		// resourceDirs are listed as directories, without a size.
		resourceDirs: dirs,
	}}
	m.chatMode = true
	m.chatWorkspace = "transformers"
	return m
}

// Commands taking an existing filename must offer it — typing outcome names by
// hand is exactly what the picker exists to avoid.
func TestParamSuggestionsOutcomeAndResourceFiles(t *testing.T) {
	m := wsFileModel(t,
		[]string{"chat-2026-08-06.md", "review.md"},
		[]string{"paper.pdf"},
		[]string{"data"},
	)

	for _, cmd := range []string{"/outcome-view", "/outcome-edit", "/outcome-delete", "/outcome-remove"} {
		items := m.paramSuggestions(cmd, "")
		if len(items) != 2 {
			t.Fatalf("%s: got %d items, want 2", cmd, len(items))
		}
		if items[0].cmd != "chat-2026-08-06.md" || items[1].cmd != "review.md" {
			t.Errorf("%s: got %q/%q, want the two outcome files", cmd, items[0].cmd, items[1].cmd)
		}
		if items[0].desc != "5 B" {
			t.Errorf("%s: desc = %q, want the file size", cmd, items[0].desc)
		}
	}

	for _, cmd := range []string{"/resource-view", "/resource-edit", "/resource-delete", "/resource-remove"} {
		items := m.paramSuggestions(cmd, "")
		if len(items) != 2 {
			t.Fatalf("%s: got %d items, want 2", cmd, len(items))
		}
		// Directories sort ahead of files and carry no size.
		if items[0].cmd != "data" || items[0].desc != "dir" {
			t.Errorf("%s: first item = %q/%q, want data/dir", cmd, items[0].cmd, items[0].desc)
		}
		if items[1].cmd != "paper.pdf" {
			t.Errorf("%s: second item = %q, want paper.pdf", cmd, items[1].cmd)
		}
	}
}

// Outside chat the picker follows the Workspaces tree cursor; with no workspace
// in context there is nothing to list.
func TestParamSuggestionsOutcomeWorkspaceResolution(t *testing.T) {
	m := wsFileModel(t, []string{"review.md"}, nil, nil)

	m.chatMode = false
	m.chatWorkspace = ""
	m.wsRows = []wsRow{{kind: wsRowWorkspace, wsIdx: 0}}
	m.wsCursor = 0
	if items := m.paramSuggestions("/outcome-view", ""); len(items) != 1 {
		t.Errorf("workspace under cursor: got %d items, want 1", len(items))
	}

	m.wsRows = nil
	m.wsCursor = -1
	if items := m.paramSuggestions("/outcome-view", ""); items != nil {
		t.Errorf("no workspace in context: got %v, want nil", items)
	}
}

// Commands that name a new file have nothing to pick, so the hint names the
// directory the file lands in instead.
func TestParamHintForNewWorkspaceFiles(t *testing.T) {
	m := wsFileModel(t, nil, nil, nil)

	for cmd, want := range map[string]string{
		"/outcome-new":   "creating in: transformers/outcomes",
		"/outcome-save":  "creating in: transformers/outcomes",
		"/save":          "creating in: transformers/outcomes",
		"/resource-new":  "creating in: transformers/resources",
		"/resource-save": "creating in: transformers/resources",
	} {
		if got := m.paramHintFor(cmd); got != want {
			t.Errorf("%s: got %q, want %q", cmd, got, want)
		}
	}

	if got := m.paramSuggestions("/outcome-new", ""); got != nil {
		t.Errorf("/outcome-new suggestions = %v, want nil", got)
	}
}

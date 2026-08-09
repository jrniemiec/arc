package tui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/bubbles/textarea"

	"github.com/jrniemiec/arc/config"
)

// pathHome builds a fake home directory and points HOME at it, so ~ and $HOME
// in a typed path resolve somewhere the test controls.
func pathHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	for _, name := range []string{"notes.md", ".hidden"} {
		if err := os.WriteFile(filepath.Join(home, name), []byte("body\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(home, "papers"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	return home
}

func itemNames(items []cmdCompletion) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.cmd
	}
	return out
}

func TestPathEntriesKeepsTypedPrefix(t *testing.T) {
	home := pathHome(t)
	m := &Model{input: textarea.New(), cfg: config.Config{DataRoot: t.TempDir()}}

	// The prefix survives verbatim: completing ~ must not paste an absolute path.
	got := itemNames(m.paramSuggestions("/resource-add", "~/"))
	want := []string{"~/notes.md", "~/papers/"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("~/ = %v, want %v", got, want)
	}

	// Nothing typed yet: no listing until an explicit trigger appears.
	if got := m.paramSuggestions("/resource-add", ""); got != nil {
		t.Errorf("empty arg = %v, want nil", got)
	}
	// A bare name with no separator is not a trigger either.
	if got := m.paramSuggestions("/resource-add", "note"); got != nil {
		t.Errorf("bare name = %v, want nil", got)
	}

	// $VAR expands for the lookup but stays in the value.
	t.Setenv("PAPERS", filepath.Join(home, "papers"))
	if err := os.WriteFile(filepath.Join(home, "papers", "attention.pdf"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got = itemNames(m.paramSuggestions("/resource-add", "$PAPERS/"))
	if len(got) != 1 || got[0] != "$PAPERS/attention.pdf" {
		t.Errorf("$PAPERS/ = %v, want [$PAPERS/attention.pdf]", got)
	}

	// Directories are marked as such; files carry their size.
	items := m.paramSuggestions("/resource-add", "~/")
	if items[1].desc != "dir" {
		t.Errorf("papers desc = %q, want dir", items[1].desc)
	}
	if items[0].desc != "5 B" {
		t.Errorf("notes.md desc = %q, want a size", items[0].desc)
	}
}

func TestPathEntriesHidesDotfilesUntilAsked(t *testing.T) {
	pathHome(t)
	m := &Model{input: textarea.New(), cfg: config.Config{DataRoot: t.TempDir()}}

	for _, name := range itemNames(m.paramSuggestions("/resource-add", "~/")) {
		if name == "~/.hidden" {
			t.Fatal("dotfile listed without a dot typed")
		}
	}
	var found bool
	for _, name := range itemNames(m.paramSuggestions("/resource-add", "~/.")) {
		if name == "~/.hidden" {
			found = true
		}
	}
	if !found {
		t.Error("dotfile missing after a dot was typed")
	}
}

// Relative prefixes anchor at the arc doc root, not $HOME or the process's
// launch directory, and only kick in once an explicit trigger is typed.
func TestPathEntriesAnchorsAtArcRoot(t *testing.T) {
	pathHome(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "articles"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(root), "outside.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := &Model{input: textarea.New(), cfg: config.Config{DataRoot: root}}

	// A bare relative name, with no separator typed, is not a trigger.
	if got := m.paramSuggestions("/resource-add", "articles"); got != nil {
		t.Errorf("bare relative name = %v, want nil", got)
	}

	// "./" is a trigger, and lists the arc root itself rather than $HOME or CWD.
	got := itemNames(m.paramSuggestions("/resource-add", "./"))
	want := []string{"./articles/", "./config.json"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("./ = %v, want %v", got, want)
	}

	// "../" is a trigger too, and walks up from the arc root, not $HOME or CWD.
	if got := itemNames(m.paramSuggestions("/resource-add", "../")); !containsItem(got, "../outside.txt") {
		t.Errorf("../ = %v, want it to include ../outside.txt", got)
	}

	// A plain "/" is unchanged: absolute paths still mean the filesystem root.
	if got := m.paramSuggestions("/resource-add", "/"); got == nil {
		t.Error("/ = nil, want the filesystem root listing")
	}
}

func containsItem(items []string, want string) bool {
	for _, it := range items {
		if it == want {
			return true
		}
	}
	return false
}

// /outcome-add completes paths the same way, minus the flags a flat directory
// has no use for.
func TestPathSuggestionsOutcomeAdd(t *testing.T) {
	pathHome(t)
	m := &Model{input: textarea.New(), cfg: config.Config{DataRoot: t.TempDir()}}
	m.workspaceItems = []workspaceItem{{name: "transformers", resourceDirs: []string{"data"}}}
	m.chatMode = true
	m.chatWorkspace = "transformers"

	got := itemNames(m.paramSuggestions("/outcome-add", "~/"))
	if len(got) != 2 || got[0] != "~/notes.md" || got[1] != "~/papers/" {
		t.Errorf("~/ = %v, want the same listing /resource-add gives", got)
	}
	if got := itemNames(m.paramSuggestions("/outcome-add", "~/notes.md --")); len(got) != 1 || got[0] != "--as" {
		t.Errorf("dash = %v, want only --as", got)
	}
	// Outcomes are flat, so --into is not a thing to complete for.
	if got := m.paramSuggestions("/outcome-add", "~/notes.md --into "); got != nil {
		t.Errorf("--into = %v, want nil", got)
	}
	if got := m.paramSuggestions("/outcome-add", "~/notes.md --as "); got != nil {
		t.Errorf("--as = %v, want nil", got)
	}
	// The quoted-path rule applies here too.
	if got := paramLastToken("/outcome-add", `"~/My Documents/re`); got != "~/My Documents/re" {
		t.Errorf("quoted token = %q, want the whole path", got)
	}
}

// Only the path token is a path — the flag values around it are not.
func TestPathSuggestionsFlagArguments(t *testing.T) {
	pathHome(t)
	m := &Model{input: textarea.New(), cfg: config.Config{DataRoot: t.TempDir()}}
	m.workspaceItems = []workspaceItem{{name: "transformers", resourceDirs: []string{"data"}}}
	m.chatMode = true
	m.chatWorkspace = "transformers"

	if got := itemNames(m.paramSuggestions("/resource-add", "~/notes.md --into ")); len(got) != 1 || got[0] != "data" {
		t.Errorf("--into = %v, want existing resource folders", got)
	}
	if got := m.paramSuggestions("/resource-add", "https://x.test/a --as "); got != nil {
		t.Errorf("--as = %v, want nil", got)
	}
	if got := m.paramSuggestions("/resource-add", "~/notes.md --comment some note"); got != nil {
		t.Errorf("--comment = %v, want nil", got)
	}
	if got := itemNames(m.paramSuggestions("/resource-add", "~/notes.md --")); len(got) != 3 || got[0] != "--into" {
		t.Errorf("dash = %v, want the flag list", got)
	}
	if got := m.paramSuggestions("/resource-add", "https://x.test/a"); got != nil {
		t.Errorf("URL = %v, want nil", got)
	}
}

// A quoted path holds spaces, so the picker cannot split it on them.
func TestParamTokenStartQuotedPath(t *testing.T) {
	tests := []struct {
		cmd, arg, want string
	}{
		{"/resource-add", `~/pap`, "~/pap"},
		{"/resource-add", `--into data ~/pap`, "~/pap"},
		{"/resource-add", `"~/My Doc`, "~/My Doc"},
		{"/resource-add", `--into data "~/My Documents/re`, "~/My Documents/re"},
		{"/resource-add", `"~/My Documents/a.pdf" --into `, ""},
		// Other commands keep the plain last-space rule.
		{"/help", "article sea", "sea"},
		{"/collection-add", "trans", "trans"},
	}
	for _, tt := range tests {
		if got := paramLastToken(tt.cmd, tt.arg); got != tt.want {
			t.Errorf("paramLastToken(%q, %q) = %q, want %q", tt.cmd, tt.arg, got, tt.want)
		}
	}
}

func TestAcceptParamQuotesAndDescends(t *testing.T) {
	home := pathHome(t)
	if err := os.Mkdir(filepath.Join(home, "My Docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "My Docs", "report.pdf"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := &Model{input: textarea.New(), cfg: config.Config{DataRoot: t.TempDir()}}

	// Accepting a directory with spaces opens the quote and keeps it open, so
	// the next segment lands inside the path, and lists that directory at once.
	m.input.SetValue("/resource-add ~/My")
	m.paramItems = []cmdCompletion{{cmd: "~/My Docs/"}}
	m.paramIdx = 0
	m.acceptParam()
	if got, want := m.input.Value(), `/resource-add "~/My Docs/`; got != want {
		t.Fatalf("after accepting a directory: %q, want %q", got, want)
	}
	if len(m.paramItems) != 1 || m.paramItems[0].cmd != "~/My Docs/report.pdf" {
		t.Fatalf("picker after descent = %v, want the directory contents", itemNames(m.paramItems))
	}

	// Accepting the file closes the quote — the command is now runnable.
	m.paramIdx = 0
	m.acceptParam()
	if got, want := m.input.Value(), `/resource-add "~/My Docs/report.pdf"`; got != want {
		t.Errorf("after accepting a file: %q, want %q", got, want)
	}
	if len(m.paramItems) != 0 {
		t.Errorf("picker stayed open on a file: %v", itemNames(m.paramItems))
	}

	// A path without spaces is left unquoted.
	m.input.SetValue("/resource-add ~/no")
	m.paramItems = []cmdCompletion{{cmd: "~/notes.md"}}
	m.paramIdx = 0
	m.acceptParam()
	if got, want := m.input.Value(), "/resource-add ~/notes.md"; got != want {
		t.Errorf("unquoted path: %q, want %q", got, want)
	}
}

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Setting only data_root used to move the root while leaving articles_root,
// db_path and the rest pointing at the default ~/.arc — so arc read one tree and
// wrote another, with nothing to indicate it. The --data-root flag always
// re-derived them; the config file did not.
func TestDataRootDerivesSiblingPaths(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.jsonc")
	root := filepath.Join(dir, "tree")
	write(t, cfgPath, `{"data_root": "`+root+`"}`)

	c, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct{ name, got, want string }{
		{"articles_root", c.ArticlesRoot, filepath.Join(root, "articles")},
		{"db_path", c.DBPath, filepath.Join(root, "arc.db")},
		{"vector_path", c.VectorPath, filepath.Join(root, "index")},
		{"events_path", c.EventsPath, filepath.Join(root, "events.jsonl")},
		{"agent_path", c.AgentPath, filepath.Join(root, "agent")},
		{"log_path", c.LogPath, filepath.Join(root, "arc.log")},
	} {
		if tt.got != tt.want {
			t.Errorf("%s = %q, want %q", tt.name, tt.got, tt.want)
		}
	}
}

// Deriving must not clobber a path the user set on purpose.
func TestExplicitPathBeatsDerivation(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.jsonc")
	root := filepath.Join(dir, "tree")
	write(t, cfgPath, `{"data_root": "`+root+`", "db_path": "/somewhere/else/arc.db"}`)

	c, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if c.DBPath != "/somewhere/else/arc.db" {
		t.Errorf("db_path = %q, want the explicit value", c.DBPath)
	}
	if want := filepath.Join(root, "index"); c.VectorPath != want {
		t.Errorf("vector_path = %q, want %q — siblings still derive", c.VectorPath, want)
	}
}

// The overlay must be able to override a single path without disturbing the
// others, which is the whole point of splitting the config.
func TestOverlayOverridesOnePath(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.jsonc")
	root := filepath.Join(dir, "tree")
	write(t, cfgPath, `{"data_root": "`+root+`"}`)
	write(t, LocalPath(cfgPath), `{"db_path": "/local/only/arc.db"}`)

	c, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if c.DBPath != "/local/only/arc.db" {
		t.Errorf("db_path = %q, want the overlay value", c.DBPath)
	}
	if want := filepath.Join(root, "articles"); c.ArticlesRoot != want {
		t.Errorf("articles_root = %q, want %q", c.ArticlesRoot, want)
	}
}

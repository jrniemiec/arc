package fs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jrniemiec/arc/config"
)

// A deleted workspace must stay deleted: writes from read paths must not
// recreate its directory, and the name must remain reusable.
func TestDeletedWorkspaceStaysDeleted(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(WorkspacesRoot(root), 0755); err != nil {
		t.Fatal(err)
	}
	cfg := config.ChatConfig{GroundingMode: "corpus-first"}

	if err := CreateWorkspace(root, "transformers", "how transformers work", cfg); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := DeleteWorkspace(root, "transformers"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// The back-fill that fires while loading chat history must not resurrect it.
	if err := WriteChatConfig(root, "transformers", cfg); err == nil {
		t.Error("WriteChatConfig on a deleted workspace: want error, got nil")
	}
	if _, err := os.Stat(WorkspaceDir(root, "transformers")); !os.IsNotExist(err) {
		t.Error("workspace directory was recreated by WriteChatConfig")
	}

	// The name is reusable.
	if err := CreateWorkspace(root, "transformers", "second attempt", cfg); err != nil {
		t.Fatalf("recreate after delete: %v", err)
	}
}

func TestCreateWorkspaceAdoptsOrphanDir(t *testing.T) {
	root := t.TempDir()
	cfg := config.ChatConfig{GroundingMode: "corpus-first"}

	// An orphan left by an older build: directory and chat config, no meta.json.
	orphan := filepath.Join(WorkspaceDir(root, "transformers"), "chat")
	if err := os.MkdirAll(orphan, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphan, "config.jsonc"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	if WorkspaceExists(root, "transformers") {
		t.Error("orphan dir without meta.json should not count as an existing workspace")
	}
	if err := CreateWorkspace(root, "transformers", "adopted", cfg); err != nil {
		t.Fatalf("create over orphan: %v", err)
	}
	m, err := ReadWorkspaceMeta(root, "transformers")
	if err != nil {
		t.Fatalf("read meta after adoption: %v", err)
	}
	if m.Description != "adopted" {
		t.Errorf("description: want %q got %q", "adopted", m.Description)
	}
}

func TestCreateWorkspaceRejectsLiveWorkspace(t *testing.T) {
	root := t.TempDir()
	cfg := config.ChatConfig{GroundingMode: "corpus-first"}

	if err := CreateWorkspace(root, "transformers", "first", cfg); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := CreateWorkspace(root, "transformers", "second", cfg); err == nil {
		t.Fatal("create over a live workspace: want error, got nil")
	}
	m, err := ReadWorkspaceMeta(root, "transformers")
	if err != nil {
		t.Fatal(err)
	}
	if m.Description != "first" {
		t.Errorf("existing workspace was overwritten: description = %q", m.Description)
	}
}

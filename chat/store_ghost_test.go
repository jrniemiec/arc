package chat

import (
	"os"
	"path/filepath"
	"testing"
)

// Reading chat state for a workspace that no longer exists must not recreate
// its directory — that is what left a deleted workspace behind as a ghost that
// the Workspaces view hid but CreateWorkspace tripped over.
func TestChatStoreDoesNotResurrectDeletedWorkspace(t *testing.T) {
	root := t.TempDir()
	wsDir := filepath.Join(root, "workspaces", "transformers")

	st := NewChatStore(root, "transformers")
	if _, err := st.LoadHistory(); err == nil {
		t.Error("LoadHistory on a missing workspace: want error, got nil")
	}
	if _, err := os.Stat(wsDir); !os.IsNotExist(err) {
		t.Error("LoadHistory recreated the workspace directory")
	}
	if err := st.SaveHistory(NewHistory()); err == nil {
		t.Error("SaveHistory on a missing workspace: want error, got nil")
	}
	if _, err := os.Stat(wsDir); !os.IsNotExist(err) {
		t.Error("SaveHistory recreated the workspace directory")
	}
}

func TestChatStoreWorksForLiveWorkspace(t *testing.T) {
	root := t.TempDir()
	wsDir := filepath.Join(root, "workspaces", "transformers")
	if err := os.MkdirAll(wsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsDir, "meta.json"), []byte(`{"name":"transformers"}`), 0644); err != nil {
		t.Fatal(err)
	}

	st := NewChatStore(root, "transformers")
	h, err := st.LoadHistory()
	if err != nil {
		t.Fatalf("LoadHistory: %v", err)
	}
	h.Append(RoleUser, "hello")
	if err := st.SaveHistory(h); err != nil {
		t.Fatalf("SaveHistory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wsDir, "chat", "history.json")); err != nil {
		t.Errorf("history not written: %v", err)
	}
}

// Article chat stores address their directory directly and must keep working
// without any workspace meta.json.
func TestArticleChatStoreUnaffected(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "some-article"), 0755); err != nil {
		t.Fatal(err)
	}
	st := NewArticleChatStore(root, "some-article")
	if _, err := st.LoadHistory(); err != nil {
		t.Fatalf("article LoadHistory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "some-article", "chat")); err != nil {
		t.Errorf("article chat dir not created: %v", err)
	}
}

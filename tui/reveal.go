package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	tea "github.com/charmbracelet/bubbletea"

	storefs "github.com/jrniemiec/arc/store/fs"
)

// revealInFinder shows path in the system file manager.
//
// Directories are opened so their contents are listed; files open their parent
// folder with the file itself selected. The distinction matters on macOS, where
// plain `open <file>` launches the file's default application instead of Finder —
// only `open -R` reveals.
func revealInFinder(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	isDir := info.IsDir()

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		if isDir {
			cmd = exec.Command("open", path)
		} else {
			cmd = exec.Command("open", "-R", path)
		}
	case "linux":
		dir := path
		if !isDir {
			dir = filepath.Dir(path)
		}
		cmd = exec.Command("xdg-open", dir)
	case "windows":
		if isDir {
			cmd = exec.Command("explorer", path)
		} else {
			cmd = exec.Command("explorer", "/select,"+path)
		}
	default:
		return fmt.Errorf("reveal not supported on %s", runtime.GOOS)
	}
	return cmd.Start()
}

// revealPath returns the filesystem path to reveal for the current Library
// selection, or "" when the selected row has no folder of its own.
//
// Kept free of side effects so the row-kind mapping can be tested directly.
func (m *Model) revealPath() string {
	if m.activeTab != tabLibrary {
		return ""
	}
	switch m.navSubTab {
	case navSubTabArticles:
		return m.articleDirForItem(m.selectedNavItem())

	case navSubTabCollections:
		if m.navRowCursor < 0 || m.navRowCursor >= len(m.navRows) {
			return ""
		}
		row := m.navRows[m.navRowCursor]
		if row.kind == rowCollection {
			return storefs.CollectionDir(m.cfg.DataRoot, row.colSlug)
		}
		return m.articleDirForItem(m.selectedNavItem())

	case navSubTabWorkspaces:
		// Search results replace the tree with a flat article list.
		if m.wsSearchActive() {
			return m.articleDirForItem(m.selectedNavItem())
		}
		row := m.selectedWsRow()
		if row == nil || row.wsIdx < 0 || row.wsIdx >= len(m.workspaceItems) {
			return ""
		}
		wsDir := storefs.WorkspaceDir(m.cfg.DataRoot, m.workspaceItems[row.wsIdx].name)
		switch row.kind {
		case wsRowWorkspace, wsRowAtticGroup:
			// The attic is a manifest inside the workspace, not a directory.
			return wsDir
		case wsRowCollection, wsRowAtticCollection:
			return storefs.CollectionDir(m.cfg.DataRoot, row.colSlug)
		case wsRowArticle, wsRowAtticArticle:
			return m.articleDirForSlug(row.slug)
		case wsRowResourceGroup:
			return filepath.Join(wsDir, "resources")
		case wsRowOutcomeGroup:
			return filepath.Join(wsDir, "outcomes")
		case wsRowScratch, wsRowResource, wsRowResourceDir, wsRowOutcome:
			return m.wsFilePathForRow(row)
		}
	}
	return ""
}

// articleDirForItem returns the article directory for a nav item, or "" if nil.
func (m *Model) articleDirForItem(item *navItem) string {
	if item == nil {
		return ""
	}
	if item.root != "" {
		return item.root
	}
	return m.articleDirForSlug(item.id)
}

// articleDirForSlug returns the article directory for a slug. Attic rows carry
// only a slug, so the root recorded on the nav item is not always available.
func (m *Model) articleDirForSlug(slug string) string {
	if slug == "" {
		return ""
	}
	for i := range m.navItemsAll {
		if m.navItemsAll[i].id == slug && m.navItemsAll[i].root != "" {
			return m.navItemsAll[i].root
		}
	}
	return filepath.Join(m.cfg.ArticlesRoot, slug)
}

// revealSelected reveals the selected Library item in the system file manager.
func (m *Model) revealSelected() tea.Cmd {
	path := m.revealPath()
	if path == "" {
		m.statusMsg = "nothing to reveal here"
		return nil
	}
	if err := revealInFinder(path); err != nil {
		m.statusMsg = fmt.Sprintf("reveal failed: %v", err)
		return nil
	}
	m.statusMsg = "revealed " + filepath.Base(path)
	return nil
}

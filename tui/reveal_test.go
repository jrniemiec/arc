package tui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jrniemiec/arc/config"
	storefs "github.com/jrniemiec/arc/store/fs"
)

// revealModel builds a Library model with one expanded workspace holding a
// collection, an article, a resource file, a resource dir and an outcome.
func revealModel(t *testing.T) *Model {
	t.Helper()
	root := t.TempDir()
	articlesRoot := filepath.Join(root, "articles")
	if err := os.MkdirAll(filepath.Join(articlesRoot, "attention"), 0o755); err != nil {
		t.Fatal(err)
	}

	m := &Model{
		activeTab: tabLibrary,
		navSubTab: navSubTabWorkspaces,
		cfg:       config.Config{DataRoot: root, ArticlesRoot: articlesRoot},
	}
	m.navItemsAll = []navItem{{
		id:   "attention",
		root: filepath.Join(articlesRoot, "attention"),
	}}
	m.workspaceItems = []workspaceItem{{
		name:             "transformers",
		articles:         []string{"attention"},
		collectionSlugs:  []string{"papers"},
		resources:        []string{"paper.pdf"},
		resourceDirs:     []string{"data"},
		outcomes:         []string{"review.md"},
		atticArticles:    []string{"bert"},
		atticCollections: []string{"old"},
	}}
	return m
}

// Every workspace row kind must resolve to a folder or file on disk — the whole
// point of one key is that it never dead-ends on a row the user can select.
func TestRevealPathWorkspaceRows(t *testing.T) {
	m := revealModel(t)
	root := m.cfg.DataRoot
	wsDir := storefs.WorkspaceDir(root, "transformers")

	tests := []struct {
		name string
		row  wsRow
		want string
	}{
		{"workspace", wsRow{kind: wsRowWorkspace}, wsDir},
		{"scratch", wsRow{kind: wsRowScratch}, storefs.ScratchPath(root, "transformers")},
		{"collection", wsRow{kind: wsRowCollection, colSlug: "papers"}, storefs.CollectionDir(root, "papers")},
		{"article", wsRow{kind: wsRowArticle, slug: "attention"}, filepath.Join(m.cfg.ArticlesRoot, "attention")},
		{"resource group", wsRow{kind: wsRowResourceGroup}, filepath.Join(wsDir, "resources")},
		{"resource dir", wsRow{kind: wsRowResourceDir, resourceName: "data"}, filepath.Join(wsDir, "resources", "data")},
		{"resource file", wsRow{kind: wsRowResource, resourceName: "paper.pdf"}, filepath.Join(wsDir, "resources", "paper.pdf")},
		{"outcome group", wsRow{kind: wsRowOutcomeGroup}, filepath.Join(wsDir, "outcomes")},
		{"outcome file", wsRow{kind: wsRowOutcome, outcomeName: "review.md"}, filepath.Join(wsDir, "outcomes", "review.md")},
		{"attic group", wsRow{kind: wsRowAtticGroup}, wsDir},
		{"attic article", wsRow{kind: wsRowAtticArticle, slug: "bert"}, filepath.Join(m.cfg.ArticlesRoot, "bert")},
		{"attic collection", wsRow{kind: wsRowAtticCollection, colSlug: "old"}, storefs.CollectionDir(root, "old")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m.wsRows = []wsRow{tt.row}
			m.wsCursor = 0
			if got := m.revealPath(); got != tt.want {
				t.Errorf("revealPath() = %q, want %q", got, tt.want)
			}
		})
	}
}

// The Articles and Collections sub-tabs resolve through different cursors than
// the workspace tree, so each needs its own check.
func TestRevealPathArticlesAndCollections(t *testing.T) {
	m := revealModel(t)
	articleDir := filepath.Join(m.cfg.ArticlesRoot, "attention")

	m.navSubTab = navSubTabArticles
	m.navItems = m.navItemsAll
	m.navCursor = 0
	if got := m.revealPath(); got != articleDir {
		t.Errorf("articles sub-tab: revealPath() = %q, want %q", got, articleDir)
	}

	m.navSubTab = navSubTabCollections
	m.navRows = []navRow{
		{kind: rowCollection, colSlug: "papers"},
		{kind: rowArticle, item: &m.navItemsAll[0]},
	}

	m.navRowCursor = 0
	want := storefs.CollectionDir(m.cfg.DataRoot, "papers")
	if got := m.revealPath(); got != want {
		t.Errorf("collection row: revealPath() = %q, want %q", got, want)
	}

	m.navRowCursor = 1
	if got := m.revealPath(); got != articleDir {
		t.Errorf("article row in collection: revealPath() = %q, want %q", got, articleDir)
	}
}

// Reveal is Library-only, and an empty selection must not resolve to the data
// root — revealing "everything" on a stray keypress is worse than doing nothing.
func TestRevealPathEmptySelections(t *testing.T) {
	m := revealModel(t)

	m.wsRows = nil
	m.wsCursor = 0
	if got := m.revealPath(); got != "" {
		t.Errorf("empty workspace tree: revealPath() = %q, want \"\"", got)
	}

	m.navSubTab = navSubTabArticles
	m.navItems = nil
	m.navCursor = 0
	if got := m.revealPath(); got != "" {
		t.Errorf("no articles: revealPath() = %q, want \"\"", got)
	}

	m.activeTab = tabAgent
	m.navSubTab = navSubTabWorkspaces
	m.wsRows = []wsRow{{kind: wsRowWorkspace}}
	if got := m.revealPath(); got != "" {
		t.Errorf("agent tab: revealPath() = %q, want \"\"", got)
	}
}

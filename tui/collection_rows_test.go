package tui

import "testing"

// modelWithExpandedCollection builds a Collections tree holding one expanded
// collection whose children are articles 0 and 2 of navItemsAll, mirroring what
// loadCollectionArticlesCmd produces (navItemsAll order, filtered to members).
func modelWithExpandedCollection() *Model {
	m := &Model{
		navItemsAll: []navItem{
			{id: "20260805-alpha", collections: []string{"papers"}},
			{id: "20260805-beta"},
			{id: "20260805-gamma", collections: []string{"papers"}},
			{id: "20260805-delta"},
		},
	}
	m.navRows = []navRow{
		{kind: rowCollection, colSlug: "papers", expanded: true, colCount: 2},
		{kind: rowArticle, item: &m.navItemsAll[0], indented: true},
		{kind: rowArticle, item: &m.navItemsAll[2], indented: true},
		{kind: rowCollection, colSlug: "notes"},
	}
	return m
}

func childSlugs(m *Model) []string {
	var out []string
	for i := 1; i < len(m.navRows); i++ {
		r := m.navRows[i]
		if r.kind != rowArticle || !r.indented {
			break
		}
		out = append(out, r.item.id)
	}
	return out
}

func TestInsertCollectionChildKeepsNavItemsOrder(t *testing.T) {
	// beta sits between alpha and gamma in navItemsAll, so its row belongs between them.
	m := modelWithExpandedCollection()
	m.insertCollectionChild(0, "20260805-beta")

	got := childSlugs(m)
	want := []string{"20260805-alpha", "20260805-beta", "20260805-gamma"}
	if len(got) != len(want) {
		t.Fatalf("children = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("children = %v, want %v", got, want)
		}
	}
	// The next collection's header must survive the insert.
	last := m.navRows[len(m.navRows)-1]
	if last.kind != rowCollection || last.colSlug != "notes" {
		t.Errorf("trailing row = %+v, want the notes header", last)
	}
}

func TestInsertCollectionChildAppendsAfterLastChild(t *testing.T) {
	// delta sorts after every current member, so it lands at the end of the block —
	// not after the following collection header.
	m := modelWithExpandedCollection()
	m.insertCollectionChild(0, "20260805-delta")

	got := childSlugs(m)
	if len(got) != 3 || got[2] != "20260805-delta" {
		t.Fatalf("children = %v, want delta last", got)
	}
	if m.navRows[len(m.navRows)-1].colSlug != "notes" {
		t.Error("insert displaced the following collection header")
	}
}

func TestInsertCollectionChildSkipsCollapsed(t *testing.T) {
	m := modelWithExpandedCollection()
	m.navRows[0].expanded = false
	before := len(m.navRows)
	m.insertCollectionChild(0, "20260805-beta")
	if len(m.navRows) != before {
		t.Errorf("collapsed collection gained a row: %d → %d", before, len(m.navRows))
	}
}

func TestInsertCollectionChildIgnoresDuplicate(t *testing.T) {
	m := modelWithExpandedCollection()
	before := len(m.navRows)
	m.insertCollectionChild(0, "20260805-alpha")
	if len(m.navRows) != before {
		t.Errorf("already-shown article was inserted again: %d → %d", before, len(m.navRows))
	}
}

func TestInsertCollectionChildMovesCursorWithRows(t *testing.T) {
	// Cursor on gamma; inserting beta above it must keep it on gamma.
	m := modelWithExpandedCollection()
	m.navRowCursor = 2
	m.insertCollectionChild(0, "20260805-beta")
	if got := m.navRows[m.navRowCursor].item.id; got != "20260805-gamma" {
		t.Errorf("cursor landed on %q, want 20260805-gamma", got)
	}
}

func TestSyncCollectionRowsAddInserts(t *testing.T) {
	m := modelWithExpandedCollection()
	m.syncCollectionRows(collectionMembershipMsg{
		articleSlug: "20260805-beta",
		collSlug:    "papers",
		added:       true,
		count:       3,
	})
	if got := len(childSlugs(m)); got != 3 {
		t.Fatalf("children = %d, want 3", got)
	}
	if m.navRows[0].colCount != 3 {
		t.Errorf("header count = %d, want 3", m.navRows[0].colCount)
	}
}

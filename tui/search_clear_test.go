package tui

import "testing"

// searchedModel returns a model with an active article search: navItems holds a
// one-item result set, navItemsAll the full list, and navFilter the badge.
func searchedModel() Model {
	all := []navItem{{id: "a"}, {id: "b"}, {id: "c"}}
	return Model{
		navItems:    all[:1],
		navItemsAll: all,
		navCursor:   0,
		navFilter:   `search: "a" · 1 results  ·  esc or /clear to reset`,
	}
}

func TestClearNavSearchRestoresLists(t *testing.T) {
	m := searchedModel()
	if !m.clearNavSearch() {
		t.Fatal("clearNavSearch returned false with an active filter")
	}
	if m.navFilter != "" {
		t.Errorf("navFilter = %q, want empty", m.navFilter)
	}
	if len(m.navItems) != 3 {
		t.Errorf("navItems not restored: got %d items, want 3", len(m.navItems))
	}
}

func TestClearNavSearchNoopWhenUnfiltered(t *testing.T) {
	m := Model{navItems: []navItem{{id: "a"}}, navItemsAll: []navItem{{id: "a"}}}
	if m.clearNavSearch() {
		t.Error("clearNavSearch reported a clear with no active filter")
	}
}

// wsFocusName is a persisted view choice, not a search artifact, so leaving a
// context must not silently drop the user out of workspace solo mode.
func TestClearNavSearchKeepsWorkspaceFocus(t *testing.T) {
	m := searchedModel()
	m.wsFocusName = "research"
	m.wsSearchName = "research"
	m.clearNavSearch()
	if m.wsFocusName != "research" {
		t.Errorf("wsFocusName = %q, want it preserved", m.wsFocusName)
	}
	if m.wsSearchName != "" {
		t.Errorf("wsSearchName = %q, want empty", m.wsSearchName)
	}
}

// The bug this fixes: a search run on one sub-tab stayed live — and kept its
// badge — after navigating to another.
func TestSwitchNavSubTabClearsSearch(t *testing.T) {
	m := searchedModel()
	m.activeTab = tabLibrary
	m.navSubTab = navSubTabArticles

	m.switchNavSubTab(navSubTabCollections)

	if m.navFilter != "" {
		t.Errorf("navFilter survived sub-tab switch: %q", m.navFilter)
	}
	if len(m.navItems) != 3 {
		t.Errorf("navItems still filtered after sub-tab switch: got %d, want 3", len(m.navItems))
	}
}

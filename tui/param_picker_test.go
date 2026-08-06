package tui

import (
	"testing"

	"github.com/charmbracelet/bubbles/textarea"
)

// The param picker swallows Enter to fill in the selected suggestion. When the
// argument is already typed in full that fill is a no-op, and swallowing Enter
// makes the command silently never run — which is what happened to
// /collection-add <existing-collection>.
func TestParamAlreadyTyped(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		items    []string
		idx      int
		expected bool
	}{
		{"exact match executes", "/collection-add transformers", []string{"transformers"}, 0, true},
		{"case-insensitive match executes", "/collection-add Transformers", []string{"transformers"}, 0, true},
		{"partial arg still completes", "/collection-add trans", []string{"transformers"}, 0, false},
		{"longer than suggestion completes", "/collection-add transformers-old", []string{"transformers"}, 0, false},
		{"no arg typed yet completes", "/collection-add ", []string{"transformers"}, 0, false},
		{"no space means no param picker", "/collection-add", []string{"transformers"}, 0, false},
		{"multi-word arg matches last token", "/help article search", []string{"search"}, 0, true},
		{"nothing selected", "/collection-add transformers", []string{"transformers"}, -1, false},
		{"empty item list", "/collection-add transformers", nil, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &Model{input: textarea.New()}
			m.input.SetValue(tt.input)
			for _, it := range tt.items {
				m.paramItems = append(m.paramItems, cmdCompletion{cmd: it})
			}
			m.paramIdx = tt.idx

			if got := m.paramAlreadyTyped(); got != tt.expected {
				t.Errorf("paramAlreadyTyped() = %v, want %v (input %q)", got, tt.expected, tt.input)
			}
		})
	}
}

// The param picker shows what a command will act on implicitly, so the subject
// is visible while the object is being chosen.
func TestParamHintFor(t *testing.T) {
	m := &Model{input: textarea.New()}
	m.navRows = []navRow{
		{kind: rowCollection, colSlug: "transformers"},
		{kind: rowArticle, indented: true, item: &navItem{id: "20260805-attention"}},
		{kind: rowCollection, colSlug: "systems"},
	}

	m.navRowCursor = 1 // article inside "transformers"
	if got, want := m.paramHintFor("/article-remove"), "removing from: transformers"; got != want {
		t.Errorf("child row: got %q, want %q", got, want)
	}

	m.navRowCursor = 2 // header of "systems"
	if got, want := m.paramHintFor("/article-remove"), "removing from: systems"; got != want {
		t.Errorf("header row: got %q, want %q", got, want)
	}

	if got := m.paramHintFor("/search"); got != "" {
		t.Errorf("command with nothing implicit: got %q, want empty", got)
	}
}

// Picking a collection is a "does this article belong here?" question, so the
// picker shows the description, falling back to the count when there is none.
func TestCollectionParamDesc(t *testing.T) {
	m := &Model{input: textarea.New()}
	m.navRowsAll = []navRow{
		{kind: rowCollection, colSlug: "transformers", colDesc: "Attention mechanics\nand interpretability", colCount: 4},
		{kind: rowCollection, colSlug: "systems", colCount: 7},
		{kind: rowArticle, indented: true, item: &navItem{id: "20260805-attention"}},
	}

	if got, want := m.collectionParamDesc("transformers"), "Attention mechanics and interpretability"; got != want {
		t.Errorf("described collection: got %q, want %q", got, want)
	}
	if got, want := m.collectionParamDesc("systems"), "7 articles"; got != want {
		t.Errorf("undescribed collection: got %q, want %q", got, want)
	}
	if got := m.collectionParamDesc("nonexistent"); got != "" {
		t.Errorf("unknown slug: got %q, want empty", got)
	}
}

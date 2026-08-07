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

// Article slugs carry a date prefix and often a leading article word, so a plain
// prefix test finds nothing by title — hence token matching.
func TestParamMatchRank(t *testing.T) {
	const slug = "20260805-the-annotated-transformer"

	tests := []struct {
		name      string
		candidate string
		partial   string
		want      int
	}{
		{"empty partial matches everything", slug, "", 0},
		{"whole-value prefix ranks first", slug, "20260805-the", 0},
		{"date token still matches", slug, "20260805", 0},
		{"title word past the date", slug, "transformer", 1},
		{"leading article word", slug, "the", 1},
		{"mid-word fragment does not match", slug, "ransformer", -1},
		{"absent word does not match", slug, "attention", -1},
		{"plain slug prefix", "transformers", "trans", 0},
		{"underscore delimiter", "ml_reading_list", "reading", 1},
		{"space delimiter", "deep learning", "learning", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := paramMatchRank(tt.candidate, tt.partial); got != tt.want {
				t.Errorf("paramMatchRank(%q, %q) = %d, want %d", tt.candidate, tt.partial, got, tt.want)
			}
		})
	}
}

func TestFilterParamItemsRanksPrefixesFirst(t *testing.T) {
	all := []cmdCompletion{
		{cmd: "20260805-the-annotated-transformer"},
		{cmd: "transformers"},
		{cmd: "20260805-the-transformer-family"},
	}
	got, overflow := filterParamItems(all, "transform", paramPickerMax)
	if overflow != 0 {
		t.Fatalf("overflow = %d, want 0", overflow)
	}
	if len(got) != 3 {
		t.Fatalf("got %d matches, want 3", len(got))
	}
	// Whole-value prefix first; the two token hits keep their original order.
	want := []string{"transformers", "20260805-the-annotated-transformer", "20260805-the-transformer-family"}
	for i, w := range want {
		if got[i].cmd != w {
			t.Errorf("position %d = %q, want %q", i, got[i].cmd, w)
		}
	}
}

// An uncapped picker squeezes the nav and content panes to a single line, since
// view.go counts every rendered completion line as a fixed row.
func TestFilterParamItemsCaps(t *testing.T) {
	var all []cmdCompletion
	for i := 0; i < paramPickerMax+7; i++ {
		all = append(all, cmdCompletion{cmd: "article-" + string(rune('a'+i))})
	}
	got, overflow := filterParamItems(all, "article", paramPickerMax)
	if len(got) != paramPickerMax {
		t.Errorf("got %d items, want %d", len(got), paramPickerMax)
	}
	if overflow != 7 {
		t.Errorf("overflow = %d, want 7", overflow)
	}
}

func TestFilterParamItemsNoMatches(t *testing.T) {
	all := []cmdCompletion{{cmd: "transformers"}, {cmd: "ml-reading"}}
	got, overflow := filterParamItems(all, "zzz", paramPickerMax)
	if len(got) != 0 || overflow != 0 {
		t.Errorf("got %d items / overflow %d, want 0 / 0", len(got), overflow)
	}
}

// The picker block is counted as fixed rows by view.go, so its cap tracks the
// terminal height — the same 30% share multi-line status content takes.
func TestParamPickerLimit(t *testing.T) {
	tests := []struct {
		height int
		want   int
	}{
		{0, 3},   // height not measured yet — floor
		{10, 3},  // 30% is 3
		{24, 7},  // small terminal
		{40, 12}, // 30% is 12, exactly the ceiling
		{80, 12}, // tall terminal — ceilinged
	}
	for _, tt := range tests {
		m := &Model{height: tt.height}
		if got := m.paramPickerLimit(); got != tt.want {
			t.Errorf("height %d: limit = %d, want %d", tt.height, got, tt.want)
		}
	}
}

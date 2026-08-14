package tui

import "testing"

func TestStripCorrectionTags(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"bare text", "hello world", "hello world"},
		{"wrapped", "<text>hello world</text>", "hello world"},
		{"wrapped with padding", "  <text> hello world </text>  ", "hello world"},
		{"uppercase tags", "<TEXT>hello world</TEXT>", "hello world"},
		{"opening tag only", "<text>hello world", "hello world"},
		{"closing tag only", "hello world</text>", "hello world"},
		{"doubled", "<text><text>hello</text></text>", "hello"},
		{"inner tag preserved", "<text>see <text> below</text>", "see <text> below"},
		{"multiline", "<text>line one\nline two</text>", "line one\nline two"},
		{"empty", "", ""},
		{"tags only", "<text></text>", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripCorrectionTags(tt.in); got != tt.want {
				t.Errorf("stripCorrectionTags(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

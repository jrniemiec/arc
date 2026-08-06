package service

import "testing"

func TestTrimWrappingQuotes(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "How transformers work", "How transformers work"},
		{"double", `"How transformers work"`, "How transformers work"},
		{"single", `'How transformers work'`, "How transformers work"},
		{"curly double", "“How transformers work”", "How transformers work"},
		{"curly single", "‘How transformers work’", "How transformers work"},
		{"outer whitespace", `  "How transformers work"  `, "How transformers work"},
		{"inner whitespace", `" How transformers work "`, "How transformers work"},
		{"only one pair stripped", `""quoted""`, `"quoted"`},
		{"mismatched pair", `"How transformers work'`, `"How transformers work'`},
		{"leading only", `"How transformers work`, `"How transformers work`},
		{"trailing only", `How transformers work"`, `How transformers work"`},
		{"apostrophe inside", "Chris' notes on attention", "Chris' notes on attention"},
		{"curly apostrophe inside", "Chris’ notes on attention", "Chris’ notes on attention"},
		{"interior quotes kept", `"Attention Is All You Need" and friends`, `"Attention Is All You Need" and friends`},
		{"empty", "", ""},
		{"lone quote", `"`, `"`},
		{"empty quoted", `""`, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TrimWrappingQuotes(tt.in); got != tt.want {
				t.Errorf("TrimWrappingQuotes(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

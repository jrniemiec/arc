package service

import "strings"

// quotePairs maps an opening quote rune to the closing rune that pairs with it.
// Straight quotes close with themselves; the curly variants do not.
var quotePairs = map[rune]rune{
	'"':  '"',
	'\'': '\'',
	'“':  '”',
	'‘':  '’',
}

// TrimWrappingQuotes removes a single matching pair of quotes wrapping s.
//
// Users typing into the TUI have no shell to strip quotes for them, so
// /describe "some text" would otherwise store the quotes literally and leak
// them into LLM prompts. Only one pair is removed, so a description that is
// itself a quotation can be preserved by wrapping it twice.
func TrimWrappingQuotes(s string) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) < 2 {
		return s
	}
	if closer, ok := quotePairs[r[0]]; ok && r[len(r)-1] == closer {
		return strings.TrimSpace(string(r[1 : len(r)-1]))
	}
	return s
}

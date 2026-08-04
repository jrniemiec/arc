package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/jrniemiec/arc/flashcards"
)

// renderCards turns a flashcards JSON file into display lines for the content
// pane, along with a parallel slice naming the card each line belongs to ("" for
// lines owned by no card).
//
// Questions always show; an answer shows only when its card ID is in revealed.
// Collapsed, a whole deck fits on one screen and doubles as a self-test.
//
// Output is plain text like every other content section, so the line cursor and
// TTS work on it without special cases.
func renderCards(data []byte, slug string, revealed map[string]bool, width int) (lines []string, cardIDs []string) {
	cards, err := flashcards.Parse(slug, data)
	if err != nil {
		return []string{"(unreadable flashcards: " + err.Error() + ")"}, []string{""}
	}

	if width < 40 {
		width = 40
	}

	// add appends lines all owned by the same card.
	add := func(id string, ls ...string) {
		for _, l := range ls {
			lines = append(lines, l)
			cardIDs = append(cardIDs, id)
		}
	}

	for i, c := range cards {
		num := fmt.Sprintf("%3d", i+1)
		cardType := c.Type
		if cardType == "" {
			cardType = "card"
		}

		if !revealed[c.ID] {
			// "  1  concept   <question wrapped under a hanging indent>"
			head := fmt.Sprintf("%s  %-9s ", num, cardType)
			add(c.ID, wrapIndent(c.Front, width, head, strings.Repeat(" ", len(head)))...)
			continue
		}

		header := fmt.Sprintf("%s  %-9s", num, cardType)
		if tags := formatTags(c.Tags); tags != "" {
			pad := width - lipgloss.Width(header) - lipgloss.Width(tags)
			if pad < 1 {
				pad = 1
			}
			header += strings.Repeat(" ", pad) + tags
		}
		add(c.ID, header)
		add(c.ID, wrapIndent(c.Front, width, "     Q  ", "        ")...)
		add(c.ID, wrapIndent(c.Back, width, "     A  ", "        ")...)
		add(c.ID, "")
	}
	return lines, cardIDs
}

// cardsHeader builds the Cards section header, showing how many answers are
// open and which keys act on them.
func cardsHeader(data []byte, slug string, revealed map[string]bool) string {
	cards, err := flashcards.Parse(slug, data)
	if err != nil {
		return "── Cards ──"
	}

	open := 0
	for _, c := range cards {
		if revealed[c.ID] {
			open++
		}
	}

	switch {
	case open == 0:
		return fmt.Sprintf("── Cards ──  %d · [space] reveal · [A] all · [D] delete", len(cards))
	case open == len(cards):
		return fmt.Sprintf("── Cards ──  %d · all revealed · [A] hide · [D] delete", len(cards))
	default:
		return fmt.Sprintf("── Cards ──  %d · %d revealed · [A] all · [D] delete", len(cards), open)
	}
}

// cardIDsIn returns every card ID in a flashcards file, in display order.
// Used by the reveal-all toggle.
func cardIDsIn(data []byte, slug string) []string {
	cards, err := flashcards.Parse(slug, data)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(cards))
	for _, c := range cards {
		out = append(out, c.ID)
	}
	return out
}

// formatTags renders tags as "#a #b #c".
func formatTags(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t != "" {
			out = append(out, "#"+t)
		}
	}
	return strings.Join(out, " ")
}

// wrapIndent word-wraps text to width, prefixing the first line with first and
// every continuation line with cont.
//
// Widths are measured with lipgloss.Width, not len: card text is full of
// arrows, dashes and quotes that are several bytes wide but one column.
func wrapIndent(text string, width int, first, cont string) []string {
	avail := width - lipgloss.Width(first)
	if avail < 20 {
		avail = 20
	}

	words := strings.Fields(text)
	if len(words) == 0 {
		return []string{strings.TrimRight(first, " ")}
	}

	var lines []string
	cur := words[0]
	for _, w := range words[1:] {
		if lipgloss.Width(cur)+1+lipgloss.Width(w) > avail {
			lines = append(lines, cur)
			cur = w
			continue
		}
		cur += " " + w
	}
	lines = append(lines, cur)

	for i := range lines {
		if i == 0 {
			lines[i] = first + lines[i]
		} else {
			lines[i] = cont + lines[i]
		}
	}
	return lines
}

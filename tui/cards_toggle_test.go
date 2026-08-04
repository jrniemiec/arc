package tui

import (
	"strings"
	"testing"
)

// modelWithCards builds a Model whose content document is the rendered sample
// deck, mirroring what loadContent produces.
func modelWithCards(t *testing.T) (*Model, []string) {
	t.Helper()
	id := ids(t, "slug")

	lines, cardIDs := renderCards(sampleCards, "slug", nil, 80)
	// Two header lines precede the cards, as in loadContent.
	m := &Model{
		contentLines:   append([]string{"── Cards ──", ""}, lines...),
		contentCardIDs: append([]string{"", ""}, cardIDs...),
	}
	m.contentHas[ctCards] = true
	return m, id
}

func TestCardIDAtCursor(t *testing.T) {
	m, id := modelWithCards(t)

	// Header lines belong to no card.
	m.contentLineCursor = 0
	if got := m.cardIDAtCursor(); got != "" {
		t.Errorf("header line maps to %q, want \"\"", got)
	}

	// First card line.
	m.contentLineCursor = 2
	if got := m.cardIDAtCursor(); got != id[0] {
		t.Errorf("first card line maps to %q, want %q", got, id[0])
	}

	// Out of range in both directions.
	m.contentLineCursor = -1
	if got := m.cardIDAtCursor(); got != "" {
		t.Errorf("negative cursor maps to %q, want \"\"", got)
	}
	m.contentLineCursor = len(m.contentCardIDs) + 10
	if got := m.cardIDAtCursor(); got != "" {
		t.Errorf("past-end cursor maps to %q, want \"\"", got)
	}
}

func TestToggleCardAtCursorDoesNothingOffCard(t *testing.T) {
	m, _ := modelWithCards(t)
	m.contentLineCursor = 0 // header line

	if cmd := m.toggleCardAtCursor(); cmd != nil {
		t.Error("expected no command when the cursor is not on a card")
	}
	if len(m.revealedCards) != 0 {
		t.Errorf("reveal set changed: %v", m.revealedCards)
	}
	if m.pendingCardFocus != "" {
		t.Errorf("pendingCardFocus set to %q", m.pendingCardFocus)
	}
}

func TestToggleCardAtCursorRevealsAndHides(t *testing.T) {
	m, id := modelWithCards(t)
	m.contentLineCursor = 2 // first card

	m.toggleCardAtCursor()
	if !m.revealedCards[id[0]] {
		t.Fatal("first toggle did not reveal the card")
	}
	if m.pendingCardFocus != id[0] {
		t.Errorf("pendingCardFocus = %q, want %q", m.pendingCardFocus, id[0])
	}
	if m.revealedCards[id[1]] {
		t.Error("toggling one card revealed another")
	}

	m.toggleCardAtCursor()
	if m.revealedCards[id[0]] {
		t.Error("second toggle did not hide the card")
	}
}

func TestToggleAllCardsNeedsADeck(t *testing.T) {
	m := &Model{} // no cards loaded
	if cmd := m.toggleAllCards(); cmd != nil {
		t.Error("expected no command when the article has no cards")
	}
}

func TestToggleAllCardsClearsWhenAnyRevealed(t *testing.T) {
	m, id := modelWithCards(t)
	m.revealedCards = map[string]bool{id[0]: true}

	m.toggleAllCards()
	if len(m.revealedCards) != 0 {
		t.Errorf("expected the set to be cleared, got %v", m.revealedCards)
	}
	if !m.jumpToCards {
		t.Error("expected a jump back to the Cards section")
	}
}

// Revealing a card must not scroll the view: the section header and everything
// above the card has to stay exactly where it was.
func TestRestoreContentPositionKeepsViewStationary(t *testing.T) {
	id := ids(t, "slug")

	// Document as loadContent builds it, with a Flash section above the cards
	// so there is something to scroll past. The pane must be shorter than the
	// document, or there is nothing to scroll and offset 0 is correct.
	before, beforeIDs := renderCards(sampleCards, "slug", nil, 80)
	lead := []string{"── Flash ──", "", "some flash text", "", "── Cards ──  2 · [space] reveal", ""}
	leadIDs := make([]string, len(lead))

	m := &Model{height: 20} // a 5-line viewport, shorter than the document
	m.contentLines = append(append([]string{}, lead...), before...)
	m.contentCardIDs = append(append([]string{}, leadIDs...), beforeIDs...)

	// Reader has scrolled so the Cards header sits at the top of the pane.
	m.contentScroll = 4
	m.contentLineCursor = 6 // first card

	// Reveal card 1: the reload delivers a longer document.
	m.pendingCardFocus = id[0]
	m.pendingScroll = m.contentScroll
	after, afterIDs := renderCards(sampleCards, "slug", map[string]bool{id[0]: true}, 80)
	msg := contentLoadedMsg{
		lines:   append(append([]string{}, lead...), after...),
		cardIDs: append(append([]string{}, leadIDs...), afterIDs...),
	}
	m.contentLines = msg.lines
	m.contentCardIDs = msg.cardIDs
	m.restoreContentPosition(msg)

	if m.contentScroll != 4 {
		t.Errorf("scroll moved to %d, want 4 — the Cards header scrolled off", m.contentScroll)
	}
	if m.contentLineCursor != 6 {
		t.Errorf("cursor = %d, want 6 (the revealed card)", m.contentLineCursor)
	}
	if got := m.contentCardIDs[m.contentLineCursor]; got != id[0] {
		t.Errorf("cursor sits on card %q, want %q", got, id[0])
	}
	viewH := m.contentViewHeight()
	if m.contentLineCursor < m.contentScroll || m.contentLineCursor >= m.contentScroll+viewH {
		t.Errorf("cursor %d is outside the visible window [%d,%d)",
			m.contentLineCursor, m.contentScroll, m.contentScroll+viewH)
	}
}

// Hiding a card shortens the document; the offset must not point past the end.
func TestRestoreContentPositionClampsAfterShrink(t *testing.T) {
	id := ids(t, "slug")
	lines, cardIDs := renderCards(sampleCards, "slug", nil, 80)

	m := &Model{height: 30}
	m.contentLines = lines
	m.contentCardIDs = cardIDs
	m.pendingCardFocus = id[0]
	m.pendingScroll = 500 // stale offset from a much longer document

	m.restoreContentPosition(contentLoadedMsg{lines: lines, cardIDs: cardIDs})

	if m.contentScroll > len(lines) {
		t.Errorf("scroll %d is past the end of a %d-line document", m.contentScroll, len(lines))
	}
	if m.contentScroll < 0 {
		t.Errorf("scroll went negative: %d", m.contentScroll)
	}
}

// Cards are the last section, so their offset is routinely closer to the end of
// the document than one pane-height. Restoring the scroll must not drag the view
// backwards to keep the pane full — that shows the tail of the Body section
// above the deck.
func TestRestoreContentPositionKeepsShortTailSectionAtTop(t *testing.T) {
	id := ids(t, "slug")

	// 400 lines of body, then the Cards section: a tall pane cannot be filled
	// from the Cards offset.
	body := make([]string, 400)
	for i := range body {
		body[i] = "body line"
	}
	bodyIDs := make([]string, len(body))

	build := func(rev map[string]bool) ([]string, []string) {
		cl, cids := renderCards(sampleCards, "slug", rev, 80)
		lines := append(append([]string{}, body...), "── Cards ──", "")
		lids := append(append([]string{}, bodyIDs...), "", "")
		return append(lines, cl...), append(lids, cids...)
	}

	m := &Model{height: 60} // pane much taller than the Cards section
	lines, cardIDs := build(nil)
	m.contentLines, m.contentCardIDs = lines, cardIDs

	cardsOffset := len(body)
	m.contentScroll = cardsOffset // Cards header pinned to the top of the pane
	m.contentLineCursor = cardsOffset + 2

	m.pendingCardFocus = id[0]
	m.pendingScroll = m.contentScroll
	nl, nids := build(map[string]bool{id[0]: true})
	m.contentLines, m.contentCardIDs = nl, nids
	m.restoreContentPosition(contentLoadedMsg{lines: nl, cardIDs: nids})

	if m.contentScroll != cardsOffset {
		t.Errorf("scroll = %d, want %d — the view slid back into the Body section",
			m.contentScroll, cardsOffset)
	}
}

// Flashcard generation is a 10-30s LLM call; the status line has to animate or
// it reads as a hang.
func TestFlashcardStatusLineAnimates(t *testing.T) {
	m := Model{width: 70, cardsRunning: true, cardsLabel: "generating flashcards for my-article"}

	seen := map[string]bool{}
	for f := 0; f < 6; f++ {
		m.spinnerFrame = f
		seen[m.renderStatusLine()] = true
	}
	if len(seen) < 2 {
		t.Error("status line is identical across frames — the spinner is not animating")
	}

	// Progress from the pipeline drives the label, and the label reaches the bar.
	m.spinnerFrame = 0
	if out := stripANSI(m.renderStatusLine()); !strings.Contains(out, "generating flashcards for my-article") {
		t.Errorf("label missing from status line: %q", out)
	}

	// Must match the other LLM status lines: braille spinner leading, then the
	// label — not the colour-wave used for TTS and populate.
	m.cardsLabel = "flashcards · my-article"
	want := stripANSI(renderWaveIndicatorLeading(3, "flashcards · my-article", ActiveTheme.StreamingText, ActiveTheme.Dimmed))
	m.spinnerFrame = 3
	if got := stripANSI(m.renderStatusLine()); got != want {
		t.Errorf("status line = %q, want %q (the leading-spinner form used by chat/askX)", got, want)
	}
}

// stripANSI removes colour escapes so assertions can match plain text.
func stripANSI(s string) string {
	var b strings.Builder
	inEsc := false
	for _, r := range s {
		switch {
		case r == '\x1b':
			inEsc = true
		case inEsc && r == 'm':
			inEsc = false
		case !inEsc:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// The card keys are content-pane keys that only work when the cursor sits on a
// card. Listing them under "universal" advertises them on the Collections,
// Workspaces and Agent tabs, where they do nothing — and puts a second `space`
// and a second `D` in a list that already binds both.
func TestContextKeysScopesFlashcardKeys(t *testing.T) {
	joined := func(lines []string) string { return strings.Join(lines, "\n") }

	// The universal section only — sections are separated by a blank line, and
	// in the full listing universal is followed by several others.
	universalOnly := func(lines []string) string {
		var sec []string
		in := false
		for _, l := range lines {
			switch {
			case strings.HasPrefix(l, "universal:"):
				in = true
			case in && strings.TrimSpace(l) == "":
				return joined(sec)
			}
			if in {
				sec = append(sec, l)
			}
		}
		return joined(sec)
	}

	// An article with no deck must not advertise the card keys at all.
	m := &Model{width: 100}
	out := joined(m.contextKeys(false))
	for _, key := range []string{"reveal/hide the answer", "reveal/hide every answer", "delete this deck"} {
		if strings.Contains(out, key) {
			t.Errorf("card key %q offered for an article with no deck", key)
		}
	}

	// With a deck loaded they appear, in their own section.
	m.contentHas[ctCards] = true
	out = joined(m.contextKeys(false))
	if !strings.Contains(out, "flashcards (on a card):") {
		t.Error("no flashcards section for an article that has a deck")
	}
	for _, key := range []string{"reveal/hide the answer", "reveal/hide every answer", "delete this deck"} {
		if !strings.Contains(out, key) {
			t.Errorf("card key %q missing", key)
		}
	}

	// They must live in their own section, never in universal.
	if u := universalOnly(m.contextKeys(false)); strings.Contains(u, "reveal/hide") {
		t.Error("card keys leaked into the universal section")
	}

	// The full listing (/?) carries the section regardless of what is selected.
	all := joined((&Model{width: 100}).contextKeys(true))
	if !strings.Contains(all, "flashcards (on a card):") {
		t.Error("/? full listing is missing the flashcards section")
	}

	// `space` and `D` are bound universally too; the universal list must
	// describe each exactly once or the help contradicts itself.
	u := universalOnly((&Model{width: 100}).contextKeys(true))
	if n := strings.Count(u, "\n  space "); n != 1 {
		t.Errorf("universal lists `space` %d times, want 1", n)
	}
}

// Package flashcards parses and normalizes flashcard files.
//
// Two shapes exist on disk. Every file written so far is a bare JSON array of
// cards using "front"/"back" keys — that is what the style prompts ask for and
// what the LLM returns. The design log and testdata specify a wrapper object
// with a "cards" array using "q"/"a" keys. Parse accepts both so neither shape
// is a migration event.
package flashcards

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// Card is one flashcard, normalized across the on-disk shapes.
type Card struct {
	ID    string   // derived, not stored on disk — see CardID
	Type  string   // concept | fact | insight
	Front string   // the question
	Back  string   // the answer
	Tags  []string
}

// rawCard covers both key namings in one struct so a single unmarshal handles
// either shape.
type rawCard struct {
	Type  string   `json:"type"`
	Front string   `json:"front"`
	Back  string   `json:"back"`
	Q     string   `json:"q"`
	A     string   `json:"a"`
	Tags  []string `json:"tags"`
}

type wrapper struct {
	Cards []rawCard `json:"cards"`
}

// Parse reads a flashcards file into normalized cards.
//
// slug seeds the derived card IDs; pass "" when the cards are not tied to an
// article (piped text). Cards missing a front or a back are skipped — a
// half-written card is not reviewable. An empty result is an error: it means
// the model returned valid JSON that is not a deck.
func Parse(slug string, data []byte) ([]Card, error) {
	raws, err := parseRaw(data)
	if err != nil {
		return nil, err
	}

	cards := make([]Card, 0, len(raws))
	for _, r := range raws {
		front := strings.TrimSpace(first(r.Front, r.Q))
		back := strings.TrimSpace(first(r.Back, r.A))
		if front == "" || back == "" {
			continue
		}
		cards = append(cards, Card{
			ID:    CardID(slug, front),
			Type:  strings.TrimSpace(r.Type),
			Front: front,
			Back:  back,
			Tags:  r.Tags,
		})
	}

	if len(cards) == 0 {
		return nil, fmt.Errorf("no usable flashcards found")
	}
	return cards, nil
}

// parseRaw handles the shape ambiguity: bare array first (the common case),
// wrapper object second.
func parseRaw(data []byte) ([]rawCard, error) {
	var arr []rawCard
	if err := json.Unmarshal(data, &arr); err == nil {
		return arr, nil
	}

	var w wrapper
	if err := json.Unmarshal(data, &w); err == nil && w.Cards != nil {
		return w.Cards, nil
	}

	return nil, fmt.Errorf("flashcards: expected a JSON array of cards or an object with a \"cards\" array")
}

// CardID derives a stable identifier from the article slug and the question
// text. It is not stored in the file: the LLM does not emit ids, and any id it
// did emit would not survive regeneration. Deriving from the question means
// review history follows a card as long as its wording is unchanged.
func CardID(slug, front string) string {
	sum := sha1.Sum([]byte(slug + "\x00" + normalize(front)))
	return hex.EncodeToString(sum[:])[:8]
}

// normalize collapses case and whitespace so trivial reformatting of a question
// does not orphan its review history.
func normalize(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// Fronts returns just the question text of each card, for full-text indexing.
func Fronts(cards []Card) []string {
	out := make([]string, 0, len(cards))
	for _, c := range cards {
		out = append(out, c.Front)
	}
	return out
}

func first(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

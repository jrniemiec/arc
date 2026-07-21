package tui

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/jrniemiec/arc/store"
)

// atRefPattern matches @<numID> or @<numID>-<suffix>.
// Suffix options: flash/f, summ/s, body/b, meta/m
var atRefPattern = regexp.MustCompile(`@(\d+)(?:-(flash|f|summ|s|body|b|meta|m))?`)

// resolveAtRefs finds all @<numID> tokens in the input, resolves them to article
// content, and returns the expanded string. Returns an error if any token is
// invalid (unknown ID, collection ID, etc.).
func (m *Model) resolveAtRefs(input string) (string, error) {
	return m.resolveAtRefsWithLimit(input, 0)
}

// resolveAtRefsDisplay is like resolveAtRefs but truncates each expansion to
// maxLen characters (adding "...") for display in the TUI chat history.
func (m *Model) resolveAtRefsDisplay(input string, maxLen int) (string, error) {
	return m.resolveAtRefsWithLimit(input, maxLen)
}

func (m *Model) resolveAtRefsWithLimit(input string, maxLen int) (string, error) {
	matches := atRefPattern.FindAllStringSubmatchIndex(input, -1)
	if len(matches) == 0 {
		return input, nil
	}

	ctx := context.Background()

	// Validate all tokens first before expanding any.
	type tokenInfo struct {
		start, end int
		numID      int
		suffix     string
	}
	var tokens []tokenInfo

	for _, match := range matches {
		fullStart, fullEnd := match[0], match[1]
		numStr := input[match[2]:match[3]]
		numID, _ := strconv.Atoi(numStr)

		suffix := ""
		if match[4] >= 0 {
			suffix = input[match[4]:match[5]]
		}

		// Check if it's a collection.
		isColl, err := m.svc.IsCollectionNumID(ctx, numID)
		if err != nil {
			return "", fmt.Errorf("@%d: lookup error: %w", numID, err)
		}
		if isColl {
			return "", fmt.Errorf("@%d is a collection, not an article", numID)
		}

		// Verify article exists.
		_, err = m.svc.GetArticleByNumID(ctx, numID)
		if err != nil {
			return "", fmt.Errorf("@%d not found", numID)
		}

		tokens = append(tokens, tokenInfo{
			start:  fullStart,
			end:    fullEnd,
			numID:  numID,
			suffix: suffix,
		})
	}

	// Expand tokens in reverse order to preserve offsets.
	result := input
	for i := len(tokens) - 1; i >= 0; i-- {
		tok := tokens[i]
		content, err := m.expandAtRef(ctx, tok.numID, tok.suffix)
		if err != nil {
			return "", err
		}
		if maxLen > 0 && len(content) > maxLen {
			content = content[:maxLen] + "..."
		}
		result = result[:tok.start] + content + result[tok.end:]
	}

	return result, nil
}

// expandAtRef produces the replacement content for a single @ref token.
func (m *Model) expandAtRef(ctx context.Context, numID int, suffix string) (string, error) {
	a, err := m.svc.GetArticleByNumID(ctx, numID)
	if err != nil {
		return "", fmt.Errorf("@%d: %w", numID, err)
	}

	// Normalize suffix.
	switch suffix {
	case "f":
		suffix = "flash"
	case "s":
		suffix = "summ"
	case "b":
		suffix = "body"
	case "m":
		suffix = "meta"
	}

	switch suffix {
	case "flash":
		text, err := m.svc.ReadFlash(a)
		if err != nil {
			return "", fmt.Errorf("@%d-flash: %w", numID, err)
		}
		return fmt.Sprintf("--- Article %d: %s (flash) ---\n%s\n---\n", numID, a.Title, text), nil

	case "summ":
		text, err := m.svc.ReadSummary(a)
		if err != nil {
			return "", fmt.Errorf("@%d-summ: %w", numID, err)
		}
		return fmt.Sprintf("--- Article %d: %s (summary) ---\n%s\n---\n", numID, a.Title, text), nil

	case "body":
		text, err := m.svc.ReadBody(a)
		if err != nil {
			return "", fmt.Errorf("@%d-body: %w", numID, err)
		}
		return fmt.Sprintf("--- Article %d: %s (body) ---\n%s\n---\n", numID, a.Title, text), nil

	case "meta":
		return formatMeta(numID, a), nil

	default:
		// Full: meta + flash + summary + body
		var sb strings.Builder
		sb.WriteString(formatMeta(numID, a))

		if text, err := m.svc.ReadFlash(a); err == nil {
			sb.WriteString(fmt.Sprintf("--- Flash ---\n%s\n---\n\n", text))
		}
		if text, err := m.svc.ReadSummary(a); err == nil {
			sb.WriteString(fmt.Sprintf("--- Summary ---\n%s\n---\n\n", text))
		}
		if text, err := m.svc.ReadBody(a); err == nil {
			sb.WriteString(fmt.Sprintf("--- Body ---\n%s\n---\n", text))
		}
		return sb.String(), nil
	}
}

// formatMeta formats article metadata as a readable text block.
func formatMeta(numID int, a store.Article) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("--- Article %d: %s (meta) ---\n", numID, a.Title))
	sb.WriteString(fmt.Sprintf("ID: %d\n", numID))
	sb.WriteString(fmt.Sprintf("Slug: %s\n", a.ID))
	if a.URL != "" {
		sb.WriteString(fmt.Sprintf("URL: %s\n", a.URL))
	}
	if a.Author != "" {
		sb.WriteString(fmt.Sprintf("Author: %s\n", a.Author))
	}
	if a.SourceType != "" {
		sb.WriteString(fmt.Sprintf("Source: %s\n", a.SourceType))
	}
	sb.WriteString(fmt.Sprintf("Ingested: %s\n", a.IngestedAt.Format("2006-01-02")))
	if len(a.Collections) > 0 {
		sb.WriteString(fmt.Sprintf("Collections: %s\n", strings.Join(a.Collections, ", ")))
	}
	if len(a.Tags) > 0 {
		tags := make([]string, len(a.Tags))
		for i, t := range a.Tags {
			tags[i] = t.Value
		}
		sb.WriteString(fmt.Sprintf("Tags: %s\n", strings.Join(tags, ", ")))
	}
	sb.WriteString("---\n\n")
	return sb.String()
}

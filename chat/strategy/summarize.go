package strategy

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jrniemiec/arc/chat"
)

// SummaryStore is the minimal interface the summarize strategy needs.
// coversThrough is the timestamp of the last message covered by the summary —
// stored as time.Time so it remains stable as tool messages accumulate in raw history.
type SummaryStore interface {
	LoadSummary() (text string, coversThrough time.Time, err error)
	SaveSummary(text string, coversThrough time.Time) error
}

// SummarizeStrategy keeps recent messages verbatim and compresses older history
// into a rolling summary using a secondary LLM call.
type SummarizeStrategy struct {
	SummarizerProvider chat.Provider
	SummarizerBudget   int // token budget available for summarizer input
	WorkspaceName      string
	Store              SummaryStore
	Ctx                context.Context
	Out                io.Writer
	Budget             int     // effective budget for main model
	VerbatimRatio      float64 // fraction of budget kept verbatim (default 0.4)
}

func (s *SummarizeStrategy) Name() string { return StrategySummarize }

func (s *SummarizeStrategy) Apply(h *chat.History, prompt string) []chat.Message {
	if h == nil || len(h.Msgs) == 0 {
		return nil
	}
	if s.Budget <= 0 {
		return nil
	}

	msgs := h.CollapseForContext()
	verbatimBudget := int(float64(s.Budget) * s.verbatimRatio())

	summaryText, coversThroughTS, err := s.Store.LoadSummary()
	if err != nil {
		s.warnf("failed to load summary: %v — falling back to token-budget", err)
		return s.tokenBudgetFallback(h, prompt)
	}

	// Resolve coversThrough index by matching the stored timestamp against the
	// collapsed slice. This is stable across sessions regardless of how many
	// tool messages accumulate in raw history.
	coversThrough := -1
	if !coversThroughTS.IsZero() {
		for i, m := range msgs {
			if m.Time.Equal(coversThroughTS) {
				coversThrough = i
				break
			}
		}
	}

	verbatimStart := 0
	if coversThrough >= 0 && coversThrough < len(msgs) {
		verbatimStart = coversThrough + 1
	} else if coversThrough >= len(msgs) {
		verbatimStart = len(msgs)
	}
	verbatimMsgs := msgs[verbatimStart:]

	summaryTokens := chat.ApproxTokens(summaryText)
	verbatimTokens := totalTokens(verbatimMsgs)

	needsCompaction := false
	if summaryText == "" {
		allTokens := totalTokens(msgs)
		needsCompaction = allTokens > s.Budget
	} else {
		overflow := summaryTokens + verbatimTokens - s.Budget
		if overflow > 0 {
			overflowMsgs := identifyOverflow(verbatimMsgs, verbatimBudget)
			overflowTokens := totalTokens(overflowMsgs)
			needsCompaction = overflowTokens > int(float64(verbatimBudget)*0.2)
		}
	}

	if needsCompaction {
		newSummary, newCoversThroughTS, ok := s.compact(msgs, summaryText, coversThroughTS, verbatimStart, verbatimBudget)
		if ok {
			summaryText = newSummary
			coversThroughTS = newCoversThroughTS
			// Re-resolve verbatimStart from the new timestamp.
			coversThrough = -1
			for i, m := range msgs {
				if m.Time.Equal(coversThroughTS) {
					coversThrough = i
					break
				}
			}
			verbatimStart = coversThrough + 1
			if verbatimStart > len(msgs) {
				verbatimStart = len(msgs)
			}
			verbatimMsgs = msgs[verbatimStart:]
		}
	}

	return s.buildContext(summaryText, verbatimMsgs, verbatimBudget, prompt)
}

func (s *SummarizeStrategy) verbatimRatio() float64 {
	if s.VerbatimRatio > 0 {
		return s.VerbatimRatio
	}
	return 0.4
}

func (s *SummarizeStrategy) compact(
	msgs []chat.Message,
	existingSummary string,
	oldCoversThroughTS time.Time,
	verbatimStart int,
	verbatimBudget int,
) (string, time.Time, bool) {
	verbatimMsgs := msgs[verbatimStart:]
	overflowMsgs := identifyOverflow(verbatimMsgs, verbatimBudget)
	if len(overflowMsgs) == 0 && existingSummary == "" {
		return existingSummary, oldCoversThroughTS, false
	}

	if s.SummarizerBudget > 0 {
		inputLimit := int(float64(s.SummarizerBudget)*0.8) - chat.ApproxTokens(existingSummary)
		if inputLimit < 0 {
			inputLimit = 0
		}
		overflowMsgs = trimToTokenLimit(overflowMsgs, inputLimit)
	}

	if s.Out != nil {
		allTokens := totalTokens(msgs)
		summaryTokens := chat.ApproxTokens(existingSummary)
		verbatimTokens := totalTokens(verbatimMsgs)
		fmt.Fprintf(s.Out, "Compacting history for workspace '%s'\n", s.WorkspaceName)
		fmt.Fprintf(s.Out, "  history:         %d messages (~%s tokens)\n", len(msgs), chat.FormatTokens(allTokens))
		if existingSummary != "" {
			fmt.Fprintf(s.Out, "  summary covers:  through %s (~%s tokens)\n", oldCoversThroughTS.Format(time.RFC3339), chat.FormatTokens(summaryTokens))
		}
		fmt.Fprintf(s.Out, "  verbatim window: %d messages (~%s tokens)\n", len(verbatimMsgs), chat.FormatTokens(verbatimTokens))
		fmt.Fprintf(s.Out, "  compacting:      %d overflow messages\n", len(overflowMsgs))
	}

	var sb strings.Builder
	if existingSummary != "" {
		sb.WriteString("Previous summary:\n")
		sb.WriteString(existingSummary)
		sb.WriteString("\n\nNew exchanges to incorporate:\n")
	}
	for _, m := range overflowMsgs {
		sb.WriteString(fmt.Sprintf("[%s]: %s\n", m.Role, m.Content))
	}

	summarizationPrompt := `You are a document summarizer. You will receive a long technical conversation covering multiple topics.

Your task: produce one unified summary covering ALL topics in the conversation from start to finish.

For each distinct topic found in the conversation:
- Write a topic heading
- Write 2-4 bullet points with specific facts, exact commands, variable names, or decisions

Rules:
- Cover every topic present, in the order they appear
- Preserve exact syntax: commands, flags, function names, variable names, code snippets
- Do not generalize — state the actual fact
- No introduction, no conclusion, no meta-commentary

Conversation to summarize:

` + sb.String()

	newSummaryText, _, err := s.SummarizerProvider.Chat(
		s.Ctx,
		"",
		[]chat.Message{{Role: chat.RoleUser, Content: summarizationPrompt}},
	)
	if err != nil {
		s.warnf("history compaction failed: %v — sending partial context", err)
		return existingSummary, oldCoversThroughTS, false
	}

	newCoversThrough := verbatimStart + len(overflowMsgs) - 1
	var newCoversThroughTS time.Time
	if newCoversThrough >= 0 && newCoversThrough < len(msgs) {
		newCoversThroughTS = msgs[newCoversThrough].Time
	}

	if s.Out != nil {
		remainingVerbatim := msgs[newCoversThrough+1:]
		remainingTokens := totalTokens(remainingVerbatim)
		totalCtx := chat.ApproxTokens(newSummaryText) + remainingTokens
		fmt.Fprintf(s.Out, "  summary updated: covers through %s\n", newCoversThroughTS.Format(time.RFC3339))
		fmt.Fprintf(s.Out, "  context window:  ~%s summary + ~%s verbatim = ~%s total\n",
			chat.FormatTokens(chat.ApproxTokens(newSummaryText)),
			chat.FormatTokens(remainingTokens),
			chat.FormatTokens(totalCtx))
	}

	if err := s.Store.SaveSummary(newSummaryText, newCoversThroughTS); err != nil {
		s.warnf("failed to save summary: %v", err)
	}
	return newSummaryText, newCoversThroughTS, true
}

func (s *SummarizeStrategy) buildContext(summaryText string, verbatimMsgs []chat.Message, verbatimBudget int, prompt string) []chat.Message {
	remaining := verbatimBudget - chat.ApproxTokens(prompt)
	var selected []chat.Message
	used := 0
	for i := len(verbatimMsgs) - 1; i >= 0; i-- {
		cost := chat.ApproxTokens(verbatimMsgs[i].Content)
		if used+cost > remaining {
			break
		}
		used += cost
		selected = append(selected, verbatimMsgs[i])
	}
	for l, r := 0, len(selected)-1; l < r; l, r = l+1, r-1 {
		selected[l], selected[r] = selected[r], selected[l]
	}

	var out []chat.Message
	if summaryText != "" {
		out = append(out, chat.Message{
			Role:    chat.RoleAssistant,
			Content: "[Context summary]\n" + summaryText,
		})
	}
	return append(out, selected...)
}

func (s *SummarizeStrategy) tokenBudgetFallback(h *chat.History, prompt string) []chat.Message {
	fb := &TokenBudgetStrategy{Budget: s.Budget, ReserveTokens: 512}
	return fb.Apply(h, prompt)
}

func (s *SummarizeStrategy) warnf(format string, args ...any) {
	if s.Out == nil {
		return
	}
	fmt.Fprintf(s.Out, "Warning: "+format+"\n", args...)
}

func identifyOverflow(msgs []chat.Message, verbatimBudget int) []chat.Message {
	total := totalTokens(msgs)
	if total <= verbatimBudget {
		return nil
	}
	excess := total - verbatimBudget
	var overflow []chat.Message
	accumulated := 0
	for _, m := range msgs {
		if accumulated >= excess {
			break
		}
		accumulated += chat.ApproxTokens(m.Content)
		overflow = append(overflow, m)
	}
	return overflow
}

func trimToTokenLimit(msgs []chat.Message, limit int) []chat.Message {
	total := totalTokens(msgs)
	if total <= limit {
		return msgs
	}
	for len(msgs) > 0 && total > limit {
		total -= chat.ApproxTokens(msgs[0].Content)
		msgs = msgs[1:]
	}
	return msgs
}

func totalTokens(msgs []chat.Message) int {
	total := 0
	for _, m := range msgs {
		total += chat.ApproxTokens(m.Content)
	}
	return total
}

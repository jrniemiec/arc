package strategy

import "github.com/jrniemiec/arc/chat"

// TokenBudgetStrategy keeps the most recent messages that fit within a token budget.
// Oldest messages are dropped first.
type TokenBudgetStrategy struct {
	Budget        int // effective token ceiling
	ReserveTokens int // pre-subtracted: system prompt tokens + overhead
}

func (s *TokenBudgetStrategy) Name() string { return StrategyTokenBudget }

func (s *TokenBudgetStrategy) Apply(h *chat.History, prompt string) []chat.Message {
	if h == nil || len(h.Msgs) == 0 {
		return nil
	}
	budget := s.Budget - s.ReserveTokens - chat.ApproxTokens(prompt)
	if budget <= 0 {
		return nil
	}

	msgs := h.CollapseForContext()
	var selected []chat.Message
	used := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		cost := chat.ApproxTokens(msgs[i].Content)
		if used+cost > budget {
			break
		}
		used += cost
		selected = append(selected, msgs[i])
	}

	// Reverse to chronological order.
	for l, r := 0, len(selected)-1; l < r; l, r = l+1, r-1 {
		selected[l], selected[r] = selected[r], selected[l]
	}
	return selected
}

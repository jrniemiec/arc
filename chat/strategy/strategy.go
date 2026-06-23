package strategy

import (
	"strings"

	"github.com/jrniemiec/arc/chat"
)

const (
	StrategyTail        = "tail"
	StrategyTokenBudget = "token-budget"
	StrategySummarize   = "summarize"
)

// ContextStrategy selects which messages from History to include in the context window.
type ContextStrategy interface {
	Name() string
	Apply(h *chat.History, prompt string) []chat.Message
}

// New instantiates the strategy for the given name.
// budget is the token ceiling (used by token-budget and summarize strategies).
// systemPrompt is used to compute the reserve tokens for token-budget.
// maxUserMessages is used by the tail strategy.
func New(name string, budget int, systemPrompt string, maxUserMessages int) ContextStrategy {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case StrategyTokenBudget:
		if budget <= 0 {
			return &TailStrategy{MaxUserMessages: maxUserMessages}
		}
		reserve := chat.ApproxTokens(systemPrompt) + 512
		return &TokenBudgetStrategy{Budget: budget, ReserveTokens: reserve}
	default: // "tail" or empty
		return &TailStrategy{MaxUserMessages: maxUserMessages}
	}
}

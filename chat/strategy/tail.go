package strategy

import "github.com/jrniemiec/arc/chat"

// TailStrategy keeps the last N user turns of history.
type TailStrategy struct {
	MaxUserMessages int
}

func (s *TailStrategy) Name() string { return StrategyTail }

func (s *TailStrategy) Apply(h *chat.History, _ string) []chat.Message {
	return h.ToMessages(s.MaxUserMessages)
}

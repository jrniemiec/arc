package strategy

import "github.com/jrniemiec/arc/chat"

// TailStrategy keeps the last N user turns of history.
type TailStrategy struct {
	MaxUserMessages int
}

func (s *TailStrategy) Name() string { return StrategyTail }

func (s *TailStrategy) Apply(h *chat.History, _ string) []chat.Message {
	msgs := h.CollapseForContext()
	if len(msgs) == 0 {
		return nil
	}
	maxUser := s.MaxUserMessages
	if maxUser <= 0 {
		return msgs
	}
	// Find the start index such that only the last maxUser user turns are included.
	userCount := 0
	start := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == chat.RoleUser {
			userCount++
			if userCount >= maxUser {
				start = i
				break
			}
		}
	}
	return msgs[start:]
}

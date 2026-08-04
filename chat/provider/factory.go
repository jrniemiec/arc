package provider

import (
	"fmt"
	"strings"

	"github.com/jrniemiec/arc/chat"
	"github.com/jrniemiec/arc/config"
)

// New creates a Provider from an arc config.Profile.
// maxOutputTokens caps response length; 0 uses the provider default.
func New(p config.Profile, maxOutputTokens int) (chat.Provider, error) {
	switch strings.ToLower(strings.TrimSpace(p.Provider)) {
	case "anthropic":
		return NewAnthropicProvider(p.Model, maxOutputTokens, p.Thinking)
	case "openai":
		return NewOpenAIProvider(p.Model)
	case "ollama":
		return NewOllamaProvider(p.Host, p.Model, p.Info.ContextWindow)
	default:
		return nil, fmt.Errorf("unknown provider %q", p.Provider)
	}
}

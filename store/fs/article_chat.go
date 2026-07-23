package fs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ArticleChatConfig is the per-article chat configuration stored in
// <articlesRoot>/<slug>/chat/config.json.
type ArticleChatConfig struct {
	Profile string `json:"profile"` // sticky profile override (empty = use global default)
}

// ArticleChatDir returns the chat directory for an article.
func ArticleChatDir(articlesRoot, slug string) string {
	return filepath.Join(articlesRoot, slug, "chat")
}

// ReadArticleChatConfig reads the per-article chat config.
// Returns zero value if no config exists.
func ReadArticleChatConfig(articlesRoot, slug string) (ArticleChatConfig, error) {
	path := filepath.Join(ArticleChatDir(articlesRoot, slug), "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ArticleChatConfig{}, nil
		}
		return ArticleChatConfig{}, fmt.Errorf("read article chat config: %w", err)
	}
	var cfg ArticleChatConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return ArticleChatConfig{}, fmt.Errorf("parse article chat config: %w", err)
	}
	return cfg, nil
}

// WriteArticleChatConfig writes the per-article chat config atomically.
func WriteArticleChatConfig(articlesRoot, slug string, cfg ArticleChatConfig) error {
	dir := ArticleChatDir(articlesRoot, slug)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create article chat dir: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "config.json.tmp")
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "config.json"))
}

// HasArticleChat returns true if the article has a non-empty chat history.
func HasArticleChat(articlesRoot, slug string) bool {
	path := filepath.Join(ArticleChatDir(articlesRoot, slug), "history.json")
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Size() > 10 // skip trivially empty files (e.g. "{}" or "[]")
}

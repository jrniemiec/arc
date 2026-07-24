package service

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	chatpkg "github.com/jrniemiec/arc/chat"
	storefs "github.com/jrniemiec/arc/store/fs"
)

// SyncChatsResult reports what was archived.
type SyncChatsResult struct {
	Messages int // total messages archived
	Sources  int // number of sources that had new messages
}

// SyncChats archives new messages from AskX and all article chats into
// ~/.arc/chat_archive.jsonl, using a watermark per source to copy only new messages.
func (s *Service) SyncChats() (SyncChatsResult, error) {
	dataRoot := s.cfg.DataRoot
	articlesRoot := s.cfg.ArticlesRoot

	slog.Info("chat archive: sync started", "data_root", dataRoot)

	state, err := storefs.ReadArchiveState(dataRoot)
	if err != nil {
		return SyncChatsResult{}, fmt.Errorf("read archive state: %w", err)
	}

	var result SyncChatsResult
	now := time.Now().UTC()

	// --- AskX (global) ---
	askxHistory, err := storefs.ReadAskXHistory(dataRoot, "")
	if err != nil {
		return SyncChatsResult{}, fmt.Errorf("read askx history: %w", err)
	}
	if n := archiveAskX(dataRoot, askxHistory, state.AskX, now, &state, &result); n > 0 {
		slog.Info("chat archive: askx archived", "messages", n)
	}

	// --- Article chats ---
	entries, err := os.ReadDir(articlesRoot)
	if err != nil && !os.IsNotExist(err) {
		return SyncChatsResult{}, fmt.Errorf("read articles dir: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		slug := e.Name()
		histPath := filepath.Join(articlesRoot, slug, "chat", "history.json")
		if _, err := os.Stat(histPath); os.IsNotExist(err) {
			continue
		}
		store := chatpkg.NewArticleChatStore(articlesRoot, slug)
		hist, err := store.LoadHistory()
		if err != nil {
			slog.Warn("chat archive: skip article", "slug", slug, "err", err)
			continue
		}
		watermark := state.Articles[slug]
		if n := archiveArticle(dataRoot, articlesRoot, slug, hist, watermark, now, &state, &result); n > 0 {
			slog.Info("chat archive: article archived", "slug", slug, "messages", n)
		}
	}

	if err := storefs.WriteArchiveState(dataRoot, state); err != nil {
		return result, fmt.Errorf("write archive state: %w", err)
	}

	slog.Info("chat archive: sync done", "messages", result.Messages, "sources", result.Sources)
	return result, nil
}

// archiveAskX filters and appends new AskX messages. Returns count archived.
func archiveAskX(dataRoot string, h *storefs.AskXHistory, watermark, now time.Time, state *storefs.ArchiveState, result *SyncChatsResult) int {
	var msgs []storefs.ArchiveMessage
	var latestTime time.Time

	for _, m := range h.Messages {
		if !includeRole(m.Role) {
			continue
		}
		if !m.Time.IsZero() && !m.Time.After(watermark) {
			continue
		}
		msgs = append(msgs, storefs.ArchiveMessage{
			Role:    m.Role,
			Content: m.Content,
			Time:    m.Time,
		})
		if m.Time.After(latestTime) {
			latestTime = m.Time
		}
	}
	if len(msgs) == 0 {
		return 0
	}

	entry := storefs.ArchiveEntry{
		Type:       "askx",
		DateFrom:   msgs[0].Time,
		DateTo:     msgs[len(msgs)-1].Time,
		ArchivedAt: now,
		Messages:   msgs,
	}
	if err := storefs.AppendArchiveEntry(dataRoot, entry); err != nil {
		slog.Warn("chat archive: append askx entry", "err", err)
		return 0
	}
	state.AskX = latestTime
	result.Messages += len(msgs)
	result.Sources++
	return len(msgs)
}

// archiveArticle filters and appends new article chat messages. Returns count archived.
func archiveArticle(dataRoot, articlesRoot, slug string, h *chatpkg.History, watermark, now time.Time, state *storefs.ArchiveState, result *SyncChatsResult) int {
	var msgs []storefs.ArchiveMessage
	var latestTime time.Time

	for _, m := range h.Msgs {
		if !includeRole(m.Role) {
			continue
		}
		if !m.Time.IsZero() && !m.Time.After(watermark) {
			continue
		}
		msgs = append(msgs, storefs.ArchiveMessage{
			Role:    m.Role,
			Content: m.Content,
			Time:    m.Time,
		})
		if m.Time.After(latestTime) {
			latestTime = m.Time
		}
	}
	if len(msgs) == 0 {
		return 0
	}

	// Read article title from meta.json (best-effort).
	title := slug
	metaPath := filepath.Join(articlesRoot, slug, "meta.json")
	if meta, err := storefs.ReadMeta(metaPath); err == nil && meta.Title != "" {
		title = meta.Title
	}

	entry := storefs.ArchiveEntry{
		Type:       "article",
		Slug:       slug,
		Title:      title,
		DateFrom:   msgs[0].Time,
		DateTo:     msgs[len(msgs)-1].Time,
		ArchivedAt: now,
		Messages:   msgs,
	}
	if err := storefs.AppendArchiveEntry(dataRoot, entry); err != nil {
		slog.Warn("chat archive: append article entry", "slug", slug, "err", err)
		return 0
	}
	state.Articles[slug] = latestTime
	result.Messages += len(msgs)
	result.Sources++
	return len(msgs)
}

// includeRole returns true for roles that should be included in the archive.
func includeRole(role string) bool {
	switch role {
	case "user", "assistant", "note":
		return true
	default:
		return false
	}
}

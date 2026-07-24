package fs

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ArchiveMessage is one message in an archived chat session.
type ArchiveMessage struct {
	Role    string    `json:"role"`    // "user", "assistant", or "note"
	Content string    `json:"content"`
	Time    time.Time `json:"time,omitempty"`
}

// ArchiveEntry is one archived chat session block (AskX or article chat).
type ArchiveEntry struct {
	Type       string           `json:"type"`                  // "askx" or "article"
	Slug       string           `json:"slug,omitempty"`        // article slug (article type only)
	Title      string           `json:"title,omitempty"`       // article title (article type only)
	DateFrom   time.Time        `json:"date_from"`
	DateTo     time.Time        `json:"date_to"`
	ArchivedAt time.Time        `json:"archived_at"`
	Messages   []ArchiveMessage `json:"messages"`
}

// ArchiveState tracks the last-archived timestamp per source.
type ArchiveState struct {
	AskX     time.Time            `json:"askx"`
	Articles map[string]time.Time `json:"articles"`
}

func archiveStatePath(dataRoot string) string {
	return filepath.Join(dataRoot, "archive_state.json")
}

func chatArchivePath(dataRoot string) string {
	return filepath.Join(dataRoot, "chat_archive.jsonl")
}

// ReadArchiveState reads the archive watermark state.
// Returns zero-value state if the file does not exist.
func ReadArchiveState(dataRoot string) (ArchiveState, error) {
	data, err := os.ReadFile(archiveStatePath(dataRoot))
	if err != nil {
		if os.IsNotExist(err) {
			return ArchiveState{Articles: map[string]time.Time{}}, nil
		}
		return ArchiveState{}, fmt.Errorf("read archive state: %w", err)
	}
	var s ArchiveState
	if err := json.Unmarshal(data, &s); err != nil {
		return ArchiveState{}, fmt.Errorf("parse archive state: %w", err)
	}
	if s.Articles == nil {
		s.Articles = map[string]time.Time{}
	}
	return s, nil
}

// WriteArchiveState writes the archive watermark state atomically.
func WriteArchiveState(dataRoot string, s ArchiveState) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := archiveStatePath(dataRoot) + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, archiveStatePath(dataRoot))
}

// AppendArchiveRunEntry appends a run-delimiter entry to chat_archive.jsonl.
// This marks the start of each /chats-archive run so sessions can be grouped visually.
func AppendArchiveRunEntry(dataRoot string, ts time.Time) error {
	return AppendArchiveEntry(dataRoot, ArchiveEntry{
		Type:       "archive-run",
		ArchivedAt: ts,
	})
}

// AppendArchiveEntry appends one entry to chat_archive.jsonl.
func AppendArchiveEntry(dataRoot string, entry ArchiveEntry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal archive entry: %w", err)
	}
	f, err := os.OpenFile(chatArchivePath(dataRoot), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open chat_archive.jsonl: %w", err)
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%s\n", data)
	return err
}

// ReadAllArchiveEntries reads all entries from chat_archive.jsonl in order (oldest first).
func ReadAllArchiveEntries(dataRoot string) ([]ArchiveEntry, error) {
	f, err := os.Open(chatArchivePath(dataRoot))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open chat_archive.jsonl: %w", err)
	}
	defer f.Close()

	var entries []ArchiveEntry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024) // 4MB per line
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e ArchiveEntry
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("parse archive entry: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, scanner.Err()
}

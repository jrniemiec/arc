package feed

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Store tracks which feed items have already been seen.
// State is persisted as one JSON file per feed URL in the given directory.
type Store struct {
	dir string
}

// FeedState is the persisted state for a single feed.
type FeedState struct {
	URL      string    `json:"url"`
	LastPoll time.Time `json:"last_poll"`
	Seen     []string  `json:"seen"` // list of GUIDs
}

// NewStore creates a state store backed by the given directory.
// The directory is created if it doesn't exist.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}
	return &Store{dir: dir}, nil
}

// NewItems returns items from the list that haven't been seen before.
// Order is preserved.
func (s *Store) NewItems(feedURL string, items []Item) ([]Item, error) {
	state, err := s.load(feedURL)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(state.Seen))
	for _, guid := range state.Seen {
		seen[guid] = true
	}
	var out []Item
	for _, item := range items {
		if !seen[item.GUID] {
			out = append(out, item)
		}
	}
	return out, nil
}

// MarkSeen records the given items as seen for the feed URL.
// It merges with existing state rather than replacing it.
func (s *Store) MarkSeen(feedURL string, items []Item) error {
	state, err := s.load(feedURL)
	if err != nil {
		return err
	}
	seen := make(map[string]bool, len(state.Seen))
	for _, guid := range state.Seen {
		seen[guid] = true
	}
	for _, item := range items {
		if !seen[item.GUID] {
			state.Seen = append(state.Seen, item.GUID)
		}
	}
	state.URL = feedURL
	state.LastPoll = time.Now().UTC()
	return s.save(feedURL, state)
}

// Load returns the current state for a feed, or empty state if none exists.
func (s *Store) load(feedURL string) (FeedState, error) {
	path := s.path(feedURL)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return FeedState{URL: feedURL}, nil
	}
	if err != nil {
		return FeedState{}, fmt.Errorf("read state %s: %w", path, err)
	}
	var state FeedState
	if err := json.Unmarshal(data, &state); err != nil {
		return FeedState{}, fmt.Errorf("parse state %s: %w", path, err)
	}
	return state, nil
}

func (s *Store) save(feedURL string, state FeedState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	path := s.path(feedURL)
	return os.WriteFile(path, data, 0644)
}

func (s *Store) path(feedURL string) string {
	h := sha256.Sum256([]byte(feedURL))
	name := fmt.Sprintf("%x.json", h[:8])
	return filepath.Join(s.dir, name)
}

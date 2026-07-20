package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// RunRecord is one entry appended to agent/runs.jsonl after each agent run.
// It provides a structured audit trail of every agent invocation.
type RunRecord struct {
	RunID       string       `json:"run_id"`                 // agent-YYYYMMDD-HHMMSS
	RunType     string       `json:"run_type"`               // "daily" | "decisions"
	SourceRunID string       `json:"source_run_id,omitempty"` // for decisions runs: the originating daily run ID
	StartedAt  time.Time    `json:"started_at"`
	FinishedAt time.Time    `json:"finished_at"`
	Feeds      []FeedRecord `json:"feeds"`

	// Totals across all feeds.
	TotalNew      int     `json:"total_new"`      // items seen for the first time
	TotalFilter   int     `json:"total_filter"`   // items sent to LLM filter
	TotalIngest   int     `json:"total_ingest"`   // items ingested
	TotalMaybe    int     `json:"total_maybe"`    // items marked maybe
	TotalSkip     int     `json:"total_skip"`     // items skipped
	TotalCostUSD  float64 `json:"total_cost_usd"` // cumulative LLM cost across all ingested articles

	Error string `json:"error,omitempty"` // top-level error if the run failed
}

// FeedRecord summarises one feed's outcome within a run.
type FeedRecord struct {
	URL    string `json:"url"`
	Name   string `json:"name,omitempty"`
	New     int     `json:"new"`      // new GUIDs seen
	Filter  int     `json:"filter"`   // sent to LLM
	Ingest  int     `json:"ingest"`   // ingested
	Maybe   int     `json:"maybe"`    // maybe
	Skip    int     `json:"skip"`     // skipped
	CostUSD float64 `json:"cost_usd"` // cumulative LLM cost for this feed's ingested articles
	Error   string  `json:"error,omitempty"`

	// Items holds per-article decisions, populated during the run.
	// Available for verbose reporting; omitted from the JSONL run log to keep it compact.
	Items []ItemDecision `json:"-"`
}

// ItemDecision records the filter outcome for a single feed item.
// It is used both in-memory (FeedRecord.Items) and on-disk (DecisionsFile).
type ItemDecision struct {
	Verdict string `json:"verdict"`          // "ingest" | "maybe" | "skip"
	Action  string `json:"action,omitempty"` // "-" = pending skip, "+" = user wants to ingest
	Status  string `json:"status,omitempty"` // "done" | "pending"
	Title   string `json:"title"`
	URL     string `json:"url"`
	Reason  string `json:"reason"`
}

// DecisionsFeedRecord is the per-feed section of a decisions file.
type DecisionsFeedRecord struct {
	Name  string         `json:"name"`
	Items []ItemDecision `json:"items"`
}

// DecisionsFile is written to ~/.arc/agent/decisions-<run-id>.json after each
// normal agent run. The user edits Action fields ("-" → "+") on skipped items
// they want to ingest, then runs: arc agent run --decisions <file>
type DecisionsFile struct {
	RunID     string                `json:"run_id"`
	CreatedAt time.Time             `json:"created_at"`
	Feeds     []DecisionsFeedRecord `json:"feeds"`
}

// WriteDecisionsFile marshals df to path as indented JSON.
func WriteDecisionsFile(path string, df DecisionsFile) error {
	data, err := json.MarshalIndent(df, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal decisions file: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write decisions file: %w", err)
	}
	return nil
}

// LoadDecisionsFile reads and decodes a decisions file from path.
func LoadDecisionsFile(path string) (DecisionsFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return DecisionsFile{}, fmt.Errorf("open decisions file: %w", err)
	}
	defer f.Close()

	var df DecisionsFile
	if err := json.NewDecoder(f).Decode(&df); err != nil {
		return DecisionsFile{}, fmt.Errorf("decode decisions file: %w", err)
	}
	return df, nil
}

// LoadRuns reads all RunRecords from the JSONL file at path, most recent last.
// Returns an empty slice (not an error) if the file does not exist.
func LoadRuns(path string) ([]RunRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open runs file: %w", err)
	}
	defer f.Close()

	var recs []RunRecord
	dec := json.NewDecoder(f)
	for dec.More() {
		var rec RunRecord
		if err := dec.Decode(&rec); err != nil {
			break // stop on first malformed record
		}
		recs = append(recs, rec)
	}
	return recs, nil
}

// NewRunID returns a fresh run ID for the current time.
func NewRunID() string {
	return "agent-" + time.Now().UTC().Format("20060102-150405")
}

// AppendRun appends rec to the file at path as pretty-printed JSON followed by
// a blank line. json.NewDecoder handles multi-line JSON objects, so the file
// remains machine-readable while being human-readable in an editor.
func AppendRun(path string, rec RunRecord) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open runs file: %w", err)
	}
	defer f.Close()

	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal run record: %w", err)
	}
	data = append(data, '\n', '\n') // blank line between records
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write run record: %w", err)
	}
	return nil
}

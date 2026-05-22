// Package pipeline orchestrates the full arc ingest flow:
// extract → title → slug → summarize → flash → flashcards → write → index
package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/jrniemiec/arc/config"
	"github.com/jrniemiec/arc/ingest/extractor"
	"github.com/jrniemiec/arc/store"
	"github.com/jrniemiec/arc/store/fs"
	"github.com/jrniemiec/llm"
)

// Request describes one ingest operation.
type Request struct {
	// Exactly one of URL or File must be set. File="-" reads stdin.
	URL  string
	File string

	// Optional metadata overrides
	Title      string // if empty, LLM generates it
	Collection string

	// Generation model/style overrides (empty = use config defaults)
	SummaryModel   string
	SummaryStyle   string
	FlashModel     string
	FlashcardModel string
	FlashcardStyle string

	// Flags
	NoFlashcards bool
	DryRun       bool

	// Progress is called with a human-readable status message at each pipeline step.
	// May be nil.
	Progress func(msg string)
}

// Result is returned after a successful ingest.
type Result struct {
	Slug string
	Cost store.CostRecord
}

// Run executes the full ingest pipeline and returns the new article slug.
// If req.DryRun is true it returns without writing anything.
func Run(ctx context.Context, cfg config.Config, req Request) (Result, error) {
	progress := req.Progress
	if progress == nil {
		progress = func(string) {}
	}

	// ── 1. Extract ────────────────────────────────────────────────────────
	var extracted extractor.Result
	var sourceType string

	switch {
	case req.URL != "":
		progress("fetching " + req.URL)
		var err error
		extracted, err = extractor.FromURL(ctx, req.URL)
		if err != nil {
			return Result{}, fmt.Errorf("extract url: %w", err)
		}
		sourceType = "url"

	case strings.HasSuffix(strings.ToLower(req.File), ".pdf"):
		progress("extracting PDF " + req.File)
		var err error
		extracted, err = extractor.FromPDF(ctx, req.File)
		if err != nil {
			return Result{}, fmt.Errorf("extract pdf: %w", err)
		}
		sourceType = "pdf"

	default:
		progress("reading " + req.File)
		var err error
		extracted, err = extractor.FromFile(req.File)
		if err != nil {
			return Result{}, fmt.Errorf("extract file: %w", err)
		}
		sourceType = "text"
	}

	if strings.TrimSpace(extracted.Text) == "" {
		return Result{}, fmt.Errorf("extraction produced no text")
	}

	words := len(strings.Fields(extracted.Text))
	progress(fmt.Sprintf("extracted %d words", words))

	if req.DryRun {
		return Result{}, nil
	}

	// ── 2. Resolve profiles and styles ───────────────────────────────────
	summaryStyle := first(req.SummaryStyle, cfg.Ingest.SummaryStyle)
	flashcardStyle := first(req.FlashcardStyle, cfg.Ingest.FlashcardStyle)

	summaryProf, err := lookupProfile(cfg, first(req.SummaryModel, cfg.Ingest.SummaryProfile))
	if err != nil {
		return Result{}, fmt.Errorf("summary profile: %w", err)
	}
	flashProf, err := lookupProfile(cfg, first(req.FlashModel, cfg.Ingest.FlashProfile))
	if err != nil {
		return Result{}, fmt.Errorf("flash profile: %w", err)
	}
	flashcardProf, err := lookupProfile(cfg, first(req.FlashcardModel, cfg.Ingest.FlashcardProfile))
	if err != nil {
		return Result{}, fmt.Errorf("flashcard profile: %w", err)
	}

	// ── 3. Build LLM providers ────────────────────────────────────────────
	newProvider := func(prof config.Profile) (llm.Provider, error) {
		return llm.New(llm.ProviderConfig{
			Provider: prof.Provider,
			Model:    prof.Model,
			Host:     prof.Host,
			APIKey:   resolveAPIKey(prof.Provider),
		})
	}

	var costRec store.CostRecord

	// ── 4. Title ──────────────────────────────────────────────────────────
	title := req.Title
	if title == "" {
		if extracted.Title != "" {
			title = extracted.Title
			progress("title: " + title)
		} else {
			progress("generating title...")
			p, err := newProvider(flashProf)
			if err != nil {
				return Result{}, fmt.Errorf("llm provider: %w", err)
			}
			snippet := truncateWords(extracted.Text, 500)
			t, u, err := chat(ctx, p,
				"You are a precise editor. Return only a short article title, no punctuation at the end, no quotes.",
				"Generate a concise title (5–10 words) for this article:\n\n"+snippet)
			if err != nil {
				return Result{}, fmt.Errorf("generate title: %w", err)
			}
			title = strings.TrimSpace(t)
			costRec.Flash.InputTokens += u.InputTokens
			costRec.Flash.OutputTokens += u.OutputTokens
			progress("title: " + title)
		}
	}

	// ── 5. Slug ───────────────────────────────────────────────────────────
	date := time.Now().Format("20060102")
	slug := date + "-" + slugify(title)
	progress("slug: " + slug)

	// ── 6. Summarize (map-reduce over chunks) ─────────────────────────────
	chunks := splitWords(extracted.Text, chunkWords)
	if len(chunks) > 1 {
		progress(fmt.Sprintf("summarizing (%d chunks, model: %s)...", len(chunks), summaryProf.Model))
	} else {
		progress(fmt.Sprintf("summarizing (model: %s)...", summaryProf.Model))
	}
	summaryProvider, err := newProvider(summaryProf)
	if err != nil {
		return Result{}, fmt.Errorf("llm provider: %w", err)
	}
	summaryText, su, err := summarize(ctx, summaryProvider, extracted.Text, summaryStyle)
	if err != nil {
		return Result{}, fmt.Errorf("summarize: %w", err)
	}
	costRec.Summary = store.CostEntry{
		Model:        summaryProf.Model,
		InputTokens:  su.InputTokens,
		OutputTokens: su.OutputTokens,
		USD:          cfg.CalcCost(summaryProf.Model, su.InputTokens, su.OutputTokens),
	}
	progress(fmt.Sprintf("summary done ($%.4f)", costRec.Summary.USD))

	// ── 7. Flash ──────────────────────────────────────────────────────────
	progress(fmt.Sprintf("generating flash (model: %s)...", flashProf.Model))
	flashProvider, err := newProvider(flashProf)
	if err != nil {
		return Result{}, fmt.Errorf("llm provider: %w", err)
	}
	flashText, fu, err := generateFlash(ctx, flashProvider, summaryText)
	if err != nil {
		return Result{}, fmt.Errorf("flash: %w", err)
	}
	costRec.Flash.Model = flashProf.Model
	costRec.Flash.InputTokens += fu.InputTokens
	costRec.Flash.OutputTokens += fu.OutputTokens
	costRec.Flash.USD = cfg.CalcCost(flashProf.Model, costRec.Flash.InputTokens, costRec.Flash.OutputTokens)

	// ── 8. Flashcards ─────────────────────────────────────────────────────
	var flashcardsJSON []byte
	if !req.NoFlashcards {
		progress(fmt.Sprintf("generating flashcards (model: %s)...", flashcardProf.Model))
		fcProvider, err := newProvider(flashcardProf)
		if err != nil {
			return Result{}, fmt.Errorf("llm provider: %w", err)
		}
		fj, cu, err := generateFlashcards(ctx, fcProvider, summaryText, flashcardStyle)
		if err != nil {
			return Result{}, fmt.Errorf("flashcards: %w", err)
		}
		flashcardsJSON = fj
		costRec.Flashcards = store.CostEntry{
			Model:        flashcardProf.Model,
			InputTokens:  cu.InputTokens,
			OutputTokens: cu.OutputTokens,
			USD:          cfg.CalcCost(flashcardProf.Model, cu.InputTokens, cu.OutputTokens),
		}
	}

	costRec.TotalUSD = costRec.Summary.USD + costRec.Flash.USD + costRec.Flashcards.USD

	// ── 9. Write files ────────────────────────────────────────────────────
	progress("writing files...")
	dir := filepath.Join(cfg.ArticlesRoot, slug)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return Result{}, fmt.Errorf("mkdir %s: %w", dir, err)
	}

	if err := os.WriteFile(filepath.Join(dir, "body.txt"), []byte(extracted.Text), 0644); err != nil {
		return Result{}, fmt.Errorf("write body: %w", err)
	}

	summaryFile := fmt.Sprintf("summary.%s.%s.txt", summaryStyle, summaryProf.Model)
	if err := os.WriteFile(filepath.Join(dir, summaryFile), []byte(summaryText), 0644); err != nil {
		return Result{}, fmt.Errorf("write summary: %w", err)
	}

	flashFile := fmt.Sprintf("flash.%s.txt", flashProf.Model)
	if err := os.WriteFile(filepath.Join(dir, flashFile), []byte(flashText), 0644); err != nil {
		return Result{}, fmt.Errorf("write flash: %w", err)
	}

	if len(flashcardsJSON) > 0 {
		fcFile := fmt.Sprintf("flashcards.%s.%s.json", flashcardStyle, flashcardProf.Model)
		if err := os.WriteFile(filepath.Join(dir, fcFile), flashcardsJSON, 0644); err != nil {
			return Result{}, fmt.Errorf("write flashcards: %w", err)
		}
	}

	// Source sidecar
	if req.URL != "" {
		if err := os.WriteFile(filepath.Join(dir, "source.url"), []byte(req.URL+"\n"), 0644); err != nil {
			return Result{}, fmt.Errorf("write source.url: %w", err)
		}
	} else if req.File != "" && req.File != "-" {
		if strings.ToLower(filepath.Ext(req.File)) == ".pdf" {
			data, _ := os.ReadFile(req.File)
			_ = os.WriteFile(filepath.Join(dir, "source.pdf"), data, 0644)
		}
	}

	// ── 10. meta.json ─────────────────────────────────────────────────────
	now := time.Now().UTC().Format(time.RFC3339)
	collections := []string{}
	if req.Collection != "" {
		collections = []string{req.Collection}
	}
	meta := fs.Meta{
		ID:             slug,
		Title:          title,
		URL:            req.URL,
		SourceType:     sourceType,
		Author:         extracted.Author,
		Language:       extracted.Language,
		IngestedAt:     now,
		Collections:    collections,
		SummaryModel:   summaryProf.Model,
		SummaryStyle:   summaryStyle,
		FlashModel:     flashProf.Model,
		FlashcardModel: flashcardProf.Model,
		FlashcardStyle: flashcardStyle,
	}
	if err := fs.WriteMeta(dir, meta); err != nil {
		return Result{}, fmt.Errorf("write meta: %w", err)
	}

	// ── 11. Append to events.jsonl ────────────────────────────────────────
	appendEvent(cfg.EventsPath, store.Event{
		TS:        time.Now().UTC(),
		Type:      "ingest",
		ArticleID: slug,
		Cost:      &costRec,
	})

	return Result{Slug: slug, Cost: costRec}, nil
}

// ── LLM helpers ───────────────────────────────────────────────────────────────

const chunkWords = 3000

// chat wraps llm.Provider.Chat for single-turn calls.
func chat(ctx context.Context, p llm.Provider, system, prompt string) (string, llm.Usage, error) {
	return p.Chat(ctx, system, []llm.Message{{Role: llm.RoleUser, Content: prompt}})
}

func summarize(ctx context.Context, p llm.Provider, text, style string) (string, llm.Usage, error) {
	chunks := splitWords(text, chunkWords)
	var total llm.Usage

	if len(chunks) == 1 {
		return summarizeChunk(ctx, p, chunks[0], style, &total)
	}

	var chunkSummaries []string
	for _, chunk := range chunks {
		s, _, err := summarizeChunk(ctx, p, chunk, style, &total)
		if err != nil {
			return "", total, err
		}
		chunkSummaries = append(chunkSummaries, s)
	}

	combined := strings.Join(chunkSummaries, "\n\n---\n\n")
	final, _, err := summarizeChunk(ctx, p, combined, style, &total)
	return final, total, err
}

func summarizeChunk(ctx context.Context, p llm.Provider, text, style string, total *llm.Usage) (string, llm.Usage, error) {
	result, u, err := chat(ctx, p, summarySystemPrompt(style),
		"Summarize the following article:\n\n"+text)
	total.InputTokens += u.InputTokens
	total.OutputTokens += u.OutputTokens
	return result, u, err
}

func summarySystemPrompt(style string) string {
	switch style {
	case "bullets":
		return "You are a precise knowledge curator. Summarize in concise bullet points. No fluff, no preamble."
	case "technical":
		return "You are a technical writer. Summarize with emphasis on methods, results, and implementation details. Use markdown."
	default: // study-notes
		return "You are a knowledge curator. Write structured study notes: key concepts, insights, and takeaways. Use markdown headers and bullets."
	}
}

func generateFlash(ctx context.Context, p llm.Provider, summary string) (string, llm.Usage, error) {
	return chat(ctx, p,
		"You write ultra-concise audio summaries. 3-5 sentences. No markdown. Natural speech rhythm. No bullet points.",
		"Write a flash summary (spoken aloud) for this article summary:\n\n"+summary)
}

func generateFlashcards(ctx context.Context, p llm.Provider, summary, style string) ([]byte, llm.Usage, error) {
	raw, u, err := chat(ctx, p,
		flashcardSystemPrompt(style),
		"Generate flashcards from this article summary. Return valid JSON only, no markdown fences:\n\n"+summary)
	if err != nil {
		return nil, u, err
	}

	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	if !json.Valid([]byte(raw)) {
		return nil, u, fmt.Errorf("flashcards LLM returned invalid JSON")
	}
	return []byte(raw), u, nil
}

func flashcardSystemPrompt(style string) string {
	base := `You generate flashcards as a JSON array. Each card:
{"type":"concept|fact|insight","front":"question","back":"answer","tags":["tag1"]}.
Written for the ear — no markdown, natural language. Return only the JSON array.`
	switch style {
	case "socratic":
		return base + " Use probing questions that test deep understanding."
	case "cloze":
		return base + " Use fill-in-the-blank style fronts."
	default:
		return base
	}
}

// ── Utilities ─────────────────────────────────────────────────────────────────

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(title string) string {
	s := strings.ToLower(title)
	s = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return '-'
	}, s)
	s = nonAlnum.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 60 {
		s = s[:60]
		if i := strings.LastIndex(s, "-"); i > 30 {
			s = s[:i]
		}
	}
	return s
}

func first(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func truncateWords(text string, maxWords int) string {
	words := strings.Fields(text)
	if len(words) <= maxWords {
		return text
	}
	return strings.Join(words[:maxWords], " ")
}

func splitWords(text string, chunkSize int) []string {
	words := strings.Fields(text)
	if len(words) <= chunkSize {
		return []string{text}
	}
	var chunks []string
	for i := 0; i < len(words); i += chunkSize {
		end := i + chunkSize
		if end > len(words) {
			end = len(words)
		}
		chunks = append(chunks, strings.Join(words[i:end], " "))
	}
	return chunks
}

// lookupProfile resolves a profile name from config. Returns an error if not found.
func lookupProfile(cfg config.Config, name string) (config.Profile, error) {
	p, ok := cfg.Profile(name)
	if !ok {
		return config.Profile{}, fmt.Errorf("unknown profile %q — check ~/.arc/config.json or run: arc profiles", name)
	}
	return p, nil
}

func resolveAPIKey(provider string) string {
	switch strings.ToLower(provider) {
	case "anthropic":
		for _, k := range []string{"ARC_ANTHROPIC_API_KEY", "ANTHROPIC_API_KEY"} {
			if v := strings.TrimSpace(os.Getenv(k)); v != "" {
				return v
			}
		}
	case "openai":
		for _, k := range []string{"ARC_OPENAI_API_KEY", "OPENAI_API_KEY"} {
			if v := strings.TrimSpace(os.Getenv(k)); v != "" {
				return v
			}
		}
	}
	return ""
}

func appendEvent(path string, ev store.Event) {
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(data, '\n'))
}

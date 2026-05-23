// Package pipeline orchestrates the full arc ingest flow:
// extract → title → slug → summarize → flash → flashcards → write → index
package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	briefchunk "github.com/jrniemiec/brief/chunk"
	briefllm "github.com/jrniemiec/brief/llm"
	briefsummarize "github.com/jrniemiec/brief/summarize"
	brieftypes "github.com/jrniemiec/brief/types"
	"github.com/jrniemiec/arc/config"
	"github.com/jrniemiec/arc/ingest/extractor"
	"github.com/jrniemiec/arc/store"
	"github.com/jrniemiec/arc/store/fs"
	"github.com/jrniemiec/llm"
)

// SummarizeRequest describes a standalone summarize operation.
type SummarizeRequest struct {
	Text     string // plain text to summarize
	Title    string // optional, used for document metadata
	Source   string // optional URL or file path
	Style    string // overrides config ingest.summary_style
	Profile  string // profile name, overrides config ingest.summary_profile
	Progress func(string)
}

// SummarizeResult holds the output of a standalone summarize operation.
type SummarizeResult struct {
	Text  string
	Model string
	Style string
	Usage llm.Usage
}

// Summarize runs just the summarize step against plain text.
// It does not write any files.
func Summarize(ctx context.Context, cfg config.Config, req SummarizeRequest) (SummarizeResult, error) {
	progress := req.Progress
	if progress == nil {
		progress = func(string) {}
	}

	style := first(req.Style, cfg.Ingest.SummaryStyle)
	profileName := first(req.Profile, cfg.Ingest.SummaryProfile)

	prof, err := lookupProfile(cfg, profileName)
	if err != nil {
		return SummarizeResult{}, fmt.Errorf("profile: %w", err)
	}

	p, err := llm.New(llm.ProviderConfig{
		Provider: prof.Provider,
		Model:    prof.Model,
		Host:     prof.Host,
		APIKey:   resolveAPIKey(prof.Provider),
	})
	if err != nil {
		return SummarizeResult{}, fmt.Errorf("llm provider: %w", err)
	}

	text, usage, err := summarizeText(ctx, p, req.Text, req.Title, req.Source, style, prof.Model,
		cfg.Ingest.ChunkTokens, cfg.Ingest.SummaryMaxTokens, cfg.StylePrompt(style), progress)
	if err != nil {
		return SummarizeResult{}, err
	}

	return SummarizeResult{Text: text, Model: prof.Model, Style: style, Usage: usage}, nil
}

// FlashRequest describes a standalone flash generation operation.
type FlashRequest struct {
	Text     string // text to flash-summarize (summary or body)
	Profile  string // profile name override
	Progress func(string)
}

// FlashResult holds the output of a standalone flash operation.
type FlashResult struct {
	Text  string
	Model string
	Usage llm.Usage
}

// Flash runs just the flash generation step against plain text.
// It does not write any files.
func Flash(ctx context.Context, cfg config.Config, req FlashRequest) (FlashResult, error) {
	progress := req.Progress
	if progress == nil {
		progress = func(string) {}
	}

	profileName := first(req.Profile, cfg.Ingest.FlashProfile)
	prof, err := lookupProfile(cfg, profileName)
	if err != nil {
		return FlashResult{}, fmt.Errorf("profile: %w", err)
	}

	p, err := llm.New(llm.ProviderConfig{
		Provider: prof.Provider,
		Model:    prof.Model,
		Host:     prof.Host,
		APIKey:   resolveAPIKey(prof.Provider),
	})
	if err != nil {
		return FlashResult{}, fmt.Errorf("llm provider: %w", err)
	}

	progress(fmt.Sprintf("generating flash (model: %s)...", prof.Model))
	text, usage, err := generateFlash(ctx, p, req.Text, cfg.Ingest.FlashSystemPrompt, cfg.Ingest.FlashMaxTokens)
	if err != nil {
		return FlashResult{}, err
	}

	return FlashResult{Text: text, Model: prof.Model, Usage: usage}, nil
}

// FlashcardsRequest describes a standalone flashcard generation operation.
type FlashcardsRequest struct {
	Text     string // text to generate flashcards from
	Style    string // "socratic" | "cloze"
	Profile  string // profile name override
	Progress func(string)
}

// FlashcardsResult holds the output of a standalone flashcard operation.
type FlashcardsResult struct {
	JSON  []byte
	Model string
	Style string
	Usage llm.Usage
}

// Flashcards runs just the flashcard generation step against plain text.
// It does not write any files.
func Flashcards(ctx context.Context, cfg config.Config, req FlashcardsRequest) (FlashcardsResult, error) {
	progress := req.Progress
	if progress == nil {
		progress = func(string) {}
	}

	style := first(req.Style, cfg.Ingest.FlashcardStyle)
	profileName := first(req.Profile, cfg.Ingest.FlashcardProfile)

	prof, err := lookupProfile(cfg, profileName)
	if err != nil {
		return FlashcardsResult{}, fmt.Errorf("profile: %w", err)
	}

	p, err := llm.New(llm.ProviderConfig{
		Provider: prof.Provider,
		Model:    prof.Model,
		Host:     prof.Host,
		APIKey:   resolveAPIKey(prof.Provider),
	})
	if err != nil {
		return FlashcardsResult{}, fmt.Errorf("llm provider: %w", err)
	}

	progress(fmt.Sprintf("generating flashcards (style: %s, model: %s)...", style, prof.Model))
	data, usage, err := generateFlashcards(ctx, p, req.Text, style, cfg.FlashcardStylePrompt(style))
	if err != nil {
		return FlashcardsResult{}, err
	}

	return FlashcardsResult{JSON: data, Model: prof.Model, Style: style, Usage: usage}, nil
}

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
	Slug            string
	Cost            store.CostRecord
	ExtractionStats string // human-readable extraction stats line
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

	stats := extracted.Stats()
	progress(stats)

	if req.DryRun {
		return Result{ExtractionStats: stats}, nil
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
	summaryProvider, err := newProvider(summaryProf)
	if err != nil {
		return Result{}, fmt.Errorf("llm provider: %w", err)
	}
	summaryText, su, err := summarizeText(ctx, summaryProvider, extracted.Text, title, req.URL, summaryStyle, summaryProf.Model, cfg.Ingest.ChunkTokens, cfg.Ingest.SummaryMaxTokens, cfg.StylePrompt(summaryStyle), progress)
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
	flashText, fu, err := generateFlash(ctx, flashProvider, summaryText, cfg.Ingest.FlashSystemPrompt, cfg.Ingest.FlashMaxTokens)
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
		fj, cu, err := generateFlashcards(ctx, fcProvider, summaryText, flashcardStyle, cfg.FlashcardStylePrompt(flashcardStyle))
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

	return Result{Slug: slug, Cost: costRec, ExtractionStats: stats}, nil
}

// ── LLM helpers ───────────────────────────────────────────────────────────────

// chat wraps llm.Provider.Chat for single-turn calls using brief's interface.
func chat(ctx context.Context, p llm.Provider, system, prompt string) (string, llm.Usage, error) {
	return p.Chat(ctx, system, []llm.Message{{Role: llm.RoleUser, Content: prompt}})
}

// briefAdapter bridges github.com/jrniemiec/llm.Provider → brief/llm.Provider.
// brief expects a simpler single-turn interface; we wrap the multi-turn one.
// onCall, if set, is invoked before each LLM call (used for per-chunk progress).
type briefAdapter struct {
	p      llm.Provider
	model  string
	onCall func()
}

func (a *briefAdapter) Name() string { return a.model }
func (a *briefAdapter) Chat(ctx context.Context, system, prompt string) (string, briefllm.Usage, error) {
	if a.onCall != nil {
		a.onCall()
	}
	out, u, err := a.p.Chat(ctx, system, []llm.Message{{Role: llm.RoleUser, Content: prompt}})
	return out, briefllm.Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens}, err
}

// summarizeText uses brief's paragraph-aware chunking and map-reduce summarization.
func summarizeText(ctx context.Context, p llm.Provider, text, title, source, style, model string, chunkTokens, maxTokens int, systemPrompt string, progress func(string)) (string, llm.Usage, error) {
	doc := brieftypes.Document{
		ID:     brieftypes.ShortID(brieftypes.HashText(text)),
		Hash:   brieftypes.HashText(text),
		Title:  title,
		Source: source,
		Text:   text,
	}

	chunks, err := briefchunk.ChunkDocument(doc, chunkTokens)
	if err != nil {
		return "", llm.Usage{}, fmt.Errorf("chunk: %w", err)
	}

	// Build per-chunk token counts up front so the progress adapter can report them.
	chunkTokenCounts := make([]int, len(chunks))
	for i, ch := range chunks {
		chunkTokenCounts[i] = briefchunk.ApproxTokens(ch.Text)
	}

	n := len(chunks)
	call := 0 // tracks which LLM call we're on (map calls first, then reduce)
	adapter := &briefAdapter{
		p:     p,
		model: model,
		onCall: func() {
			call++
			if call <= n {
				// MAP phase
				progress(fmt.Sprintf("chunk %d/%d (~%d tokens, style: %s, model: %s)", call, n, chunkTokenCounts[call-1], style, model))
			} else {
				// REDUCE phase
				progress(fmt.Sprintf("reducing %d chunk summaries (style: %s)...", n, style))
			}
		},
	}

	summary, bu, err := briefsummarize.SummarizeDocument(ctx, io.Discard, adapter, doc, chunks, style, systemPrompt, maxTokens, false)
	if err != nil {
		return "", llm.Usage{}, fmt.Errorf("summarize: %w", err)
	}

	return summary.Markdown, llm.Usage{InputTokens: bu.InputTokens, OutputTokens: bu.OutputTokens}, nil
}


func generateFlash(ctx context.Context, p llm.Provider, text, systemPrompt string, maxTokens int) (string, llm.Usage, error) {
	out, u, err := p.Chat(ctx, systemPrompt, []llm.Message{
		{Role: llm.RoleUser, Content: "Write a flash summary for this text:\n\n" + text},
	})
	_ = maxTokens // passed to provider config in future; LLMs self-limit short outputs well
	return out, u, err
}

func generateFlashcards(ctx context.Context, p llm.Provider, text, style, systemPrompt string) ([]byte, llm.Usage, error) {
	raw, u, err := chat(ctx, p,
		systemPrompt,
		"Generate flashcards from this text. Return valid JSON only, no markdown fences:\n\n"+text)
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

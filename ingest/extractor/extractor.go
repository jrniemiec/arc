// Package extractor pulls plain text from URLs, PDFs, and text files.
package extractor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	readability "codeberg.org/readeck/go-readability/v2"
)

// Result holds the extracted content and any metadata gleaned during extraction.
type Result struct {
	Text     string // plain text body
	Title    string // title if detected (may be empty)
	Author   string // byline if detected
	Language string // language code if detected
	HTML     string // raw HTML (URL extraction only, for source.html sidecar)

	// Transfer stats — set by FromURL; zero for file/PDF sources.
	DownloadBytes    int           // raw HTTP response body size in bytes
	DownloadDuration time.Duration // time to fetch and read the response

	// Size stats — set for all sources.
	SourceBytes    int // original source size (download body, file, or PDF)
	ExtractedBytes int // plain text size after extraction
}

// Stats returns a one-line human-readable summary of extraction metrics.
func (r Result) Stats() string {
	words := len(strings.Fields(r.Text))
	tokens := len(r.Text) / 4 // ~4 chars per token

	extracted := formatBytes(r.ExtractedBytes)

	if r.DownloadBytes > 0 && r.DownloadDuration > 0 {
		mbits := float64(r.DownloadBytes*8) / r.DownloadDuration.Seconds() / 1_000_000
		pct := 0
		if r.SourceBytes > 0 {
			pct = r.ExtractedBytes * 100 / r.SourceBytes
		}
		return fmt.Sprintf("downloaded %s in %.1fs (%.2f Mbits/s) — extracted %s (%d%%), %d words, ~%d tokens",
			formatBytes(r.DownloadBytes), r.DownloadDuration.Seconds(), mbits,
			extracted, pct, words, tokens)
	}

	if r.SourceBytes > 0 && r.SourceBytes != r.ExtractedBytes {
		pct := r.ExtractedBytes * 100 / r.SourceBytes
		return fmt.Sprintf("read %s — extracted %s (%d%%), %d words, ~%d tokens",
			formatBytes(r.SourceBytes), extracted, pct, words, tokens)
	}

	return fmt.Sprintf("read %s — %d words, ~%d tokens", extracted, words, tokens)
}

// extractJinaTitle extracts the first H1 title from Jina markdown output.
func extractJinaTitle(md string) string {
	for _, line := range strings.SplitN(md, "\n", 20) {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimPrefix(line, "# ")
		}
	}
	return ""
}

// cleanJinaMarkdown strips navigation noise, image refs, bare URLs, and boilerplate
// from Jina reader markdown output, leaving clean readable prose.
func cleanJinaMarkdown(md string) string {
	lines := strings.Split(md, "\n")
	var out []string
	skipPatterns := []string{
		"sign in", "sign up", "subscribe", "member-only",
		"open in app", "get the app", "follow me on", "follow us on",
		"clap for this story", "responses",
	}

	for _, line := range lines {
		// Strip image references: ![alt](url)
		if strings.HasPrefix(strings.TrimSpace(line), "![") {
			continue
		}
		// Strip bare URL lines: lines that are just a markdown link with no description
		// e.g. [https://...](https://...) or just https://...
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[http") || strings.HasPrefix(trimmed, "http") {
			continue
		}
		// Skip navigation boilerplate (case-insensitive)
		lower := strings.ToLower(trimmed)
		skip := false
		for _, p := range skipPatterns {
			if strings.Contains(lower, p) && len(strings.Fields(trimmed)) < 8 {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		out = append(out, line)
	}

	// Collapse runs of 3+ blank lines to 2
	result := strings.Join(out, "\n")
	for strings.Contains(result, "\n\n\n\n") {
		result = strings.ReplaceAll(result, "\n\n\n\n", "\n\n\n")
	}
	return strings.TrimSpace(result)
}

// isBotCheckPage returns true if the extracted text looks like a bot-verification page.
func isBotCheckPage(text string) bool {
	lower := strings.ToLower(text)
	triggers := []string{
		"security verification",
		"verifying you are not a bot",
		"please enable javascript",
		"checking your browser",
		"ddos protection",
		"just a moment",
	}
	count := 0
	for _, t := range triggers {
		if strings.Contains(lower, t) {
			count++
		}
	}
	// Require at least 2 signals and short text (real articles are longer)
	return count >= 2 && len(strings.Fields(text)) < 200
}

func formatBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// FromURL fetches a URL and extracts the main article text via Readability.
func FromURL(ctx context.Context, rawURL string) (Result, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return Result{}, fmt.Errorf("parse url: %w", err)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return Result{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", "arc/1.0 (+https://github.com/jrniemiec/arc)")

	fetchStart := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("fetch %s: %w", rawURL, err)
	}

	// On 403, retry via Jina reader proxy (handles paywalls and bot-detection).
	viaJina := false
	if resp.StatusCode == http.StatusForbidden {
		resp.Body.Close()
		jinaURL := "https://r.jina.ai/" + rawURL
		req2, err := http.NewRequestWithContext(ctx, http.MethodGet, jinaURL, nil)
		if err != nil {
			return Result{}, fmt.Errorf("build jina request: %w", err)
		}
		req2.Header.Set("User-Agent", req.Header.Get("User-Agent"))
		req2.Header.Set("X-Return-Format", "markdown")
		resp, err = client.Do(req2)
		if err != nil {
			return Result{}, fmt.Errorf("fetch via jina %s: %w", rawURL, err)
		}
		viaJina = true
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return Result{}, fmt.Errorf("fetch %s: HTTP %d", rawURL, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Result{}, fmt.Errorf("read response: %w", err)
	}
	downloadDuration := time.Since(fetchStart)

	var text, title, author, language string

	if viaJina {
		// Jina returns cleaned markdown — extract text directly without readability.
		text = cleanJinaMarkdown(string(body))
		title = extractJinaTitle(string(body))
	} else {
		parser := readability.NewParser()
		article, err := parser.Parse(bytes.NewReader(body), parsed)
		if err != nil {
			return Result{}, fmt.Errorf("readability parse: %w", err)
		}
		var textBuf bytes.Buffer
		if err := article.RenderText(&textBuf); err != nil {
			return Result{}, fmt.Errorf("render text: %w", err)
		}
		text = strings.TrimSpace(textBuf.String())
		title = article.Title()
		author = article.Byline()
		language = article.Language()
	}

	// Detect bot-check pages that slipped through (Jina fallback may return these).
	if isBotCheckPage(text) {
		return Result{}, fmt.Errorf("fetch %s: site requires JavaScript or bot verification — try downloading the page manually", rawURL)
	}

	return Result{
		Text:             text,
		Title:            title,
		Author:           author,
		Language:         language,
		HTML:             string(body),
		DownloadBytes:    len(body),
		DownloadDuration: downloadDuration,
		SourceBytes:      len(body),
		ExtractedBytes:   len(text),
	}, nil
}

// FromPDF extracts text from a PDF file.
// Tries pdftotext (poppler) first; falls back to a message directing the user to install it
// if unavailable (pure-Go PDF fallback is not implemented in Phase 1).
func FromPDF(ctx context.Context, path string) (Result, error) {
	// Try pdftotext
	if _, err := exec.LookPath("pdftotext"); err == nil {
		return fromPDFWithPoppler(ctx, path)
	}

	return Result{}, fmt.Errorf(
		"pdftotext not found — install with: brew install poppler\n" +
			"(or convert the PDF to text manually and use: arc ingest file.txt)")
}

func fromPDFWithPoppler(ctx context.Context, path string) (Result, error) {
	cmd := exec.CommandContext(ctx, "pdftotext", "-layout", path, "-")
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf

	if err := cmd.Run(); err != nil {
		return Result{}, fmt.Errorf("pdftotext: %w — %s", err, strings.TrimSpace(errBuf.String()))
	}

	text := strings.TrimSpace(out.String())
	if text == "" {
		return Result{}, fmt.Errorf("pdftotext produced no output for %s", path)
	}
	var sourceBytes int
	if info, err := os.Stat(path); err == nil {
		sourceBytes = int(info.Size())
	}
	return Result{Text: text, SourceBytes: sourceBytes, ExtractedBytes: len(text)}, nil
}

// FromFile reads plain text (or stdin if path is "-").
func FromFile(path string) (Result, error) {
	var data []byte
	var err error

	if path == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return Result{}, fmt.Errorf("read file %s: %w", path, err)
	}

	text := strings.TrimSpace(string(data))
	return Result{Text: text, SourceBytes: len(data), ExtractedBytes: len(text)}, nil
}

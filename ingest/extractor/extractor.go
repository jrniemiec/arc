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

	resp, err := client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("fetch %s: %w", rawURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return Result{}, fmt.Errorf("fetch %s: HTTP %d", rawURL, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Result{}, fmt.Errorf("read response: %w", err)
	}

	parser := readability.NewParser()
	article, err := parser.Parse(bytes.NewReader(body), parsed)
	if err != nil {
		return Result{}, fmt.Errorf("readability parse: %w", err)
	}

	var textBuf bytes.Buffer
	if err := article.RenderText(&textBuf); err != nil {
		return Result{}, fmt.Errorf("render text: %w", err)
	}

	return Result{
		Text:     strings.TrimSpace(textBuf.String()),
		Title:    article.Title(),
		Author:   article.Byline(),
		Language: article.Language(),
		HTML:     string(body),
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
	return Result{Text: text}, nil
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

	return Result{Text: strings.TrimSpace(string(data))}, nil
}

// Package extractor pulls plain text from URLs, PDFs, and text files.
package extractor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

// userAgent mimics a real Chrome browser to reduce Cloudflare/CDN bot-scoring.
const userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36"

// pageKind classifies a short or suspicious page response.
type pageKind int

const (
	pageKindNormal  pageKind = iota
	pageKindBotCheck         // Cloudflare / JS challenge
	pageKindPaywall          // membership / subscription gate
)

// classifyPage returns the kind of page based on content signals.
// Returns pageKindNormal when the page looks like real article content.
func classifyPage(text string) pageKind {
	lower := strings.ToLower(text)
	words := len(strings.Fields(text))

	// Only classify short responses — real articles are longer.
	if words >= 200 {
		return pageKindNormal
	}

	botTriggers := []string{
		"security verification",
		"verifying you are not a bot",
		"please enable javascript",
		"checking your browser",
		"ddos protection",
		"just a moment",
	}
	botCount := 0
	for _, t := range botTriggers {
		if strings.Contains(lower, t) {
			botCount++
		}
	}
	if botCount >= 2 {
		return pageKindBotCheck
	}

	paywallTriggers := []string{
		"become a member",
		"member-only",
		"members only",
		"upgrade your membership",
		"start your membership",
		"get unlimited access",
		"unlock this story",
		"read the full story",
		"this story is only",
		"subscribe to read",
		"subscription required",
		"sign in to read",
	}
	for _, t := range paywallTriggers {
		if strings.Contains(lower, t) {
			return pageKindPaywall
		}
	}

	return pageKindNormal
}

// logShortContent logs up to 500 chars of text at DEBUG when word count is low,
// so we can see exactly what the page returned.
func logShortContent(label, rawURL, text string) {
	words := len(strings.Fields(text))
	if words >= 300 {
		return
	}
	preview := text
	if len(preview) > 500 {
		preview = preview[:500] + "…"
	}
	slog.Debug(label, "url", rawURL, "words", words, "preview", preview)
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

// cookiesForURL returns cookies from the jar map that match the URL's host.
// A jar entry matches if the URL host equals or ends with the domain key.
func cookiesForURL(rawURL string, jars map[string]string) []*http.Cookie {
	if len(jars) == 0 {
		return nil
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}
	host := strings.ToLower(u.Hostname())

	for domain, path := range jars {
		domain = strings.ToLower(strings.TrimPrefix(domain, "."))
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return parseNetscapeCookies(path, host)
		}
	}
	return nil
}

// parseNetscapeCookies reads a Netscape/curl cookie jar file and returns HTTP cookies.
// Lines starting with '#' are comments (except #HttpOnly_ prefixed domain lines).
// Format: domain \t flag \t path \t secure \t expiry \t name \t value
func parseNetscapeCookies(path, host string) []*http.Cookie {
	// Expand ~ in path
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		slog.Error("cookie jar read failed", "path", path, "host", host, "err", err)
		return nil
	}
	now := time.Now()
	var cookies []*http.Cookie
	var nExpired, nSession, nValid int
	for _, line := range strings.Split(string(data), "\n") {
		// Strip #HttpOnly_ prefix — these are valid cookies
		line = strings.TrimPrefix(line, "#HttpOnly_")
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 7 {
			continue
		}
		name := fields[5]
		expiry := int64(0)
		if v, err := strconv.ParseInt(strings.TrimSpace(fields[4]), 10, 64); err == nil {
			expiry = v
		}
		switch {
		case expiry == 0:
			nSession++
			slog.Debug("cookie loaded (session)", "host", host, "name", name)
		case time.Unix(expiry, 0).Before(now):
			nExpired++
			slog.Warn("cookie expired", "host", host, "name", name,
				"expired_at", time.Unix(expiry, 0).Format(time.RFC3339),
				"expired_ago", now.Sub(time.Unix(expiry, 0)).Round(time.Minute).String(),
			)
			continue // skip expired cookies
		default:
			nValid++
			slog.Debug("cookie loaded (valid)", "host", host, "name", name,
				"expires_at", time.Unix(expiry, 0).Format(time.RFC3339),
				"expires_in", time.Until(time.Unix(expiry, 0)).Round(time.Minute).String(),
			)
		}
		cookies = append(cookies, &http.Cookie{
			Name:  name,
			Value: fields[6],
		})
	}
	slog.Info("cookie jar loaded", "host", host, "path", path,
		"total", nValid+nSession+nExpired,
		"valid", nValid, "session", nSession, "expired", nExpired,
		"sending", len(cookies),
	)
	return cookies
}

// fetchViaJina retries a URL through the Jina reader proxy.
func fetchViaJina(ctx context.Context, client *http.Client, rawURL, userAgent string) (*http.Response, bool, error) {
	jinaURL := "https://r.jina.ai/" + rawURL
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jinaURL, nil)
	if err != nil {
		return nil, false, fmt.Errorf("build jina request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("X-Return-Format", "markdown")
	slog.Debug("jina fetch start", "url", rawURL, "jina_url", jinaURL)
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		slog.Error("jina fetch failed", "url", rawURL, "elapsed", time.Since(start).Round(time.Millisecond), "err", err)
		return nil, false, fmt.Errorf("fetch via jina %s: %w", rawURL, err)
	}
	slog.Debug("jina fetch response",
		"url", rawURL,
		"status", resp.StatusCode,
		"content_type", resp.Header.Get("Content-Type"),
		"content_length", resp.Header.Get("Content-Length"),
		"elapsed", time.Since(start).Round(time.Millisecond),
	)
	return resp, true, nil
}

// FromURL fetches a URL and extracts the main article text via Readability.
func FromURL(ctx context.Context, rawURL string) (Result, error) {
	return fromURL(ctx, rawURL, nil)
}

// FromURLWithCookies fetches a URL using the provided cookie jar map.
func FromURLWithCookies(ctx context.Context, rawURL string, cookieJars map[string]string) (Result, error) {
	return fromURL(ctx, rawURL, cookieJars)
}

func fromURL(ctx context.Context, rawURL string, cookieJars map[string]string) (Result, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return Result{}, fmt.Errorf("parse url: %w", err)
	}

	var redirectCount int
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			redirectCount++
			slog.Debug("http redirect",
				"url", rawURL,
				"redirect_to", req.URL.String(),
				"hop", redirectCount,
			)
			if redirectCount > 10 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return Result{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)

	// Inject cookies from the matching jar, if any.
	cookies := cookiesForURL(rawURL, cookieJars)
	hasCookies := len(cookies) > 0
	for _, c := range cookies {
		req.AddCookie(c)
	}

	slog.Debug("http fetch start", "url", rawURL, "has_cookies", hasCookies, "cookie_count", len(cookies))
	fetchStart := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		slog.Error("fetch failed", "url", rawURL, "elapsed", time.Since(fetchStart).Round(time.Millisecond), "err", err)
		return Result{}, fmt.Errorf("fetch %s: %w", rawURL, err)
	}
	slog.Debug("http fetch response",
		"url", rawURL,
		"status", resp.StatusCode,
		"content_type", resp.Header.Get("Content-Type"),
		"content_length", resp.Header.Get("Content-Length"),
		"server", resp.Header.Get("Server"),
		"cf_ray", resp.Header.Get("CF-Ray"),
		"x_cache", resp.Header.Get("X-Cache"),
		"redirects", redirectCount,
		"elapsed", time.Since(fetchStart).Round(time.Millisecond),
	)

	// On 403, always retry via Jina — it can bypass both bot-checks and paywalls
	// independently of whether we have cookies for the site.
	viaJina := false
	if resp.StatusCode == http.StatusForbidden {
		slog.Warn("HTTP 403 received, retrying via Jina",
			"url", rawURL,
			"has_cookies", hasCookies,
			"status", resp.StatusCode,
			"server", resp.Header.Get("Server"),
			"cf_ray", resp.Header.Get("CF-Ray"),
			"x_cache", resp.Header.Get("X-Cache"),
		)
		resp.Body.Close()
		resp, viaJina, err = fetchViaJina(ctx, client, rawURL, req.Header.Get("User-Agent"))
		if err != nil {
			slog.Error("jina fallback failed", "url", rawURL, "err", err)
			return Result{}, err
		}
		slog.Info("jina fallback succeeded", "url", rawURL, "status", resp.StatusCode)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		slog.Error("HTTP error",
			"url", rawURL,
			"status", resp.StatusCode,
			"has_cookies", hasCookies,
			"server", resp.Header.Get("Server"),
			"content_type", resp.Header.Get("Content-Type"),
			"cf_ray", resp.Header.Get("CF-Ray"),
		)
		return Result{}, fmt.Errorf("fetch %s: HTTP %d", rawURL, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Result{}, fmt.Errorf("read response: %w", err)
	}
	downloadDuration := time.Since(fetchStart)

	var text, title, author, language string

	if viaJina {
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

	// If direct fetch returned a bot/paywall page, retry via Jina.
	if !viaJina {
		if kind := classifyPage(text); kind != pageKindNormal {
			slog.Warn("suspicious page detected, retrying via Jina",
				"url", rawURL,
				"kind", kind,
				"has_cookies", hasCookies,
				"extracted_words", len(strings.Fields(text)),
			)
			logShortContent("direct fetch short content", rawURL, text)
			resp.Body.Close()
			jinaResp, _, err := fetchViaJina(ctx, client, rawURL, userAgent)
			if err != nil {
				slog.Error("jina fallback failed", "url", rawURL, "kind", kind, "err", err)
				return Result{}, fmt.Errorf("fetch %s: %w", rawURL, err)
			}
			defer jinaResp.Body.Close()
			jinaBody, err := io.ReadAll(jinaResp.Body)
			if err != nil {
				slog.Error("read jina response failed", "url", rawURL, "err", err)
				return Result{}, fmt.Errorf("read jina response: %w", err)
			}
			text = cleanJinaMarkdown(string(jinaBody))
			title = extractJinaTitle(string(jinaBody))
			body = jinaBody
			viaJina = true
		}
	}

	// Final guard — classify what Jina returned.
	switch kind := classifyPage(text); kind {
	case pageKindBotCheck:
		logShortContent("jina bot-check content", rawURL, text)
		slog.Error("bot-check page persists after Jina fallback",
			"url", rawURL, "has_cookies", hasCookies, "extracted_words", len(strings.Fields(text)))
		return Result{}, fmt.Errorf("fetch %s: site requires JavaScript or bot verification — try downloading the page manually", rawURL)
	case pageKindPaywall:
		logShortContent("jina paywall content", rawURL, text)
		slog.Error("paywall detected after Jina fallback",
			"url", rawURL, "has_cookies", hasCookies, "extracted_words", len(strings.Fields(text)))
		return Result{}, fmt.Errorf("fetch %s: paywalled content — refresh your cookie jar or download the page manually", rawURL)
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

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

	"github.com/imroc/req/v3"
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

// ── Format detection ──────────────────────────────────────────────────────────

// docFormat identifies the content format of a fetched or local resource.
// To add a new format: add a constant here, a case in detectFormat, and a body extractor below.
type docFormat int

const (
	formatHTML docFormat = iota // HTML / unknown — extracted via readability
	formatPDF                   // PDF — extracted via pdftotext
	// formatEPUB, formatDOCX, ... added here
)

// detectFormat returns the document format based on Content-Type and body magic bytes.
// Content-Type takes priority; magic bytes are the fallback for servers that lie or omit it.
func detectFormat(contentType string, body []byte) docFormat {
	ct := strings.ToLower(contentType)
	switch {
	case strings.Contains(ct, "application/pdf"):
		return formatPDF
	case len(body) >= 5 && string(body[:5]) == "%PDF-":
		return formatPDF
	default:
		return formatHTML
	}
}

// detectFormatFile returns the document format for a local file path,
// using the file extension first, then magic bytes from the file header.
func detectFormatFile(path string) docFormat {
	if strings.EqualFold(filepath.Ext(path), ".pdf") {
		return formatPDF
	}
	// Magic byte sniff for files with non-standard extensions.
	f, err := os.Open(path)
	if err != nil {
		return formatHTML
	}
	defer f.Close()
	magic := make([]byte, 5)
	if n, _ := f.Read(magic); n == 5 && string(magic) == "%PDF-" {
		return formatPDF
	}
	return formatHTML
}

// ── Body extractors ───────────────────────────────────────────────────────────
// Each extractor receives the raw body bytes and returns a Result.
// Download stats (DownloadBytes, DownloadDuration, SourceBytes) are set by the caller.

// extractCanonicalURL parses the canonical URL from HTML body via <link rel="canonical">
// or <meta property="og:url">. Returns empty string if not found.
func extractCanonicalURL(body []byte) string {
	s := string(body)
	// <link rel="canonical" href="...">
	if i := strings.Index(s, `rel="canonical"`); i != -1 {
		chunk := s[max(0, i-200) : min(len(s), i+200)]
		if j := strings.Index(chunk, `href="`); j != -1 {
			rest := chunk[j+6:]
			if k := strings.Index(rest, `"`); k != -1 {
				return rest[:k]
			}
		}
		// also handle href before rel
		if j := strings.LastIndex(s[:i], `href="`); j != -1 && i-j < 300 {
			rest := s[j+6:]
			if k := strings.Index(rest, `"`); k != -1 {
				return rest[:k]
			}
		}
	}
	// <meta property="og:url" content="...">
	if i := strings.Index(s, `og:url`); i != -1 {
		chunk := s[i : min(len(s), i+200)]
		if j := strings.Index(chunk, `content="`); j != -1 {
			rest := chunk[j+9:]
			if k := strings.Index(rest, `"`); k != -1 {
				return rest[:k]
			}
		}
	}
	return ""
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// extractBodyHTML parses HTML via readability and returns plain text.
func extractBodyHTML(rawURL string, parsed *url.URL, body []byte) (Result, error) {
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

// extractBodyJina parses Jina reader markdown output into plain text.
func extractBodyJina(body []byte) Result {
	text := cleanJinaMarkdown(string(body))
	return Result{
		Text:  text,
		Title: extractJinaTitle(string(body)),
		HTML:  string(body),
	}
}

// extractBodyPDF writes body to a temp file and extracts text via pdftotext.
func extractBodyPDF(ctx context.Context, rawURL string, body []byte) (Result, error) {
	if _, err := exec.LookPath("pdftotext"); err != nil {
		return Result{}, fmt.Errorf(
			"pdftotext not found — install with: brew install poppler\n" +
				"(source: %s)", rawURL)
	}
	tmp, err := os.CreateTemp("", "arc-pdf-*.pdf")
	if err != nil {
		return Result{}, fmt.Errorf("create temp pdf: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return Result{}, fmt.Errorf("write temp pdf: %w", err)
	}
	tmp.Close()

	slog.Debug("extracting pdf body", "source", rawURL, "tmp", tmpPath, "size", len(body))
	return runPdftotext(ctx, tmpPath)
}

// ── Public API ────────────────────────────────────────────────────────────────

// FromURL fetches a URL and extracts the main article text.
func FromURL(ctx context.Context, rawURL string) (Result, error) {
	return fromURL(ctx, rawURL, nil)
}

// FromURLWithCookies fetches a URL using the provided cookie jar map.
func FromURLWithCookies(ctx context.Context, rawURL string, cookieJars map[string]string) (Result, error) {
	return fromURL(ctx, rawURL, cookieJars)
}

// FromFile reads and extracts text from a local file or stdin ("-").
// Format is detected automatically: PDF by extension and magic bytes, everything else as plain text.
func FromFile(ctx context.Context, path string) (Result, error) {
	if path == "-" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return Result{}, fmt.Errorf("read stdin: %w", err)
		}
		text := strings.TrimSpace(string(data))
		return Result{Text: text, SourceBytes: len(data), ExtractedBytes: len(text)}, nil
	}

	switch detectFormatFile(path) {
	case formatPDF:
		return FromPDF(ctx, path)
	default:
		data, err := os.ReadFile(path)
		if err != nil {
			return Result{}, fmt.Errorf("read file %s: %w", path, err)
		}
		text := strings.TrimSpace(string(data))
		return Result{Text: text, SourceBytes: len(data), ExtractedBytes: len(text)}, nil
	}
}

// CanonicalURL extracts the canonical URL from an HTML Result's body.
// Returns empty string if not found or if the canonical equals the original URL.
func CanonicalURL(result Result, originalURL string) string {
	if result.HTML == "" {
		return ""
	}
	canonical := extractCanonicalURL([]byte(result.HTML))
	if canonical == "" || canonical == originalURL {
		return ""
	}
	// Must be a valid absolute URL.
	u, err := url.Parse(canonical)
	if err != nil || !u.IsAbs() {
		return ""
	}
	return canonical
}

// FromURLViaJina fetches a URL directly through the Jina reader proxy, bypassing
// the normal direct-fetch + fallback chain. Use this when a direct fetch succeeded
// but returned a teaser/paywall (too few words) — Jina sometimes has full-text cached.
func FromURLViaJina(ctx context.Context, rawURL string) (Result, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	slog.Debug("teaser jina retry", "url", rawURL)
	resp, _, err := fetchViaJina(ctx, client, rawURL)
	if err != nil {
		return Result{}, fmt.Errorf("jina fetch: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Result{}, fmt.Errorf("read jina response: %w", err)
	}
	result := extractBodyJina(body)
	result.SourceBytes = len(body)
	result.ExtractedBytes = len(result.Text)
	slog.Debug("teaser jina result", "url", rawURL, "words", len(strings.Fields(result.Text)))
	return result, nil
}

// FromPDF extracts text from a local PDF file via pdftotext.
func FromPDF(ctx context.Context, path string) (Result, error) {
	if _, err := exec.LookPath("pdftotext"); err != nil {
		return Result{}, fmt.Errorf(
			"pdftotext not found — install with: brew install poppler\n" +
				"(or convert the PDF to text manually and use: arc ingest file.txt)")
	}
	return runPdftotext(ctx, path)
}

// ── HTTP fetch ────────────────────────────────────────────────────────────────

// userAgent mimics a real Chrome browser to reduce Cloudflare/CDN bot-scoring.
const userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36"

func fromURL(ctx context.Context, rawURL string, cookieJars map[string]string) (Result, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return Result{}, fmt.Errorf("parse url: %w", err)
	}

	body, contentType, downloadDuration, fromJina, err := httpFetch(ctx, rawURL, cookieJars)
	if err != nil {
		return Result{}, err
	}

	var result Result

	// Body came from Jina on a 403 — parse as Jina markdown directly, no further retry.
	if fromJina {
		slog.Debug("body from jina fallback, extracting as markdown", "url", rawURL)
		result = extractBodyJina(body)
		if kind := classifyPage(result.Text); kind != pageKindNormal {
			logShortContent("jina fallback short content", rawURL, result.Text)
			slog.Error("jina fallback returned bot-check or paywall", "url", rawURL, "kind", kind, "words", len(strings.Fields(result.Text)))
			switch kind {
			case pageKindBotCheck:
				return Result{}, fmt.Errorf("fetch %s: site requires JavaScript or bot verification — try downloading the page manually", rawURL)
			case pageKindPaywall:
				return Result{}, fmt.Errorf("fetch %s: paywalled content — refresh your cookie jar or download the page manually", rawURL)
			}
		}
		result.DownloadBytes = len(body)
		result.DownloadDuration = downloadDuration
		result.SourceBytes = len(body)
		result.ExtractedBytes = len(result.Text)
		return result, nil
	}

	// Detect format and dispatch to the appropriate body extractor.
	format := detectFormat(contentType, body)
	slog.Debug("format detected", "url", rawURL, "format", format, "content_type", contentType)

	switch format {
	case formatPDF:
		result, err = extractBodyPDF(ctx, rawURL, body)
		if err != nil {
			return Result{}, err
		}
		result.DownloadBytes = len(body)
		result.DownloadDuration = downloadDuration
		result.SourceBytes = len(body)
		return result, nil

	default: // formatHTML
		result, err = extractBodyHTML(rawURL, parsed, body)
		if err != nil {
			return Result{}, fmt.Errorf("extract html: %w", err)
		}
		// If the page looks like a bot-check or paywall, retry via Jina once.
		if kind := classifyPage(result.Text); kind != pageKindNormal {
			slog.Warn("suspicious page, retrying via Jina",
				"url", rawURL, "kind", kind, "words", len(strings.Fields(result.Text)))
			logShortContent("direct fetch short content", rawURL, result.Text)
			result, body, err = retryViaJina(ctx, rawURL)
			if err != nil {
				return Result{}, err
			}
		}
		result.DownloadBytes = len(body)
		result.DownloadDuration = downloadDuration
		result.SourceBytes = len(body)
		result.ExtractedBytes = len(result.Text)
		return result, nil
	}
}

// httpFetch performs the HTTP request with cookie injection, redirect logging,
// and automatic Jina fallback on 403. Returns the response body, content-type, timing,
// and viaJina=true when the body came from the Jina proxy (markdown, not HTML).
//
// Uses Chrome TLS + HTTP/2 fingerprint impersonation (via imroc/req) to reduce
// Cloudflare bot-scoring. Falls back to Jina on 403.
func httpFetch(ctx context.Context, rawURL string, cookieJars map[string]string) (body []byte, contentType string, duration time.Duration, viaJina bool, err error) {
	cookies := cookiesForURL(rawURL, cookieJars)
	hasCookies := len(cookies) > 0

	// Build a req client that impersonates Chrome TLS + HTTP/2 fingerprints.
	// This defeats JA3/JA4 and HTTP/2 SETTINGS fingerprinting at the CDN layer.
	//
	// OnBeforeRequest fires before every request including redirects. We use it to
	// inject cookies for the current request URL — this handles cross-domain redirects
	// (e.g. medium.com/publication/... → levelup.gitconnected.com/...) where the
	// initial and final domains have separate cookie jars.
	var redirectCount int
	client := req.C().
		ImpersonateChrome().
		SetTimeout(30 * time.Second).
		SetRedirectPolicy(req.MaxRedirectPolicy(10)).
		OnBeforeRequest(func(c *req.Client, r *req.Request) error {
			if redirectCount > 0 && r.RawRequest != nil {
				currentURL := r.RawRequest.URL.String()
				slog.Debug("http redirect", "from", rawURL, "to", currentURL, "hop", redirectCount)
				// Inject cookies for the redirect target domain.
				for _, ck := range cookiesForURL(currentURL, cookieJars) {
					r.RawRequest.AddCookie(ck)
				}
			}
			redirectCount++
			return nil
		})

	r := client.R().SetContext(ctx)
	for _, c := range cookies {
		r.SetCookies(c)
	}

	slog.Debug("http fetch start", "url", rawURL, "has_cookies", hasCookies, "cookie_count", len(cookies))
	start := time.Now()
	resp, err := r.Get(rawURL)
	if err != nil {
		slog.Error("fetch failed", "url", rawURL, "elapsed", time.Since(start).Round(time.Millisecond), "err", err)
		return nil, "", 0, false, fmt.Errorf("fetch %s: %w", rawURL, err)
	}
	slog.Debug("http fetch response",
		"url", rawURL, "status", resp.StatusCode,
		"content_type", resp.GetContentType(),
		"server", resp.Header.Get("Server"),
		"cf_ray", resp.Header.Get("CF-Ray"),
		"x_cache", resp.Header.Get("X-Cache"),
		"redirects", redirectCount-1,
		"elapsed", time.Since(start).Round(time.Millisecond),
	)

	// On 403, retry via Jina — bypasses Cloudflare JS challenges and some paywalls.
	fromJina := false
	if resp.StatusCode == http.StatusForbidden {
		slog.Warn("HTTP 403, retrying via Jina",
			"url", rawURL, "has_cookies", hasCookies,
			"server", resp.Header.Get("Server"), "cf_ray", resp.Header.Get("CF-Ray"),
		)
		jinaClient := &http.Client{Timeout: 30 * time.Second}
		jinaResp, _, err := fetchViaJina(ctx, jinaClient, rawURL)
		if err != nil {
			slog.Error("jina fallback failed", "url", rawURL, "err", err)
			return nil, "", 0, false, err
		}
		defer jinaResp.Body.Close()
		slog.Info("jina fallback succeeded", "url", rawURL, "status", jinaResp.StatusCode)
		body, err = io.ReadAll(jinaResp.Body)
		if err != nil {
			return nil, "", 0, false, fmt.Errorf("read jina response: %w", err)
		}
		return body, jinaResp.Header.Get("Content-Type"), time.Since(start), true, nil
	}

	if resp.StatusCode >= 400 {
		slog.Error("HTTP error",
			"url", rawURL, "status", resp.StatusCode,
			"has_cookies", hasCookies, "server", resp.Header.Get("Server"),
			"content_type", resp.GetContentType(), "cf_ray", resp.Header.Get("CF-Ray"),
		)
		return nil, "", 0, false, fmt.Errorf("fetch %s: HTTP %d", rawURL, resp.StatusCode)
	}

	body, err = resp.ToBytes()
	if err != nil {
		return nil, "", 0, false, fmt.Errorf("read response: %w", err)
	}
	return body, resp.GetContentType(), time.Since(start), fromJina, nil
}

// retryViaJina fetches rawURL via the Jina reader proxy and extracts text.
// Returns the Result, the raw body (for stats), and any error.
func retryViaJina(ctx context.Context, rawURL string) (Result, []byte, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, _, err := fetchViaJina(ctx, client, rawURL)
	if err != nil {
		return Result{}, nil, fmt.Errorf("fetch %s: %w", rawURL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Result{}, nil, fmt.Errorf("read jina response: %w", err)
	}
	result := extractBodyJina(body)

	switch kind := classifyPage(result.Text); kind {
	case pageKindBotCheck:
		logShortContent("jina bot-check content", rawURL, result.Text)
		slog.Error("bot-check page persists after Jina", "url", rawURL, "words", len(strings.Fields(result.Text)))
		return Result{}, nil, fmt.Errorf("fetch %s: site requires JavaScript or bot verification — try downloading the page manually", rawURL)
	case pageKindPaywall:
		logShortContent("jina paywall content", rawURL, result.Text)
		slog.Error("paywall persists after Jina", "url", rawURL, "words", len(strings.Fields(result.Text)))
		return Result{}, nil, fmt.Errorf("fetch %s: paywalled content — refresh your cookie jar or download the page manually", rawURL)
	}
	return result, body, nil
}

// fetchViaJina fetches a URL through the Jina reader proxy.
func fetchViaJina(ctx context.Context, client *http.Client, rawURL string) (*http.Response, bool, error) {
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
		"url", rawURL, "status", resp.StatusCode,
		"content_type", resp.Header.Get("Content-Type"),
		"elapsed", time.Since(start).Round(time.Millisecond),
	)
	return resp, true, nil
}

// ── PDF extraction ────────────────────────────────────────────────────────────

// runPdftotext runs pdftotext on a local file and returns extracted text.
func runPdftotext(ctx context.Context, path string) (Result, error) {
	// No -layout: plain flowing text without column-padding spaces.
	// -layout inflates whitespace on multi-column PDFs, causing token over-counting.
	cmd := exec.CommandContext(ctx, "pdftotext", path, "-")
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
	text = collapseWhitespace(text)

	var sourceBytes int
	if info, err := os.Stat(path); err == nil {
		sourceBytes = int(info.Size())
	}
	return Result{Text: text, SourceBytes: sourceBytes, ExtractedBytes: len(text)}, nil
}

// ── Page classification ───────────────────────────────────────────────────────

// pageKind classifies a short or suspicious page response.
type pageKind int

const (
	pageKindNormal   pageKind = iota
	pageKindBotCheck          // Cloudflare / JS challenge
	pageKindPaywall           // membership / subscription gate
)

// classifyPage returns the kind of page based on content signals.
// Returns pageKindNormal for real article content or word count ≥ 200.
func classifyPage(text string) pageKind {
	lower := strings.ToLower(text)
	if len(strings.Fields(text)) >= 200 {
		return pageKindNormal
	}
	botTriggers := []string{
		"security verification", "verifying you are not a bot",
		"please enable javascript", "checking your browser",
		"ddos protection", "just a moment",
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
		"become a member", "member-only", "members only",
		"upgrade your membership", "start your membership",
		"get unlimited access", "unlock this story",
		"read the full story", "this story is only",
		"subscribe to read", "subscription required", "sign in to read",
	}
	for _, t := range paywallTriggers {
		if strings.Contains(lower, t) {
			return pageKindPaywall
		}
	}
	return pageKindNormal
}

// ── Cookie handling ───────────────────────────────────────────────────────────

// cookiesForURL returns cookies from the jar map that match the URL's host.
func cookiesForURL(rawURL string, jars map[string]string) []*http.Cookie {
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
	if len(jars) > 0 {
		slog.Warn("no cookie jar configured for host", "host", host)
	}
	return nil
}

// parseNetscapeCookies reads a Netscape/curl cookie jar file.
// Format: domain \t flag \t path \t secure \t expiry \t name \t value
// Expired cookies are skipped and logged as WARN.
func parseNetscapeCookies(path, host string) []*http.Cookie {
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
			continue
		default:
			nValid++
			slog.Debug("cookie loaded (valid)", "host", host, "name", name,
				"expires_at", time.Unix(expiry, 0).Format(time.RFC3339),
				"expires_in", time.Until(time.Unix(expiry, 0)).Round(time.Minute).String(),
			)
		}
		cookies = append(cookies, &http.Cookie{Name: name, Value: fields[6]})
	}
	slog.Info("cookie jar loaded", "host", host, "path", path,
		"total", nValid+nSession+nExpired, "valid", nValid, "session", nSession,
		"expired", nExpired, "sending", len(cookies),
	)
	return cookies
}

// ── Jina markdown cleaning ────────────────────────────────────────────────────

func extractJinaTitle(md string) string {
	for _, line := range strings.SplitN(md, "\n", 20) {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimPrefix(line, "# ")
		}
	}
	return ""
}

func cleanJinaMarkdown(md string) string {
	lines := strings.Split(md, "\n")
	var out []string
	skipPatterns := []string{
		"sign in", "sign up", "subscribe", "member-only",
		"open in app", "get the app", "follow me on", "follow us on",
		"clap for this story", "responses",
	}
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "![") {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[http") || strings.HasPrefix(trimmed, "http") {
			continue
		}
		lower := strings.ToLower(trimmed)
		skip := false
		for _, p := range skipPatterns {
			if strings.Contains(lower, p) && len(strings.Fields(trimmed)) < 8 {
				skip = true
				break
			}
		}
		if !skip {
			out = append(out, line)
		}
	}
	result := strings.Join(out, "\n")
	for strings.Contains(result, "\n\n\n\n") {
		result = strings.ReplaceAll(result, "\n\n\n\n", "\n\n\n")
	}
	return strings.TrimSpace(result)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// collapseWhitespace normalizes runs of spaces and blank lines in PDF-extracted text.
func collapseWhitespace(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		for strings.Contains(line, "  ") {
			line = strings.ReplaceAll(line, "  ", " ")
		}
		lines[i] = strings.TrimRight(line, " \t")
	}
	result := strings.Join(lines, "\n")
	for strings.Contains(result, "\n\n\n\n") {
		result = strings.ReplaceAll(result, "\n\n\n\n", "\n\n\n")
	}
	return strings.TrimSpace(result)
}

// logShortContent logs up to 500 chars of text at DEBUG when word count is low.
func logShortContent(label, rawURL, text string) {
	if len(strings.Fields(text)) >= 300 {
		return
	}
	preview := text
	if len(preview) > 500 {
		preview = preview[:500] + "…"
	}
	slog.Debug(label, "url", rawURL, "words", len(strings.Fields(text)), "preview", preview)
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

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jrniemiec/arc/chat"
	"github.com/jrniemiec/arc/chat/corpusmap"
	"github.com/jrniemiec/arc/config"
	"github.com/jrniemiec/arc/store"
	"github.com/jrniemiec/arc/store/fs"
	"github.com/jrniemiec/arc/store/sqlite"
)

const bodyTruncateTokens = 8000
const searchLimit = 10

// ExecTool runs a single tool call and returns the result text.
func ExecTool(ctx context.Context, cfg config.Config, workspaceName string, call chat.ToolCall) (string, error) {
	switch call.Name {
	case "read_article":
		return execReadArticle(cfg, call)
	case "search_workspace":
		return execSearchWorkspace(ctx, cfg, workspaceName, call)
	case "search_library":
		return execSearchLibrary(ctx, cfg, call)
	default:
		return fmt.Sprintf("Unknown tool: %s", call.Name), nil
	}
}

// --- read_article ---

type readArticleArgs struct {
	Slug  string `json:"slug"`
	Level string `json:"level"`
}

func execReadArticle(cfg config.Config, call chat.ToolCall) (string, error) {
	var args readArticleArgs
	if err := json.Unmarshal(call.Input, &args); err != nil {
		return "", fmt.Errorf("read_article: invalid args: %w", err)
	}
	if args.Slug == "" {
		return "Error: slug is required.", nil
	}
	if args.Level == "" {
		args.Level = "summary"
	}

	articleDir := filepath.Join(cfg.ArticlesRoot, args.Slug)

	// Check that the article exists.
	if _, err := os.Stat(articleDir); os.IsNotExist(err) {
		return fmt.Sprintf("Article [%s] not found.", args.Slug), nil
	}

	switch args.Level {
	case "summary":
		return readSummary(cfg, articleDir, args.Slug)
	case "body":
		return readBody(articleDir)
	default:
		return fmt.Sprintf("Unknown level %q — use \"summary\" or \"body\".", args.Level), nil
	}
}

func readSummary(cfg config.Config, articleDir, slug string) (string, error) {
	path := fs.ResolveSummary(articleDir, cfg.PreferredStyles, cfg.PreferredModels)
	if path == "" {
		return fmt.Sprintf("No summary available for article [%s].", slug), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("Error reading summary for [%s]: %v", slug, err), nil
	}
	content := strings.TrimSpace(string(data))

	// Append body size hint so the model can decide whether to escalate.
	bodyPath := filepath.Join(articleDir, "body.txt")
	if bodyData, err := os.ReadFile(bodyPath); err == nil {
		bodyTokens := approxTokens(string(bodyData))
		content += fmt.Sprintf("\n\n(body available: ~%d tokens)", bodyTokens)
	}
	return content, nil
}

func readBody(articleDir string) (string, error) {
	bodyPath := filepath.Join(articleDir, "body.txt")
	data, err := os.ReadFile(bodyPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "No body text available for this article.", nil
		}
		return "", fmt.Errorf("read body: %w", err)
	}
	content := strings.TrimSpace(string(data))
	if approxTokens(content) > bodyTruncateTokens {
		content = truncateToTokens(content, bodyTruncateTokens)
		content += "\n\n[truncated — body exceeds 8k tokens]"
	}
	return content, nil
}

// --- search_workspace ---

type searchArgs struct {
	Query string `json:"query"`
}

func execSearchWorkspace(ctx context.Context, cfg config.Config, workspaceName string, call chat.ToolCall) (string, error) {
	var args searchArgs
	if err := json.Unmarshal(call.Input, &args); err != nil {
		return "", fmt.Errorf("search_workspace: invalid args: %w", err)
	}
	if args.Query == "" {
		return "Error: query is required.", nil
	}

	// Gather workspace article slugs for filtering.
	slugs, err := corpusmap.GatherWorkspaceSlugs(cfg.DataRoot, workspaceName)
	if err != nil {
		return fmt.Sprintf("Error gathering workspace articles: %v", err), nil
	}
	if len(slugs) == 0 {
		return "No articles in this workspace to search.", nil
	}

	db, err := sqlite.Open(ctx, cfg.DBPath)
	if err != nil {
		return fmt.Sprintf("Error opening database: %v", err), nil
	}
	defer db.Close()

	results, err := db.Search(ctx, store.Query{
		Text: args.Query,
		Mode: store.QueryKeyword,
		TopK: searchLimit,
		Filter: store.Filter{
			Slugs: slugs,
		},
	})
	if err != nil {
		return fmt.Sprintf("Search error: %v", err), nil
	}

	return formatSearchResults(results, len(results)), nil
}

// --- search_library ---

func execSearchLibrary(ctx context.Context, cfg config.Config, call chat.ToolCall) (string, error) {
	var args searchArgs
	if err := json.Unmarshal(call.Input, &args); err != nil {
		return "", fmt.Errorf("search_library: invalid args: %w", err)
	}
	if args.Query == "" {
		return "Error: query is required.", nil
	}

	db, err := sqlite.Open(ctx, cfg.DBPath)
	if err != nil {
		return fmt.Sprintf("Error opening database: %v", err), nil
	}
	defer db.Close()

	results, err := db.Search(ctx, store.Query{
		Text: args.Query,
		Mode: store.QueryKeyword,
		TopK: searchLimit,
	})
	if err != nil {
		return fmt.Sprintf("Search error: %v", err), nil
	}

	// For library search, add flash-or-title for each hit since they're not
	// in the corpus map.
	enriched := make([]enrichedResult, len(results))
	for i, r := range results {
		enriched[i].result = r
		dir := filepath.Join(cfg.ArticlesRoot, r.Article.ID)
		flashPath := fs.ResolveFlash(dir, cfg.PreferredModels)
		if flashPath != "" {
			if data, err := os.ReadFile(flashPath); err == nil {
				enriched[i].flash = strings.TrimSpace(string(data))
			}
		}
	}

	return formatLibraryResults(enriched), nil
}

func formatSearchResults(results []store.Result, totalCount int) string {
	if len(results) == 0 {
		return "No matches found."
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%d matches, showing %d:\n", totalCount, len(results)))
	for _, r := range results {
		sb.WriteString(fmt.Sprintf("\n[%s] %s\n", r.Article.ID, r.Article.Title))
		if r.Excerpt != "" {
			sb.WriteString(r.Excerpt)
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

type enrichedResult struct {
	result store.Result
	flash  string
}

func formatLibraryResults(results []enrichedResult) string {
	if len(results) == 0 {
		return "No matches found."
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%d matches, showing %d:\n", len(results), len(results)))
	for _, r := range results {
		sb.WriteString(fmt.Sprintf("\n[%s] %s\n", r.result.Article.ID, r.result.Article.Title))
		if r.result.Excerpt != "" {
			sb.WriteString(r.result.Excerpt)
			sb.WriteString("\n")
		}
		if r.flash != "" {
			sb.WriteString(r.flash)
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// --- helpers ---

func approxTokens(s string) int {
	return (len(s) + 3) / 4
}

func truncateToTokens(s string, maxTokens int) string {
	maxBytes := maxTokens * 4
	if len(s) <= maxBytes {
		return s
	}
	// Cut at a UTF-8 safe boundary by truncating to the last valid rune.
	truncated := s[:maxBytes]
	// Find the last newline within the last 200 bytes to get a clean break.
	if idx := strings.LastIndex(truncated[max(0, len(truncated)-200):], "\n"); idx >= 0 {
		truncated = truncated[:max(0, len(truncated)-200)+idx]
	}
	return truncated
}

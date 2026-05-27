// Package mcp implements the arc MCP server.
// It exposes arc's knowledge base as tools Claude can call mid-conversation.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/jrniemiec/arc/service"
	"github.com/jrniemiec/arc/store"
)


// New creates a configured MCPServer with all arc tools registered.
func New(svc *service.Service) *server.MCPServer {
	s := server.NewMCPServer(
		"arc",
		"1.0.0",
		server.WithToolCapabilities(false),
	)

	s.AddTool(toolSearch(), handleSearch(svc))
	s.AddTool(toolRead(), handleRead(svc))
	s.AddTool(toolList(), handleList(svc))
	s.AddTool(toolGetStats(), handleGetStats(svc))

	return s
}

// ── Tool definitions ──────────────────────────────────────────────────────────

func toolSearch() mcpgo.Tool {
	return mcpgo.NewTool("search",
		mcpgo.WithDescription("Search the arc knowledge base using full-text, semantic (vector), or hybrid search. Returns matching articles with excerpts."),
		mcpgo.WithString("query",
			mcpgo.Required(),
			mcpgo.Description("Search query string"),
		),
		mcpgo.WithString("mode",
			mcpgo.Description("Search mode: fts (full-text), vector (semantic), or hybrid (default: hybrid)"),
			mcpgo.Enum("fts", "vector", "hybrid"),
		),
		mcpgo.WithNumber("limit",
			mcpgo.Description("Maximum number of results to return (default: 5)"),
		),
	)
}

func toolRead() mcpgo.Tool {
	return mcpgo.NewTool("read",
		mcpgo.WithDescription("Read the content of a specific article from the arc knowledge base. Use the slug from search results."),
		mcpgo.WithString("slug",
			mcpgo.Required(),
			mcpgo.Description("Article slug (from search or list results). Fuzzy matching is supported."),
		),
		mcpgo.WithString("part",
			mcpgo.Description("Which part to read: summary (default), flash (short audio-style summary), or flashcards (study cards as JSON)"),
			mcpgo.Enum("summary", "flash", "flashcards"),
		),
	)
}

func toolList() mcpgo.Tool {
	return mcpgo.NewTool("list",
		mcpgo.WithDescription("List articles in the arc knowledge base, most recent first."),
		mcpgo.WithNumber("limit",
			mcpgo.Description("Maximum number of articles to return (default: 20)"),
		),
		mcpgo.WithString("tag",
			mcpgo.Description("Filter by tag (e.g. 'teaser', 'ai', 'go')"),
		),
		mcpgo.WithString("collection",
			mcpgo.Description("Filter by collection ID"),
		),
	)
}

func toolGetStats() mcpgo.Tool {
	return mcpgo.NewTool("get_stats",
		mcpgo.WithDescription("Get statistics about the arc knowledge base: article count, embed coverage, cost breakdown."),
	)
}

// ── Handlers ─────────────────────────────────────────────────────────────────

func handleSearch(svc *service.Service) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		query, err := req.RequireString("query")
		if err != nil {
			return mcpgo.NewToolResultError("missing required parameter: query"), nil
		}

		modeStr := req.GetString("mode", "hybrid")
		limit := int(req.GetFloat("limit", 5))
		if limit <= 0 {
			limit = 5
		}

		var mode store.QueryMode
		switch modeStr {
		case "fts":
			mode = store.QueryKeyword
		case "vector":
			mode = store.QuerySemantic
		default:
			mode = store.QueryCombined
		}

		slog.Debug("mcp search", "query", query, "mode", modeStr, "limit", limit)

		results, err := svc.Search(ctx, service.SearchRequest{
			Query: query,
			Mode:  mode,
			Limit: limit,
		})
		if err != nil {
			slog.Error("mcp search failed", "err", err)
			return mcpgo.NewToolResultErrorFromErr("search failed", err), nil
		}

		if len(results) == 0 {
			return mcpgo.NewToolResultText("No results found."), nil
		}

		var sb strings.Builder
		for i, r := range results {
			fmt.Fprintf(&sb, "%d. **%s**\n", i+1, r.Article.Title)
			fmt.Fprintf(&sb, "   slug: %s\n", r.Article.ID)
			if r.Excerpt != "" {
				fmt.Fprintf(&sb, "   %s\n", r.Excerpt)
			}
			fmt.Fprintf(&sb, "   source: %s  score: %.3f\n\n", r.Source, r.Score)
		}

		slog.Debug("mcp search done", "hits", len(results))
		return mcpgo.NewToolResultText(sb.String()), nil
	}
}

func handleRead(svc *service.Service) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		slug, err := req.RequireString("slug")
		if err != nil {
			return mcpgo.NewToolResultError("missing required parameter: slug"), nil
		}
		partStr := req.GetString("part", "summary")

		var part service.Part
		switch partStr {
		case "flash":
			part = service.PartFlash
		case "flashcards":
			part = service.PartFlashcards
		default:
			part = service.PartSummary
		}

		slog.Debug("mcp read", "slug", slug, "part", partStr)

		text, err := svc.Read(ctx, service.ReadRequest{ID: slug, Part: part})
		if err != nil {
			slog.Error("mcp read failed", "slug", slug, "err", err)
			return mcpgo.NewToolResultErrorFromErr(fmt.Sprintf("could not read %q", slug), err), nil
		}

		return mcpgo.NewToolResultText(text), nil
	}
}

func handleList(svc *service.Service) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		limit := int(req.GetFloat("limit", 20))
		if limit <= 0 {
			limit = 20
		}
		tag := req.GetString("tag", "")
		collection := req.GetString("collection", "")

		slog.Debug("mcp list", "limit", limit, "tag", tag, "collection", collection)

		var tags []string
		if tag != "" {
			tags = []string{tag}
		}

		articles, err := svc.List(ctx, store.Filter{
			Limit:      limit,
			Tags:       tags,
			Collection: collection,
		})
		if err != nil {
			slog.Error("mcp list failed", "err", err)
			return mcpgo.NewToolResultErrorFromErr("list failed", err), nil
		}

		if len(articles) == 0 {
			return mcpgo.NewToolResultText("No articles found."), nil
		}

		var sb strings.Builder
		for _, a := range articles {
			fmt.Fprintf(&sb, "- **%s**\n", a.Title)
			fmt.Fprintf(&sb, "  slug: %s  date: %s\n", a.ID, a.IngestedAt.Format("2006-01-02"))
			if len(a.Tags) > 0 {
				tagNames := make([]string, len(a.Tags))
				for i, t := range a.Tags {
					tagNames[i] = t.Value
				}
				fmt.Fprintf(&sb, "  tags: %s\n", strings.Join(tagNames, ", "))
			}
		}

		slog.Debug("mcp list done", "count", len(articles))
		return mcpgo.NewToolResultText(sb.String()), nil
	}
}

func handleGetStats(svc *service.Service) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		slog.Debug("mcp get_stats")

		stats, err := svc.Stats(ctx)
		if err != nil {
			slog.Error("mcp get_stats failed", "err", err)
			return mcpgo.NewToolResultErrorFromErr("stats failed", err), nil
		}

		b, _ := json.MarshalIndent(stats, "", "  ")
		return mcpgo.NewToolResultText(string(b)), nil
	}
}

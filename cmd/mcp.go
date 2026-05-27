package cmd

import (
	"fmt"
	"log/slog"

	"github.com/spf13/cobra"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jrniemiec/arc/mcp"
)

var mcpHTTPAddr string

func init() {
	mcpCmd.Flags().StringVar(&mcpHTTPAddr, "http", "", "serve over HTTP+SSE on this address (e.g. :8080); default is stdio")
	rootCmd.AddCommand(mcpCmd)
}

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Start the arc MCP server",
	Long: `Start an MCP (Model Context Protocol) server that exposes arc's knowledge
base as tools Claude can call mid-conversation.

Stdio mode (default) — for Claude Desktop / Claude Code:
  arc mcp

HTTP+SSE mode — persistent daemon, supports multiple clients:
  arc mcp --http :8080

Tools exposed:
  search     Full-text, semantic, or hybrid search with excerpts
  read       Read summary, flash, or flashcards for an article
  list       List recent articles with metadata
  get_stats  Knowledge base statistics and cost breakdown

Claude Desktop / Claude Code configuration (~/.claude.json or equivalent):
  {
    "mcpServers": {
      "arc": { "command": "arc", "args": ["mcp"] }
    }
  }`,
	RunE: func(cmd *cobra.Command, args []string) error {
		svc := svcFrom(cmd)
		s := mcp.New(svc)

		if mcpHTTPAddr != "" {
			slog.Info("mcp server starting", "transport", "http+sse", "addr", mcpHTTPAddr)
			sse := mcpserver.NewSSEServer(s, mcpserver.WithBaseURL("http://"+mcpHTTPAddr))
			if err := sse.Start(mcpHTTPAddr); err != nil {
				return fmt.Errorf("mcp http server: %w", err)
			}
			return nil
		}

		slog.Info("mcp server starting", "transport", "stdio")
		if err := mcpserver.ServeStdio(s); err != nil {
			return fmt.Errorf("mcp stdio server: %w", err)
		}
		return nil
	},
}

// Package tools defines the tool definitions and execution for workspace chat.
// Tools are thin wrappers over existing arc functionality (filesystem reads,
// FTS search). They are filtered by grounding mode before each API request.
package tools

import (
	"encoding/json"

	"github.com/jrniemiec/arc/chat"
	"github.com/jrniemiec/arc/chat/prompt"
)

// mustJSON marshals v and returns the raw JSON. Panics on error (schemas are
// compile-time constants, so marshaling cannot fail).
func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

var readArticleDef = chat.ToolDef{
	Name:        "read_article",
	Description: `Read an article from this workspace. Returns the summary by default — request level "body" only when you need exact figures, code, or precise wording that the summary lacks.`,
	InputSchema: mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"slug": map[string]any{
				"type":        "string",
				"description": "Article ID as shown in [brackets] in the article list",
			},
			"level": map[string]any{
				"type":        "string",
				"enum":        []string{"summary", "body"},
				"description": `"summary" (default) returns the article summary plus body size. "body" returns the full text, truncated at ~8k tokens if larger.`,
			},
		},
		"required": []string{"slug"},
	}),
}

var searchWorkspaceDef = chat.ToolDef{
	Name:        "search_workspace",
	Description: "Full-text search over the bodies of articles in this workspace. Use when looking for a specific passage, term, or detail and you are not sure which article contains it. Returns up to 10 hits with text snippets.",
	InputSchema: mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Search query (FTS5 syntax: plain words, quoted phrases, OR/AND/NOT)",
			},
		},
		"required": []string{"query"},
	}),
}

var searchLibraryDef = chat.ToolDef{
	Name:        "search_library",
	Description: "Full-text search over the user's entire article library (not just this workspace). Use when the workspace does not cover a topic but the user may have saved relevant material elsewhere. Each hit includes a flash summary or title.",
	InputSchema: mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Search query (FTS5 syntax: plain words, quoted phrases, OR/AND/NOT)",
			},
		},
		"required": []string{"query"},
	}),
}

// ToolSet returns tool definitions filtered by grounding mode.
// web_search is not a custom tool — it uses Anthropic's native server-side
// web search, enabled via a feature flag on the request.
func ToolSet(mode string) []chat.ToolDef {
	tools := []chat.ToolDef{readArticleDef, searchWorkspaceDef}
	if mode == prompt.ModeCorpusFirst || mode == prompt.ModeOpen {
		tools = append(tools, searchLibraryDef)
	}
	return tools
}

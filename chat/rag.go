package chat

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jrniemiec/arc/config"
	"github.com/jrniemiec/arc/store/fs"
)

const (
	ragSummaryThreshold = 10 // ≤10 articles → use full summaries
	ragFlashThreshold   = 30 // 11-30 → flashes; 31+ → ranked + capped
	ragTopK             = 25 // max articles in the prefix for large workspaces
)

// RAG mode constants.
const (
	RAGModeOpen   = "open"
	RAGModeStrict = "strict"
	RAGModeHybrid = "hybrid"
)

// RAGModeEntry describes a single RAG mode for listing.
type RAGModeEntry struct {
	Name        string
	Instruction string // empty for "open" (no instruction injected)
}

// ragModeInstructions maps mode names to their default instruction text.
// "open" has no instruction — the model freely blends corpus and own knowledge.
var ragModeInstructions = map[string]string{
	RAGModeOpen:   "",
	RAGModeStrict: "Answer ONLY based on the articles below. If the answer is not covered, say so.",
	RAGModeHybrid: "Prioritize the articles below. When supplementing with outside knowledge, state that explicitly.",
}

// ListRAGModes returns all available RAG modes with their default instructions,
// in a stable display order.
func ListRAGModes() []RAGModeEntry {
	return []RAGModeEntry{
		{Name: RAGModeOpen, Instruction: ragModeInstructions[RAGModeOpen]},
		{Name: RAGModeStrict, Instruction: ragModeInstructions[RAGModeStrict]},
		{Name: RAGModeHybrid, Instruction: ragModeInstructions[RAGModeHybrid]},
	}
}

// RAGModeInstruction returns the effective instruction text for the given mode
// and optional override. If override is non-empty, it is used instead of the
// built-in instruction for the mode.
func RAGModeInstruction(mode, override string) string {
	if override != "" {
		return override
	}
	return ragModeInstructions[mode]
}

// ragArticle holds resolved content for one article in the RAG prefix.
type ragArticle struct {
	slug    string
	title   string
	content string
	mtime   int64 // article dir mtime (unix seconds) for recency ranking
}

// BuildRAGContext assembles article content from the workspace corpus into a
// plain text prefix suitable for prepending to the system prompt.
//
// The ragInstruction parameter is the effective instruction text (from
// RAGModeInstruction). If non-empty, it is inserted before the knowledge base
// block.
//
// Returns an empty string (no error) if the workspace has no articles or no
// readable content files.
func BuildRAGContext(cfg config.Config, workspaceName, ragInstruction string) (string, error) {
	slugs, err := gatherWorkspaceSlugs(cfg.DataRoot, workspaceName)
	if err != nil {
		return "", fmt.Errorf("rag: gather slugs: %w", err)
	}
	if len(slugs) == 0 {
		return "", nil
	}

	// Determine content depth tier.
	useSummary := len(slugs) <= ragSummaryThreshold

	// Resolve content file and metadata for each article.
	var articles []ragArticle
	for _, slug := range slugs {
		dir := filepath.Join(cfg.ArticlesRoot, slug)
		info, err := os.Stat(dir)
		if err != nil {
			continue // broken symlink or missing directory
		}

		// Resolve content file.
		var contentPath string
		if useSummary {
			contentPath = fs.ResolveSummary(dir, cfg.PreferredStyles, cfg.PreferredModels)
			if contentPath == "" {
				// Fallback to flash if no summary available.
				contentPath = fs.ResolveFlash(dir, cfg.PreferredModels)
			}
		} else {
			contentPath = fs.ResolveFlash(dir, cfg.PreferredModels)
		}
		if contentPath == "" {
			continue // no content file available
		}

		data, err := os.ReadFile(contentPath)
		if err != nil || len(data) == 0 {
			continue
		}

		// Read title from meta.json.
		title := slug
		metaPath := filepath.Join(dir, "meta.json")
		if meta, err := fs.ReadMeta(metaPath); err == nil && meta.Title != "" {
			title = meta.Title
		}

		articles = append(articles, ragArticle{
			slug:    slug,
			title:   title,
			content: strings.TrimSpace(string(data)),
			mtime:   info.ModTime().Unix(),
		})
	}

	if len(articles) == 0 {
		return "", nil
	}

	// For large workspaces, rank by recency and cap at top-K.
	if len(articles) > ragFlashThreshold {
		sort.Slice(articles, func(i, j int) bool {
			return articles[i].mtime > articles[j].mtime // newest first
		})
		if len(articles) > ragTopK {
			articles = articles[:ragTopK]
		}
	}

	// Assemble the prefix.
	var sb strings.Builder

	if ragInstruction != "" {
		sb.WriteString(ragInstruction)
		sb.WriteString("\n\n")
	}

	sb.WriteString("[KNOWLEDGE BASE]\n")
	for i, a := range articles {
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(fmt.Sprintf("\nArticle: %s (%s)\n", a.title, a.slug))
		sb.WriteString(a.content)
		sb.WriteString("\n")
	}
	sb.WriteString("\n[END KNOWLEDGE BASE]")
	return sb.String(), nil
}

// gatherWorkspaceSlugs collects all unique article slugs from the workspace:
// direct article symlinks + articles from linked collections.
func gatherWorkspaceSlugs(dataRoot, workspaceName string) ([]string, error) {
	seen := make(map[string]bool)

	// Direct workspace articles.
	direct, _, err := fs.ListWorkspaceArticles(dataRoot, workspaceName)
	if err != nil {
		return nil, err
	}
	for _, slug := range direct {
		seen[slug] = true
	}

	// Articles from workspace collections.
	cols, _ := fs.ListWorkspaceCollections(dataRoot, workspaceName)
	for _, col := range cols {
		colArticles, _, err := fs.ListCollectionArticles(dataRoot, col)
		if err != nil {
			continue // skip broken collections
		}
		for _, slug := range colArticles {
			seen[slug] = true
		}
	}

	slugs := make([]string, 0, len(seen))
	for slug := range seen {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs) // deterministic order before any ranking
	return slugs, nil
}

// Package corpusmap builds the compact article inventory that is included
// in every chat system prompt. The map gives the model awareness of what
// exists in the workspace; content comes through tools.
package corpusmap

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jrniemiec/arc/config"
	"github.com/jrniemiec/arc/store/fs"
)

const defaultBudget = 12000 // tokens

// Result holds the built corpus map text and its fingerprint.
type Result struct {
	Text        string // formatted map text for the system prompt
	Fingerprint string // hex hash for cache invalidation
	Articles    int    // number of articles in the map
}

// entry holds resolved data for one article.
type entry struct {
	slug       string
	title      string
	flash      string
	ingestedAt time.Time
	flashMtime int64
}

// fingerprintData is the structure hashed for cache invalidation.
type fingerprintData struct {
	Slugs       []string `json:"slugs"`
	Attic       []string `json:"attic"`
	FlashMtimes []int64  `json:"flash_mtimes"`
}

// ComputeFingerprint checks whether the workspace has changed since the
// last map build. It is cheap: readdir + stat only, no file content reads.
// Called every turn.
func ComputeFingerprint(cfg config.Config, workspaceName string) (string, error) {
	slugs, err := GatherWorkspaceSlugs(cfg.DataRoot, workspaceName)
	if err != nil {
		return "", err
	}
	sort.Strings(slugs)

	attic := gatherAtticSlugs(cfg.DataRoot, workspaceName)
	sort.Strings(attic)

	mtimes := make([]int64, len(slugs))
	for i, slug := range slugs {
		dir := filepath.Join(cfg.ArticlesRoot, slug)
		flashPath := fs.ResolveFlash(dir, cfg.PreferredModels)
		if flashPath != "" {
			if info, err := os.Stat(flashPath); err == nil {
				mtimes[i] = info.ModTime().Unix()
			}
		}
	}

	return hashFingerprint(fingerprintData{
		Slugs:       slugs,
		Attic:       attic,
		FlashMtimes: mtimes,
	}), nil
}

// Build assembles the corpus map text for a workspace. It reads all flash
// files and meta.json for each article — expensive, called once at engine
// init and again only when the fingerprint changes.
//
// budget is the token limit for the map; 0 uses the default (12k).
// The map always lists every article; when over budget, flashes are
// dropped (title-only entries) starting from the oldest articles.
func Build(cfg config.Config, workspaceName, description string, budget int) (Result, error) {
	if budget <= 0 {
		budget = defaultBudget
	}

	slugs, err := GatherWorkspaceSlugs(cfg.DataRoot, workspaceName)
	if err != nil {
		return Result{}, err
	}
	attic := gatherAtticSlugs(cfg.DataRoot, workspaceName)

	// Load metadata + flash for each article.
	entries := make([]entry, 0, len(slugs))
	for _, slug := range slugs {
		dir := filepath.Join(cfg.ArticlesRoot, slug)

		var e entry
		e.slug = slug

		// Title from meta.json, fall back to slug.
		metaPath := filepath.Join(dir, "meta.json")
		if meta, err := fs.ReadMeta(metaPath); err == nil && meta.Title != "" {
			e.title = meta.Title
			if meta.IngestedAt != "" {
				e.ingestedAt, _ = time.Parse(time.RFC3339, meta.IngestedAt)
			}
		} else {
			e.title = slug
		}

		// Flash content.
		flashPath := fs.ResolveFlash(dir, cfg.PreferredModels)
		if flashPath != "" {
			if data, err := os.ReadFile(flashPath); err == nil {
				e.flash = strings.TrimSpace(string(data))
			}
			if info, err := os.Stat(flashPath); err == nil {
				e.flashMtime = info.ModTime().Unix()
			}
		}

		entries = append(entries, e)
	}

	// Sort by ingest date, newest first.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].ingestedAt.After(entries[j].ingestedAt)
	})

	// Compute fingerprint from what we already have.
	sortedSlugs := make([]string, len(entries))
	mtimes := make([]int64, len(entries))
	for i, e := range entries {
		sortedSlugs[i] = e.slug
		mtimes[i] = e.flashMtime
	}
	sort.Strings(sortedSlugs)
	sortedAttic := make([]string, len(attic))
	copy(sortedAttic, attic)
	sort.Strings(sortedAttic)

	fingerprint := hashFingerprint(fingerprintData{
		Slugs:       sortedSlugs,
		Attic:       sortedAttic,
		FlashMtimes: mtimes,
	})

	// Build map text with budget.
	var sb strings.Builder
	header := fmt.Sprintf("Workspace: %s\nDescription: %s\n\nArticles (%d):\n", workspaceName, description, len(entries))
	sb.WriteString(header)
	tokensUsed := approxTokens(header)

	for i := range entries {
		e := &entries[i]
		line := fmt.Sprintf("\n[%s] \"%s\"", e.slug, e.title)
		lineTokens := approxTokens(line)

		if e.flash != "" {
			flashTokens := approxTokens(e.flash)
			if tokensUsed+lineTokens+flashTokens+1 <= budget {
				sb.WriteString(line)
				sb.WriteString("\n")
				sb.WriteString(e.flash)
				sb.WriteString("\n")
				tokensUsed += lineTokens + flashTokens + 1
				continue
			}
		}

		// Title-only fallback (always fits, ~10 tokens).
		sb.WriteString(line)
		sb.WriteString("\n")
		tokensUsed += lineTokens
	}

	mapText := sb.String()

	// Save to disk for inspection.
	chatDir := filepath.Join(cfg.DataRoot, "workspaces", workspaceName, "chat")
	if err := os.MkdirAll(chatDir, 0755); err == nil {
		_ = os.WriteFile(filepath.Join(chatDir, "corpus-map.txt"), []byte(mapText), 0644)
	}

	return Result{
		Text:        mapText,
		Fingerprint: fingerprint,
		Articles:    len(entries),
	}, nil
}

// GatherWorkspaceSlugs collects all unique article slugs from the workspace:
// direct article symlinks + articles from linked collections.
func GatherWorkspaceSlugs(dataRoot, workspaceName string) ([]string, error) {
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
			continue
		}
		for _, slug := range colArticles {
			seen[slug] = true
		}
	}

	slugs := make([]string, 0, len(seen))
	for slug := range seen {
		slugs = append(slugs, slug)
	}
	return slugs, nil
}

// gatherAtticSlugs returns article slugs in the workspace attic.
func gatherAtticSlugs(dataRoot, workspaceName string) []string {
	return fs.ListAtticArticles(dataRoot, workspaceName)
}

// hashFingerprint returns a hex-encoded sha256 of the JSON-serialized data.
func hashFingerprint(data fingerprintData) string {
	b, _ := json.Marshal(data)
	h := sha256.Sum256(b)
	return fmt.Sprintf("%x", h)
}

// approxTokens estimates token count as ceil(len(s)/4).
func approxTokens(s string) int {
	return (len(s) + 3) / 4
}

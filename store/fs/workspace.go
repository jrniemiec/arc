package fs

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jrniemiec/arc/config"
)

// WorkspaceMeta is the on-disk representation of a workspace's meta.json.
type WorkspaceMeta struct {
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	Status      string     `json:"status"` // "active" | "archived"
	PinnedAt    *time.Time `json:"pinned_at,omitempty"`
}

// ResourceEntry describes one file or directory in a workspace's resources/ directory.
type ResourceEntry struct {
	Name   string // relative path within resources/ (e.g. "file.txt" or "dir/sub/file.txt")
	IsURL  bool   // true if .url stub
	IsDir  bool   // true if directory
	SrcURL string // set if IsURL
	Size   int64
}

// WorkspacesRoot returns the path to the workspaces directory.
func WorkspacesRoot(dataRoot string) string {
	return filepath.Join(dataRoot, "workspaces")
}

// WorkspaceDir returns the path to a specific workspace directory.
func WorkspaceDir(dataRoot, name string) string {
	return filepath.Join(dataRoot, "workspaces", name)
}

// CreateWorkspace creates a new workspace directory with all subdirectories,
// writes meta.json, and writes chat/config.json from chatCfg.
// Returns an error if the workspace already exists.
func CreateWorkspace(dataRoot, name, description string, chatCfg config.ChatConfig) error {
	dir := WorkspaceDir(dataRoot, name)
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("workspace %q already exists", name)
	}

	for _, sub := range []string{"articles", "collections", "resources", "outcomes", "chat"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0755); err != nil {
			return fmt.Errorf("create workspace subdir %s: %w", sub, err)
		}
	}

	m := WorkspaceMeta{
		Name:        name,
		Description: description,
		CreatedAt:   time.Now().UTC(),
		Status:      "active",
	}
	if err := WriteWorkspaceMeta(dataRoot, m); err != nil {
		return err
	}
	// Create empty scratch file.
	if err := EnsureScratch(dataRoot, name); err != nil {
		return err
	}
	// Create empty askX file.
	if err := EnsureAskX(dataRoot, name); err != nil {
		return err
	}
	return WriteChatConfig(dataRoot, name, chatCfg)
}

// ── Scratch helpers ─────────────────────────────────────────────────────────

// ScratchName returns the scratch filename for a workspace.
// Per-workspace: "scratch-<workspace>.md"; global: "scratch.md".
func ScratchName(workspace string) string {
	if workspace != "" {
		return "scratch-" + workspace + ".md"
	}
	return "scratch.md"
}

// ScratchPath returns the path to the scratch file.
// If workspace is non-empty, returns the per-workspace scratch; otherwise the global one.
func ScratchPath(dataRoot, workspace string) string {
	if workspace != "" {
		dir := WorkspaceDir(dataRoot, workspace)
		newPath := filepath.Join(dir, ScratchName(workspace))
		// Migrate: rename old scratch.md → scratch-<ws>.md on first access.
		if _, err := os.Stat(newPath); os.IsNotExist(err) {
			oldPath := filepath.Join(dir, "scratch.md")
			if _, err2 := os.Stat(oldPath); err2 == nil {
				_ = os.Rename(oldPath, newPath)
			}
		}
		return newPath
	}
	return filepath.Join(dataRoot, "scratch.md")
}

// EnsureScratch creates the scratch file if it does not exist.
func EnsureScratch(dataRoot, workspace string) error {
	path := ScratchPath(dataRoot, workspace)
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	return os.WriteFile(path, []byte(""), 0644)
}

// ReadScratch reads the scratch file and returns its content.
func ReadScratch(dataRoot, workspace string) (string, error) {
	data, err := os.ReadFile(ScratchPath(dataRoot, workspace))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}

// AppendScratch appends a line to the scratch file.
// If this is the first append for today, a date separator is inserted first.
func AppendScratch(dataRoot, workspace, msg string) error {
	if err := EnsureScratch(dataRoot, workspace); err != nil {
		return err
	}
	path := ScratchPath(dataRoot, workspace)

	// Check if today's separator already exists.
	now := time.Now()
	dateTag := now.Format("Mon, January 2, 2006")
	needSep := true
	if data, err := os.ReadFile(path); err == nil {
		if strings.Contains(string(data), dateTag) {
			needSep = false
		}
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	if needSep {
		sep := fmt.Sprintf("---------- %s %s ----------\n",
			dateTag, now.Format("15:04"))
		if _, err := f.WriteString(sep); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(f, "• %s\n", msg)
	return err
}

// ClearScratch truncates the scratch file to zero length.
func ClearScratch(dataRoot, workspace string) error {
	path := ScratchPath(dataRoot, workspace)
	return os.WriteFile(path, nil, 0644)
}

// ── AskX helpers ────────────────────────────────────────────────────────────

// AskXMessage is one turn in an askX conversation (user or assistant).
type AskXMessage struct {
	Role    string    `json:"role"`    // "user" or "assistant"
	Content string    `json:"content"`
	Time    time.Time `json:"time,omitempty"`
}

// AskXHistory is the stored askX message history.
type AskXHistory struct {
	Messages []AskXMessage `json:"messages"`
}

// AskXName returns the askx filename for a workspace.
// Per-workspace: "askx-<workspace>.json"; global: "askx.json".
func AskXName(workspace string) string {
	if workspace != "" {
		return "askx-" + workspace + ".json"
	}
	return "askx.json"
}

// AskXPath returns the path to the askx file.
// If workspace is non-empty, returns the per-workspace askx; otherwise the global one.
func AskXPath(dataRoot, workspace string) string {
	if workspace != "" {
		return filepath.Join(WorkspaceDir(dataRoot, workspace), AskXName(workspace))
	}
	return filepath.Join(dataRoot, "askx.json")
}

// EnsureAskX creates the askX file with empty history if it does not exist.
func EnsureAskX(dataRoot, workspace string) error {
	path := AskXPath(dataRoot, workspace)
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	return SaveAskXHistory(dataRoot, workspace, &AskXHistory{})
}

// ReadAskXHistory reads the askX history from JSON. Returns empty history if missing.
func ReadAskXHistory(dataRoot, workspace string) (*AskXHistory, error) {
	path := AskXPath(dataRoot, workspace)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &AskXHistory{}, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return &AskXHistory{}, nil
	}
	var h AskXHistory
	if err := json.Unmarshal(data, &h); err != nil {
		return nil, fmt.Errorf("parse askx history: %w", err)
	}
	return &h, nil
}

// SaveAskXHistory writes the askX history as JSON atomically.
func SaveAskXHistory(dataRoot, workspace string, h *AskXHistory) error {
	path := AskXPath(dataRoot, workspace)
	data, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ReadWorkspaceMeta reads meta.json from a workspace directory.
func ReadWorkspaceMeta(dataRoot, name string) (WorkspaceMeta, error) {
	path := filepath.Join(WorkspaceDir(dataRoot, name), "meta.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return WorkspaceMeta{}, fmt.Errorf("workspace %q not found", name)
		}
		return WorkspaceMeta{}, fmt.Errorf("read workspace meta %s: %w", name, err)
	}
	var m WorkspaceMeta
	if err := json.Unmarshal(data, &m); err != nil {
		return WorkspaceMeta{}, fmt.Errorf("parse workspace meta %s: %w", name, err)
	}
	return m, nil
}

// WriteWorkspaceMeta writes meta.json to a workspace directory.
func WriteWorkspaceMeta(dataRoot string, m WorkspaceMeta) error {
	path := filepath.Join(WorkspaceDir(dataRoot, m.Name), "meta.json")
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal workspace meta: %w", err)
	}
	return os.WriteFile(path, data, 0644)
}

// PinWorkspace sets PinnedAt on a workspace's meta.json.
func PinWorkspace(dataRoot, name string, t time.Time) error {
	m, err := ReadWorkspaceMeta(dataRoot, name)
	if err != nil {
		return err
	}
	m.PinnedAt = &t
	return WriteWorkspaceMeta(dataRoot, m)
}

// UnpinWorkspace clears PinnedAt on a workspace's meta.json.
func UnpinWorkspace(dataRoot, name string) error {
	m, err := ReadWorkspaceMeta(dataRoot, name)
	if err != nil {
		return err
	}
	m.PinnedAt = nil
	return WriteWorkspaceMeta(dataRoot, m)
}

// ListWorkspaces walks the workspaces root and returns metadata for all workspaces.
// Missing or malformed meta.json entries are skipped.
func ListWorkspaces(dataRoot string) ([]WorkspaceMeta, error) {
	root := WorkspacesRoot(dataRoot)
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read workspaces dir: %w", err)
	}
	var ws []WorkspaceMeta
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		m, err := ReadWorkspaceMeta(dataRoot, e.Name())
		if err != nil {
			continue
		}
		ws = append(ws, m)
	}
	return ws, nil
}

// RenameWorkspace moves a workspace directory to a new name and updates meta.json.
func RenameWorkspace(dataRoot, oldName, newName string) error {
	oldDir := WorkspaceDir(dataRoot, oldName)
	newDir := WorkspaceDir(dataRoot, newName)

	if _, err := os.Stat(oldDir); os.IsNotExist(err) {
		return fmt.Errorf("workspace %q not found", oldName)
	}
	if _, err := os.Stat(newDir); err == nil {
		return fmt.Errorf("workspace %q already exists", newName)
	}
	if err := os.Rename(oldDir, newDir); err != nil {
		return fmt.Errorf("rename workspace dir: %w", err)
	}
	m, err := ReadWorkspaceMeta(dataRoot, newName)
	if err != nil {
		return fmt.Errorf("read meta after rename: %w", err)
	}
	m.Name = newName
	return WriteWorkspaceMeta(dataRoot, m)
}

// DeleteWorkspace removes the workspace directory entirely.
func DeleteWorkspace(dataRoot, name string) error {
	dir := WorkspaceDir(dataRoot, name)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("workspace %q not found", name)
	}
	return os.RemoveAll(dir)
}

// ── Articles ──────────────────────────────────────────────────────────────────

// ErrAlreadyInWorkspace is returned when an article or collection is already linked.
var ErrAlreadyInWorkspace = fmt.Errorf("already in workspace")

// articleManifestEntry is one entry in the ordered article manifest.
type articleManifestEntry struct {
	Slug     string `json:"slug"`
	LinkedAt string `json:"linked_at"`
}

// articleManifest is an ordered list of linked articles.
type articleManifest []articleManifestEntry

func articleManifestPath(dataRoot, wsName string) string {
	return filepath.Join(WorkspaceDir(dataRoot, wsName), "articles.json")
}

func readArticleManifest(dataRoot, wsName string) articleManifest {
	data, err := os.ReadFile(articleManifestPath(dataRoot, wsName))
	if err != nil {
		return nil
	}

	// Try new format (ordered list) first.
	var list articleManifest
	if err := json.Unmarshal(data, &list); err == nil && (len(list) == 0 || list[0].Slug != "") {
		return list
	}

	// Fall back to old format (map[string]string) and convert.
	var oldMap map[string]string
	if err := json.Unmarshal(data, &oldMap); err != nil {
		return nil
	}
	for slug, ts := range oldMap {
		list = append(list, articleManifestEntry{Slug: slug, LinkedAt: ts})
	}
	return list
}

func writeArticleManifest(dataRoot, wsName string, m articleManifest) {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(articleManifestPath(dataRoot, wsName), data, 0644)
}

// manifestIndex returns the index of slug in the manifest, or -1 if not found.
func manifestIndex(m articleManifest, slug string) int {
	for i, e := range m {
		if e.Slug == slug {
			return i
		}
	}
	return -1
}

// AddArticleToWorkspace creates a relative symlink from workspace/articles/<slug>
// to the article directory and records the linking timestamp in articles.json.
func AddArticleToWorkspace(dataRoot, articlesRoot, articleSlug, workspaceName string) error {
	wsDir := WorkspaceDir(dataRoot, workspaceName)
	linkPath := filepath.Join(wsDir, "articles", articleSlug)

	if _, err := os.Lstat(linkPath); err == nil {
		return ErrAlreadyInWorkspace
	}

	articleDir := filepath.Join(articlesRoot, articleSlug)
	rel, err := filepath.Rel(filepath.Join(wsDir, "articles"), articleDir)
	if err != nil {
		return fmt.Errorf("compute rel path: %w", err)
	}
	if err := os.Symlink(rel, linkPath); err != nil {
		return err
	}

	// Append to ordered manifest.
	manifest := readArticleManifest(dataRoot, workspaceName)
	if manifestIndex(manifest, articleSlug) == -1 {
		manifest = append(manifest, articleManifestEntry{
			Slug:     articleSlug,
			LinkedAt: time.Now().Format(time.RFC3339),
		})
	}
	writeArticleManifest(dataRoot, workspaceName, manifest)
	return nil
}

// RemoveArticleFromWorkspace removes the symlink for an article from the workspace
// and removes its entry from articles.json.
func RemoveArticleFromWorkspace(dataRoot, workspaceName, articleSlug string) error {
	linkPath := filepath.Join(WorkspaceDir(dataRoot, workspaceName), "articles", articleSlug)
	info, err := os.Lstat(linkPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("article %q not in workspace %q", articleSlug, workspaceName)
		}
		return err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("%s is not a symlink — refusing to delete", linkPath)
	}
	if err := os.Remove(linkPath); err != nil {
		return err
	}

	// Remove from manifest.
	manifest := readArticleManifest(dataRoot, workspaceName)
	if idx := manifestIndex(manifest, articleSlug); idx >= 0 {
		manifest = append(manifest[:idx], manifest[idx+1:]...)
	}
	writeArticleManifest(dataRoot, workspaceName, manifest)
	return nil
}

// ListWorkspaceArticles returns article slugs linked in a workspace.
// Articles are returned in manifest order (insertion order).
// Symlinks on disk but not in the manifest are appended at the end (alphabetical).
// Broken symlinks are reported separately.
func ListWorkspaceArticles(dataRoot, name string) (articles []string, broken []string, err error) {
	dir := filepath.Join(WorkspaceDir(dataRoot, name), "articles")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("read workspace articles dir: %w", err)
	}

	// Collect valid symlinks into a set.
	onDisk := make(map[string]bool)
	for _, e := range entries {
		info, err := os.Lstat(filepath.Join(dir, e.Name()))
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		target := filepath.Join(dir, e.Name())
		if _, err := os.Stat(target); err != nil {
			broken = append(broken, e.Name())
			continue
		}
		onDisk[e.Name()] = true
	}

	// Build result in manifest order, keeping only slugs that exist on disk.
	manifest := readArticleManifest(dataRoot, name)
	seen := make(map[string]bool, len(manifest))
	for _, entry := range manifest {
		if onDisk[entry.Slug] {
			articles = append(articles, entry.Slug)
			seen[entry.Slug] = true
		}
	}

	// Append any on-disk slugs not in the manifest (alphabetical).
	var extra []string
	for slug := range onDisk {
		if !seen[slug] {
			extra = append(extra, slug)
		}
	}
	sort.Strings(extra)
	articles = append(articles, extra...)

	return articles, broken, nil
}

// ── Collections ───────────────────────────────────────────────────────────────

// collectionManifest maps slug → RFC3339 linked-at timestamp.
type collectionManifest map[string]string

func collectionManifestPath(dataRoot, wsName string) string {
	return filepath.Join(WorkspaceDir(dataRoot, wsName), "collections.json")
}

func readCollectionManifest(dataRoot, wsName string) collectionManifest {
	data, err := os.ReadFile(collectionManifestPath(dataRoot, wsName))
	if err != nil {
		return collectionManifest{}
	}
	var m collectionManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return collectionManifest{}
	}
	return m
}

func writeCollectionManifest(dataRoot, wsName string, m collectionManifest) {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(collectionManifestPath(dataRoot, wsName), data, 0644)
}

// AddCollectionToWorkspace creates a relative symlink from workspace/collections/<slug>
// to the collection directory and records the linking timestamp in collections.json.
func AddCollectionToWorkspace(dataRoot, workspaceName, collectionSlug string) error {
	wsDir := WorkspaceDir(dataRoot, workspaceName)
	linkPath := filepath.Join(wsDir, "collections", collectionSlug)

	if _, err := os.Lstat(linkPath); err == nil {
		return ErrAlreadyInWorkspace
	}

	colDir := CollectionDir(dataRoot, collectionSlug)
	rel, err := filepath.Rel(filepath.Join(wsDir, "collections"), colDir)
	if err != nil {
		return fmt.Errorf("compute rel path: %w", err)
	}
	if err := os.Symlink(rel, linkPath); err != nil {
		return err
	}

	// Record linking timestamp.
	manifest := readCollectionManifest(dataRoot, workspaceName)
	manifest[collectionSlug] = time.Now().Format(time.RFC3339)
	writeCollectionManifest(dataRoot, workspaceName, manifest)
	return nil
}

// RemoveCollectionFromWorkspace removes the symlink for a collection from the workspace
// and removes its entry from collections.json.
func RemoveCollectionFromWorkspace(dataRoot, workspaceName, collectionSlug string) error {
	linkPath := filepath.Join(WorkspaceDir(dataRoot, workspaceName), "collections", collectionSlug)
	info, err := os.Lstat(linkPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("collection %q not in workspace %q", collectionSlug, workspaceName)
		}
		return err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("%s is not a symlink — refusing to delete", linkPath)
	}
	if err := os.Remove(linkPath); err != nil {
		return err
	}

	// Remove from manifest.
	manifest := readCollectionManifest(dataRoot, workspaceName)
	delete(manifest, collectionSlug)
	writeCollectionManifest(dataRoot, workspaceName, manifest)
	return nil
}

// ListWorkspaceCollections returns collection slugs linked in a workspace.
// Collections are sorted by linking timestamp (from collections.json manifest).
// Collections without a manifest entry are sorted by slug and listed first.
func ListWorkspaceCollections(dataRoot, name string) ([]string, error) {
	dir := filepath.Join(WorkspaceDir(dataRoot, name), "collections")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read workspace collections dir: %w", err)
	}
	var cols []string
	for _, e := range entries {
		info, err := os.Lstat(filepath.Join(dir, e.Name()))
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		target := filepath.Join(dir, e.Name())
		if _, err := os.Stat(target); err != nil {
			continue // broken symlink — skip silently
		}
		cols = append(cols, e.Name())
	}

	// Sort by manifest linked-at timestamp.
	manifest := readCollectionManifest(dataRoot, name)
	sort.SliceStable(cols, func(i, j int) bool {
		ti, oki := manifest[cols[i]]
		tj, okj := manifest[cols[j]]
		if !oki && !okj {
			return cols[i] < cols[j]
		}
		if !oki {
			return true
		}
		if !okj {
			return false
		}
		return ti < tj
	})

	return cols, nil
}

// ── Resources ─────────────────────────────────────────────────────────────────

// AddFileResource copies a local file into workspace/resources/[into/].
// If into is non-empty, the file is placed inside that subdirectory.
// Returns the relative path of the stored file within resources/.
func AddFileResource(dataRoot, workspaceName, srcPath, into string) (string, error) {
	// Expand ~ in path.
	if strings.HasPrefix(srcPath, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("get home dir: %w", err)
		}
		srcPath = filepath.Join(home, srcPath[2:])
	}

	info, err := os.Stat(srcPath)
	if err != nil {
		return "", fmt.Errorf("resource file not found: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%q is a directory — only files are supported", srcPath)
	}

	basename := filepath.Base(srcPath)
	resDir := filepath.Join(WorkspaceDir(dataRoot, workspaceName), "resources", into)
	if into != "" {
		if err := os.MkdirAll(resDir, 0755); err != nil {
			return "", fmt.Errorf("create resource subdir: %w", err)
		}
	}
	destPath := filepath.Join(resDir, basename)

	if _, err := os.Stat(destPath); err == nil {
		return "", fmt.Errorf("resource %q already exists in workspace", filepath.Join(into, basename))
	}

	src, err := os.Open(srcPath)
	if err != nil {
		return "", fmt.Errorf("open source file: %w", err)
	}
	defer src.Close()

	dst, err := os.Create(destPath)
	if err != nil {
		return "", fmt.Errorf("create resource file: %w", err)
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		return "", fmt.Errorf("copy resource file: %w", err)
	}
	return filepath.Join(into, basename), nil
}

// AddDirResource recursively copies a directory into workspace/resources/[into/]<dirname>/.
// If into is non-empty, the directory is placed inside that subdirectory.
// Symlinks inside the source are skipped; hidden files are included.
// Returns the relative path of the stored directory within resources/.
func AddDirResource(dataRoot, workspaceName, srcPath, into string) (string, error) {
	// Trailing slash means "copy contents of directory" (rsync convention).
	copyContents := strings.HasSuffix(srcPath, "/") || strings.HasSuffix(srcPath, string(filepath.Separator))

	if strings.HasPrefix(srcPath, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("get home dir: %w", err)
		}
		srcPath = filepath.Join(home, srcPath[2:])
	}

	info, err := os.Lstat(srcPath)
	if err != nil {
		return "", fmt.Errorf("resource dir not found: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%q is not a directory", srcPath)
	}

	var relPath, destDir string
	if copyContents {
		if into == "" {
			return "", fmt.Errorf("trailing slash (copy contents) requires --into <dir>")
		}
		relPath = into
	} else {
		relPath = filepath.Join(into, filepath.Base(srcPath))
	}
	destDir = filepath.Join(WorkspaceDir(dataRoot, workspaceName), "resources", relPath)

	if copyContents {
		if err := os.MkdirAll(destDir, 0755); err != nil {
			return "", fmt.Errorf("create target dir: %w", err)
		}
	} else if _, err := os.Stat(destDir); err == nil {
		return "", fmt.Errorf("resource %q already exists in workspace", relPath)
	}

	err = filepath.WalkDir(srcPath, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		// Skip symlinks.
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		// Also check via Lstat for symlink dirs that WalkDir may follow.
		linfo, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if linfo.Mode()&os.ModeSymlink != 0 {
			if linfo.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		rel, err := filepath.Rel(srcPath, path)
		if err != nil {
			return err
		}
		dest := filepath.Join(destDir, rel)

		if d.IsDir() {
			return os.MkdirAll(dest, 0755)
		}

		src, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("open %s: %w", rel, err)
		}
		defer src.Close()

		dst, err := os.Create(dest)
		if err != nil {
			return fmt.Errorf("create %s: %w", rel, err)
		}
		defer dst.Close()

		if _, err := io.Copy(dst, src); err != nil {
			return fmt.Errorf("copy %s: %w", rel, err)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("copy directory: %w", err)
	}
	return relPath, nil
}

// AddURLResource writes a .url stub file containing the URL into workspace/resources/.
// Returns the basename of the stored stub file.
func AddURLResource(dataRoot, workspaceName, rawURL, customName, comment string) (string, error) {
	basename := urlToBasename(rawURL)
	if customName != "" {
		basename = customName
		if !strings.HasSuffix(basename, ".url") {
			basename += ".url"
		}
	}
	destPath := filepath.Join(WorkspaceDir(dataRoot, workspaceName), "resources", basename)

	if _, err := os.Stat(destPath); err == nil {
		return "", fmt.Errorf("resource %q already exists in workspace", basename)
	}

	content := rawURL + "\n"
	if comment != "" {
		content += comment + "\n"
	}
	if err := os.WriteFile(destPath, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("write url stub: %w", err)
	}
	return basename, nil
}

// RemoveWorkspaceResource removes a file or directory from workspace/resources/.
func RemoveWorkspaceResource(dataRoot, workspaceName, basename string) error {
	path := filepath.Join(WorkspaceDir(dataRoot, workspaceName), "resources", basename)
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("resource %q not found in workspace %q", basename, workspaceName)
		}
		return err
	}
	if info.IsDir() {
		return os.RemoveAll(path)
	}
	return os.Remove(path)
}

// ListWorkspaceResources returns top-level entries in workspace/resources/.
// Directories are returned as single entries with IsDir=true.
func ListWorkspaceResources(dataRoot, name string) ([]ResourceEntry, error) {
	dir := filepath.Join(WorkspaceDir(dataRoot, name), "resources")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read workspace resources dir: %w", err)
	}
	var resources []ResourceEntry
	for _, e := range entries {
		if e.IsDir() {
			resources = append(resources, ResourceEntry{Name: e.Name(), IsDir: true})
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		re := ResourceEntry{Name: e.Name(), Size: info.Size()}
		if strings.HasSuffix(e.Name(), ".url") {
			re.IsURL = true
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err == nil {
				line, _, _ := strings.Cut(string(data), "\n")
				re.SrcURL = strings.TrimSpace(line)
			}
		}
		resources = append(resources, re)
	}
	return resources, nil
}

// ListWorkspaceDirResources returns entries inside a subdirectory of workspace/resources/.
// The relDir is relative to resources/ (e.g. "mydir" or "mydir/sub").
func ListWorkspaceDirResources(dataRoot, name, relDir string) ([]ResourceEntry, error) {
	dir := filepath.Join(WorkspaceDir(dataRoot, name), "resources", relDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read resource dir %s: %w", relDir, err)
	}
	var resources []ResourceEntry
	for _, e := range entries {
		entryName := filepath.Join(relDir, e.Name())
		if e.IsDir() {
			resources = append(resources, ResourceEntry{Name: entryName, IsDir: true})
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		re := ResourceEntry{Name: entryName, Size: info.Size()}
		if strings.HasSuffix(e.Name(), ".url") {
			re.IsURL = true
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err == nil {
				line, _, _ := strings.Cut(string(data), "\n")
				re.SrcURL = strings.TrimSpace(line)
			}
		}
		resources = append(resources, re)
	}
	return resources, nil
}

// ── Outcomes ──────────────────────────────────────────────────────────────────

// ListWorkspaceOutcomes returns filenames in workspace/outcomes/.
func ListWorkspaceOutcomes(dataRoot, name string) ([]string, error) {
	dir := filepath.Join(WorkspaceDir(dataRoot, name), "outcomes")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read workspace outcomes dir: %w", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// ReadWorkspaceOutcome reads a file from workspace/outcomes/.
func ReadWorkspaceOutcome(dataRoot, name, filename string) ([]byte, error) {
	path := filepath.Join(WorkspaceDir(dataRoot, name), "outcomes", filename)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read outcome %s: %w", filename, err)
	}
	return data, nil
}

// RemoveWorkspaceOutcome removes a file from workspace/outcomes/.
func RemoveWorkspaceOutcome(dataRoot, workspaceName, basename string) error {
	path := filepath.Join(WorkspaceDir(dataRoot, workspaceName), "outcomes", basename)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("outcome %q not found in workspace %q", basename, workspaceName)
	}
	return os.Remove(path)
}

// WriteWorkspaceOutcome writes a file to workspace/outcomes/.
func WriteWorkspaceOutcome(dataRoot, name, filename string, data []byte) error {
	path := filepath.Join(WorkspaceDir(dataRoot, name), "outcomes", filename)
	return os.WriteFile(path, data, 0644)
}

// MkdirWorkspaceResource creates a directory inside workspace/resources/.
// The relPath can be nested (e.g. "a/b/c") — intermediate dirs are created.
func MkdirWorkspaceResource(dataRoot, workspaceName, relPath string) error {
	dir := filepath.Join(WorkspaceDir(dataRoot, workspaceName), "resources", relPath)
	return os.MkdirAll(dir, 0755)
}

// WriteWorkspaceResource writes a file to workspace/resources/.
func WriteWorkspaceResource(dataRoot, name, filename string, data []byte) error {
	dir := filepath.Join(WorkspaceDir(dataRoot, name), "resources")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, filename), data, 0644)
}

// ── Chat config ───────────────────────────────────────────────────────────────

// ReadChatConfig reads chat/config.jsonc from a workspace.
// Falls back to legacy chat/config.json then chat/chat.json if not found.
// When reading from a legacy file, config.jsonc is written immediately so
// the workspace self-migrates on first access.
// Returns zero value if no config file exists.
func ReadChatConfig(dataRoot, name string) (config.ChatConfig, error) {
	chatDir := filepath.Join(WorkspaceDir(dataRoot, name), "chat")

	// Preferred: config.jsonc — already migrated, return directly.
	if data, err := os.ReadFile(filepath.Join(chatDir, "config.jsonc")); err == nil {
		var cfg config.ChatConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return config.ChatConfig{}, fmt.Errorf("parse chat config: %w", err)
		}
		return cfg, nil
	}

	// Legacy files — parse then migrate to config.jsonc.
	for _, filename := range []string{"config.json", "chat.json"} {
		data, err := os.ReadFile(filepath.Join(chatDir, filename))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return config.ChatConfig{}, fmt.Errorf("read chat config: %w", err)
		}
		var cfg config.ChatConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return config.ChatConfig{}, fmt.Errorf("parse chat config: %w", err)
		}
		// Migrate: write commented config.jsonc alongside the legacy file.
		// Ignore write errors — migration is best-effort.
		_ = WriteChatConfig(dataRoot, name, cfg)
		return cfg, nil
	}
	return config.ChatConfig{}, nil
}

// WriteChatConfig writes chat/config.jsonc to a workspace with inline comments
// documenting every field. The file is always fully regenerated — it is
// machine-managed. User edits survive only until arc rewrites the file (e.g.
// via /mode or /profile), so the disclaimer at the top warns accordingly.
func WriteChatConfig(dataRoot, name string, cfg config.ChatConfig) error {
	dir := filepath.Join(WorkspaceDir(dataRoot, name), "chat")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create chat dir: %w", err)
	}
	data := formatChatConfig(cfg)
	return os.WriteFile(filepath.Join(dir, "config.jsonc"), data, 0644)
}

// formatChatConfig renders a ChatConfig as a commented JSONC file.
func formatChatConfig(cfg config.ChatConfig) []byte {
	q := func(s string) string { return `"` + s + `"` }
	return []byte(fmt.Sprintf(`// workspace chat configuration — managed by arc
// NOTE: arc rewrites this file when you use /mode or /profile.
// Edit manually only when arc is not running, then restart to apply changes.
{
  // LLM profile used for this workspace's chat sessions.
  // Must be an Anthropic profile (tool calling requires Anthropic).
  // Empty: falls back to ingest.flash_profile, then the first available profile.
  // Use /profile in the TUI to switch without editing this file.
  "profile": %s,

  // How the assistant sources its answers.
  // "corpus-only"  — workspace articles only; never uses general knowledge.
  // "corpus-first" — articles first, then general knowledge to fill gaps.
  // "open"         — articles, library, general knowledge, and web search.
  // Use /mode in the TUI to switch without editing this file.
  "grounding_mode": %s,

  // How conversation history is trimmed to fit the context window.
  // "tail"         — keep the last max_user_messages turns (default).
  // "token-budget" — keep as many turns as fit within context_limit tokens.
  // "summarize"    — compress old turns via LLM using summarizer_profile.
  "strategy": %s,

  // Maximum number of past user turns kept by the "tail" strategy. Default: 50.
  "max_user_messages": %d,

  // Token budget for "token-budget" and "summarize" strategies.
  // 0 means no explicit limit (provider context window is used).
  "context_limit": %d,

  // Maximum tokens in each response. 0 uses the provider default (4096).
  "max_output_tokens": %d,

  // Profile used to compress history in the "summarize" strategy.
  // Empty: falls back to the main profile above.
  "summarizer_profile": %s,

  // Fraction of the token budget kept as verbatim recent messages in the
  // "summarize" strategy. The remainder is covered by the LLM summary.
  // Default: 0.4 (40%% verbatim, 60%% summary).
  "verbatim_ratio": %g
}
`,
		q(cfg.Profile),
		q(cfg.GroundingMode),
		q(cfg.Strategy),
		cfg.MaxUserMessages,
		cfg.ContextLimit,
		cfg.MaxOutputTokens,
		q(cfg.SummarizerProfile),
		cfg.VerbatimRatio,
	))
}

// ── Helpers ───────────────────────────────────────────────────────────────────

var nonAlphanumRe = regexp.MustCompile(`[^a-z0-9]+`)

// urlToBasename converts a URL to a filesystem-safe .url filename.
// e.g. https://youtube.com/watch?v=abc → youtube-com-watch-v-abc.url
func urlToBasename(rawURL string) string {
	u, err := url.Parse(rawURL)
	s := rawURL
	if err == nil {
		s = u.Host + u.Path
		if u.RawQuery != "" {
			s += "-" + u.RawQuery
		}
	}
	s = strings.ToLower(s)
	s = nonAlphanumRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 80 {
		s = s[:80]
		s = strings.TrimRight(s, "-")
	}
	if s == "" {
		s = "resource"
	}
	return s + ".url"
}

// ── Attic ────────────────────────────────────────────────────────────────────

func atticArticleManifestPath(dataRoot, wsName string) string {
	return filepath.Join(WorkspaceDir(dataRoot, wsName), "attic-articles.json")
}

func readAtticArticleManifest(dataRoot, wsName string) articleManifest {
	data, err := os.ReadFile(atticArticleManifestPath(dataRoot, wsName))
	if err != nil {
		return nil
	}
	var list articleManifest
	if err := json.Unmarshal(data, &list); err != nil {
		return nil
	}
	return list
}

func writeAtticArticleManifest(dataRoot, wsName string, m articleManifest) {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(atticArticleManifestPath(dataRoot, wsName), data, 0644)
}

func atticCollectionManifestPath(dataRoot, wsName string) string {
	return filepath.Join(WorkspaceDir(dataRoot, wsName), "attic-collections.json")
}

func readAtticCollectionManifest(dataRoot, wsName string) collectionManifest {
	data, err := os.ReadFile(atticCollectionManifestPath(dataRoot, wsName))
	if err != nil {
		return collectionManifest{}
	}
	var m collectionManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return collectionManifest{}
	}
	return m
}

func writeAtticCollectionManifest(dataRoot, wsName string, m collectionManifest) {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(atticCollectionManifestPath(dataRoot, wsName), data, 0644)
}

// MoveArticleToAttic removes an article symlink and moves its manifest entry to the attic.
func MoveArticleToAttic(dataRoot, wsName, slug string) error {
	// Remove symlink.
	linkPath := filepath.Join(WorkspaceDir(dataRoot, wsName), "articles", slug)
	if info, err := os.Lstat(linkPath); err == nil && info.Mode()&os.ModeSymlink != 0 {
		_ = os.Remove(linkPath)
	}

	// Remove from active manifest, capture timestamp.
	active := readArticleManifest(dataRoot, wsName)
	linkedAt := time.Now().Format(time.RFC3339)
	if idx := manifestIndex(active, slug); idx >= 0 {
		linkedAt = active[idx].LinkedAt
		active = append(active[:idx], active[idx+1:]...)
	}
	writeArticleManifest(dataRoot, wsName, active)

	// Add to attic manifest (if not already there).
	attic := readAtticArticleManifest(dataRoot, wsName)
	if manifestIndex(attic, slug) == -1 {
		attic = append(attic, articleManifestEntry{Slug: slug, LinkedAt: linkedAt})
	}
	writeAtticArticleManifest(dataRoot, wsName, attic)
	return nil
}

// MoveArticleFromAttic restores an article from the attic back to the active workspace.
func MoveArticleFromAttic(dataRoot, articlesRoot, wsName, slug string) error {
	// Remove from attic manifest.
	attic := readAtticArticleManifest(dataRoot, wsName)
	idx := manifestIndex(attic, slug)
	if idx == -1 {
		return fmt.Errorf("article %q not in attic of workspace %q", slug, wsName)
	}
	linkedAt := attic[idx].LinkedAt
	attic = append(attic[:idx], attic[idx+1:]...)
	writeAtticArticleManifest(dataRoot, wsName, attic)

	// Restore symlink.
	wsDir := WorkspaceDir(dataRoot, wsName)
	linkPath := filepath.Join(wsDir, "articles", slug)
	if _, err := os.Lstat(linkPath); err != nil {
		articleDir := filepath.Join(articlesRoot, slug)
		rel, err := filepath.Rel(filepath.Join(wsDir, "articles"), articleDir)
		if err != nil {
			return fmt.Errorf("compute rel path: %w", err)
		}
		_ = os.Symlink(rel, linkPath)
	}

	// Add back to active manifest.
	active := readArticleManifest(dataRoot, wsName)
	if manifestIndex(active, slug) == -1 {
		active = append(active, articleManifestEntry{Slug: slug, LinkedAt: linkedAt})
	}
	writeArticleManifest(dataRoot, wsName, active)
	return nil
}

// RemoveArticleFromAttic removes an article from the attic entirely (no restore).
func RemoveArticleFromAttic(dataRoot, wsName, slug string) error {
	attic := readAtticArticleManifest(dataRoot, wsName)
	idx := manifestIndex(attic, slug)
	if idx == -1 {
		return fmt.Errorf("article %q not in attic of workspace %q", slug, wsName)
	}
	attic = append(attic[:idx], attic[idx+1:]...)
	writeAtticArticleManifest(dataRoot, wsName, attic)
	return nil
}

// MoveCollectionToAttic removes a collection symlink and moves its manifest entry to the attic.
func MoveCollectionToAttic(dataRoot, wsName, colSlug string) error {
	// Remove symlink.
	linkPath := filepath.Join(WorkspaceDir(dataRoot, wsName), "collections", colSlug)
	if info, err := os.Lstat(linkPath); err == nil && info.Mode()&os.ModeSymlink != 0 {
		_ = os.Remove(linkPath)
	}

	// Remove from active manifest, capture timestamp.
	active := readCollectionManifest(dataRoot, wsName)
	linkedAt, ok := active[colSlug]
	if !ok {
		linkedAt = time.Now().Format(time.RFC3339)
	}
	delete(active, colSlug)
	writeCollectionManifest(dataRoot, wsName, active)

	// Add to attic manifest.
	attic := readAtticCollectionManifest(dataRoot, wsName)
	if _, exists := attic[colSlug]; !exists {
		attic[colSlug] = linkedAt
	}
	writeAtticCollectionManifest(dataRoot, wsName, attic)
	return nil
}

// MoveCollectionFromAttic restores a collection from the attic back to the active workspace.
func MoveCollectionFromAttic(dataRoot, wsName, colSlug string) error {
	// Remove from attic manifest.
	attic := readAtticCollectionManifest(dataRoot, wsName)
	linkedAt, ok := attic[colSlug]
	if !ok {
		return fmt.Errorf("collection %q not in attic of workspace %q", colSlug, wsName)
	}
	delete(attic, colSlug)
	writeAtticCollectionManifest(dataRoot, wsName, attic)

	// Restore symlink.
	wsDir := WorkspaceDir(dataRoot, wsName)
	linkPath := filepath.Join(wsDir, "collections", colSlug)
	if _, err := os.Lstat(linkPath); err != nil {
		colDir := CollectionDir(dataRoot, colSlug)
		rel, err := filepath.Rel(filepath.Join(wsDir, "collections"), colDir)
		if err != nil {
			return fmt.Errorf("compute rel path: %w", err)
		}
		_ = os.Symlink(rel, linkPath)
	}

	// Add back to active manifest.
	active := readCollectionManifest(dataRoot, wsName)
	if _, exists := active[colSlug]; !exists {
		active[colSlug] = linkedAt
	}
	writeCollectionManifest(dataRoot, wsName, active)
	return nil
}

// RemoveCollectionFromAttic removes a collection from the attic entirely (no restore).
func RemoveCollectionFromAttic(dataRoot, wsName, colSlug string) error {
	attic := readAtticCollectionManifest(dataRoot, wsName)
	if _, ok := attic[colSlug]; !ok {
		return fmt.Errorf("collection %q not in attic of workspace %q", colSlug, wsName)
	}
	delete(attic, colSlug)
	writeAtticCollectionManifest(dataRoot, wsName, attic)
	return nil
}

// ListAtticArticles returns article slugs in the workspace attic (manifest order).
func ListAtticArticles(dataRoot, wsName string) []string {
	m := readAtticArticleManifest(dataRoot, wsName)
	slugs := make([]string, len(m))
	for i, e := range m {
		slugs[i] = e.Slug
	}
	return slugs
}

// ListAtticCollections returns collection slugs in the workspace attic (sorted by timestamp).
func ListAtticCollections(dataRoot, wsName string) []string {
	m := readAtticCollectionManifest(dataRoot, wsName)
	cols := make([]string, 0, len(m))
	for slug := range m {
		cols = append(cols, slug)
	}
	sort.Strings(cols)
	return cols
}

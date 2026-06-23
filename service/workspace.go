package service

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/jrniemiec/arc/config"
	"github.com/jrniemiec/arc/store/fs"
)

// CreateWorkspace creates a new workspace, writing chat/chat.json from the global template.
func (s *Service) CreateWorkspace(ctx context.Context, name, description string) error {
	if err := fs.CreateWorkspace(s.cfg.DataRoot, name, description, s.cfg.Chat); err != nil {
		return fmt.Errorf("create workspace: %w", err)
	}
	return nil
}

// ListWorkspaces returns all workspaces with counts.
func (s *Service) ListWorkspaces(ctx context.Context, includeArchived bool) ([]WorkspaceInfo, error) {
	metas, err := fs.ListWorkspaces(s.cfg.DataRoot)
	if err != nil {
		return nil, fmt.Errorf("list workspaces: %w", err)
	}
	var out []WorkspaceInfo
	for _, m := range metas {
		if !includeArchived && m.Status == "archived" {
			continue
		}
		info, err := s.buildWorkspaceInfo(m)
		if err != nil {
			continue
		}
		out = append(out, info)
	}
	return out, nil
}

// GetWorkspace returns info for a single workspace.
func (s *Service) GetWorkspace(ctx context.Context, name string) (WorkspaceInfo, error) {
	m, err := fs.ReadWorkspaceMeta(s.cfg.DataRoot, name)
	if err != nil {
		return WorkspaceInfo{}, err
	}
	return s.buildWorkspaceInfo(m)
}

// buildWorkspaceInfo populates a WorkspaceInfo from a WorkspaceMeta.
func (s *Service) buildWorkspaceInfo(m fs.WorkspaceMeta) (WorkspaceInfo, error) {
	articles, broken, _ := fs.ListWorkspaceArticles(s.cfg.DataRoot, m.Name)
	for _, b := range broken {
		slog.Warn("broken workspace article symlink", "workspace", m.Name, "article", b)
	}
	cols, _ := fs.ListWorkspaceCollections(s.cfg.DataRoot, m.Name)
	resources, _ := fs.ListWorkspaceResources(s.cfg.DataRoot, m.Name)
	outcomes, _ := fs.ListWorkspaceOutcomes(s.cfg.DataRoot, m.Name)
	chatCfg, _ := fs.ReadChatConfig(s.cfg.DataRoot, m.Name)

	wsDir := fs.WorkspaceDir(s.cfg.DataRoot, m.Name)
	_, hasSystemErr := os.Stat(filepath.Join(wsDir, "system.txt"))
	_, hasHistoryErr := os.Stat(filepath.Join(wsDir, "chat", "history.json"))

	return WorkspaceInfo{
		Name:            m.Name,
		Description:     m.Description,
		Status:          m.Status,
		CreatedAt:       m.CreatedAt,
		ArticleCount:    len(articles),
		CollectionCount: len(cols),
		ResourceCount:   len(resources),
		OutcomeCount:    len(outcomes),
		HasSystem:       hasSystemErr == nil,
		HasHistory:      hasHistoryErr == nil,
		ChatConfig:      chatCfg,
	}, nil
}

// ResolveWorkspaceName resolves a user-supplied query to a workspace name.
// Tries exact match first, then substring match.
func (s *Service) ResolveWorkspaceName(ctx context.Context, query string) (string, error) {
	metas, err := fs.ListWorkspaces(s.cfg.DataRoot)
	if err != nil {
		return "", fmt.Errorf("list workspaces: %w", err)
	}

	// Exact match first.
	for _, m := range metas {
		if m.Name == query {
			return m.Name, nil
		}
	}

	// Substring match.
	q := strings.ToLower(query)
	var matches []string
	for _, m := range metas {
		if strings.Contains(strings.ToLower(m.Name), q) {
			matches = append(matches, m.Name)
		}
	}

	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no workspace matching %q", query)
	case 1:
		return matches[0], nil
	default:
		msg := fmt.Sprintf("%q matches multiple workspaces — be more specific:\n", query)
		for _, m := range matches {
			msg += fmt.Sprintf("  %s\n", m)
		}
		return "", fmt.Errorf("%s", strings.TrimRight(msg, "\n"))
	}
}

// RenameWorkspace renames a workspace.
func (s *Service) RenameWorkspace(ctx context.Context, oldName, newName string) error {
	return fs.RenameWorkspace(s.cfg.DataRoot, oldName, newName)
}

// ArchiveWorkspace sets workspace status to "archived".
func (s *Service) ArchiveWorkspace(ctx context.Context, name string) error {
	m, err := fs.ReadWorkspaceMeta(s.cfg.DataRoot, name)
	if err != nil {
		return err
	}
	m.Status = "archived"
	return fs.WriteWorkspaceMeta(s.cfg.DataRoot, m)
}

// DeleteWorkspace removes a workspace directory entirely.
func (s *Service) DeleteWorkspace(ctx context.Context, name string) error {
	return fs.DeleteWorkspace(s.cfg.DataRoot, name)
}

// SetWorkspaceDescription updates the description in meta.json.
func (s *Service) SetWorkspaceDescription(ctx context.Context, name, text string) error {
	m, err := fs.ReadWorkspaceMeta(s.cfg.DataRoot, name)
	if err != nil {
		return err
	}
	m.Description = text
	return fs.WriteWorkspaceMeta(s.cfg.DataRoot, m)
}

// SetWorkspaceSystemPrompt writes system.txt for a workspace.
func (s *Service) SetWorkspaceSystemPrompt(ctx context.Context, name, text string) error {
	if _, err := fs.ReadWorkspaceMeta(s.cfg.DataRoot, name); err != nil {
		return err
	}
	wsDir := fs.WorkspaceDir(s.cfg.DataRoot, name)
	return os.WriteFile(filepath.Join(wsDir, "system.txt"), []byte(text+"\n"), 0644)
}

// GetWorkspaceSystemPrompt reads system.txt for a workspace.
func (s *Service) GetWorkspaceSystemPrompt(ctx context.Context, name string) (string, error) {
	if _, err := fs.ReadWorkspaceMeta(s.cfg.DataRoot, name); err != nil {
		return "", err
	}
	wsDir := fs.WorkspaceDir(s.cfg.DataRoot, name)
	data, err := os.ReadFile(filepath.Join(wsDir, "system.txt"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read system.txt: %w", err)
	}
	return string(data), nil
}

// GetChatConfig reads chat/chat.json for a workspace.
func (s *Service) GetChatConfig(ctx context.Context, name string) (config.ChatConfig, error) {
	return fs.ReadChatConfig(s.cfg.DataRoot, name)
}

// SetChatConfig writes chat/chat.json for a workspace.
func (s *Service) SetChatConfig(ctx context.Context, name string, cfg config.ChatConfig) error {
	return fs.WriteChatConfig(s.cfg.DataRoot, name, cfg)
}

// ── Articles ──────────────────────────────────────────────────────────────────

// AddArticlesToWorkspace links one or more articles into a workspace.
func (s *Service) AddArticlesToWorkspace(ctx context.Context, workspaceName string, slugs []string) error {
	if _, err := fs.ReadWorkspaceMeta(s.cfg.DataRoot, workspaceName); err != nil {
		return err
	}
	var errs []string
	for _, slug := range slugs {
		resolved, err := s.ResolveSlug(ctx, slug)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", slug, err))
			continue
		}
		articleDir := filepath.Join(s.cfg.ArticlesRoot, resolved)
		if _, err := os.Stat(articleDir); os.IsNotExist(err) {
			errs = append(errs, fmt.Sprintf("%s: article not found", resolved))
			continue
		}
		if err := fs.AddArticleToWorkspace(s.cfg.DataRoot, s.cfg.ArticlesRoot, resolved, workspaceName); err != nil {
			if err == fs.ErrAlreadyInWorkspace {
				errs = append(errs, fmt.Sprintf("%s: already in workspace", resolved))
				continue
			}
			errs = append(errs, fmt.Sprintf("%s: %v", resolved, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// RemoveArticlesFromWorkspace removes one or more article links from a workspace.
func (s *Service) RemoveArticlesFromWorkspace(ctx context.Context, workspaceName string, slugs []string) error {
	if _, err := fs.ReadWorkspaceMeta(s.cfg.DataRoot, workspaceName); err != nil {
		return err
	}
	var errs []string
	for _, slug := range slugs {
		if err := fs.RemoveArticleFromWorkspace(s.cfg.DataRoot, workspaceName, slug); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", slug, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// ── Collections ───────────────────────────────────────────────────────────────

// AddCollectionsToWorkspace links one or more collections into a workspace.
func (s *Service) AddCollectionsToWorkspace(ctx context.Context, workspaceName string, cols []string) error {
	if _, err := fs.ReadWorkspaceMeta(s.cfg.DataRoot, workspaceName); err != nil {
		return err
	}
	var errs []string
	for _, col := range cols {
		resolved, err := s.ResolveCollectionSlug(ctx, col)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", col, err))
			continue
		}
		if _, err := fs.ReadCollectionMeta(s.cfg.DataRoot, resolved); err != nil {
			errs = append(errs, fmt.Sprintf("%s: collection not found", resolved))
			continue
		}
		if err := fs.AddCollectionToWorkspace(s.cfg.DataRoot, workspaceName, resolved); err != nil {
			if err == fs.ErrAlreadyInWorkspace {
				errs = append(errs, fmt.Sprintf("%s: already in workspace", resolved))
				continue
			}
			errs = append(errs, fmt.Sprintf("%s: %v", resolved, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// RemoveCollectionsFromWorkspace removes one or more collection links from a workspace.
func (s *Service) RemoveCollectionsFromWorkspace(ctx context.Context, workspaceName string, cols []string) error {
	if _, err := fs.ReadWorkspaceMeta(s.cfg.DataRoot, workspaceName); err != nil {
		return err
	}
	var errs []string
	for _, col := range cols {
		if err := fs.RemoveCollectionFromWorkspace(s.cfg.DataRoot, workspaceName, col); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", col, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// ── Resources ─────────────────────────────────────────────────────────────────

// AddResourcesToWorkspace adds one or more files or URLs to workspace/resources/.
// URLs (http:// or https://) are stored as .url stubs; everything else is hard-copied.
func (s *Service) AddResourcesToWorkspace(ctx context.Context, workspaceName string, paths []string) error {
	if _, err := fs.ReadWorkspaceMeta(s.cfg.DataRoot, workspaceName); err != nil {
		return err
	}
	var errs []string
	for _, p := range paths {
		var err error
		if strings.HasPrefix(p, "http://") || strings.HasPrefix(p, "https://") {
			_, err = fs.AddURLResource(s.cfg.DataRoot, workspaceName, p)
		} else {
			_, err = fs.AddFileResource(s.cfg.DataRoot, workspaceName, p)
		}
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", p, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// RemoveResourcesFromWorkspace removes one or more files from workspace/resources/.
func (s *Service) RemoveResourcesFromWorkspace(ctx context.Context, workspaceName string, basenames []string) error {
	if _, err := fs.ReadWorkspaceMeta(s.cfg.DataRoot, workspaceName); err != nil {
		return err
	}
	var errs []string
	for _, b := range basenames {
		if err := fs.RemoveWorkspaceResource(s.cfg.DataRoot, workspaceName, b); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", b, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// ListWorkspaceResources returns all resources in a workspace.
func (s *Service) ListWorkspaceResources(ctx context.Context, workspaceName string) ([]fs.ResourceEntry, error) {
	return fs.ListWorkspaceResources(s.cfg.DataRoot, workspaceName)
}

// ── Outcomes ──────────────────────────────────────────────────────────────────

// ListWorkspaceOutcomes returns filenames in workspace/outcomes/.
func (s *Service) ListWorkspaceOutcomes(ctx context.Context, workspaceName string) ([]string, error) {
	return fs.ListWorkspaceOutcomes(s.cfg.DataRoot, workspaceName)
}

// ReadWorkspaceOutcome reads a file from workspace/outcomes/.
func (s *Service) ReadWorkspaceOutcome(ctx context.Context, workspaceName, filename string) (string, error) {
	data, err := fs.ReadWorkspaceOutcome(s.cfg.DataRoot, workspaceName, filename)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// SaveWorkspaceOutcome writes a file to workspace/outcomes/.
func (s *Service) SaveWorkspaceOutcome(ctx context.Context, workspaceName, filename string, data []byte) error {
	return fs.WriteWorkspaceOutcome(s.cfg.DataRoot, workspaceName, filename, data)
}

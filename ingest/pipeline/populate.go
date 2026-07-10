package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jrniemiec/arc/config"
	"github.com/jrniemiec/arc/internal/clog"
	"github.com/jrniemiec/llm"
)

// PopulateRequest holds the inputs for a two-pass workspace populate.
type PopulateRequest struct {
	WorkspaceName        string
	WorkspaceDescription string
	Profile              string
	Hint                 string // free-form user guidance appended to prompts
	IncludeCollections   bool   // when true, collections are included in pass 1
	Progress             func(string)
}

// PopulateCollection is a collection with its description and member titles.
type PopulateCollection struct {
	Slug        string
	Description string
	Titles      []string // member article titles
}

// PopulateArticle is an article slug + title for the inventory.
type PopulateArticle struct {
	Slug  string
	Title string
}

// PopulateCandidate is a candidate article with its flash summary for Pass 2.
type PopulateCandidate struct {
	Slug  string
	Flash string
}

// PopulateResult holds the final populate suggestions.
type PopulateResult struct {
	Collections  []string // collection slugs
	Articles     []string // article slugs (not covered by selected collections)
	InputTokens  int
	OutputTokens int
}

// pass1Response is the JSON structure returned by the LLM in Pass 1.
type pass1Response struct {
	Collections []string `json:"collections"`
	Articles    []string `json:"articles"`
}

// pass2Response is the JSON structure returned by the LLM in Pass 2.
type pass2Response struct {
	Articles []string `json:"articles"`
}

// WorkspacePopulate runs the two-pass LLM selection for workspace population.
func WorkspacePopulate(
	ctx context.Context,
	cfg config.Config,
	req PopulateRequest,
	collections []PopulateCollection,
	articles []PopulateArticle,
	flashLookup func(slug string) string, // returns flash text for a slug, or ""
) (PopulateResult, error) {
	profileName := req.Profile
	if profileName == "" {
		profileName = cfg.WorkspacePopulateProfileName()
	}
	prof, err := lookupProfile(cfg, profileName)
	if err != nil {
		return PopulateResult{}, fmt.Errorf("profile: %w", err)
	}
	p, err := llm.New(llm.ProviderConfig{
		Provider: prof.Provider,
		Model:    prof.Model,
		Host:     prof.Host,
		APIKey:   resolveAPIKey(prof.Provider),
	})
	if err != nil {
		return PopulateResult{}, fmt.Errorf("llm provider: %w", err)
	}

	clog.Info("workspace populate started",
		"workspace", req.WorkspaceName,
		"profile", profileName,
		"model", prof.Model,
		"collections", len(collections),
		"articles", len(articles),
	)

	// --- Pass 1: Shortlist from titles ---
	var pass1Collections []string
	if req.Progress != nil {
		if req.IncludeCollections {
			req.Progress(fmt.Sprintf("pass 1: shortlisting from %d collections, %d articles (model: %s)...", len(collections), len(articles), prof.Model))
		} else {
			req.Progress(fmt.Sprintf("pass 1: shortlisting from %d articles (model: %s)...", len(articles), prof.Model))
		}
	}

	var pass1Sys string
	if req.IncludeCollections {
		pass1Sys = cfg.WorkspacePopulatePass1WithCollectionsPrompt()
	} else {
		pass1Sys = cfg.WorkspacePopulatePass1Prompt()
	}

	pass1Prompt := buildPass1Prompt(req.WorkspaceName, req.WorkspaceDescription, req.Hint, req.IncludeCollections, collections, articles)
	clog.Raw("populate pass1 system prompt", pass1Sys)
	clog.Raw("populate pass1 user prompt", pass1Prompt)

	pass1Resp, pass1Usage, err := p.Chat(ctx, pass1Sys, []llm.Message{
		{Role: llm.RoleUser, Content: pass1Prompt},
	})
	if err != nil {
		return PopulateResult{}, fmt.Errorf("pass 1 llm: %w", err)
	}
	totalIn := pass1Usage.InputTokens
	totalOut := pass1Usage.OutputTokens
	clog.Raw("populate pass1 response", pass1Resp)

	var p1 pass1Response
	if err := parseJSON(pass1Resp, &p1); err != nil {
		return PopulateResult{}, fmt.Errorf("pass 1 parse: %w", err)
	}

	if req.IncludeCollections {
		pass1Collections = p1.Collections
	}

	clog.Info("pass 1 complete",
		"collections_selected", len(pass1Collections),
		"article_candidates", len(p1.Articles),
	)
	if req.Progress != nil {
		if req.IncludeCollections {
			req.Progress(fmt.Sprintf("pass 1: selected %d collections, %d article candidates", len(pass1Collections), len(p1.Articles)))
		} else {
			req.Progress(fmt.Sprintf("pass 1: shortlisted %d article candidates", len(p1.Articles)))
		}
	}

	// --- Pass 2: Refine with flash summaries ---
	var candidates []PopulateCandidate
	for _, slug := range p1.Articles {
		flash := flashLookup(slug)
		if flash == "" {
			clog.Debug("pass 2: no flash for candidate, including with title only", "slug", slug)
		}
		candidates = append(candidates, PopulateCandidate{Slug: slug, Flash: flash})
	}

	if len(candidates) == 0 {
		clog.Info("pass 2 skipped: no article candidates")
		return PopulateResult{
			Collections:  pass1Collections,
			Articles:     nil,
			InputTokens:  totalIn,
			OutputTokens: totalOut,
		}, nil
	}

	if req.Progress != nil {
		req.Progress(fmt.Sprintf("pass 2: refining %d candidates with flash summaries...", len(candidates)))
	}

	pass2Sys := cfg.WorkspacePopulatePass2Prompt()
	pass2Prompt := buildPass2Prompt(req.WorkspaceName, req.WorkspaceDescription, req.Hint, pass1Collections, candidates)
	clog.Raw("populate pass2 system prompt", pass2Sys)
	clog.Raw("populate pass2 user prompt", pass2Prompt)

	pass2Resp, pass2Usage, err := p.Chat(ctx, pass2Sys, []llm.Message{
		{Role: llm.RoleUser, Content: pass2Prompt},
	})
	if err != nil {
		return PopulateResult{}, fmt.Errorf("pass 2 llm: %w", err)
	}
	totalIn += pass2Usage.InputTokens
	totalOut += pass2Usage.OutputTokens
	clog.Raw("populate pass2 response", pass2Resp)

	var p2 pass2Response
	if err := parseJSON(pass2Resp, &p2); err != nil {
		return PopulateResult{}, fmt.Errorf("pass 2 parse: %w", err)
	}

	clog.Info("pass 2 complete", "articles_selected", len(p2.Articles))
	if req.Progress != nil {
		req.Progress(fmt.Sprintf("pass 2: selected %d articles", len(p2.Articles)))
	}

	return PopulateResult{
		Collections:  pass1Collections,
		Articles:     p2.Articles,
		InputTokens:  totalIn,
		OutputTokens: totalOut,
	}, nil
}


func buildPass1Prompt(name, description, hint string, includeCollections bool, collections []PopulateCollection, articles []PopulateArticle) string {
	var sb strings.Builder

	sb.WriteString("## Workspace\n")
	sb.WriteString(fmt.Sprintf("Name: %s\n", name))
	sb.WriteString(fmt.Sprintf("Purpose: %s\n", description))
	if hint != "" {
		sb.WriteString(fmt.Sprintf("Guidance: %s\n", hint))
	}
	sb.WriteString("\n")

	if includeCollections && len(collections) > 0 {
		sb.WriteString("## Available Collections\n\n")
		for _, c := range collections {
			sb.WriteString(fmt.Sprintf("### %s (%d articles)\n", c.Slug, len(c.Titles)))
			if c.Description != "" {
				sb.WriteString(fmt.Sprintf("Description: %s\n", c.Description))
			}
			for _, t := range c.Titles {
				sb.WriteString(fmt.Sprintf("- %s\n", t))
			}
			sb.WriteString("\n")
		}
	}

	sb.WriteString("## Available Articles\n\n")
	for _, a := range articles {
		sb.WriteString(fmt.Sprintf("- %s | %s\n", a.Slug, a.Title))
	}

	return sb.String()
}

func buildPass2Prompt(name, description, hint string, selectedCollections []string, candidates []PopulateCandidate) string {
	var sb strings.Builder

	sb.WriteString("## Workspace\n")
	sb.WriteString(fmt.Sprintf("Name: %s\n", name))
	sb.WriteString(fmt.Sprintf("Purpose: %s\n", description))
	if hint != "" {
		sb.WriteString(fmt.Sprintf("Guidance: %s\n", hint))
	}
	sb.WriteString("\n")

	if len(selectedCollections) > 0 {
		sb.WriteString("## Already Selected Collections\n")
		for _, c := range selectedCollections {
			sb.WriteString(fmt.Sprintf("- %s\n", c))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("## Candidate Articles\n\n")
	for _, c := range candidates {
		sb.WriteString(fmt.Sprintf("### %s\n", c.Slug))
		if c.Flash != "" {
			sb.WriteString(c.Flash)
		} else {
			sb.WriteString("(no flash summary available)")
		}
		sb.WriteString("\n\n")
	}

	return sb.String()
}

// parseJSON extracts JSON from an LLM response that may contain markdown fences.
func parseJSON(resp string, v any) error {
	resp = strings.TrimSpace(resp)
	// Strip markdown code fences if present.
	if idx := strings.Index(resp, "{"); idx > 0 {
		resp = resp[idx:]
	}
	if idx := strings.LastIndex(resp, "}"); idx >= 0 {
		resp = resp[:idx+1]
	}
	return json.Unmarshal([]byte(resp), v)
}

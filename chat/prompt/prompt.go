// Package prompt assembles the system prompt for workspace chat sessions.
// The prompt has four parts, concatenated in order:
//
//  1. system.txt — workspace persona (user-authored, optional)
//  2. BASE block — always present, instructs tool usage and attribution
//  3. MODE block — one of three grounding-mode policies
//  4. Corpus map — the article inventory built by corpusmap.Build
package prompt

import "strings"

// Grounding mode constants.
const (
	ModeCorpusOnly  = "corpus-only"
	ModeCorpusFirst = "corpus-first"
	ModeOpen        = "open"
)

// DefaultMode is the grounding mode used when none is configured.
const DefaultMode = ModeCorpusFirst

// ValidMode returns true if mode is a recognized grounding mode.
func ValidMode(mode string) bool {
	switch mode {
	case ModeCorpusOnly, ModeCorpusFirst, ModeOpen:
		return true
	}
	return false
}

// baseBlock is always present in the system prompt.
const baseBlock = `You are the chat assistant for the workspace described below. The user has collected a set of articles on this topic and wants to learn from them in conversation with you.

ABOUT THE ARTICLE LIST BELOW
The list shows every article in this workspace: its ID in [brackets], its title, and a short overview (a "flash"). The flashes are for orientation only — they tell you what exists and roughly what each article covers. They are NOT the articles' content.

RULES FOR USING ARTICLES
- Never quote, closely paraphrase, or present specific claims from an article based on its flash alone. If you need what an article actually says, fetch it first with read_article.
- read_article returns the article's summary by default. The summary is usually enough. Request level "body" only when the summary lacks the specific detail you need (exact figures, code, precise wording).
- Use search_workspace when you are looking for a specific passage or term and are not sure which article contains it.
- If several articles are relevant, you may fetch them in parallel.
- If the user asks about something no article covers, check the list again, then say so plainly. Never invent article content.

LABELING SOURCES
Every substantive claim in your answers must be traceable. Mark:
- claims from workspace articles: name the article, e.g. (from "Understanding VACUUM")
- claims from your own general knowledge: e.g. (general knowledge)
- claims from the web: cite the URL
When your general knowledge disagrees with an article, say so explicitly — this is valuable to the user, not a problem to smooth over.

STYLE
Personal notes are never shown to you and require no handling. Be direct. When you fetched something to answer, it is fine to mention what you read.`

const modeCorpusOnly = `SOURCE POLICY: CORPUS ONLY
Answer strictly from the content of this workspace's articles.
- If the articles do not cover the question, say "this isn't covered in your articles" — then stop. Do not fill the gap from general knowledge.
- You may use general knowledge only to interpret and explain article content, never to add facts beyond it.
- You may suggest which existing article is most likely to help.
Available tools: read_article, search_workspace.`

const modeCorpusFirst = `SOURCE POLICY: CORPUS FIRST
Ground your answers in the workspace articles first, then extend with your general knowledge where it helps.
- When the articles and your knowledge conflict, present the article's position first and clearly note the disagreement.
- Before answering from general knowledge alone, check whether a workspace article covers the topic — the list below tells you.
- You may also search the user's wider article library (search_library) when the workspace doesn't cover something but the user may have saved relevant material elsewhere. Tell the user when a library article outside this workspace looks relevant.
Available tools: read_article, search_workspace, search_library.`

const modeOpen = `SOURCE POLICY: OPEN
Use every source available: workspace articles, the user's wider library, your general knowledge, and the web.
- Still check the workspace first — it defines what the user has already read and cares about.
- Use web_search for anything recent, fast-moving, or absent from the corpus. Always cite URLs for web-sourced claims.
- When a web result looks like something worth keeping, mention that the user may want to save it.
Available tools: read_article, search_workspace, search_library, web_search.`

// CapHitNudge is injected alongside the final tool results when the
// per-turn tool-round cap is reached. It is NOT part of the system prompt.
const CapHitNudge = `Tool budget for this turn is exhausted. Answer now with what you have. If something you went looking for is still missing, say explicitly what you could not verify.`

// ModeBlock returns the grounding-mode instruction block for the given mode.
// Unknown modes fall back to corpus-first.
func ModeBlock(mode string) string {
	switch mode {
	case ModeCorpusOnly:
		return modeCorpusOnly
	case ModeCorpusFirst:
		return modeCorpusFirst
	case ModeOpen:
		return modeOpen
	default:
		return modeCorpusFirst
	}
}

// AssembleSystemPrompt builds the full system prompt from its four parts.
// persona is the user-authored system.txt content (may be empty).
// mode is the grounding mode ("corpus-only", "corpus-first", "open").
// corpusMap is the article inventory text from corpusmap.Build.
func AssembleSystemPrompt(persona, mode, corpusMap string) string {
	parts := make([]string, 0, 4)
	if p := strings.TrimSpace(persona); p != "" {
		parts = append(parts, p)
	}
	parts = append(parts, baseBlock)
	parts = append(parts, ModeBlock(mode))
	if cm := strings.TrimSpace(corpusMap); cm != "" {
		parts = append(parts, cm)
	}
	return strings.Join(parts, "\n\n")
}

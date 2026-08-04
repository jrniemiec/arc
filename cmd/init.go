package cmd

import (
	"bufio"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jrniemiec/arc/config"
	"github.com/jrniemiec/arc/internal/clog"
)

//go:embed agent_config.jsonc
var agentConfigTemplate []byte

func init() {
	initCmd.PersistentPreRunE = func(*cobra.Command, []string) error { return nil }
	rootCmd.AddCommand(initCmd)
}

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize arc — guided setup wizard",
	Long: `Init creates the arc data directory and walks you through configuring
LLM providers, ingest pipeline, chat modes, and the optional agent.

Re-running arc init on an existing config will offer to overwrite it.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		w := &wizard{
			out: cmd.OutOrStdout(),
			r:   bufio.NewReader(os.Stdin),
			tty: isTTY(os.Stdout),
		}
		return w.run()
	},
}

// wizard holds the state for the interactive init flow.
type wizard struct {
	out io.Writer
	r   *bufio.Reader
	tty bool

	// collected state
	dataRoot  string
	providers []string // "openai", "anthropic", "ollama"
}

// profileChoice is a numbered option in a model selection menu.
type profileChoice struct {
	name    string
	profile config.Profile
}

func (w *wizard) run() error {
	w.print("\n")
	w.printBold("Welcome to arc — a terminal-first personal knowledge OS.\n\n")
	w.print("arc ingests articles, generates summaries and flashcards, indexes\n")
	w.print("everything for search, and lets you chat with your knowledge base.\n")

	// ── Step 1: Data directory ──────────────────────────────────────
	if err := w.stepDataDir(); err != nil {
		return err
	}

	cfgPath := filepath.Join(w.dataRoot, "config.jsonc")
	logPath := filepath.Join(w.dataRoot, "arc.log")

	// Ensure data root exists so we can initialize logging.
	if err := os.MkdirAll(w.dataRoot, 0755); err != nil {
		return fmt.Errorf("create %s: %w", w.dataRoot, err)
	}
	clog.Init(logPath, slog.LevelInfo, verboseFlag)
	clog.Info("arc init started", "data_root", w.dataRoot)

	// Check for existing config
	if _, err := os.Stat(cfgPath); err == nil {
		w.print("\narc: config already exists: %s\n", cfgPath)
		ans := w.prompt("Overwrite? [y/N]: ")
		if strings.ToLower(strings.TrimSpace(ans)) != "y" {
			clog.Info("arc init aborted — user kept existing config")
			w.print("arc: keeping existing config — edit it directly or delete and re-run arc init\n")
			return nil
		}
		clog.Info("arc init overwriting existing config", "path", cfgPath)
	}

	// ── Step 2: Providers ───────────────────────────────────────────
	if err := w.stepProviders(); err != nil {
		return err
	}
	clog.Info("arc init providers selected", "providers", strings.Join(w.providers, ","))

	// ── Step 3: Ingest pipeline ─────────────────────────────────────
	cfg := config.Default()
	cfg.DataRoot = w.dataRoot
	cfg.ArticlesRoot = filepath.Join(w.dataRoot, "articles")
	cfg.DBPath = filepath.Join(w.dataRoot, "arc.db")
	cfg.VectorPath = filepath.Join(w.dataRoot, "index")
	cfg.EventsPath = filepath.Join(w.dataRoot, "events.jsonl")
	cfg.AgentPath = filepath.Join(w.dataRoot, "agent")
	cfg.LogPath = logPath

	if err := w.stepIngest(&cfg); err != nil {
		return err
	}
	clog.Info("arc init ingest configured",
		"summary_profile", cfg.Ingest.SummaryProfile,
		"summary_style", cfg.Ingest.SummaryStyle,
		"flash_profile", cfg.Ingest.FlashProfile,
		"flashcards", cfg.Ingest.Flashcards,
		"flashcard_profile", cfg.Ingest.FlashcardProfile,
		"flashcard_style", cfg.Ingest.FlashcardStyle,
		"embed_profile", cfg.Ingest.EmbedProfile,
	)

	// ── Step 4: Chat ────────────────────────────────────────────────
	if err := w.stepChat(&cfg); err != nil {
		return err
	}
	clog.Info("arc init chat configured",
		"chat_profile", cfg.Chat.Profile,
		"grounding_mode", cfg.Chat.GroundingMode,
		"article_chat_profile", cfg.ArticleChat.Profile,
		"askx_profile", cfg.AskX.Profile,
	)

	// ── Write config ────────────────────────────────────────────────
	// Create the full directory skeleton — the structure itself
	// documents arc's capabilities to the user.
	dirs := []string{
		w.dataRoot,
		cfg.ArticlesRoot,                          // ~/.arc/articles/
		filepath.Join(w.dataRoot, "collections"),   // ~/.arc/collections/
		filepath.Join(w.dataRoot, "workspaces"),    // ~/.arc/workspaces/
		cfg.AgentPath,                              // ~/.arc/agent/
		filepath.Join(w.dataRoot, "cookies"),        // ~/.arc/cookies/
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
	}
	clog.Info("arc init directories created", "dirs", strings.Join(dirs, ", "))

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("serialize config: %w", err)
	}
	if err := os.WriteFile(cfgPath, data, 0644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	clog.Info("arc init config written", "path", cfgPath)
	clog.Raw("arc init config.jsonc", string(data))
	w.print("\n")
	w.printGreen("Config written: %s\n", cfgPath)

	// ── Step 5: Agent (optional) ────────────────────────────────────
	w.stepAgent(cfg.AgentPath)

	// ── Done ────────────────────────────────────────────────────────
	clog.Info("arc init complete")
	w.printSection("Done")
	w.print("You can edit the config file directly at any time.\n")
	w.print("Run %s to view active settings.\n", w.boldText("arc config"))
	w.print("Run %s to see all available LLM profiles.\n", w.boldText("arc profiles"))
	w.print("\nFor paywalled sites (Medium, Substack, etc.), see %s in config.jsonc.\n", w.boldText("cookie_jars"))
	w.print("\nTry it:\n")
	w.print("  arc ingest https://example.com/article\n")
	w.print("  arc                     # launch the TUI\n\n")
	return nil
}

// ── Step 1: Data directory ──────────────────────────────────────────────

func (w *wizard) stepDataDir() error {
	w.printSection("Step 1: Data directory")
	w.print("Where should arc store its data?\n")
	w.print("  [1] ~/.arc (default)\n")
	w.print("  [2] Custom path\n\n")

	ans := w.prompt("> ")
	ans = strings.TrimSpace(ans)

	if ans == "" || ans == "1" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("get home dir: %w", err)
		}
		w.dataRoot = filepath.Join(home, ".arc")
	} else if ans == "2" {
		path := w.prompt("Path: ")
		path = strings.TrimSpace(path)
		if path == "" {
			return fmt.Errorf("no path provided")
		}
		// Expand ~ manually
		if strings.HasPrefix(path, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("get home dir: %w", err)
			}
			path = filepath.Join(home, path[2:])
		}
		w.dataRoot = path
	} else {
		return fmt.Errorf("invalid choice: %s", ans)
	}

	w.print("\n  → %s\n", w.dataRoot)
	return nil
}

// ── Step 2: Providers ───────────────────────────────────────────────────

func (w *wizard) stepProviders() error {
	w.printSection("Step 2: LLM Providers")
	w.print("arc uses LLMs for summarization, flashcard generation, chat, and\n")
	w.print("article filtering. You need at least one provider configured.\n\n")
	w.print("Which providers do you have access to?\n")
	w.print("  [1] OpenAI      — needs OPENAI_API_KEY\n")
	w.print("  [2] Anthropic   — needs ANTHROPIC_API_KEY\n")
	w.print("  [3] Ollama      — local, no key needed\n\n")

	ans := w.prompt("Select all that apply (e.g. 1,2): ")
	ans = strings.TrimSpace(ans)
	if ans == "" {
		return fmt.Errorf("no providers selected — arc needs at least one LLM provider")
	}

	providerMap := map[string]string{"1": "openai", "2": "anthropic", "3": "ollama"}
	seen := map[string]bool{}
	for _, part := range strings.Split(ans, ",") {
		part = strings.TrimSpace(part)
		prov, ok := providerMap[part]
		if !ok {
			return fmt.Errorf("invalid choice: %s", part)
		}
		if !seen[prov] {
			w.providers = append(w.providers, prov)
			seen[prov] = true
		}
	}
	w.print("\n")

	// Check env vars
	if seen["openai"] {
		if k := os.Getenv("OPENAI_API_KEY"); k != "" {
			w.printGreen("  ✓ OPENAI_API_KEY    — found\n")
		} else if k := os.Getenv("ARC_OPENAI_API_KEY"); k != "" {
			w.printGreen("  ✓ ARC_OPENAI_API_KEY — found\n")
		} else {
			w.printYellow("  ✗ OPENAI_API_KEY    — not set\n")
			w.print("    → add to ~/.zshrc: export OPENAI_API_KEY=\"sk-...\"\n")
		}
	}
	if seen["anthropic"] {
		if k := os.Getenv("ANTHROPIC_API_KEY"); k != "" {
			w.printGreen("  ✓ ANTHROPIC_API_KEY — found\n")
		} else if k := os.Getenv("ARC_ANTHROPIC_API_KEY"); k != "" {
			w.printGreen("  ✓ ARC_ANTHROPIC_API_KEY — found\n")
		} else {
			w.printYellow("  ✗ ANTHROPIC_API_KEY — not set\n")
			w.print("    → add to ~/.zshrc: export ANTHROPIC_API_KEY=\"sk-ant-...\"\n")
		}
	}
	if seen["ollama"] {
		w.printGreen("  ✓ Ollama            — no key needed\n")
	}

	// Provider-specific warnings
	w.print("\n")
	if !seen["openai"] {
		w.printDim("  Note: without OpenAI, semantic search is unavailable (embedding\n")
		w.printDim("  requires text-embedding-3-small). Full-text search (FTS5) still works.\n")
		w.printDim("  Summary fallback also requires OpenAI.\n\n")
	}
	if !seen["anthropic"] {
		w.printDim("  Note: without Anthropic, web search tools in chat won't be available.\n")
		w.printDim("  Default agent filter profile (haiku) must be changed to an available model.\n\n")
	}
	if len(w.providers) == 1 && w.providers[0] == "ollama" {
		w.printYellow("  Warning: Ollama-only mode has limitations:\n")
		w.printYellow("  - Tool calling not supported — workspace chat becomes plain conversation\n")
		w.printYellow("  - Agent feed filtering won't work (requires tool calling)\n")
		w.printYellow("  - No semantic search (requires OpenAI for embeddings)\n\n")
	}

	w.print("Model lists in the following steps are filtered to your selected providers.\n")
	w.print("You can add more providers later by editing ~/.arc/config.jsonc.\n")
	return nil
}

// ── Step 3: Ingest ──────────────────────────────────────────────────────

func (w *wizard) stepIngest(cfg *config.Config) error {
	w.printSection("Step 3: Ingestion pipeline")
	w.print("When you ingest an article, arc runs this pipeline:\n")
	w.print("  extract → summarize → flash summary → [flashcards] → embed\n\n")
	w.print("Each phase has different LLM requirements.\n")

	// ── Summarize model ─────────────────
	w.printSubSection("Summarize")
	w.print("Generates detailed summaries of articles. Needs good comprehension\n")
	w.print("and writing quality. Longer outputs — cost scales with article length.\n")
	w.print("Recommendation: a mid-tier model balances cost and quality.\n\n")

	summaryProfile := w.pickModel("oai-mini", "haiku")
	cfg.Ingest.SummaryProfile = summaryProfile

	// ── Summary style ───────────────────
	w.print("\nSummary style:\n")
	w.print("  [1] study-notes — structured notes with key concepts (default)\n")
	w.print("  [2] bullets     — concise bullet points\n")
	w.print("  [3] technical   — detailed technical analysis\n")
	w.print("  [4] executive   — high-level executive summary\n\n")

	styleAns := w.prompt("> ")
	styleAns = strings.TrimSpace(styleAns)
	styles := map[string]string{"": "study-notes", "1": "study-notes", "2": "bullets", "3": "technical", "4": "executive"}
	if s, ok := styles[styleAns]; ok {
		cfg.Ingest.SummaryStyle = s
	} else {
		cfg.Ingest.SummaryStyle = "study-notes"
	}
	w.print("  → %s\n", cfg.Ingest.SummaryStyle)

	// ── Flash model ─────────────────────
	w.printSubSection("Flash summary")
	w.print("A 2-3 sentence digest of the article for quick scanning.\n")
	w.print("Small output — even cheap models do well here.\n\n")

	flashProfile := w.pickModel("oai-mini", "haiku")
	cfg.Ingest.FlashProfile = flashProfile

	// ── Flashcard model ─────────────────
	w.printSubSection("Flashcards")
	w.print("Generates question/answer pairs for active recall.\n")
	w.print("Needs structured JSON output — some models handle this better.\n")
	w.print("Recommendation: models with strong instruction following.\n\n")

	// Generating a deck for every article costs a call per ingest, so this is
	// off unless asked for. The model and style below apply either way — they
	// are also what 'arc flashcards <slug>' uses on demand.
	w.print("If you answer no, you can still make them per article with\n")
	w.print("'arc ingest --flashcards' or 'arc flashcards <slug> --write'.\n\n")
	fcOn := w.prompt("Generate on every ingest? [y/N]: ")
	cfg.Ingest.Flashcards = strings.ToLower(strings.TrimSpace(fcOn)) == "y"
	if cfg.Ingest.Flashcards {
		w.print("  → on for every ingest\n\n")
	} else {
		w.print("  → on request only\n\n")
	}

	flashcardProfile := w.pickModel("oai-mini", "haiku")
	cfg.Ingest.FlashcardProfile = flashcardProfile

	// ── Flashcard style ─────────────────
	w.print("\nFlashcard style:\n")
	w.print("  [1] socratic — open-ended questions that provoke thinking (default)\n")
	w.print("  [2] cloze    — fill-in-the-blank format\n\n")

	fcAns := w.prompt("> ")
	fcAns = strings.TrimSpace(fcAns)
	if fcAns == "2" {
		cfg.Ingest.FlashcardStyle = "cloze"
	} else {
		cfg.Ingest.FlashcardStyle = "socratic"
	}
	w.print("  → %s\n", cfg.Ingest.FlashcardStyle)

	// ── Embedding note ──────────────────
	hasOpenAI := w.hasProvider("openai")
	if hasOpenAI {
		cfg.Ingest.EmbedProfile = "oai-embed"
		w.print("\n")
		w.printDim("Embedding: using OpenAI text-embedding-3-small for semantic search.\n")
	} else {
		cfg.Ingest.EmbedProfile = ""
		w.print("\n")
		w.printDim("Embedding: skipped (requires OpenAI). Full-text search (FTS5) still works.\n")
		w.printDim("You can enable semantic search later by adding OPENAI_API_KEY.\n")
	}

	return nil
}

// ── Step 4: Chat ────────────────────────────────────────────────────────

func (w *wizard) stepChat(cfg *config.Config) error {
	w.printSection("Step 4: Chat")
	w.print("arc has three chat modes, each with different use cases.\n")

	// ── Workspace chat ──────────────────
	w.printSubSection("Workspace chat")
	w.print("Multi-turn research sessions grounded in your full knowledge base.\n")
	w.print("The LLM can search, read, and cross-reference your articles using tools.\n")
	w.print("Longer conversations — benefits from a capable model.\n\n")

	chatProfile := w.pickModel("oai-mini", "haiku")
	cfg.Chat.Profile = chatProfile

	// ── Grounding mode ──────────────────
	w.print("\nGrounding mode:\n")
	w.print("  [1] corpus-first — prefers your articles, falls back to model knowledge (default)\n")
	w.print("  [2] corpus-only  — only answers from your articles\n")
	w.print("  [3] open         — unconstrained\n\n")

	gmAns := w.prompt("> ")
	gmAns = strings.TrimSpace(gmAns)
	modes := map[string]string{"": "corpus-first", "1": "corpus-first", "2": "corpus-only", "3": "open"}
	if m, ok := modes[gmAns]; ok {
		cfg.Chat.GroundingMode = m
	} else {
		cfg.Chat.GroundingMode = "corpus-first"
	}
	w.print("  → %s\n", cfg.Chat.GroundingMode)

	// ── Article chat ────────────────────
	w.printSubSection("Article chat")
	w.print("Discuss a single article in depth. The article's summary is included\n")
	w.print("as context in the system prompt. Good for asking questions, clarifying\n")
	w.print("concepts, or exploring ideas from something you just read.\n\n")

	articleProfile := w.pickModel("oai-mini", "haiku")
	cfg.ArticleChat.Profile = articleProfile

	// ── AskX ────────────────────────────
	w.printSubSection("AskX")
	w.print("A lightweight chat mode for quick questions. Keeps conversation\n")
	w.print("history across turns. Great for lookups, follow-up questions, and\n")
	w.print("exploring topics without the full workspace chat overhead.\n\n")

	askxProfile := w.pickModel("oai-mini", "haiku")
	cfg.AskX.Profile = askxProfile

	return nil
}

// ── Step 5: Agent ───────────────────────────────────────────────────────

func (w *wizard) stepAgent(agentPath string) {
	w.printSection("Step 5: Agent (optional)")
	w.print("arc has an autonomous agent that polls RSS/Atom feeds, filters\n")
	w.print("articles against your interests, and ingests the good ones.\n\n")

	ans := w.prompt("Set up the agent now? [y/N]: ")
	if strings.ToLower(strings.TrimSpace(ans)) != "y" {
		clog.Info("arc init agent setup skipped")
		w.print("\n  Skipped. You can set up the agent later by editing\n")
		w.print("  %s\n", filepath.Join(agentPath, "config.jsonc"))
		return
	}

	// Write template
	if err := os.MkdirAll(agentPath, 0755); err != nil {
		w.print("\n  Error creating agent directory: %v\n", err)
		return
	}

	agentCfgPath := filepath.Join(agentPath, "config.jsonc")
	if err := os.WriteFile(agentCfgPath, agentConfigTemplate, 0644); err != nil {
		w.print("\n  Error writing agent config: %v\n", err)
		return
	}
	clog.Info("arc init agent config template written", "path", agentCfgPath)

	w.print("\nOpening agent config in your editor...\n")
	w.print("  → describe your interests, add RSS/Atom feed URLs\n")
	w.print("  → save and close when done\n\n")

	editor := editorCmd()
	cmd := exec.Command(editor, agentCfgPath)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		clog.Warn("arc init agent editor error", "err", err)
		w.print("  Editor exited with error: %v\n", err)
		w.print("  You can edit %s manually.\n", agentCfgPath)
		return
	}
	// Log the saved agent config for troubleshooting.
	if saved, err := os.ReadFile(agentCfgPath); err == nil {
		clog.Raw("arc init agent config.jsonc", string(saved))
	}
}

// ── Model picker ────────────────────────────────────────────────────────

// pickModel shows a numbered list of profiles filtered to selected providers.
// For Ollama-only, prompts for a free-text model name.
// defaultOAI is the default profile name when OpenAI is available.
// defaultAnthropic is the default when only Anthropic is available.
func (w *wizard) pickModel(defaultOAI, defaultAnthropic string) string {
	builtin := config.BuiltinProfiles()

	// Collect profiles for selected providers (skip embedding profiles)
	var choices []profileChoice
	for name, p := range builtin {
		if name == "oai-embed" {
			continue
		}
		if w.hasProvider(p.Provider) {
			choices = append(choices, profileChoice{name: name, profile: p})
		}
	}

	// Sort: by cost tier ascending, then name
	tierOrder := map[string]int{"local": 0, "very_low": 1, "low": 2, "medium": 3, "high": 4, "premium": 5}
	sort.Slice(choices, func(i, j int) bool {
		ti := tierOrder[choices[i].profile.Info.CostTier]
		tj := tierOrder[choices[j].profile.Info.CostTier]
		if ti != tj {
			return ti < tj
		}
		return choices[i].name < choices[j].name
	})

	// Ollama-only: free text input
	if len(w.providers) == 1 && w.providers[0] == "ollama" {
		ans := w.prompt("Ollama model name (e.g. llama3.1:8b): ")
		ans = strings.TrimSpace(ans)
		if ans == "" {
			ans = "llama3.1:8b"
		}
		// Find matching builtin or return the first ollama profile
		for _, c := range choices {
			if c.profile.Model == ans {
				w.print("  → %s (%s)\n", c.name, c.profile.Model)
				return c.name
			}
		}
		// Not a builtin — use "llama" profile name as closest match
		w.print("  → llama (custom: %s)\n", ans)
		return "llama"
	}

	// Determine default
	defaultProfile := defaultOAI
	if !w.hasProvider("openai") {
		defaultProfile = defaultAnthropic
	}

	// Print choices
	for i, c := range choices {
		pricing := ""
		if c.profile.Info.Pricing != nil {
			pricing = fmt.Sprintf("$%.2f/$%.2f per 1M tokens", c.profile.Info.Pricing.Input, c.profile.Info.Pricing.Output)
		} else {
			pricing = "free (local)"
		}
		def := ""
		if c.name == defaultProfile {
			def = " (default)"
		}
		label := fmt.Sprintf("  [%d] %-14s — %s%s", i+1, c.name, pricing, def)
		w.print("%s\n", label)
	}
	w.print("\n")

	ans := w.prompt("> ")
	ans = strings.TrimSpace(ans)

	if ans == "" {
		w.print("  → %s\n", defaultProfile)
		return defaultProfile
	}

	idx, err := strconv.Atoi(ans)
	if err == nil && idx >= 1 && idx <= len(choices) {
		chosen := choices[idx-1].name
		w.print("  → %s\n", chosen)
		return chosen
	}

	// Try matching by name
	for _, c := range choices {
		if c.name == ans {
			w.print("  → %s\n", c.name)
			return c.name
		}
	}

	w.print("  → %s (default)\n", defaultProfile)
	return defaultProfile
}

// ── Helpers ─────────────────────────────────────────────────────────────

func (w *wizard) hasProvider(name string) bool {
	for _, p := range w.providers {
		if p == name {
			return true
		}
	}
	return false
}

func (w *wizard) prompt(format string, args ...any) string {
	fmt.Fprintf(w.out, format, args...)
	line, _ := w.r.ReadString('\n')
	return strings.TrimRight(line, "\n\r")
}

func (w *wizard) print(format string, args ...any) {
	fmt.Fprintf(w.out, format, args...)
}

func (w *wizard) printSection(title string) {
	w.print("\n")
	if w.tty {
		w.print("%s── %s %s%s\n\n", ansiCode("bold"), title, strings.Repeat("─", max(0, 58-len(title))), ansiReset)
	} else {
		w.print("── %s %s\n\n", title, strings.Repeat("─", max(0, 58-len(title))))
	}
}

func (w *wizard) printSubSection(title string) {
	w.print("\n")
	if w.tty {
		w.print("%s─ %s ─%s\n", ansiCode("bold"), title, ansiReset)
	} else {
		w.print("─ %s ─\n", title)
	}
}

func (w *wizard) printBold(format string, args ...any) {
	if w.tty {
		fmt.Fprintf(w.out, ansiCode("bold")+format+ansiReset, args...)
	} else {
		fmt.Fprintf(w.out, format, args...)
	}
}

func (w *wizard) boldText(s string) string {
	if w.tty {
		return ansiCode("bold") + s + ansiReset
	}
	return s
}

func (w *wizard) printGreen(format string, args ...any) {
	if w.tty {
		fmt.Fprintf(w.out, ansiCode("green")+format+ansiReset, args...)
	} else {
		fmt.Fprintf(w.out, format, args...)
	}
}

func (w *wizard) printYellow(format string, args ...any) {
	if w.tty {
		fmt.Fprintf(w.out, ansiCode("yellow")+format+ansiReset, args...)
	} else {
		fmt.Fprintf(w.out, format, args...)
	}
}

func (w *wizard) printDim(format string, args ...any) {
	if w.tty {
		fmt.Fprintf(w.out, ansiCode("dim")+format+ansiReset, args...)
	} else {
		fmt.Fprintf(w.out, format, args...)
	}
}

// editorCmd returns the user's preferred editor.
func editorCmd() string {
	if e := os.Getenv("EDITOR"); e != "" {
		return e
	}
	if e := os.Getenv("VISUAL"); e != "" {
		return e
	}
	return "vi"
}

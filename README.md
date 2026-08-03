# arc

Your personal knowledge engine.

Arc is a personal knowledge OS that runs entirely from your terminal. Feed it articles, documents, PDFs, books — anything in digital form — and it distills them into summaries, audio-ready flash briefs, and flashcards. Search everything with full-text and semantic search. Chat with your knowledge using any LLM. Listen to it on the go with text-to-speech. Single install, keyboard-driven.

**A real UI, not just a CLI.** The primary interface is a full terminal UI — browse your library, read articles, manage collections, chat with workspaces, run searches, and play audio, all without leaving the terminal. A complete CLI sits underneath for scripting and automation.

**Articles, collections, workspaces.** Articles are the atomic unit — captured content with summaries, flashcards, and metadata. Collections group them by topic. Workspaces are research environments: attach articles and resources, then have a persistent conversation grounded in your curated material.

**An agent that reads for you.** Configure RSS/Atom feeds and an interest profile, and an autonomous agent filters and ingests the most relevant new content automatically. Your library grows while you sleep.

**Chat with your knowledge, your way.** Use OpenAI, Anthropic, or local Ollama models — choose the right model for each task. Three chat modes (quick queries, per-article deep dives, workspace conversations) and three grounding modes: pure RAG from your articles only, hybrid with LLM knowledge filling gaps, or fully open with live internet search.

**Everything is configurable.** Profiles, grounding modes, context strategies, summary styles, TTS voices — all accessible from both the TUI and the CLI.

## Installation

**Homebrew (macOS):**
```bash
brew install jrniemiec/arc/arc
```

**From source:**
```bash
git clone https://github.com/jrniemiec/arc.git
cd arc
make build              # builds to ./bin/arc
```

**Go install:**
```bash
go install github.com/jrniemiec/arc@latest
```

Requires Go 1.25+. Pure Go — no CGo dependencies.

## Quick start

```bash
arc init                            # guided setup wizard
arc ingest https://example.com/article
arc                                 # launch the TUI
```

### First run — `arc init`

`arc init` is a guided setup wizard that walks you through configuring arc:

1. Creates `~/.arc/` and `~/.arc/articles/`
2. Writes `~/.arc/config.jsonc` with all available LLM profiles and pricing notes
3. Prints API key setup instructions
4. Prompts you to review and edit the config
5. Validates the config before finishing

### Typical workflow

**1. Ingest an article**

```bash
arc ingest https://example.com/interesting-article
```

This runs the full pipeline: extract text, generate a summary, a flash summary (optimized for audio), flashcards, and a vector embedding for semantic search. The article lands in `~/.arc/articles/<date>-<slug>/`.

**2. Browse what you have**

```bash
arc list                            # show all articles
arc list --unread                   # only unread
arc read 20260801-interesting-article          # read the body
arc read 20260801-interesting-article --summary   # read the summary
arc read 20260801-interesting-article --flash     # read the flash summary
arc read 20260801-interesting-article --flashcards # review flashcards
```

**3. Search your knowledge base**

```bash
arc search "attention mechanisms"                # hybrid: FTS5 + semantic
arc search "attention mechanisms" --no-semantic   # keyword only
arc search "transformers" --collection ml-papers  # within a collection
```

**4. Organize into collections**

```bash
arc collections create ml-papers
arc collections add 20260801-transformer-survey ml-papers

# or let the LLM do it:
arc collections suggest --apply         # auto-create collections from your articles
arc collections assign --apply          # auto-assign uncollected articles
```

**5. Chat with your knowledge**

```bash
arc workspace new "attention research" "Survey of attention mechanisms"
arc workspace populate "attention research"   # LLM picks relevant articles
arc workspace chat "attention research"       # start a grounded conversation
```

**6. Set up the feed agent**

Configure feeds and interests in `~/.arc/agent/config.json`, then:

```bash
arc agent run                       # poll feeds, filter, ingest
arc agent run --dry-run             # preview what would be ingested
arc agent digest                    # readable summary of what was ingested
```

### Data root

By default arc stores everything under `~/.arc`. Override with:

| Method | Example |
|--------|---------|
| `--data-root` flag | `arc --data-root /data/arc list` |
| `ARC_HOME` env var | `export ARC_HOME=/data/arc` |

`--data-root` takes priority over `ARC_HOME`. There is also `--articles-root` to override just the articles location independently.

## How it works

Filesystem is the source of truth. SQLite and vector indexes are derived — rebuild anytime with `arc reindex`.

```
~/.arc/
  config.jsonc           # JSONC configuration
  arc.db                 # SQLite: metadata + FTS5 full-text index (derived)
  arc.log                # Application log
  events.jsonl           # Append-only cost tracking log
  index/                 # Vector store for semantic search
  articles/<slug>/       # One directory per article (flat, no nesting)
    body.txt
    meta.json
    source.url / source.html
    summary.<style>.<model>.txt
    flash.<model>.txt
    flashcards.<style>.<model>.json
  collections/<slug>/    # Symlinks to article directories
  agent/
    config.json          # Feed list + interest profile
    state/               # Per-feed GUID tracking
    runs.jsonl           # Agent run log
  workspaces/<name>/
    articles/            # Symlinks to articles
    collections/         # Symlinks to collections
    resources/           # Attached files, PDFs, notes
    chat/                # Chat history and config
    system.txt           # Custom system prompt
    outcomes/            # Generated output documents
```

### Multi-variant files

Articles can have multiple summary and flashcard variants, differing by style and model:

```
summary.study-notes.gpt-4o-mini.txt
summary.technical.claude-opus-4-6.txt
flashcards.socratic.claude-sonnet-4-6.json
```

The preferred variant is resolved at read time from `preferred_models` and `preferred_styles` in config. Change the config and the change applies to every article instantly — no per-article state.

## Ingestion

```
URL / file / stdin → extract → summarize → flash → flashcards → embed → index
```

Each stage is independent and composable via Unix pipes:

```bash
arc ingest https://example.com/article        # full pipeline
arc extract https://example.com/article       # extract text only (stdout)
arc summarize 20260521-article                # regenerate summary
arc flash 20260521-article                    # regenerate flash summary
arc flashcards 20260521-article               # regenerate flashcards
arc extract <url> | arc summarize --style bullets   # pipe extract into summarize
```

Reprocess existing articles (re-fetch, re-summarize, or both):

```bash
arc reprocess 20260521-article                # reprocess one article
arc reprocess --all                           # reprocess everything
arc reprocess --collection ml-papers          # reprocess a collection
arc reprocess --missing                       # only articles missing variants
```

### Supported sources

- **URLs** — with cookie jar support for paywalled sites
- **RSS/Atom feeds** — via the feed agent
- **PDFs** — text extraction via `pdftotext`
- **Plain text files** — direct passthrough
- **stdin** — `arc ingest -`

**Summary styles:** `study-notes` · `bullets` · `technical` · `executive`
**Flashcard styles:** `socratic` · `cloze`

Arc detects incomplete or paywalled content (teasers) and flags them for review. Cookie jars can be configured for paywalled sites (Medium, Substack, etc.) — see the Configuration section.

## Search

Hybrid search combining FTS5 full-text and vector semantic search:

```bash
arc search "attention mechanisms"                      # hybrid (FTS5 + vector)
arc search "attention mechanisms" --no-semantic         # FTS5 only
arc search "transformers" --collection ml-papers       # within a collection
arc search "golang" --tag programming --limit 50       # filter by tag, custom limit
```

Results show source badges: `[fts]`, `[vector]`, `[both]`.

## Collections

Collections group articles by topic. An article can belong to many collections.

```bash
arc collections create ml-papers               # create a collection
arc collections add <slug> ml-papers           # add an article
arc collections list                           # list all collections
arc collections show ml-papers                 # show collection details
arc collections delete ml-papers               # delete (keeps articles)

# LLM-assisted organization
arc collections suggest --apply                # auto-create collections from your library
arc collections assign --apply                 # auto-assign uncollected articles
```

Additional subcommands: `remove`, `read`, `search`, `rename`, `describe`, `generate-description`. Run `arc collections --help` for the full list.

## Workspaces

Workspaces are research environments with persistent chat, attached resources, and generated outcomes.

```bash
arc workspace new "attention research" "Survey of attention mechanisms"
arc workspace list                             # list workspaces
arc workspace show "attention research"        # show details
arc workspace describe "attention research" "Updated description"
arc workspace rename "attention research" "attn-survey"
arc workspace archive "attention research"     # hide but retain
arc workspace delete "attention research"      # permanent delete
```

### Adding and removing content

```bash
arc workspace add "attention research" --article <slug>
arc workspace add "attention research" --collection ml-papers
arc workspace add "attention research" --resource ./notes.pdf
arc workspace remove "attention research" --article <slug>
```

### LLM-assisted population

```bash
arc workspace populate "attention research"              # LLM picks relevant articles
arc workspace populate "attention research" --hint "focus on self-attention"
arc workspace populate "attention research" --include-collections
arc workspace populate "attention research" --dry-run    # preview without applying
```

### Workspace chat

```bash
arc workspace chat "attention research"                  # start a conversation
arc workspace chat "attention research" --clear          # clear history first
arc workspace chat "attention research" --strategy summarize  # compress old turns
```

Additional subcommands: `chat-config`, `system`, `outcomes`, `describe`, `rename`, `archive`. Run `arc workspace --help` for the full list.

## Chat

Three chat modes, all with streaming and tool use:

| Mode | Scope | Access |
|------|-------|--------|
| Workspace chat | Full knowledge base + tools | `arc workspace chat <name>` |
| Article chat | Single article context | TUI: press `c` on an article |
| AskX | Single-shot query, no history | TUI |

**Grounding modes:**
- `corpus-only` — answers only from your articles
- `corpus-first` — prefers your articles, falls back to model knowledge
- `open` — unconstrained

**Context strategies:** `tail` (last N turns) · `token-budget` (fit to window) · `summarize` (compress old turns via LLM)

**Tools available to the LLM during chat:**
`search_articles` · `read_article` · `list_articles`

## Agent

Autonomous feed ingestion. Polls configured RSS/Atom feeds, filters items against an interest profile using an LLM, and ingests approved articles.

```bash
arc agent run                    # poll feeds + ingest
arc agent run --dry-run          # filter only, no ingestion
arc agent run --focus "LLMs"     # override focus for this run
arc agent run --decisions file   # re-run with user-overridden decisions
arc agent log                    # show recent runs
arc agent log -n 20              # show more runs
arc agent digest                 # human-readable digest of latest run
arc agent digest --summary       # include full summaries
arc agent digest --tts           # TTS-friendly output (no URLs, no unicode)
arc agent stats                  # per-feed signal/noise statistics
```

### Agent configuration

Configure feeds and interest profile in `~/.arc/agent/config.json`:

```jsonc
{
  "interest_profile": "I'm interested in distributed systems, ML infrastructure, and Go.",
  "focus": "",                        // temporary emphasis (or use --focus flag)
  "notes": [],                        // ad-hoc guidance messages
  "learning_goals": [                 // topics with depth levels
    { "topic": "transformers", "depth": "building" }
  ],
  "filter_profile": "haiku",          // LLM profile for relevance filtering
  "summary_profile": "haiku",         // LLM profile for summaries
  "languages": ["en"],                // ISO 639-1 codes (empty = all)
  "feeds": [
    {
      "url": "https://example.com/feed.xml",
      "name": "Example Blog",
      "filter": "only posts about Kubernetes",   // per-feed LLM instruction
      "tags": ["k8s"],                            // pre-filter tags
      "block_domains": ["ads.example.com"],       // reject these domains
      "disabled": false
    }
  ]
}
```

### Decision override

The agent saves its filter decisions to a file. You can review, edit, and re-run with your overrides:

```bash
arc agent run --dry-run           # writes decisions file
# edit the decisions file to change skip → ingest
arc agent run --decisions <file>  # re-run with your choices
```

## TUI

Launch with `arc` (no subcommand). The TUI is the primary interface — designed for keyboard-driven browsing, reading, and chatting.

**Views:** Library (articles) · Collections · Workspaces · Search · Agent · Stats

**Key bindings:**

| Key | Action |
|-----|--------|
| `j` / `k` | Navigate up/down |
| `Tab` | Cycle panes |
| `/` | Search |
| `Enter` | Select / open |
| `d` | Delete |
| `m` | Mark read/played |
| `f` | Favorite |
| `s` | Speak (TTS) |
| `c` | Chat (article chat) |
| `Ctrl+G` | Fix input typos via LLM |
| `?` | Show all key bindings |

Mouse support: click to navigate, middle-click to open in browser.

**Theming:** `--theme auto|light|dark`

Use `--no-tui` to disable the TUI and run in headless/CLI mode.

### Input correction

Press `Ctrl+G` in any chat input to fix typos and grammar via LLM. Configure the profile and prompt in `config.jsonc`:

```jsonc
{
  "correction_profile": "oai-mini",
  "correction_prompt": "Fix typos and grammar, preserve meaning."
}
```

## MCP server

Expose your knowledge base to Claude Desktop or Claude Code:

```bash
arc mcp              # stdio transport (for Claude Desktop/Code)
arc mcp --http :8080 # HTTP+SSE transport (daemon mode)
```

**Tools:** `search` · `read` · `list` · `get_stats`

Configure in Claude Desktop or Claude Code:

```json
{
  "mcpServers": {
    "arc": { "command": "arc", "args": ["mcp"] }
  }
}
```

## TTS

macOS text-to-speech via `say(1)`. Content is preprocessed: markdown stripped, code blocks removed, URLs filtered, soft-wrapped lines joined into paragraphs.

```jsonc
{ "tts_voice": "Samantha", "tts_rate": 200 }
```

Flash summaries are specifically optimized for audio playback — 3-5 sentences, no jargon, no URLs.

## Cost tracking

Every LLM API call is logged to `~/.arc/events.jsonl` with operation type, model, token counts, and cost in USD. View aggregated costs with:

```bash
arc stats               # includes cost breakdown by model and month
arc stats --json        # machine-readable output
```

## LLM providers

Three provider backends, assignable per operation via profiles:

| Provider | Examples | Notes |
|----------|----------|-------|
| OpenAI | gpt-4o-mini, gpt-4.1, gpt-5-mini | Default for bulk operations |
| Anthropic | claude-opus-4-6, claude-sonnet-4-6, claude-haiku-4-5 | Direct HTTP API |
| Ollama | llama3.1:8b, qwen2.5-coder:7b | Local, offline |

Embedding: OpenAI `text-embedding-3-small`.

### Current limitations

- **Semantic search** requires OpenAI (for embeddings). Without `OPENAI_API_KEY`, only full-text search (FTS5) is available.
- **Web search tools** in chat are Anthropic-only.
- **Ollama** does not support tool calling. Workspace chat tools (`search_articles`, `read_article`, `list_articles`) and agent feed filtering are unavailable with Ollama — chat works as a plain conversation without knowledge base access.

**Profiles** map a short name to provider + model + parameters:

```json
{
  "profiles": {
    "oai-mini": { "provider": "openai", "model": "gpt-4o-mini" },
    "opus":     { "provider": "anthropic", "model": "claude-opus-4-6" },
    "llama":    { "provider": "ollama", "model": "llama3.1:8b" }
  }
}
```

Assign profiles per operation:

```json
{
  "ingest": {
    "summary_profile": "oai-mini",
    "flash_profile": "oai-mini",
    "flashcard_profile": "oai-mini"
  },
  "chat": { "profile": "oai-mini" },
  "article_chat": { "profile": "haiku" },
  "askx": { "profile": "haiku" }
}
```

Run `arc profiles` to list all configured profiles with pricing info. Use `arc profiles --json` for machine-readable output.

## Configuration

`~/.arc/config.jsonc` supports JSONC (comments allowed). Run `arc config` to view the active configuration.

```jsonc
{
  "data_root": "~/.arc",

  "profiles": { /* ... */ },

  "ingest": {
    "summary_profile": "oai-mini",
    "flash_profile": "oai-mini",
    "flashcard_profile": "oai-mini",
    "summary_style": "study-notes",
    "flashcard_style": "socratic",
    "flashcards": false,
    "min_words": 300
  },

  // Variant preference — global, no per-article state
  "preferred_models": ["claude-opus-4-6", "claude-sonnet-4-6", "gpt-4.1"],
  "preferred_styles": ["study-notes", "bullets", "technical"],

  "chat": {
    "profile": "oai-mini",
    "strategy": "tail",
    "grounding_mode": "corpus-first",
    "context_limit": 0,
    "max_output_tokens": 0,
    "max_user_messages": 50,
    "summarizer_profile": "",
    "verbatim_ratio": 0.4
  },

  "article_chat": { "profile": "haiku" },
  "askx": { "profile": "haiku" },

  // Input correction (Ctrl+G in TUI)
  "correction_profile": "oai-mini",
  "correction_prompt": "",

  // Cookie jars for paywalled sites
  "cookie_jars": {
    "medium.com": "~/.arc/cookies/medium.txt"
  },

  // TTS
  "tts_voice": "",
  "tts_rate": 200
}
```

### Environment variables

| Variable | Required | Purpose |
|----------|----------|---------|
| `OPENAI_API_KEY` | For OpenAI models | API authentication |
| `ANTHROPIC_API_KEY` | For Anthropic models | API authentication |
| `OPENAI_BASE_URL` | No | Custom OpenAI-compatible endpoint |

### Global flags

These flags apply to all commands:

| Flag | Purpose |
|------|---------|
| `--config <path>` | Config file path (default: `~/.arc/config.jsonc`) |
| `--data-root <path>` | Arc data root (default: `~/.arc`) |
| `--articles-root <path>` | Articles directory override |
| `--json` | JSON output |
| `--no-tui` | Disable TUI, run in headless/CLI mode |
| `--log-level <level>` | Log level: `debug`, `info`, `warn`, `error` |
| `--verbose` | Print debug-level log output to stderr |
| `--theme <mode>` | Color theme: `auto`, `light`, `dark` |

## Building

```bash
make build            # build to ./bin/arc
make install          # build + install to ~/dev/bin/arc
make test             # run all tests
make fmt              # format code
make vet              # go vet
make clean            # remove ./bin/ and ./dist/
make dist VERSION=x.y.z  # build release tarballs
```

### System dependencies

- `pdftotext` — PDF text extraction (optional)
- `say` — macOS TTS (optional)

## CLI reference

Every command supports `--help` for full flag documentation. For a complete reference:

```bash
arc help cli-commands
```

## Project structure

```
cmd/           CLI commands (cobra)
service/       Business logic layer (shared by CLI, TUI, MCP)
library/       Knowledge base composition (fs + sqlite + events)
store/
  fs/          Filesystem article storage
  sqlite/      SQLite metadata + FTS5 index
  vector/      Semantic search vector store
  event/       Append-only event log
ingest/
  pipeline/    Extract → summarize → flash → flashcards → embed → index
  extractor/   URL/PDF/file content extraction
  feed/        RSS/Atom feed parsing
  embed/       Vector embedding generation
chat/
  engine/      Chat orchestration with tool use
  provider/    LLM providers (Anthropic, OpenAI, Ollama)
  strategy/    Context window management
  tools/       LLM-callable tools
  prompt/      System prompts and grounding modes
agent/         Autonomous feed ingestion agent
tui/           Terminal UI (Bubble Tea)
mcp/           Model Context Protocol server
tts/           Text-to-speech (macOS say)
config/        Configuration system
internal/      Logging, JSONC parsing
```

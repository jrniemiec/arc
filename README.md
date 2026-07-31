# arc

A terminal-first personal knowledge OS.

Ingest articles from URLs, RSS feeds, and local files. Generate summaries, flash summaries, and flashcards. Search everything with full-text and semantic search. Chat with your knowledge base. Play content back via TTS. All in a single, pure-Go binary.

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
arc init                            # create ~/.arc, write default config
arc ingest https://example.com/article
arc                                 # launch the TUI
```

### First run — `arc init`

`arc init` creates the data directory and writes a fully annotated default config:

1. Creates `~/.arc/` and `~/.arc/articles/`
2. Writes `~/.arc/config.jsonc` with all available LLM profiles and pricing notes
3. Prints API key setup instructions
4. Prompts you to review and edit the config
5. Validates the config before finishing

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
  config.json            # JSONC configuration
  arc.db                 # SQLite: metadata + FTS5 full-text index (derived)
  arc.log                # Application log
  events.jsonl           # Append-only event log
  index/                 # Vector store for semantic search
  articles/<slug>/       # One directory per article (flat, no nesting)
    body.txt
    meta.json
    source.url / source.html
    summary.<style>.<model>.txt
    flash.<model>.txt
    flashcards.<style>.<model>.json
  agent/
    config.json          # Feed list + interest profile
    state/               # Per-feed GUID tracking
    runs.jsonl           # Agent run log
  workspaces/<name>/
    chat/history.jsonl
    system.txt           # Custom system prompt
    resources/           # Attached articles, PDFs, notes
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
```

**Supported sources:** URLs (with cookie jar for paywalled sites), RSS/Atom feeds (via agent), PDFs (via `pdftotext`), plain text files, stdin (`arc ingest -`).

**Summary styles:** `study-notes` · `bullets` · `technical` · `executive`
**Flashcard styles:** `socratic` · `cloze`

### Cookie jars (paywalled sites)

To ingest articles behind paywalls (Medium, Substack, etc.), export cookies from your browser and point arc at them:

```jsonc
// in ~/.arc/config.jsonc
"cookie_jars": {
  "medium.com": "~/.arc/cookies/medium.txt",
  "substack.com": "~/.arc/cookies/substack.txt"
}
```

Cookie files use the Netscape/curl cookie jar format. Browser extensions like "Get cookies.txt" can export them.

## Search

Hybrid search combining FTS5 full-text and vector semantic search:

```bash
arc search "attention mechanisms"                  # hybrid (FTS5 + vector)
arc search "attention mechanisms" --no-semantic     # FTS5 only
```

Results show source badges: `[fts]`, `[vector]`, `[both]`.

## Collections and workspaces

**Collections** group articles by topic. An article can belong to many collections.

```bash
arc collections list
arc collections create ml-papers
arc collections add 20260521-transformer-survey ml-papers
arc collections suggest --apply         # LLM-assisted collection creation
arc collections assign --apply          # LLM-assisted article assignment
arc collections read ml-papers          # read all articles in a collection
arc collections search "transformers"   # search within collections
```

**Workspaces** are research environments with persistent chat, attached resources, and generated outcomes.

```bash
arc workspace new "attention research" "Survey of attention mechanisms"
arc workspace populate "attention research"   # LLM-assisted content selection
arc workspace chat "attention research"       # chat with grounded context
```

## Chat

Three chat modes, all with streaming and tool use:

| Mode | Scope | Access |
|------|-------|--------|
| Workspace chat | Full knowledge base + tools | `arc workspace chat <name>` |
| Article chat | Single article context | TUI |
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
arc agent log                    # show recent runs
arc agent digest                 # human-readable digest of latest run
arc agent stats                  # agent statistics
```

Configure feeds and interest profile in `~/.arc/agent/config.json`.

## TUI

Launch with `arc` (no subcommand). The TUI is the primary interface — designed for keyboard-driven browsing, reading, and chatting.

**Tabs:** Library · Agent · Stats

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
| `c` | Chat |
| `?` | Show key bindings |

Mouse support: click to navigate, middle-click to open in browser.

Use `--no-tui` to disable the TUI and run in headless/CLI mode.

## MCP server

Expose your knowledge base to Claude Desktop or Claude Code:

```bash
arc mcp              # stdio transport (for Claude Desktop/Code)
arc mcp --http :8080 # HTTP+SSE transport (daemon mode)
```

**Tools:** `search` · `read` · `list` · `get_stats`

## TTS

macOS text-to-speech via `say(1)`. Content is preprocessed: markdown stripped, code blocks removed, URLs filtered, soft-wrapped lines joined into paragraphs.

```json
{ "tts_voice": "Samantha", "tts_rate": 200 }
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

Run `arc profiles` to list all configured profiles.

## Configuration

`~/.arc/config.json` supports JSONC (comments allowed). Run `arc config` to view the active configuration.

```jsonc
{
  "data_root": "~/.arc",

  "profiles": { /* ... */ },

  "ingest": {
    "summary_profile": "oai-mini",
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
    "grounding_mode": "corpus-first"
  },

  // Cookie jars for paywalled sites
  "cookie_jars": {
    "medium.com": "~/.arc/cookies/medium.txt"
  },

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

## Building

```bash
make build            # build to ./bin/arc
make test             # run all tests
make fmt              # format code
make vet              # go vet
make clean            # remove ./bin/
```

### System dependencies

- `pdftotext` — PDF text extraction (optional)
- `say` — macOS TTS (optional)

## CLI reference

```
arc                       launch TUI (default when interactive)
arc init                  initialize ~/.arc
arc ingest <url|file|->   full ingestion pipeline
arc extract <url|file|->  extract plain text
arc summarize [slug]      generate summary
arc flash [slug]          generate flash summary
arc flashcards [slug]     generate flashcards
arc search <query>        hybrid search
arc list                  list articles
arc read <slug>           read article content
arc open <slug>           open article URL in browser
arc delete [slug]         delete article
arc reprocess [slug]      re-run pipeline on existing articles
arc reindex               rebuild SQLite + vector index from filesystem
arc collections ...       manage collections
arc workspace ...         manage workspaces
arc agent ...             feed agent commands
arc mcp                   start MCP server
arc stats                 knowledge base statistics
arc profiles              list LLM profiles
arc config                show active configuration
arc home                  print data root path
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

# arc

> Terminal-first personal knowledge OS. Filesystem-native, LLM-agnostic, keyboard-driven.

`arc` ingests articles, PDFs, RSS feeds, and local files, and distills each
into a summary and a **flash** — a 3-5 sentence synthesis derived
from the summary, useful for quick triage, RAG context, and audio playback. Content is indexed for hybrid
full-text and semantic search and can be queried conversationally through
OpenAI, Anthropic, or local Ollama models. Single binary, no runtime
dependencies.

**TUI-first, CLI-complete.** The primary interface is a full terminal UI —
library browsing, reading, collection management, search, workspace chat,
and audio playback. A complete CLI sits underneath for scripting and
automation; both are backed by the same service layer.

**Articles, collections, workspaces.** Articles are the atomic unit:
captured content plus summaries, flashes, and metadata. Collections group
articles by topic; an article may belong to several. Workspaces are research
environments — attached articles and resources, a persistent conversation
grounded in that material, and generated outcome documents.

**Autonomous ingestion.** A feed agent polls configured RSS/Atom sources,
filters items against an interest profile via LLM, and ingests approved
articles unattended. Filter decisions are logged and can be overridden and
re-run.

**Three chat modes, three grounding modes.** Single-shot queries,
per-article context, or multi-turn workspace conversations with tool use.
Grounding is selectable per workspace: `corpus-only` (pure RAG),
`corpus-first` (hybrid), or `open` (with internet search).

**Configurable per operation.** Profiles, grounding modes, context
strategies, summary styles, and TTS settings are set in `config.jsonc` and
reachable from both the TUI and the CLI.

---

## Design

**The filesystem is the source of truth.** Articles are plain text and JSON
in a flat directory tree. SQLite, the FTS5 index, and the vector store are
derived artifacts, rebuildable at any time with `arc reindex`. A corrupted
index, a failed schema change, or a switch of embedding provider costs
nothing but the time to rebuild — and the library remains readable with
`cat`, `grep`, and `rg` whether or not arc is installed.
Original source is retained alongside extracted text, so `arc open` can hand
any article to its native viewer — browser, PDF reader, or `$EDITOR` — when
the extraction isn't enough.

**Models are assigned per operation, not per application.** Named profiles
map to a provider, model, and parameters; each pipeline stage, chat mode,
and utility function selects its own. Bulk summarization can run on a cheap
model while workspace chat runs on an expensive one. Every call is logged to
an append-only event log with token counts and USD cost, so the tradeoff is
measurable rather than assumed.

**Summary variants are resolved at read time.** Multiple summaries per
article coexist as `summary.<style>.<model>.txt`; preference order lives in
config and is applied when the article is read, not when it is written.
Changing one config line changes which variant every article serves, with no
migration, no per-article state, and no reprocessing.

**The agent's decisions are reviewable.** Feed filtering writes its
accept/skip reasoning to a file that can be edited and replayed with
`--decisions`. Per-feed signal-to-noise statistics are tracked across runs.
Automation is auditable and correctable rather than opaque.

**Grounding is a per-workspace setting.** `corpus-only` restricts answers to
the local corpus, `corpus-first` falls back to model knowledge, `open`
permits live search. The appropriate posture differs by research question,
so it is configuration rather than a fixed property of the tool.

**One service layer, three front ends.** The TUI, the CLI, and the MCP
server are thin surfaces over a shared service package. No capability exists
in one and not the others, and scripted use is a first-class path rather
than an afterthought.

---

## Contents

- [Design](#design)
- [Features](#features)
- [Requirements](#requirements)
- [Installation](#installation)
- [Quick start](#quick-start)
- [Configuration](#configuration)
- [Ingestion](#ingestion)
- [Search](#search)
- [Collections](#collections)
- [Workspaces](#workspaces)
- [Chat](#chat)
- [Agent](#agent)
- [TUI](#tui)
- [Key bindings](#key-bindings)
- [MCP server](#mcp-server)
- [Text-to-speech](#text-to-speech)
- [Cost tracking](#cost-tracking)
- [Data layout](#data-layout)
- [CLI reference](#cli-reference)
- [Build reference](#build-reference)
- [Project structure](#project-structure)

---

## Features

- **Full TUI** — Bubble Tea-based, keyboard-driven, browse/read/search/chat without leaving the terminal
- **Ingestion pipeline** — URL, PDF, local file, or stdin; extract → summarize → flash → flashcards → embed → index
- **Multi-variant summaries** — multiple styles (study-notes, bullets, technical, executive) and models per article; preferred variant resolved at read time from config
- **Flashcards** — socratic and cloze styles, generated from summaries or body text
- **Hybrid search** — FTS5 full-text + vector semantic search; results tagged `[fts]`, `[vector]`, `[both]`
- **Collections** — group articles by topic; LLM-assisted creation and assignment
- **Workspaces** — research environments with attached articles, collections, resources, persistent chat, and generated outcomes
- **Three chat modes** — AskX (single-shot), article chat (scoped to one article), workspace chat (multi-turn, grounded)
- **Three grounding modes** — corpus-only (pure RAG), corpus-first (hybrid), open (with internet search)
- **Three context strategies** — tail, token-budget, summarize (rolling LLM-generated summary)
- **Autonomous agent** — polls RSS/Atom feeds, LLM-filters against interest profile, auto-ingests relevant articles
- **Three LLM providers** — OpenAI, Anthropic, Ollama; assignable per operation via named profiles
- **Text-to-speech** — macOS `say(1)`, flash summaries optimized for audio, content preprocessed
- **MCP server** — expose your knowledge base to Claude Desktop or Claude Code
- **Batch ingestion** — ingest from a file of URLs; duplicates skipped, errors logged per-item
- **Native viewer dispatch** — `arc open` routes by content type: HTML to the browser, PDF to the system PDF viewer, text and Markdown to `$EDITOR`; originals are retained alongside extracted text
- **Cookie jars** — Netscape-format cookie files for paywalled sites (Medium, Substack, etc.)
- **Cost tracking** — every LLM call logged with operation, model, tokens, and USD cost
- **Input correction** — `Ctrl+G` sends chat input to an LLM for spell/grammar correction
- **Reprocessing** — re-run the pipeline on existing articles; selective by collection, missing variants, or all
- **Single binary** — pure Go, `CGO_ENABLED=0`, no runtime dependencies, brew-distributable

---

## Requirements

**Go:** 1.25 or later required to build from source.

**API keys:** Set in your environment before running:

| Provider | Environment variable | Notes |
|---|---|---|
| OpenAI | `OPENAI_API_KEY` | Required for embeddings and OpenAI models |
| Anthropic | `ANTHROPIC_API_KEY` | Required for Anthropic models |
| Ollama | — | No key needed; local inference |

`OPENAI_BASE_URL` can be set for custom OpenAI-compatible endpoints.

**Optional system dependencies:**

- `pdftotext` — PDF text extraction
- `say` — macOS TTS

---

## Installation

**Homebrew (macOS):**

```bash
brew install jrniemiec/arc/arc
```

**From source:**

```bash
git clone https://github.com/jrniemiec/arc.git
cd arc
make install        # runs tests, builds, installs to ~/dev/bin/arc
```

Or build only:

```bash
make build          # outputs to ./bin/arc
```

**Go install:**

```bash
go install github.com/jrniemiec/arc@latest
```

---

## Quick start

```bash
arc init                            # guided setup wizard
arc ingest https://example.com/article
arc                                 # launch the TUI
```

`arc init` creates `~/.arc/`, writes `~/.arc/config.jsonc` with all available LLM profiles and pricing notes, prints API key setup instructions, and validates the configuration.

For a guided walkthrough of the full workflow (ingest → browse → search → organize → chat → agent), see [docs/guide.md](docs/guide.md).

---

## Configuration

Config lives at `~/.arc/config.jsonc` (JSONC — comments allowed). Override path with `--config <path>`. Run `arc config` to view the active configuration.

```jsonc
{
  "data_root": "~/.arc",

  "profiles": {
    "oai-mini":   { "provider": "openai", "model": "gpt-4o-mini" },
    "opus":       { "provider": "anthropic", "model": "claude-opus-5", "thinking": "disabled" },
    "haiku":      { "provider": "anthropic", "model": "claude-haiku-4-5-20251001" },
    "opus-4-6":   { "provider": "anthropic", "model": "claude-opus-4-6", "legacy": true },
    "llama":      { "provider": "ollama", "model": "llama3.1:8b" }
  },

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
  "preferred_models": ["claude-opus-5", "claude-sonnet-5", "claude-opus-4-6", "gpt-4.1"],
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
    "medium.com": "~/.arc/cookies/medium.txt",
    "substack.com": "~/.arc/cookies/substack.txt"
  },

  // TTS
  "tts_voice": "",
  "tts_rate": 200
}
```

### Profiles

Profiles map a short name to a provider, model, and parameters. Assign profiles per operation:

| Config key | Controls | Default |
|---|---|---|
| `ingest.summary_profile` | Summary generation | `oai-mini` |
| `ingest.flash_profile` | Flash summary generation | `oai-mini` |
| `ingest.flashcard_profile` | Flashcard generation | `oai-mini` |
| `chat.profile` | Workspace chat | `oai-mini` |
| `article_chat.profile` | Per-article chat | `haiku` |
| `askx.profile` | Single-shot queries | `haiku` |
| `correction_profile` | Input correction (Ctrl+G) | `oai-mini` |

Per-profile fields:

| Field | Applies to | Values | Meaning |
|---|---|---|---|
| `provider` | all | `openai` \| `anthropic` \| `ollama` | Which client to use |
| `model` | all | model ID | Exact model string sent to the provider |
| `host` | Ollama | URL | Defaults to `http://localhost:11434` |
| `think` | Ollama | bool | Enable reasoning mode (Qwen3, DeepSeek-R1) |
| `thinking` | Anthropic | `""` \| `disabled` \| `adaptive` | `""` omits the parameter; `disabled` is the default for the built-in profiles |
| `legacy` | all | bool | Superseded model, kept for reproducing older artifacts. Sorts to the bottom of pickers; still fully usable |

Run `arc profiles` to list all configured profiles with pricing info. Profiles are grouped by
provider, most capable first, with legacy models last — the same order as the TUI `/model` picker.

### Providers

| Provider | Config value | Models | Notes |
|---|---|---|---|
| OpenAI | `openai` | gpt-4o-mini, gpt-4.1, gpt-5-mini | Default for bulk operations |
| Anthropic | `anthropic` | claude-opus-5, claude-sonnet-5, claude-haiku-4-5 | Direct HTTP API |
| Ollama | `ollama` | llama3.1:8b, qwen2.5-coder:7b | Local, no API key, no tool calling |

Embedding: OpenAI `text-embedding-3-small` (required for semantic search).

### Limitations

- **Semantic search** requires `OPENAI_API_KEY` (for embeddings). Without it, only FTS5 keyword search is available.
- **Web search tools** in chat are Anthropic-only.
- **Ollama** does not support tool calling. Chat tools (`search_articles`, `read_article`, `list_articles`) and agent feed filtering are unavailable — chat works as plain conversation.

### Data root

By default arc stores everything under `~/.arc`. Override with:

| Method | Example |
|---|---|
| `--data-root` flag | `arc --data-root /data/arc list` |
| `ARC_HOME` env var | `export ARC_HOME=/data/arc` |

`--data-root` takes priority over `ARC_HOME`. `--articles-root` overrides just the articles location.

### Global flags

| Flag | Purpose |
|---|---|
| `--config <path>` | Config file path (default: `~/.arc/config.jsonc`) |
| `--data-root <path>` | Arc data root (default: `~/.arc`) |
| `--articles-root <path>` | Articles directory override |
| `--json` | JSON output |
| `--no-tui` | Disable TUI, run in headless/CLI mode |
| `--log-level <level>` | Log level: `debug`, `info`, `warn`, `error` |
| `--verbose` | Print debug-level log output to stderr |
| `--theme <mode>` | Color theme: `auto`, `light`, `dark` |

---

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

### Batch ingestion

Ingest many URLs at once from a file (one URL or file path per line, `#` comments and blank lines ignored):

```bash
arc ingest --file urls.txt
cat urls.txt | arc ingest --file -            # or pipe from stdin
```

Duplicates are automatically skipped. Errors are logged per-item without aborting the batch.

### Reprocessing

```bash
arc reprocess 20260521-article                # reprocess one article
arc reprocess --all                           # reprocess everything
arc reprocess --collection ml-papers          # reprocess a collection
arc reprocess --missing                       # only articles missing variants
```

### Supported sources

| Source | Notes |
|---|---|
| URLs | With cookie jar support for paywalled sites |
| RSS/Atom feeds | Via the feed agent |
| PDFs | Text extraction via `pdftotext` |
| Plain text files | Direct passthrough |
| stdin | `arc ingest -` |

### Summary and flashcard styles

| Type | Styles |
|---|---|
| Summary | `study-notes` · `bullets` · `technical` · `executive` |
| Flashcard | `socratic` · `cloze` |

### Multi-variant files

Articles can have multiple summary and flashcard variants, differing by style and model:

```
summary.study-notes.gpt-4o-mini.txt
summary.technical.claude-opus-5.txt
flashcards.socratic.claude-sonnet-5.json
```

The preferred variant is resolved at read time from `preferred_models` and `preferred_styles` in config. Change the config and the change applies to every article instantly — no per-article state.

Arc detects incomplete or paywalled content (teasers) and flags them for review.

---

## Search

Hybrid search combining FTS5 full-text and vector semantic search:

```bash
arc search "attention mechanisms"                      # hybrid (FTS5 + vector)
arc search "attention mechanisms" --no-semantic         # FTS5 only
arc search "transformers" --collection ml-papers       # within a collection
arc search "golang" --tag programming --limit 50       # filter by tag, custom limit
```

Results show source badges: `[fts]`, `[vector]`, `[both]`.

---

## Collections

Collections group articles by topic. An article can belong to many collections.

```bash
arc collections create ml-papers               # create a collection
arc collections add <slug> ml-papers           # add an article
arc collections list                           # list all collections
arc collections show ml-papers                 # show collection details
arc collections remove <slug> ml-papers        # remove an article
arc collections read ml-papers                 # read collection articles
arc collections search "transformers"          # search within a collection
arc collections rename ml-papers ml-research   # rename a collection
arc collections delete ml-papers               # delete (keeps articles)
arc collections delete ml-papers --purge       # delete collection and its articles
```

### Descriptions

```bash
arc collections describe ml-papers "Papers on machine learning"
arc collections describe-all                   # bulk edit descriptions
arc collections generate-description ml-papers # LLM-generated description
arc collections generate-description-all       # generate for all collections
```

### LLM-assisted organization

```bash
arc collections suggest --apply                # auto-create collections from your library
arc collections assign --apply                 # auto-assign uncollected articles
arc collections assign --uncollected-fresh     # assign only recently ingested articles
```

---

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

### Chat configuration

```bash
arc workspace chat-config "attention research" --profile opus
arc workspace chat-config "attention research" --grounding-mode corpus-only
arc workspace chat-config "attention research" --strategy token-budget --context-limit 8000
arc workspace chat-config "attention research" --max-output-tokens 4096
```

### Custom system prompt

```bash
arc workspace system "attention research" "You are a research assistant specializing in NLP."
arc workspace system "attention research"   # print current prompt
```

### Outcomes

```bash
arc workspace outcomes "attention research"              # list generated outputs
arc workspace outcomes "attention research" --read notes.md  # read a specific outcome
```

---

## Chat

Three chat modes, all with streaming and tool use:

| Mode | Scope | Access |
|---|---|---|
| Workspace chat | Full knowledge base + tools | `arc workspace chat <name>` |
| Article chat | Single article context | TUI: press `c` on an article |
| AskX | Single-shot query, no history | TUI |

### Grounding modes

| Mode | Behavior |
|---|---|
| `corpus-only` | Pure RAG — answers only from your articles; can search the wider library |
| `corpus-first` | Hybrid — prefers your articles, falls back to LLM knowledge |
| `open` | Unconstrained — LLM can search the internet for current information |

### Context strategies

| Strategy | Description |
|---|---|
| `tail` | Keep last N turns |
| `token-budget` | Fit within a token ceiling |
| `summarize` | Compress older turns via LLM |

### Tools available to the LLM during chat

`search_articles` · `read_article` · `list_articles`

---

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

---

## TUI

Launch with `arc` (no subcommand). The TUI is the primary interface — designed for keyboard-driven browsing, reading, and chatting.

**Views:** Library (articles) · Collections · Workspaces · Search · Agent · Stats

**Theming:** `--theme auto|light|dark`

Mouse support: click to navigate, middle-click to open in browser.

Use `--no-tui` to disable the TUI and run in headless/CLI mode.

### Input correction

Press `Ctrl+G` in any chat input to fix typos and grammar via LLM. Configure in `config.jsonc`:

```jsonc
{
  "correction_profile": "oai-mini",
  "correction_prompt": "Fix typos and grammar, preserve meaning."
}
```

---

## Key bindings

| Key | Action |
|---|---|
| `j` / `k` | Navigate up/down |
| `Tab` | Cycle panes |
| `/` | Search |
| `Enter` | Select / open |
| `d` | Delete |
| `m` | Mark read/played |
| `f` | Favorite |
| `s` | Speak (TTS) |
| `c` | Chat (article chat) |
| `o` | Open original source in native viewer |
| `v` | View in terminal |
| `Ctrl+G` | Fix input typos via LLM |
| `?` | Show all key bindings |

---

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

---

## Text-to-speech

macOS text-to-speech via `say(1)`. Content is preprocessed: markdown stripped, code blocks removed, URLs filtered, soft-wrapped lines joined into paragraphs.

```jsonc
{ "tts_voice": "Samantha", "tts_rate": 200 }
```

In the TUI, press `s` on any article to hear its flash summary.

---

## Cost tracking

Every LLM API call is logged to `~/.arc/events.jsonl` with operation type, model, token counts, and cost in USD.

```bash
arc stats               # includes cost breakdown by model and month
arc stats --json        # machine-readable output
```

---

## Data layout

Filesystem is the source of truth. SQLite and vector indexes are derived — rebuild anytime with `arc reindex`.

```
~/.arc/
├── config.jsonc           # JSONC configuration
├── arc.db                 # SQLite: metadata + FTS5 full-text index (derived)
├── arc.log                # Application log
├── events.jsonl           # Append-only cost tracking log
├── index/                 # Vector store for semantic search
├── articles/<slug>/       # One directory per article (flat, no nesting)
│   ├── body.txt
│   ├── meta.json
│   ├── source.url / source.html
│   ├── summary.<style>.<model>.txt
│   ├── flash.<model>.txt
│   └── flashcards.<style>.<model>.json
├── collections/<slug>/    # Symlinks to article directories
├── agent/
│   ├── config.json        # Feed list + interest profile
│   ├── state/             # Per-feed GUID tracking
│   └── runs.jsonl         # Agent run log
└── workspaces/<name>/
    ├── articles/          # Symlinks to articles
    ├── collections/       # Symlinks to collections
    ├── resources/         # Attached files, PDFs, notes
    ├── chat/              # Chat history and config
    ├── system.txt         # Custom system prompt
    └── outcomes/          # Generated output documents
```

---

## CLI reference

### Ingestion

```
arc ingest <url|file|->       full ingestion pipeline
  --title <text>                override article title
  --collection <slug>           add to collection on ingest
  --summary-style <style>       summary style (study-notes, bullets, technical, executive)
  --profile <name>              LLM profile override
  --flashcards / --no-flashcards  enable/disable flashcard generation
  --no-embed                    skip vector embedding
  --file <path|->               batch mode: file with one URL per line
  --show-summary                print summary after ingest
  --show-flash                  print flash summary after ingest
  --dry-run                     extract only, no writes
  --force                       re-ingest even if already exists
  -q, --quiet                   suppress progress output

arc extract <url|file|->      extract plain text (stdout)

arc summarize [slug]          generate summary
  --style <style>               summary style
  --profile <name>              LLM profile
  --write                       write to article directory
  --json                        JSON output

arc flash [slug]              generate flash summary
  --profile <name>              LLM profile
  --write                       write to article directory
  --from-body                   generate from body instead of summary
  --json                        JSON output

arc flashcards [slug]         generate flashcards
  --style <style>               flashcard style (socratic, cloze)
  --profile <name>              LLM profile
  --write                       write to article directory
  --from-body                   generate from body instead of summary
  --json                        JSON output

arc reprocess [slug]          re-run pipeline on existing articles
  --all                         reprocess all articles
  --collection <slug>           reprocess articles in a collection
  --missing                     only articles missing variants
  --refetch                     re-fetch source content
  --clean                       remove old variants before regenerating
  --body <file|->               replace body from file
  --no-summary                  skip summary generation
  --no-flash                    skip flash generation
  --no-flashcards               skip flashcard generation
  --no-embed                    skip vector embedding
  --json                        JSON output

arc reindex                   rebuild SQLite + vector index from filesystem
  --no-embed                    skip vector embedding
```

### Reading and browsing

```
arc list                      list articles
  --collection <slug>           filter by collection
  --tag <tag>                   filter by tag
  --unread                      only unread articles
  --unplayed                    only unplayed articles
  --uncollected                 articles not in any collection
  --uncollected-fresh           uncollected + recently ingested
  --agent                       only agent-ingested articles
  --agent-run <id>              articles from a specific agent run
  --slugs                       print slugs only
  --json                        JSON output

arc read <slug>               read article content
  --summary                     read summary instead of body
  --flash                       read flash summary
  --flashcards                  read flashcards
  --model <name>                select specific model variant
  --style <name>                select specific style variant

arc search <query>            hybrid search (FTS5 + vector)
  --collection <slug>           search within a collection
  --tag <tag>                   filter by tag
  --limit <n>                   max results (default: 20)
  --no-semantic                 FTS5 keyword search only

arc open <slug>               open original source in native viewer (browser, PDF reader, or $EDITOR)

arc delete [slug]             delete article
  --agent-run <id>              delete all articles from a specific agent run
  --dry-run                     preview what would be deleted
```

### Collections

```
arc collections list [pattern]                list collections
arc collections create <slug>                 create a collection
arc collections show <slug>                   show collection details
arc collections add <slug> <collection>       add article to collection
arc collections remove <slug> <collection>    remove article from collection
arc collections read <slug>                   read collection articles
arc collections search <query>                search within a collection
arc collections rename <old> <new>            rename a collection
arc collections delete <slug>                 delete a collection
  --force                                       skip confirmation
  --purge                                       also delete articles unique to this collection
arc collections describe <slug> [text]        set collection description
arc collections describe-all                  bulk edit descriptions
arc collections generate-description <slug>   LLM-generate a description
arc collections generate-description-all      generate descriptions for all
arc collections suggest                       LLM-assisted collection creation
  --apply                                       apply suggestions immediately
  --all / --uncollected                         scope
  --count <n> --min <n> --limit <n>             tuning
  --profile <name>                              LLM profile
arc collections assign [slug]                 LLM-assisted article assignment
  --apply                                       apply assignments immediately
  --all / --uncollected-fresh                   scope
  --limit <n>                                   max articles to process
  --profile <name>                              LLM profile
```

### Workspaces

```
arc workspace new <name> [description]        create a workspace
arc workspace list                            list workspaces
  --all                                         include archived
arc workspace show <name>                     show workspace details
arc workspace describe <name> [text]          set description
arc workspace rename <old> <new>              rename workspace
arc workspace system <name> [text]            set/view custom system prompt
arc workspace archive <name>                  archive workspace
arc workspace delete <name>                   delete workspace
  --force                                       skip confirmation
arc workspace add <name>                      add content to workspace
  --article <slug,...>                          add articles
  --collection <slug,...>                       add collections
  --resource <path|url,...>                     add files or URLs
  --into <subdir>                               resource subdirectory
  --comment <text>                              annotation
arc workspace remove <name>                   remove content from workspace
  --article <slug,...>                          remove articles
  --collection <slug,...>                       remove collections
  --resource <name,...>                         remove resources
  --all-articles / --all-collections            remove all
  --dry-run                                     preview changes
arc workspace outcomes <name>                 list generated outputs
  --read <file>                                 read a specific outcome
arc workspace populate <name>                 LLM-assisted content selection
  --hint <text>                                 refine selection focus
  --include-collections                         also suggest collections
  --dry-run                                     preview without applying
  --edit                                        review before applying
  --profile <name>                              LLM profile
arc workspace chat <name>                     start chat session
  -p, --profile <name>                          LLM profile
  --strategy <strategy>                         context strategy
  --context-limit <tokens>                      token budget
  --no-stream                                   disable streaming
  --clear                                       clear history before starting
  -D, --debug                                   debug mode
arc workspace chat-config <name>              configure chat settings
  --profile <name>                              LLM profile
  --strategy <strategy>                         context strategy (tail, token-budget, summarize)
  --context-limit <tokens>                      token budget
  --max-output-tokens <tokens>                  response length cap
  --max-user-messages <n>                       tail strategy: turns to keep
  --summarizer-profile <name>                   profile for history compaction
  --verbatim-ratio <float>                      summarize: fraction kept verbatim
  --grounding-mode <mode>                       corpus-only, corpus-first, open
  --list-modes                                  show available grounding modes
```

### Agent

```
arc agent run                 poll feeds + ingest
  --dry-run                     filter only, no ingestion
  --focus <text>                temporary interest emphasis
  --decisions <file>            re-run with user-overridden decisions
  --json                        JSON output
  -v, --verbose                 verbose output
arc agent log                 show recent agent runs
  -n, --number <n>              number of runs to show (default: 10)
arc agent digest              human-readable digest of latest run
  --summary                     include full summaries
  --flash                       include flash summaries (default)
  --run <id>                    specific run ID
  --tts                         TTS-friendly output
arc agent stats               per-feed signal/noise statistics
```

### System

```
arc init                      guided setup wizard
arc stats                     knowledge base statistics
  --json                        JSON output
arc profiles                  list LLM profiles with pricing
  --json                        JSON output
arc config                    show active configuration
  --json                        JSON output
arc home                      print data root path
arc mcp                       start MCP server
  --http <addr>                 HTTP+SSE transport (default: stdio)
arc help [section]            show documentation
                                sections: readme, tutorial, tui-commands, tui-keys, cli-commands
arc tui                       launch TUI explicitly
```

---

## Build reference

```bash
make build            # build to ./bin/arc
make install          # build + install to ~/dev/bin/arc
make test             # run all tests
make fmt              # format code
make vet              # go vet
make clean            # remove ./bin/ and ./dist/
make dist VERSION=x.y.z  # build release tarballs
```

---

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

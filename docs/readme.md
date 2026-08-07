# arc

A terminal-first personal knowledge OS.

Ingest articles from URLs, RSS feeds, and local files. Generate summaries, flash summaries, and flashcards. Search everything with full-text and semantic search. Chat with your knowledge base. Play content back via TTS. All in a single, pure-Go binary.

## Installation

Homebrew (macOS):
  brew install jrniemiec/arc/arc

From source:
  git clone https://github.com/jrniemiec/arc.git
  cd arc
  make build

Go install:
  go install github.com/jrniemiec/arc@latest

Requires Go 1.25+. Pure Go — no CGo dependencies.

## How it works

Filesystem is the source of truth. SQLite and vector indexes are derived — rebuild with `arc reindex` (database + full-text) and `arc embed` (vectors).

  ~/.arc/
    config.jsonc           JSONC configuration
    arc.db                 SQLite: metadata + FTS5 full-text index (derived)
    arc.log                Application log
    events.jsonl           Append-only event log
    index/                 Vector store for semantic search
    articles/<slug>/       One directory per article (flat, no nesting)
      body.txt
      meta.json
      summary.<style>.<model>.txt
      flash.<model>.txt
      flashcards.<style>.<model>.json
    agent/
      config.jsonc         Feed list + interest profile
      state/               Per-feed GUID tracking
      runs.jsonl           Agent run log
    workspaces/<name>/
      chat/history.jsonl
      system.txt           Custom system prompt
      resources/           Attached articles, PDFs, notes
      outcomes/            Generated output documents

Articles can have multiple summary and flashcard variants. The preferred variant is resolved at read time from `preferred_models` and `preferred_styles` in config.

## Data root

By default arc stores everything under ~/.arc. Override with:

  --data-root flag     arc --data-root /data/arc list
  ARC_HOME env var     export ARC_HOME=/data/arc

--data-root takes priority over ARC_HOME.

## LLM providers

Three provider backends, assignable per operation via profiles:

  OpenAI       gpt-4o-mini, gpt-4.1, gpt-5-mini
  Anthropic    claude-opus-5, claude-sonnet-5, claude-haiku-4-5
  Ollama       llama3.1:8b, qwen2.5-coder:7b (local, offline)

Embedding: OpenAI text-embedding-3-small, via the oai-embed profile set in
ingest.embed_profile. Generated during ingest; rebuild any time with `arc embed`.

Anthropic extended thinking is selected by profile rather than a flag:

  opus, sonnet             no thinking
  opus-think, sonnet-think adaptive thinking, larger output budget
  opus-4-6, sonnet-4-6     previous generation, kept to reproduce older
                           summaries and flashcards

Thinking makes Claude reason before answering — slower, and the reasoning
tokens bill as output. Any profile name works anywhere a profile is
accepted: --profile on any command, ingest config, workspace chat config,
agent config, or the TUI /model picker.

Configure profiles in ~/.arc/config.jsonc. Run `arc profiles` to list all configured profiles,
grouped by provider with superseded models last.

## MCP server

Expose your knowledge base to Claude Desktop or Claude Code:

  arc mcp              stdio transport (for Claude Desktop/Code)
  arc mcp --http :8080 HTTP+SSE transport (daemon mode)

## System dependencies

  pdftotext   PDF text extraction (optional)
  say         macOS TTS (optional)

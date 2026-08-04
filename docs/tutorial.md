# Getting Started with arc

## 1. Initialize

Run `arc init` to create your data directory and config:

  arc init

This creates ~/.arc/, writes a default config.jsonc, and walks you through API key setup.

## 2. Set API keys

Set at least one API key in your shell profile:

  export OPENAI_API_KEY=sk-...
  export ANTHROPIC_API_KEY=sk-ant-...

OpenAI is needed for embeddings (semantic search). Anthropic or OpenAI for summaries and chat.

## 3. Ingest your first article

  arc ingest https://example.com/interesting-article

This runs the full pipeline: extract → summarize → flash → [flashcards] → embed → index.

Flashcards are the one optional stage — `ingest.flashcards` is `false` by
default, so no deck is generated unless you pass `--flashcards` or flip that
setting. You can also add one later with `arc flashcards <slug> --write`.

## 4. Launch the TUI

  arc

No subcommand needed. The TUI is the primary interface. Use Tab to cycle panes, j/k to navigate, Enter to select.

## 5. Search your knowledge

  arc search "attention mechanisms"

Hybrid search combining full-text (FTS5) and vector semantic search.

## 6. Create a workspace

Workspaces are research environments with persistent chat:

  arc workspace new "my research" "Exploring a topic"
  arc workspace populate "my research"

The populate command uses an LLM to select relevant articles from your library.

## 7. Chat with your knowledge

In the TUI, navigate to a workspace and press 'c' to start chatting. The LLM has tools to search and read your articles.

Or from the CLI:

  arc workspace chat "my research"

## 8. Set up the agent

Configure RSS feeds in ~/.arc/agent/config.json, then:

  arc agent run --dry-run    # preview what would be ingested
  arc agent run              # ingest approved articles

## Pipeline commands

Each ingestion stage is composable via Unix pipes:

  arc extract <url>                          extract text only
  arc summarize [slug]                       generate summary
  arc flash [slug]                           generate flash summary
  arc flashcards [slug]                      generate flashcards
  arc extract <url> | arc summarize          pipe extract into summarize

## Reprocessing

Regenerate summaries or re-fetch articles:

  arc reprocess 20260521-article             reprocess one article
  arc reprocess --all                        reprocess everything
  arc reprocess --collection ml-papers       reprocess a collection

# Getting started with arc

This guide walks you through the typical arc workflow — from first install to a fully organized, searchable, chat-ready knowledge base.

## First run

```bash
arc init
```

The setup wizard walks you through configuration:

1. Creates `~/.arc/` and `~/.arc/articles/`
2. Writes `~/.arc/config.jsonc` with all available LLM profiles and pricing notes
3. Prints API key setup instructions
4. Prompts you to review and edit the config
5. Validates the config before finishing

## 1. Ingest an article

```bash
arc ingest https://example.com/interesting-article
```

This runs the full pipeline: extract text, generate a summary, a flash summary (optimized for audio), and a vector embedding for semantic search. The article lands in `~/.arc/articles/<date>-<slug>/`.

Flashcards are opt-in and off by default. Add `--flashcards` to generate a deck during ingest, set `ingest.flashcards` to `true` to make it the default, or generate one later with `arc flashcards <slug> --write`.

## 2. Browse what you have

```bash
arc list                            # show all articles
arc list --unread                   # only unread
arc read 20260801-interesting-article          # read the body
arc read 20260801-interesting-article --summary   # read the summary
arc read 20260801-interesting-article --flash     # read the flash summary
arc read 20260801-interesting-article --flashcards # review flashcards
```

## 3. Search your knowledge base

```bash
arc search "attention mechanisms"                # hybrid: FTS5 + semantic
arc search "attention mechanisms" --no-semantic   # keyword only
arc search "transformers" --collection ml-papers  # within a collection
```

## 4. Organize into collections

```bash
arc collections create ml-papers
arc collections add 20260801-transformer-survey ml-papers

# or let the LLM do it:
arc collections suggest --apply         # auto-create collections from your articles
arc collections assign --apply          # auto-assign uncollected articles
```

## 5. Chat with your knowledge

```bash
arc workspace new "attention research" "Survey of attention mechanisms"
arc workspace populate "attention research"   # LLM picks relevant articles
arc workspace chat "attention research"       # start a grounded conversation
```

## 6. Set up the feed agent

Configure feeds and interests in `~/.arc/agent/config.jsonc` — by hand, or from the
TUI with `/agent-config-edit` for the file and `/feed-add` / `/feed-edit` for
individual feeds. Then:

```bash
arc agent run                       # poll feeds, filter, ingest
arc agent run --dry-run             # preview what would be ingested
arc agent digest                    # readable summary of what was ingested
```

## 7. Launch the TUI

```bash
arc                                 # TUI is the default when interactive
```

The TUI is the primary interface — browse articles, manage collections and workspaces, chat, search, and play audio, all keyboard-driven. Press `?` for all key bindings.

## 8. Listen to your articles

In the TUI, press `s` on any article to hear its flash summary via macOS TTS. Configure voice and rate in `config.jsonc`:

```jsonc
{ "tts_voice": "Samantha", "tts_rate": 200 }
```

## 9. Batch ingestion

```bash
arc ingest --file urls.txt          # one URL per line, # comments ignored
```

Duplicates are automatically skipped. Errors are logged per-item without aborting the batch.

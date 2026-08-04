# CLI Commands

## General

  arc                           launch TUI (default when interactive)
  arc init                      initialize ~/.arc with guided setup
  arc help [section]            show help (readme, tutorial, tui-commands, tui-keys, cli-commands)
  arc config                    show active configuration
  arc profiles                  list LLM profiles with pricing
  arc stats                     knowledge base statistics
  arc home                      print data root path
  arc tui                       launch TUI explicitly
    --theme <mode>                color theme: auto|light|dark

## Ingestion

  arc ingest <url|file|->       full pipeline: extract → summarize → flash → [flashcards] → index
                                flashcards off by default; see ingest.flashcards
    --title <text>                override article title
    --collection <slug>           add to collection on ingest
    --summary-style <style>       summary style (study-notes, bullets, technical, executive)
    --profile <name>              LLM profile override for all generation steps
    --flashcards                  generate flashcards (overrides config)
    --no-flashcards               skip flashcard generation (overrides config)
    --no-embed                    skip vector embedding
    --file <path|->               batch mode: file with one URL/file per line
    --show-summary                print summary after ingest
    --show-flash                  print flash summary after ingest
    --dry-run                     extract only, no writes
    --force                       re-ingest even if already exists
    -q, --quiet                   suppress progress output; print slug only

  arc extract <url|file|->      extract plain text to stdout

  arc summarize [slug]          generate or regenerate summary
    --style <style>               summary style
    --profile <name>              LLM profile
    --write                       write to article directory

  arc flash [slug]              generate or regenerate flash summary
    --profile <name>              LLM profile
    --write                       write to article directory
    --from-body                   generate from body instead of summary

  arc flashcards [slug]         generate or regenerate flashcards
    --style <style>               flashcard style (socratic, cloze)
    --profile <name>              LLM profile
    --count <n>                   target number of cards (default: scaled to length)
    --write                       write to article directory
    --from-body                   generate from body instead of summary
    --delete                      delete flashcards instead of generating them
    --model <name>                with --delete: only remove this model's variant
    --dry-run                     with --delete: show what would be removed

  arc reprocess [slug]          re-run pipeline on existing articles
    --all                         process all articles
    --collection <slug>           process articles in a collection
    --missing                     skip articles that already have all variants
    --refetch                     re-fetch body from source URL or PDF
    --clean                       delete existing variant files before regenerating
    --body <file|->               replace body.txt from file or stdin
    --no-summary                  skip summary generation
    --no-flash                    skip flash generation
    --flashcards                  generate flashcards (overrides config)
    --no-flashcards               skip flashcard generation (overrides config)
    --no-embed                    skip embedding

  arc reindex                   rebuild SQLite + vector index from filesystem
    --no-embed                    skip vector embedding

## Articles

  arc list                      list all articles
    --collection <slug>           filter by collection
    --tag <tag>                   filter by tag
    --unread                      only unread articles
    --unplayed                    only unplayed articles
    --uncollected                 articles not in any collection
    --uncollected-fresh           uncollected + recently ingested
    --agent                       only agent-ingested articles
    --agent-run <id>              articles from a specific agent run
    --slugs                       print slugs only (for scripting)

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

## Collections

  arc collections list [pattern]                list collections
    -q, --quiet                                   output slugs only, one per line

  arc collections create <slug> [description]   create a collection

  arc collections show <slug>                   show collection details
    --article <slug>                              show collections containing this article

  arc collections add <slug> <collection>       add article to collection

  arc collections remove <slug> <collection>    remove article from collection

  arc collections read <slug>                   read articles in a collection
    --flash                                       read flash summaries (default)
    --summary                                     read full summaries
    --model <name>                                prefer model variant
    --style <name>                                prefer style variant

  arc collections search <query>                search within collections

  arc collections rename <old> <new>            rename a collection

  arc collections delete <slug>                 delete a collection
    --force                                       skip confirmation
    --purge                                       also delete articles unique to this collection

  arc collections describe <slug> <text>        set collection description

  arc collections describe-all                  bulk edit descriptions

  arc collections generate-description <slug>   LLM-generate a description
    --dry-run                                     print without writing
    --edit                                        review before saving
    --force                                       overwrite existing description
    --profile <name>                              LLM profile override

  arc collections generate-description-all      generate descriptions for all collections
    --dry-run                                     print without writing
    --edit                                        review each before saving
    --force                                       overwrite existing descriptions
    --limit <n>                                   process at most N collections
    --profile <name>                              LLM profile override

  arc collections suggest [slug]                LLM-assisted collection creation
    --apply                                       interactively create and link
    --all                                         with --apply: accept all without prompting
    --uncollected                                 suggest for all uncollected articles
    --count <n>                                   target number of collections
    --min <n>                                     minimum articles per collection
    --limit <n>                                   with --uncollected: process at most N articles
    --profile <name>                              LLM profile override

  arc collections assign                        LLM-assisted article assignment
    --apply                                       create symlinks (default: dry-run)
    --all                                         with --apply: skip confirmation
    --uncollected-fresh                           only assign articles not in any collection
    --limit <n>                                   process at most N articles
    --profile <name>                              LLM profile override

## Workspaces

  arc workspace new <name> [description]        create a workspace

  arc workspace list                            list workspaces
    --all                                         include archived

  arc workspace show <name>                     show workspace details

  arc workspace describe <name> <text>          set description

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
    --comment <text>                              annotation for .url resource

  arc workspace remove <name>                   remove content from workspace
    --article <slug,...>                          remove articles
    --collection <slug,...>                       remove collections
    --resource <name,...>                         remove resources
    --all-articles                                remove all articles
    --all-collections                             remove all collections
    --dry-run                                     preview changes

  arc workspace outcomes <name>                 list generated outputs
    --read <file>                                 print a specific outcome to stdout

  arc workspace populate <name>                 LLM-assisted content selection
    --hint <text>                                 free-form guidance for the LLM
    --include-collections                         include collections in selection
    --dry-run                                     preview without applying
    --edit                                        review each suggestion interactively
    -p, --profile <name>                          LLM profile

  arc workspace chat <name>                     start chat session
    -p, --profile <name>                          LLM profile
    --strategy <strategy>                         context strategy (tail, token-budget, summarize)
    --context-limit <tokens>                      token budget override
    --no-stream                                   disable streaming
    --clear                                       clear history before starting
    -D, --debug                                   print debug info to stderr

  arc workspace chat-config <name>              configure chat settings
    --profile <name>                              set chat profile
    --strategy <strategy>                         context strategy (tail, token-budget, summarize)
    --context-limit <tokens>                      token budget (0 = unset)
    --max-output-tokens <tokens>                  response length cap (0 = provider default)
    --max-user-messages <n>                       tail strategy: past turns to keep (0 = default 50)
    --summarizer-profile <name>                   profile for history compaction
    --verbatim-ratio <float>                      summarize: fraction of budget kept verbatim (0 = default 0.4)
    --grounding-mode <mode>                       corpus-only, corpus-first, open
    --list-modes                                  list available grounding modes

## Agent

  arc agent run                 poll feeds and ingest approved articles
    --dry-run                     filter only, no ingestion
    --focus <text>                temporary interest emphasis
    --decisions <file>            re-run with user-overridden decisions
    -v, --verbose                 list every article with its verdict
    --json                        print run record as JSON

  arc agent log                 show recent agent runs
    -n, --number <n>              number of runs to show (default: 10)

  arc agent digest              human-readable digest of latest run
    --flash                       include flash summaries (default: true)
    --summary                     include full summaries
    --run <id>                    specific run ID (default: last run)
    --tts                         TTS-friendly output (no URLs, no unicode)

  arc agent stats               per-feed signal/noise statistics

## MCP Server

  arc mcp                       start MCP server (stdio transport)
    --http <addr>                 HTTP+SSE transport (e.g. :8080)

## Global flags

  --config <path>               config file (default: ~/.arc/config.jsonc)
  --data-root <path>            arc data root directory (default: ~/.arc)
  --articles-root <path>        articles directory override
  --json                        output JSON
  --no-tui                      disable TUI, run in headless/CLI mode
  --log-level <level>           log level: debug|info|warn|error
  --verbose                     print debug-level log output to stderr
  --theme <mode>                color theme: auto|light|dark

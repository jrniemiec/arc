# CLI Commands

## General

  arc                           launch TUI (default when interactive)
  arc init                      initialize ~/.arc with guided setup
  arc help [section]            show help (readme, tutorial, tui-commands, tui-keys, cli-commands)
  arc config                    show active configuration
  arc profiles                  list LLM profiles
  arc stats                     knowledge base statistics
  arc home                      print data root path

## Ingestion

  arc ingest <url|file|->       full pipeline: extract → summarize → flash → flashcards → index
  arc extract <url|file|->      extract plain text to stdout
  arc summarize [slug]          generate or regenerate summary
  arc flash [slug]              generate or regenerate flash summary
  arc flashcards [slug]         generate or regenerate flashcards
  arc reprocess [slug]          re-run pipeline on existing articles
  arc reindex                   rebuild SQLite + vector index from filesystem

## Articles

  arc list                      list all articles
  arc read <slug>               read article content
  arc search <query>            hybrid search (FTS5 + vector)
  arc open <slug>               open article URL in browser
  arc delete [slug]             delete article

## Collections

  arc collections list          list collections
  arc collections create <name> create a collection
  arc collections add <slug> <collection>   add article to collection
  arc collections read <name>   read all articles in a collection
  arc collections search <q>    search within collections
  arc collections suggest       LLM-assisted collection creation
  arc collections assign        LLM-assisted article assignment

## Workspaces

  arc workspace list            list workspaces
  arc workspace new <name> <desc>   create a workspace
  arc workspace delete <name>   delete a workspace
  arc workspace populate <name> LLM-assisted content selection
  arc workspace chat <name>     chat with workspace context

## Agent

  arc agent run                 poll feeds and ingest approved articles
  arc agent run --dry-run       filter only, no ingestion
  arc agent run --focus <topic> override focus for this run
  arc agent log                 show recent agent runs
  arc agent digest              human-readable digest of latest run
  arc agent stats               agent statistics

## MCP Server

  arc mcp                       start MCP server (stdio transport)
  arc mcp --http :8080          start MCP server (HTTP+SSE transport)

## Global flags

  --config <path>               config file (default: ~/.arc/config.jsonc)
  --data-root <path>            arc data root directory (default: ~/.arc)
  --articles-root <path>        articles directory override
  --json                        output JSON
  --no-tui                      disable TUI, run in headless/CLI mode
  --log-level <level>           log level: debug|info|warn|error
  --verbose                     print debug-level log output to stderr

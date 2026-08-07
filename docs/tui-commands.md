# TUI Commands

Type / in the command bar to enter a command. Press Tab to autocomplete.

## Global commands (available from any tab)

  /article <cmd>             article commands (list, search, ingest, ...)
  /collection <cmd>          collection commands (list, show, ...)
  /workspace <cmd>           workspace commands (list, new, delete, ...)
  /ingest <url>              add a new article
  /scratch [msg]             workspace-local scratch (append / toggle)
  /Scratch [msg]             global scratch (append / toggle)
  /askX <prompt>             workspace-local LLM query
  /AskX <prompt>             global LLM query (same as Ctrl+X)
  /reset                     reset askX context (keeps history, clears LLM context)
  /help [group]              show command reference
  /?                         show all key bindings
  /arc-home                  show active arc data root
  /config                    show resolved configuration
  /config-view               view config.jsonc in overlay
  /config-edit               open config.jsonc in $EDITOR
  /agent-config-view         view agent/config.jsonc in overlay
  /agent-config-edit         open agent/config.jsonc in $EDITOR
  /chat-config-view          view workspace chat/config.jsonc in overlay
  /chat-config-edit          open workspace chat/config.jsonc in $EDITOR
  /stats                     show library stats
  /models                    list available LLM profiles
  /workspace-profile [name]  show or set workspace chat profile (persisted)
  /workspace-model [name]    alias for /workspace-profile
  /chat-profile [name]       show or set article chat profile (persisted)
  /chat-model [name]         alias for /chat-profile
  /correction-profile [name] show or set correction profile (persisted)
  /correction-model [name]   alias for /correction-profile
  /log                       open/close debug log tail
  /agent-run [flags]         start agent feed scan (--dry-run, --focus "...")
  /agent-rerun [--dry-run]   re-run decisions for selected agent run
  /chats-archive             archive pending AskX + article chat messages
  /chats-history             browse archived chat sessions (overlay)
  /chats-export [--md|--text]  export chat archive to file and open in $EDITOR

The scoped config commands used to be named /config-agent-view, /config-agent-edit,
/config-chat-view and /config-chat-edit. Those names still work but no longer appear
in /help or tab-completion.

## Article commands (Articles sub-tab)

  /search <query> [--limit N]   full-text search (FTS5)
  /filter <tag>                 filter by tag
  /favorites                    show only favorited articles
  /clear                        clear active filter
  /tags                         list all tags
  /collections                  list all collections
  /open                         open original source in native viewer
  /read                         mark as read
  /unread                       mark as unread
  /favorite                     toggle favorite
  /chat                         open article chat session
  /collection-add <slug>        add article to a collection
  /collection-remove <slug>     remove article from a collection
  /delete [slug]                delete article (selected or by name)
  /reprocess                    regenerate summary/flash
  /flashcards [--style X] [--profile Y] [--count N]
                                generate flashcards for the selected article
                                (--count is approximate, not a hard limit)
  /flashcards-delete [--style X] [--model Y]
                                delete flashcards (confirms first)
  /ingest <url>                 add a new article

## Collection commands (Collections sub-tab)

  /search <query>            filter collections by name/slug
  /clear                     clear active filter
  /article-remove [slug]     remove article from this collection (selected or by
                             name) — or press U on the article row
  /new <slug> [description]  create a new collection
  /rename <new-slug>         rename the selected collection
  /describe [text]           show the description, or set it
  /describe-generate         generate the description from member articles (LLM)
  /delete [slug]             delete collection
  /reload                    refresh collections list from disk
  /flashcards [--style X] [--profile Y] [--count N]
                             generate flashcards for the article row under the
                             cursor (one article, not the whole collection)
  /flashcards-delete [--style X] [--model Y]
                             delete that article's flashcards (confirms first)

## Workspace commands (Workspaces sub-tab)

  /search <query>            search workspaces (or articles within focused workspace)
  /clear                     clear active filter
  /new <name> [description]  create a new workspace
  /delete [name]             delete workspace
  /rename <new-name>         rename current workspace
  /describe <text>           set workspace description
  /reload                    reload workspace from disk and reset chat engine
  /populate [flags]          LLM-assisted article selection
                               --hint "..." --profile name --dry-run --edit --include-collections
  /remove [flags]            remove articles/collections from workspace
                               --article slug --collection slug --all-articles --all-collections --dry-run
  /flashcards [--style X] [--profile Y] [--count N]
                             generate flashcards for the article row under the
                             cursor (one article, not the whole workspace)
  /flashcards-delete [--style X] [--model Y]
                             delete that article's flashcards (confirms first)

## Feed commands (Agent Feeds sub-tab)

  /feed-add                  add a new feed (opens $EDITOR with template)
  /feed-edit                 edit selected feed in $EDITOR
  /feed-toggle               toggle selected feed enabled/disabled
  /feed-delete               delete selected feed (with confirmation)

## AskX commands (when AskX pane is open)

  /profile [name]            show or set LLM profile for askX (persisted)
  /model [name]              alias for /profile
  /reset                     reset askX context
  /no-history                toggle no-history mode: send queries without prior context
  /chats-archive             archive pending messages
  /chats-history             browse archived chat sessions
  /chats-export [--md|--text]  export chat archive

## Article chat commands (when article chat is active)

  /clear                     clear conversation history
  /profile [name]            show or switch LLM profile
  /model [name]              alias for /profile
  /chat-profile [name]       show or set global article chat profile
  /chat-model [name]         alias for /chat-profile
  /stats                     show session token usage and cost
  /system                    print system prompt
  /chats-archive             archive pending messages
  /chats-history             browse archived chat sessions
  /chats-export [--md|--text]  export chat archive
  /help                      show article chat commands

## Workspace chat commands (when workspace chat is active)

  /clear                     clear conversation history
  /mode [corpus-only|corpus-first|open]   show or switch grounding mode
  /profile [name]            show or switch LLM profile for this session
  /model [name]              alias for /profile
  /reload                    rebuild corpus map to pick up article changes
  /stats                     show session token usage and cost
  /system                    print system prompt
  /meta                      show workspace details
  /save [filename]           save session to outcomes/<filename>.md
  /new <name> [description]  create a new workspace
  /delete [name]             delete workspace
  /rename <new-name>         rename current workspace
  /describe <text>           set workspace description
  /resource-list             list files in workspace/resources/
  /resource-add <path|url>   copy file/dir or add URL into resources/ (Tab completes paths)
                               --into <dir> --as <name> --comment "..."
  /resource-mkdir <name>     create a directory in resources/
  /resource-delete <name>    delete a resource file or directory
  /resource-view <name>      open resource file in viewer overlay
  /resource-edit <name>      open resource file in $EDITOR
  /resource-new <name>       create new resource file and open in $EDITOR
  /resource-save [filename]  save chat session as a resource file
  /outcome-list              list files in workspace/outcomes/
  /outcome-add <path>        copy a file into outcomes/ (flat, files only;
                               Tab completes paths)
                               --as <name>
  /outcome-delete <name>     delete an outcome file
  /outcome-view <name>       open outcome file in viewer overlay
  /outcome-edit <name>       open outcome file in $EDITOR
  /outcome-new <name>        create new outcome file and open in $EDITOR
  /outcome-save [filename]   save chat session to outcomes/ (alias of /save)
  /populate [flags]          LLM-assisted article selection
  /remove [flags]            remove articles/collections from workspace
  /scratch [msg]             workspace-local scratch
  /Scratch [msg]             global scratch
  /askX <prompt>             workspace-local LLM query
  /AskX <prompt>             global LLM query
  /reset                     reset askX context
  /article <cmd>             article commands
  /collection <cmd>          collection commands
  /workspace <cmd>           workspace commands
  /arc-home                  show active arc data root
  /config                    show resolved configuration
  /config-view               view config.jsonc in overlay
  /config-edit               open config.jsonc in $EDITOR
  /agent-config-view         view agent/config.jsonc in overlay
  /agent-config-edit         open agent/config.jsonc in $EDITOR
  /chat-config-view          view workspace chat/config.jsonc in overlay
  /chat-config-edit          open workspace chat/config.jsonc in $EDITOR
  /models                    list available LLM profiles
  /chats-archive             archive pending messages
  /chats-history             browse archived chat sessions
  /chats-export [--md|--text]  export chat archive
  /help                      show chat commands

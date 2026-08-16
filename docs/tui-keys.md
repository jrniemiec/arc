# TUI Key Bindings

## Global

  Tab            cycle panes forward (nav → content → open split pane → nav)
  Shift+Tab      cycle panes backward (nav → open split pane → content → nav)
  Alt+1          focus nav pane
  Alt+2          focus content pane
  Alt+3          focus tab bar
  Esc            focus command input; press again with input empty to focus nav
  Ctrl+S         toggle selection mode (freeze screen for native text copy)
  Ctrl+L         scratch mode: opens workspace scratch (or global outside a
                 workspace), input pane switches to note entry
  Ctrl+X         toggle askX pane
  Ctrl+O         toggle preview pane
  Ctrl+G         correct spelling/grammar of input via LLM
  Ctrl+R         refresh current view
  /              focus command input
  ?              show key bindings overlay
  q / Ctrl+C     quit

## Navigation (nav pane)

  j / ↓          move down
  k / ↑          move up
  PgDn / Ctrl+D  page down
  PgUp / Ctrl+U  page up
  g / Home       go to top
  G / End        go to bottom
  h / ←          previous sub-tab
  l / →          next sub-tab
  Enter          select item
  Space          expand/collapse tree node
  !              toggle workspace focus (solo mode)

## Article actions (nav pane)

  o              open original source in native viewer (or the selected
                 workspace resource/outcome in its default app)
  O              open URL without tracking
  v              view article in overlay
  F              reveal the selected item in Finder — works on any Library row:
                 article, collection, workspace, resource, outcome, scratch.
                 Folders open showing their contents; files open their folder
                 with the file selected. Also available as /reveal, which works
                 from workspace chat where F is typed into the input
  r              mark as read
  u              mark as unread
  f / *          toggle favorite
  D              delete article (with confirmation)
  c              open article chat
  a              move to attic (workspace context)
  b              move from attic back to workspace
  U              unlink article/collection from workspace

## Agent run actions (Runs sub-tab nav pane)

  D              delete selected run's history record (with confirmation) —
                 keeps ingested articles

## Agent feed actions (Feeds sub-tab nav pane)

  a              add new feed
  e              edit selected feed in $EDITOR
  d              toggle feed enabled/disabled
  D              delete feed (with confirmation)
  R              clear seen-item state (with confirmation) — next run
                 re-checks everything

## Tab bar (when focused)

  h / ←          previous top-level tab
  l / →          next top-level tab
  j / ↓ / Enter  drop focus to nav sub-tab bar

## Nav sub-tab bar (when focused)

  k / ↑          go to top tab bar
  j / ↓ / Enter  drop into nav list
  h / ←          previous sub-tab
  l / →          next sub-tab

## Content pane (article view)

  j / ↓          scroll down
  k / ↑          scroll up
  PgDn / Ctrl+D  page down
  PgUp / Ctrl+U  page up
  g / Home       go to top
  G / End        go to bottom
  h / ←          previous content tab (Flash/Summary/Body/Cards)
  l / →          next content tab
  o              open article URL in browser
  f / *          toggle favorite
  s              speak content (TTS)
  S              stop speaking
  [              decrease TTS rate
  ]              increase TTS rate

Flashcards (cursor on a card, Cards section):

  space          reveal/hide the answer under the cursor
  A              reveal/hide every answer
  D              delete the deck (confirms first)

## Agent content pane (run history)

  j / ↓          move down
  k / ↑          move up
  PgDn           page down
  PgUp           page up
  g / Home       go to top
  G / End        go to bottom
  Space / Enter  expand/collapse feed header
  o              open article URL in browser
  v / Enter      view ingested article
  a              queue article for ingest
  s              skip article

## Workspace chat (content pane)

  j / ↓          next message box
  k / ↑          previous message box
  PgDn           page down
  PgUp           page up
  g / Home       go to top
  G / End        go to bottom
  v              collapse/expand message box
  x              delete message box
  #              comment on message
  s              speak message (TTS)
  [              decrease TTS rate
  ]              increase TTS rate

## Article chat (split pane)

  j / ↓          next message box
  k / ↑          previous message box
  PgDn           page down
  PgUp           page up
  g / Home       go to top
  G / End        go to bottom
  c              exit article chat
  v              collapse/expand message box
  x              delete message box
  #              comment on message
  s              speak message (TTS)
  V              view full conversation in overlay
  [              decrease TTS rate
  ]              increase TTS rate

## Scratch mode (Ctrl+L)

Opens the scratch for the workspace under the nav cursor / active workspace
chat, or the global scratch if no workspace is in context. The command input
switches to note entry: typed text + Enter appends a note and stays in
scratch mode; `/command` still dispatches normally. Pressing Ctrl+L again
closes it, as does moving the nav cursor to a different workspace or
switching away from the Workspaces sub-tab (auto-close only applies to a
workspace-scoped scratch, not the global one).

## Scratch pane (when focused)

  j / ↓          next block
  k / ↑          previous block
  PgDn           page down
  PgUp           page up
  g / Home       go to top
  G / End        go to bottom
  s              speak block (TTS)
  v              collapse/expand block
  x              delete block
  e              edit block in place
  V              view in overlay
  E              edit scratch file in $EDITOR
  [              decrease TTS rate
  ]              increase TTS rate
  Esc            unfocus scratch pane
  /              open command input

## AskX pane (when focused)

  j / ↓          next box
  k / ↑          previous box
  PgDn           page down
  PgUp           page up
  g / Home       go to top
  G / End        go to bottom
  s              speak box (TTS)
  v              collapse/expand box
  x              delete box
  #              comment on box
  e              edit askX file in $EDITOR
  V              view in overlay
  [              decrease TTS rate
  ]              increase TTS rate
  Esc            unfocus askX pane
  /              open command input

## Preview pane (when focused)

  j / ↓          scroll down
  k / ↑          scroll up
  PgDn           page down
  PgUp           page up
  g / Home       go to top
  G / End        go to bottom
  s              speak content (TTS)
  V              view in overlay
  [              decrease TTS rate
  ]              increase TTS rate
  Esc            unfocus preview pane

## Resource/file overlay

  j / ↓          scroll down
  k / ↑          scroll up
  PgDn / Ctrl+D  page down
  PgUp / Ctrl+U  page up
  g / Home       go to top
  G / End        go to bottom
  s              speak line (TTS)
  e              edit file (scratch files only)
  x              delete line (scratch files only)
  [              decrease TTS rate
  ]              increase TTS rate
  q / Esc        close overlay

## Command input

  Enter          execute command / submit input
  Ctrl+J         insert newline (Shift+Enter also works)
  Ctrl+T         insert timestamp
  Ctrl+V         paste from clipboard
  ↑              previous command history
  ↓              next command history
  Tab            accept completion

## Status output pane (when focused)

  j / ↓          scroll down
  k / ↑          scroll up
  PgDn           page down
  PgUp           page up

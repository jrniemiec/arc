# TUI Key Bindings

## Global

  Tab            cycle panes forward (tab bar → nav → content → split → command)
  Shift+Tab      cycle panes backward
  Alt+1          focus nav pane
  Alt+2          focus content pane
  Alt+3          focus tab bar
  Ctrl+S         toggle selection mode (freeze screen for native text copy)
  Ctrl+L         toggle scratch pane
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

  o              open original source in native viewer
  O              open URL without tracking
  v              view article in overlay
  r              mark as read
  u              mark as unread
  f / *          toggle favorite
  D              delete article (with confirmation)
  c              open article chat
  a              move to attic (workspace context)
  b              move from attic back to workspace
  U              unlink article/collection from workspace

## Agent feed actions (Feeds sub-tab nav pane)

  a              add new feed
  e              edit selected feed in $EDITOR
  d              toggle feed enabled/disabled
  D              delete feed (with confirmation)

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
  e              edit scratch file in $EDITOR
  V              view in overlay
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

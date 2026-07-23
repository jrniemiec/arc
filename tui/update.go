package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sort"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	agentpkg "github.com/jrniemiec/arc/agent"
	"github.com/jrniemiec/arc/config"
	"github.com/jrniemiec/arc/internal/clog"
	"github.com/jrniemiec/arc/service"
	storefs "github.com/jrniemiec/arc/store/fs"
	"github.com/jrniemiec/arc/tts"
	"github.com/jrniemiec/llm"
)

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Selection mode: screen is frozen for native text selection.
	// Return unchanged model + no commands so bubbletea does not redraw.
	// Only the exit key breaks out.
	if m.selectionMode {
		if key, ok := msg.(tea.KeyMsg); ok {
			if key.Type == tea.KeyEsc || key.String() == "ctrl+s" {
				m.selectionMode = false
				m.navWidthOverride = m.preSelNavWidth
				m.selectionMaxPane = 0
				m.statusMsg = ""
				return m, tea.Batch(tea.EnableMouseCellMotion, spinnerTick())
			}
		}
		return m, nil
	}

	var cmds []tea.Cmd

	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.syncInputPrompt()
		m.syncInputHeight()

	case spinnerTickMsg:
		m.spinnerFrame++
		// Blink cursor at ~400ms (every 4 ticks of 100ms), only when command pane focused.
		if m.spinnerFrame%4 == 0 {
			if m.focus == paneCommand {
				m.cursorVisible = !m.cursorVisible
			} else {
				m.cursorVisible = false
			}
		}
		// During streaming, pull from shared buffer and rebuild lines on each tick.
		if m.chatMode && m.chatStreaming {
			if m.chatSharedBuf != nil {
				m.chatStreamBuf = m.chatSharedBuf.Get()
				m.chatActivityLine = m.chatSharedBuf.GetActivity()
			}
			m.rebuildChatLines(m.chatBuildWidth())
			chatViewH := m.chatViewHeight()
			m.chatAutoScrollToBottom(chatViewH)
		}
		if m.askxStreaming && m.askxSharedBuf != nil {
			m.askxStreamBuf = m.askxSharedBuf.Get()
			m.rebuildAskXLines()
			m.askxScrollToBottom()
		}
		if m.achatMode && m.achatStreaming && m.achatSharedBuf != nil {
			m.achatStreamBuf = m.achatSharedBuf.Get()
			m.rebuildArticleChatLines(m.achatBuildWidth())
			viewH := m.achatViewHeight()
			m.achatAutoScrollToBottom(viewH)
		}
		cmds = append(cmds, spinnerTick())

	case navLoadedMsg:
		if msg.err != "" {
			m.navErr = msg.err
		} else {
			m.navItems = msg.items
			m.navItemsAll = msg.items
			m.navCursor = 0
			m.navScroll = 0
			// Restore article cursor from saved state.
			if slug := m.restoredState.Article; slug != "" {
				for i, item := range m.navItems {
					if item.id == slug {
						m.navCursor = i
						break
					}
				}
				m.restoredState.Article = ""
			}
			// Rebuild wsRows now that article titles are available.
			if m.workspacesLoaded {
				m.wsRows = m.buildWsRows()
			}
		}
		m.navLoaded = true
		// Scan for article chat indicators.
		if len(m.navItemsAll) > 0 {
			slugs := make([]string, len(m.navItemsAll))
			for i, it := range m.navItemsAll {
				slugs[i] = it.id
			}
			cmds = append(cmds, scanArticleChatsCmd(m.cfg.ArticlesRoot, slugs))
		}
		// Trigger content load for the selected item.
		if m.navCursor >= 0 && m.navCursor < len(m.navItems) && m.navItems[m.navCursor].root != "" {
			m.contentLoading = true
			cmds = append(cmds, loadContent(m.navItems[m.navCursor].root, m.cfg.PreferredStyles, m.cfg.PreferredModels))
		}
		// Deferred expand: collections loaded before articles — trigger now.
		if slug := m.pendingExpandSlug; slug != "" && m.collectionsLoaded {
			for i, r := range m.navRows {
				if r.kind == rowCollection && r.colSlug == slug {
					cmds = append(cmds, m.expandCollection(i))
					break
				}
			}
			m.pendingExpandSlug = ""
		}

	case achatScanDoneMsg:
		m.achatHasChat = msg.hasChat

	case collectionsLoadedMsg:
		if msg.err != "" {
			m.collectionsErr = msg.err
		} else {
			rows := make([]navRow, 0, len(msg.collections))
			for _, c := range msg.collections {
				rows = append(rows, navRow{
					kind:          rowCollection,
					colSlug:       c.Slug,
					colNumID:      c.NumID,
					colName:       c.Name,
					colDesc:       c.Description,
					colCount:      c.ArticleCount,
					colCreatedAt:  c.CreatedAt,
					colHasSummary: c.HasSummary,
					colHasSystem:  c.HasSystem,
				})
			}
			m.navRows = rows
			m.navRowsAll = rows
			m.navRowCursor = 0
			m.navRowScroll = 0
			// Restore collection cursor from saved state.
			if slug := m.restoredState.Collection; slug != "" {
				for i, r := range m.navRows {
					if r.kind == rowCollection && r.colSlug == slug {
						m.navRowCursor = i
						break
					}
				}
				m.restoredState.Collection = ""
			}
			// Restore expanded collection (triggers async article load).
			// Must wait until navItemsAll is populated; if not ready, defer to navLoadedMsg.
			if slug := m.restoredState.ExpandedCollection; slug != "" {
				if m.navLoaded {
					for i, r := range m.navRows {
						if r.kind == rowCollection && r.colSlug == slug {
							cmds = append(cmds, m.expandCollection(i))
							break
						}
					}
				} else {
					// Articles not yet loaded; defer expand to navLoadedMsg.
					m.pendingExpandSlug = slug
				}
				// ExpandedCollection/NavArticle cleared after articles load.
			}
		}
		m.collectionsLoaded = true

	case collectionSearchMsg:
		if msg.err != "" {
			m.setStatusError("✗ " + msg.err)
		} else {
			rows := make([]navRow, 0, len(msg.results))
			for _, c := range msg.results {
				rows = append(rows, navRow{
					kind:         rowCollection,
					colSlug:      c.Slug,
					colNumID:     c.NumID,
					colName:      c.Name,
					colDesc:      c.Description,
					colCount:     c.ArticleCount,
					colCreatedAt: c.CreatedAt,
					colHasSummary: c.HasSummary,
					colHasSystem:  c.HasSystem,
				})
			}
			m.navRows = rows
			m.navRowCursor = 0
			m.navRowScroll = 0
			m.focus = paneNav
			n := len(rows)
			if n == 0 {
				m.statusMsg = fmt.Sprintf("no collections matching %q", msg.query)
				m.navFilter = ""
			} else {
				m.navFilter = fmt.Sprintf("collections: %q · %d results  ·  /clear to reset", msg.query, n)
				m.statusMsg = ""
			}
		}

	case collectionArticlesLoadedMsg:
		if msg.err != "" {
			m.statusMsg = "✗ " + msg.err
		} else {
			// Find header by slug (index may have shifted from concurrent expands).
			headerIdx := -1
			for i, r := range m.navRows {
				if r.kind == rowCollection && r.colSlug == msg.slug {
					headerIdx = i
					break
				}
			}
			if headerIdx >= 0 {
				m.navRows[headerIdx].expanded = true
				children := make([]navRow, 0, len(msg.items))
				for i := range msg.items {
					item := msg.items[i]
					children = append(children, navRow{
						kind:     rowArticle,
						item:     &item,
						indented: true,
					})
				}
				before := make([]navRow, headerIdx+1)
				copy(before, m.navRows[:headerIdx+1])
				after := make([]navRow, len(m.navRows)-(headerIdx+1))
				copy(after, m.navRows[headerIdx+1:])
				m.navRows = append(append(before, children...), after...)
				// Restore article cursor within the expanded collection.
				if articleSlug := m.restoredState.NavArticle; articleSlug != "" && m.restoredState.ExpandedCollection == msg.slug {
					for i := headerIdx + 1; i < len(m.navRows); i++ {
						r := m.navRows[i]
						if r.kind != rowArticle || !r.indented {
							break
						}
						if r.item != nil && r.item.id == articleSlug {
							m.navRowCursor = i
							break
						}
					}
					m.restoredState.NavArticle = ""
					m.restoredState.ExpandedCollection = ""
				}
				m.clampNavRowScroll()
				m.statusMsg = ""
			}
		}

	case workspacesLoadedMsg:
		if msg.err != "" {
			m.workspacesErr = msg.err
		} else {
			// Carry over UI state (expanded, scroll) from old items.
			old := make(map[string]*workspaceItem, len(m.workspaceItems))
			for i := range m.workspaceItems {
				old[m.workspaceItems[i].name] = &m.workspaceItems[i]
			}
			for i := range msg.items {
				if prev, ok := old[msg.items[i].name]; ok {
					msg.items[i].expanded = prev.expanded
					msg.items[i].expandedCols = prev.expandedCols
					msg.items[i].resourcesExpanded = prev.resourcesExpanded
					msg.items[i].expandedResourceDirs = prev.expandedResourceDirs
					msg.items[i].outcomesExpanded = prev.outcomesExpanded
					msg.items[i].atticExpanded = prev.atticExpanded
				}
			}
			m.workspaceItemsAll = msg.items
			// Re-apply focus filter if active.
			if m.wsFocusName != "" {
				var focused []workspaceItem
				for _, ws := range msg.items {
					if ws.name == m.wsFocusName {
						focused = append(focused, ws)
						break
					}
				}
				if len(focused) > 0 {
					m.workspaceItems = focused
				} else {
					// Focused workspace was deleted — clear focus.
					m.wsFocusName = ""
					m.workspaceItems = msg.items
				}
			} else {
				m.workspaceItems = msg.items
			}
			// Restore workspace expand state before buildWsRows so expanded rows are present.
			if name := m.restoredState.Workspace; name != "" && m.restoredState.WsExpanded {
				for i := range m.workspaceItems {
					if m.workspaceItems[i].name == name {
						m.workspaceItems[i].expanded = true
						if col := m.restoredState.WsExpandedCol; col != "" {
							if m.workspaceItems[i].expandedCols == nil {
								m.workspaceItems[i].expandedCols = make(map[string]bool)
							}
							m.workspaceItems[i].expandedCols[col] = true
						}
						break
					}
				}
			}
			m.wsRows = m.buildWsRows()
			// Restore workspace cursor from saved state, or clamp to bounds.
			if name := m.restoredState.Workspace; name != "" {
				articleSlug := m.restoredState.WsArticle
				colSlug := m.restoredState.WsExpandedCol
				for i, row := range m.wsRows {
					wsIdx := row.wsIdx
					if wsIdx < 0 || wsIdx >= len(m.workspaceItems) || m.workspaceItems[wsIdx].name != name {
						continue
					}
					if articleSlug != "" && row.kind == wsRowArticle && row.slug == articleSlug {
						m.wsCursor = i
						break
					}
					if articleSlug == "" && colSlug != "" && row.kind == wsRowCollection && row.colSlug == colSlug {
						m.wsCursor = i
						break
					}
					if articleSlug == "" && colSlug == "" && row.kind == wsRowWorkspace {
						m.wsCursor = i
						break
					}
				}
				m.restoredState.Workspace = ""
				m.restoredState.WsExpanded = false
				m.restoredState.WsExpandedCol = ""
				m.restoredState.WsArticle = ""
			}
			if m.wsCursor >= len(m.wsRows) {
				m.wsCursor = len(m.wsRows) - 1
			}
			if m.wsCursor < 0 {
				m.wsCursor = 0
			}
		}
		m.workspacesLoaded = true
		// Auto-load history for first workspace if on Workspaces sub-tab.
		if m.activeTab == tabLibrary && m.navSubTab == navSubTabWorkspaces {
			cmds = append(cmds, m.triggerWorkspaceChatLoad())
		}
		// If inside a workspace (chat mode), refresh article count.
		if m.chatMode && m.chatWorkspace != "" {
			cmds = append(cmds, m.loadChatHistoryCmd(m.chatWorkspace, false))
		}

	case agentRunDoneMsg:
		slog.Debug("agentRunDoneMsg received",
			"is_rerun", msg.isRerun, "err", msg.err,
			"new_run_id", msg.newRunID,
			"ingested", msg.rec.TotalIngest)
		m.agentRunning = false
		m.agentRunCancelFn = nil
		m.ingestLabel = ""
		m.ingestLog = nil
		if msg.err != "" {
			slog.Error("agentRunDoneMsg error", "err", msg.err)
			m.setStatusError("✗ agent: " + msg.err)
		} else if msg.isRerun {
			n := msg.rec.TotalIngest
			slog.Info("agentRunDoneMsg rerun success", "ingested", n)
			m.statusMsg = fmt.Sprintf("✓ rerun complete — %d ingested", n)
			m.statusSuccess = true
			// Stay on the original run after reload (not the new decisions-type run).
			m.restoredState.AgentRunID = m.agentRunDecisionsID
			// Reload the decisions file so done items are trimmed.
			cmds = append(cmds, loadAgentDecisions(m.cfg.AgentPath, m.agentRunDecisionsID))
			// Index newly ingested articles into SQLite (pipeline writes files only).
			if len(msg.rec.IngestedSlugs) > 0 {
				svc := m.svc
				slugs := msg.rec.IngestedSlugs
				cmds = append(cmds, func() tea.Msg {
					if err := svc.Library().IndexSlugs(context.Background(), slugs); err != nil {
						slog.Warn("index after decisions rerun failed", "err", err)
					}
					return nil
				})
			}
		} else {
			n := msg.rec.TotalIngest
			slog.Info("agentRunDoneMsg fresh run success",
				"run_id", msg.newRunID, "ingested", n)
			m.statusMsg = fmt.Sprintf("✓ agent run complete — %d ingested", n)
			m.statusSuccess = true
			// Index newly ingested articles into SQLite (pipeline writes files only).
			if len(msg.rec.IngestedSlugs) > 0 {
				svc := m.svc
				slugs := msg.rec.IngestedSlugs
				cmds = append(cmds, func() tea.Msg {
					if err := svc.Library().IndexSlugs(context.Background(), slugs); err != nil {
						slog.Warn("index after agent run failed", "err", err)
					}
					return nil
				})
			}
		}
		// Reload runs list; auto-select new run after load.
		if msg.newRunID != "" {
			m.restoredState.AgentRunID = msg.newRunID
		}
		cmds = append(cmds, loadAgentRuns(m.cfg.AgentPath))
		return m, tea.Batch(cmds...)

	case agentRunIngestedLoadedMsg:
		if m.agentRunsCursor >= 0 && m.agentRunsCursor < len(m.agentRuns) &&
			msg.runID == m.agentRuns[m.agentRunsCursor].RunID {
			m.agentRunIngested = msg.articles
			m.agentRunIngestedID = msg.runID
			m.agentRunIngestedErr = msg.err
		}

	case agentDecisionsLoadedMsg:
		if msg.runID == m.agentRunDecisionsID {
			if msg.err == "" {
				m.agentRunDecisions = msg.df
			}
		}

	case agentRunsLoadedMsg:
		if msg.err != "" {
			m.agentRunsErr = msg.err
		} else {
			// Reverse so most recent is first.
			recs := msg.runs
			for i, j := 0, len(recs)-1; i < j; i, j = i+1, j-1 {
				recs[i], recs[j] = recs[j], recs[i]
			}
			m.agentRuns = recs
			m.agentRunsErr = ""
		}
		m.agentRunsLoaded = true
		// Restore previously selected run by ID.
		if id := m.restoredState.AgentRunID; id != "" {
			for i, r := range m.agentRuns {
				if r.RunID == id {
					m.agentRunsCursor = i
					break
				}
			}
			m.restoredState.AgentRunID = ""
		}
		// Auto-load decisions for the selected run.
		cmds = append(cmds, m.triggerAgentRunDetail())

	case agentFeedsLoadedMsg:
		m.agentFeedsLoaded = true
		if msg.err != "" {
			m.agentFeedsErr = msg.err
		} else {
			m.agentFeeds = msg.feeds
			m.agentFeedsStats = msg.stats
			m.agentFeedsErr = ""
			if m.agentFeedsCursor >= len(m.agentFeeds) {
				m.agentFeedsCursor = max(0, len(m.agentFeeds)-1)
			}
		}

	case agentFeedSavedMsg:
		if msg.err != "" {
			m.setStatusError("feed: " + msg.err)
		} else {
			m.agentFeeds = msg.feeds
			if m.agentFeedsCursor >= len(m.agentFeeds) {
				m.agentFeedsCursor = max(0, len(m.agentFeeds)-1)
			}
			m.statusMsg = "feed saved"
		}

	case agentFeedRunDecisionsLoadedMsg:
		if m.agentFeedRunDecisions == nil {
			m.agentFeedRunDecisions = make(map[string]agentpkg.DecisionsFile)
		}
		if msg.err == "" {
			m.agentFeedRunDecisions[msg.fileID] = msg.df
		}

	case statsLoadedMsg:
		if msg.err == "" {
			m.stats = msg.stats
			m.statsLoaded = true
		}

	case chromeOpenedMsg:
		if msg.err == nil && msg.windowID != "" {
			m.chromeWindowID = msg.windowID
		}

	case populateEditMsg:
		m.populateRunning = false
		m.populateLabel = ""
		if len(msg.items) == 0 {
			m.statusMsg = "✗ no suggestions to review"
			break
		}
		m.populateEditing = true
		m.populateEditItems = msg.items
		m.populateEditIdx = 0
		m.populateEditWs = msg.workspace
		m.populateEditCost = msg.cost
		m.populateEditHint = msg.hint
		m.populateEditLog = msg.log
		m.focus = paneCommand
		m.cursorVisible = true
		m.input.SetValue("")
		m.input.CursorEnd()

	case statusUpdateMsg:
		if m.ingestRunning || m.agentRunning {
			if m.ingestLabel != "" {
				m.ingestLog = append(m.ingestLog, m.ingestLabel)
				if len(m.ingestLog) > 3 {
					m.ingestLog = m.ingestLog[len(m.ingestLog)-3:]
				}
			}
			m.ingestLabel = msg.text
		} else {
			m.statusMsg = msg.text
		}

	case ingestCostEstimateMsg:
		if msg.usd > 0 {
			m.ingestCostEstimate = fmt.Sprintf("⚡ est. ~$%.3f  ·  %d chunks", msg.usd, msg.nChunks)
		} else {
			m.ingestCostEstimate = fmt.Sprintf("⚡ est. cost unknown  ·  %d chunks", msg.nChunks)
		}

	case cmdDoneMsg:
		m.populateRunning = false
		m.populateLabel = ""
		m.ingestRunning = false
		if m.ingestCancelFn != nil {
			m.ingestCancelFn()
			m.ingestCancelFn = nil
		}
		m.ingestLabel = ""
		m.ingestLog = nil
		m.ingestCostEstimate = ""
		if msg.err == "" && strings.HasPrefix(msg.statusMsg, "✓") {
			m.statusSuccess = true
		}
		if msg.err != "" {
			m.setStatusError("✗ " + msg.err)
		} else {
			m.statusErr = false
			m.statusMsg = msg.statusMsg
			m.setStatusLines(msg.statusLines)
		}
		if msg.navItems != nil {
			m.navItems = msg.navItems
			m.navFilter = msg.navFilter
			m.navCursor = 0
			m.navScroll = 0
			if msg.err == "" {
				m.focus = paneNav
			}
			cmds = append(cmds, m.triggerContentLoad())
		}
		if msg.reloadNav && m.svc != nil {
			cmds = append(cmds, loadNav(m.svc))
		}
		if msg.reloadCollections && m.svc != nil {
			m.collectionsLoaded = false
			m.focus = paneNav
			cmds = append(cmds, loadCollectionsTree(m.svc))
		}
		if msg.reloadWorkspaces && m.svc != nil {
			m.workspacesLoaded = false
			m.focus = paneNav
			cmds = append(cmds, loadWorkspaces(m.svc))
		}
		if m.scratchOpen {
			m.reloadScratchLines()
		}
		if msg.resetChatEngine && msg.resetChatWorkspace != "" &&
			m.chatMode && m.chatWorkspace == msg.resetChatWorkspace {
			m.chatEngine = nil
			if m.statusMsg == "" {
				m.statusMsg = "✓ context reloaded — engine will reinitialise on next message"
			}
		}

	case correctionDoneMsg:
		m.correcting = false
		if msg.err == nil && msg.text != "" {
			corrected := m.correctionPrefix + msg.text
			m.correctionPrefix = ""
			if corrected != m.input.Value() {
				m.statusMsg = "✓ corrected"
			} else {
				m.statusMsg = "✓ no changes"
			}
			m.statusErr = false
			m.input.SetValue(corrected)
			m.input.CursorEnd()
			m.syncInputHeight()
		} else if msg.err != nil {
			errStr := msg.err.Error()
			if len(errStr) > 40 {
				errStr = errStr[:40] + "…"
			}
			m.statusMsg = "✗ " + errStr
			m.statusErr = true
		}
		cmds = append(cmds, tea.Tick(2*time.Second, func(time.Time) tea.Msg {
			return correctionFlashMsg{}
		}))

	case correctionFlashMsg:
		if m.statusMsg == "✓ corrected" || m.statusMsg == "✓ no changes" ||
			strings.HasPrefix(m.statusMsg, "✗ ") {
			m.statusMsg = ""
			m.statusErr = false
		}

	case ttsDoneMsg:
		if msg.gen != m.ttsGen {
			break // stale — a new Play or Stop has superseded this one
		}
		m.ttsCurrentText = ""
		// Drain resource paragraph-block queue.
		if len(m.resourceTTSQueue) > 0 && m.focus == paneResource {
			next := m.resourceTTSQueue[0]
			m.resourceTTSQueue = m.resourceTTSQueue[1:]
			m.resourceCursor = next.cursorLine
			m.resourceTTSText = next.text
			viewH := m.height - 4
			if viewH < 1 {
				viewH = 1
			}
			m.scrollResourceToCursor(viewH)
			text := tts.Strip(m.resourceTTSText)
			playFn := m.ttsPlayer.Play(text)
			m.ttsGen = m.ttsPlayer.Gen()
			m.ttsCurrentText = text
			cmds = append(cmds, func() tea.Msg {
				done := playFn()
				return ttsDoneMsg{err: done.Err, gen: done.Gen}
			})
			break
		}
		m.resourceTTSText = ""
		// Drain content paragraph-block queue.
		if len(m.contentTTSQueue) > 0 && m.focus == paneContent && !m.chatMode {
			next := m.contentTTSQueue[0]
			m.contentTTSQueue = m.contentTTSQueue[1:]
			m.contentLineCursor = next.cursorLine
			viewH := m.contentViewHeight()
			m.scrollContentToCursor(viewH)
			m.contentTTSText = next.text
			text := tts.Strip(m.contentTTSText)
			playFn := m.ttsPlayer.Play(text)
			m.ttsGen = m.ttsPlayer.Gen()
			m.ttsCurrentText = text
			cmds = append(cmds, func() tea.Msg {
				done := playFn()
				return ttsDoneMsg{err: done.Err, gen: done.Gen}
			})
			break
		}
		m.contentTTSText = ""
		// Drain chat paragraph-block queue.
		if len(m.chatTTSQueue) > 0 && m.focus == paneContent && m.chatMode {
			next := m.chatTTSQueue[0]
			m.chatTTSQueue = m.chatTTSQueue[1:]
			m.chatTTSCursor = next.cursorLine
			m.chatTTSText = next.text
			viewH := m.height - 4
			if viewH < 1 {
				viewH = 1
			}
			m.scrollToChatTTSLine(viewH)
			text := tts.Strip(m.chatTTSText)
			playFn := m.ttsPlayer.Play(text)
			m.ttsGen = m.ttsPlayer.Gen()
			m.ttsCurrentText = text
			cmds = append(cmds, func() tea.Msg {
				done := playFn()
				return ttsDoneMsg{err: done.Err, gen: done.Gen}
			})
			break
		}
		m.chatTTSText = ""
		// Drain preview paragraph-block queue.
		if len(m.previewTTSQueue) > 0 && m.previewOpen {
			next := m.previewTTSQueue[0]
			m.previewTTSQueue = m.previewTTSQueue[1:]
			m.previewLineCursor = next.cursorLine
			m.previewTTSText = next.text
			viewH := m.previewViewH()
			m.scrollPreviewToCursor(viewH)
			text := tts.Strip(m.previewTTSText)
			playFn := m.ttsPlayer.Play(text)
			m.ttsGen = m.ttsPlayer.Gen()
			m.ttsCurrentText = text
			cmds = append(cmds, func() tea.Msg {
				done := playFn()
				return ttsDoneMsg{err: done.Err, gen: done.Gen}
			})
			break
		}
		m.previewTTSText = ""
		m.statusMsg = ""

	case shellDoneMsg:
		m.statusErr = false
		header := "! " + msg.cmd
		output := strings.Split(strings.TrimRight(msg.output, "\n"), "\n")
		lines := make([]string, 0, 1+len(output)+1)
		lines = append(lines, header)
		lines = append(lines, output...)
		if msg.exitCode != 0 {
			lines = append(lines, fmt.Sprintf("[exit %d]", msg.exitCode))
			m.statusErr = true
		}
		m.setStatusLines(lines)
		m.focus = paneStatus

	case resourceReloadMsg:
		// Re-read the file after external editor exits.
		if m.chatMode && m.chatWorkspace != "" {
			name := msg.name
			filePath := storefs.WorkspaceDir(m.cfg.DataRoot, m.chatWorkspace) + "/resources/" + name
			if data, err := os.ReadFile(filePath); err == nil {
				text := string(data)
				if m.focus == paneResource && m.resourceName == name {
					m.resourceLines = strings.Split(text, "\n")
					if m.resourceCursor >= len(m.resourceLines) {
						m.resourceCursor = len(m.resourceLines) - 1
					}
				} else {
					// Re-open the overlay.
					m.openResourceOverlay(name, text)
				}
			}
		}

	case contentLoadedMsg:
		m.contentFiles = msg.files
		m.contentLines = msg.lines
		m.contentOffsets = msg.offsets
		m.contentHas = msg.has
		m.contentScroll = 0
		m.contentLineCursor = 0
		m.contentLoading = false

	case chatHistoryLoadedMsg:
		if msg.err != "" {
			m.statusMsg = "✗ " + msg.err
		} else {
			// Cancel any in-flight stream from a previous workspace.
			if m.chatCancelStream != nil {
				m.chatCancelStream()
				m.chatCancelStream = nil
			}
			m.chatMode = true
			m.chatEngine = nil        // lazy — engine init deferred to first message
			m.chatPendingPrompt = ""  // clear any pending prompt from previous workspace
			m.chatProfileOverride = "" // reset session-only override on workspace change
			m.chatLoadedProfile = msg.profile // profile from workspace chat/chat.json
			m.chatWorkspace = msg.workspace
			m.chatRawMsgs = msg.msgs
			m.chatArticleCount = msg.articleCount
			m.chatGroundingMode = msg.groundingMode
			m.chatWorkspaceStats = msg.workspaceStats
			m.chatAutoScroll = true
			m.chatStreaming = false
			m.chatStreamBuf = ""
			m.chatSharedBuf = nil
			m.chatLastUsage = nil
			m.chatLastElapsed = 0
			if msg.focus {
				m.focus = paneCommand
				m.cursorVisible = true
			}
			m.rebuildChatLines(m.chatBuildWidth())
			m.collapseAllBoxes()
			chatViewH := m.chatViewHeight()
			m.chatAutoScrollToBottom(chatViewH)
			m.chatBoxCursor = 0
			m.statusMsg = ""
		}

	case chatReadyMsg:
		if msg.err != "" {
			// Only show error if still on the same workspace.
			if m.chatWorkspace == msg.workspace {
				m.statusMsg = "✗ chat: " + msg.err
				m.setStatusLines([]string{"Chat initialization failed:", msg.err})
			}
		} else if m.chatMode && m.chatWorkspace == msg.workspace {
			// Only apply if user hasn't navigated away.
			m.chatEngine = msg.engine
			m.chatGroundingMode = msg.engine.GroundingMode()
			// Sync raw msgs from engine history.
			m.chatRawMsgs = msg.engine.History().Msgs
			m.rebuildChatLines(m.chatBuildWidth())
			m.statusMsg = ""
			// If a prompt was queued for this workspace, send it now.
			if m.chatPendingPrompt != "" {
				prompt := m.chatPendingPrompt
				m.chatPendingPrompt = ""
				cmds = append(cmds, m.sendChatMsg(prompt))
			}
		}

	case chatStreamDoneMsg:
		m.chatStreaming = false
		m.chatStreamBuf = ""
		m.chatSharedBuf = nil
		m.chatActivityLine = ""
		m.chatStreamingUserPrompt = ""
		if m.chatCancelStream != nil {
			m.chatCancelStream = nil
		}
		if msg.err != "" {
			m.statusMsg = "✗ " + msg.err
		} else {
			usage := msg.usage
			m.chatLastUsage = &usage
			m.chatLastElapsed = msg.elapsed
			// Build completion status: ✓ model · N tools · $cost  Xs
			profile := ""
			if m.chatEngine != nil {
				profile = m.chatEngine.Profile().Model
			}
			cost := formatUSD(m.cfg.CalcCost(profile, usage.InputTokens, usage.OutputTokens))
			status := "✓ " + profile
			if msg.toolCalls > 0 {
				status += fmt.Sprintf(" · %d tool calls", msg.toolCalls)
			}
			status += " · " + cost + fmt.Sprintf("  %.1fs", msg.elapsed.Seconds())
			m.statusMsg = status
		}
		m.rebuildChatLines(m.chatBuildWidth())
		// Leave collapse state untouched — the new response is not in chatCollapsed
		// so it renders fully expanded. Previous boxes stay as they were.
		if n := m.chatBoxCount(); n > 0 {
			if m.chatAutoScroll {
				m.chatBoxCursor = n - 1
			}
		}
		chatViewH := m.chatViewHeight()
		m.chatAutoScrollToBottom(chatViewH)
		if msg.err == "" {
			cmds = append(cmds, loadStats(m.svc), m.loadChatWorkspaceStatsCmd())
		}

	case chatWorkspaceStatsMsg:
		m.chatWorkspaceStats = msg.stats

	case achatHistoryLoadedMsg:
		if msg.err != "" {
			m.statusMsg = "✗ " + msg.err
			m.statusErr = true
		} else if m.achatMode && m.achatSlug == msg.slug {
			m.achatRawMsgs = msg.msgs
			m.achatProfile = msg.profile
			if m.achatProfile == "" {
				m.achatProfile = m.cfg.ArticleChatProfileName()
			}
			m.focus = paneCommand
			m.cursorVisible = true
			m.syncInputPrompt()
			m.rebuildArticleChatLines(m.achatBuildWidth())
			m.collapseAllArticleChatBoxes()
			viewH := m.achatViewHeight()
			m.achatAutoScrollToBottom(viewH)
			m.achatBoxCursor = 0
			m.statusMsg = ""
		}

	case achatReadyMsg:
		if msg.err != "" {
			if m.achatMode && m.achatSlug == msg.slug {
				m.statusMsg = "✗ chat: " + msg.err
			}
		} else if m.achatMode && m.achatSlug == msg.slug {
			m.achatEngine = msg.engine
			m.achatRawMsgs = msg.engine.History().Msgs
			m.rebuildArticleChatLines(m.achatBuildWidth())
			m.statusMsg = ""
			// If a prompt was queued, send it now.
			if m.achatPendingPrompt != "" {
				prompt := m.achatPendingPrompt
				m.achatPendingPrompt = ""
				cmds = append(cmds, m.sendArticleChatMsg(prompt))
			}
		}

	case achatStreamDoneMsg:
		m.achatStreaming = false
		m.achatStreamBuf = ""
		m.achatSharedBuf = nil
		if m.achatCancelStream != nil {
			m.achatCancelStream = nil
		}
		if msg.err != "" {
			m.statusMsg = "✗ " + msg.err
		} else {
			usage := msg.usage
			m.achatLastUsage = &usage
			m.achatLastElapsed = msg.elapsed
			m.achatSessionIn += usage.InputTokens
			m.achatSessionOut += usage.OutputTokens
			m.achatSessionTurns++
			profile := ""
			if m.achatEngine != nil {
				profile = m.achatEngine.Profile().Model
			}
			m.achatSessionCost += m.cfg.CalcCost(profile, usage.InputTokens, usage.OutputTokens)
			cost := formatUSD(m.cfg.CalcCost(profile, usage.InputTokens, usage.OutputTokens))
			m.statusMsg = "✓ " + profile + " · " + cost + fmt.Sprintf("  %.1fs", msg.elapsed.Seconds())
		}
		m.rebuildArticleChatLines(m.achatBuildWidth())
		if n := m.achatBoxCount(); n > 0 {
			if m.achatAutoScroll {
				m.achatBoxCursor = n - 1
			}
		}
		viewH := m.achatViewHeight()
		m.achatAutoScrollToBottom(viewH)
		// Mark article as having chat.
		if m.achatHasChat == nil {
			m.achatHasChat = map[string]bool{}
		}
		m.achatHasChat[m.achatSlug] = true

	case askxStreamDoneMsg:
		m.handleAskXStreamDone(msg)
		if msg.costUSD > 0 {
			cmds = append(cmds, loadStats(m.svc))
		}

	case tea.KeyMsg:
		cmds = append(cmds, m.handleKey(msg))

	case tea.MouseMsg:
		cmds = append(cmds, m.handleMouse(msg))

	}

	return m, tea.Batch(cmds...)
}

// handleKey routes key events based on active focus pane.
func (m *Model) handleKey(msg tea.KeyMsg) tea.Cmd {
	// Paste: skip global keys, route directly to command handler.
	if msg.Paste || (msg.String() == "ctrl+v" && m.focus == paneCommand) {
		return m.handleCommandKey(msg)
	}

	// Global keys — always active
	switch {
	case msg.String() == "ctrl+s":
		m.selectionMode = true
		m.preSelNavWidth = m.navWidthOverride
		// Maximize the focused pane (hide the other).
		switch m.focus {
		case paneNav:
			m.selectionMaxPane = paneNav
			m.navWidthOverride = m.width - 1
		case paneContent:
			m.selectionMaxPane = paneContent
			m.navWidthOverride = 0
		default:
			m.selectionMaxPane = 0 // no maximization for command pane
		}
		// One final redraw shows the status message, then screen freezes.
		return tea.DisableMouse
	case msg.String() == "ctrl+c" && m.focus == paneCommand && m.input.Value() != "":
		m.copyToClipboard(m.input.Value())
		return nil
	case key.Matches(msg, keys.Quit) && !(m.focus == paneCommand && msg.String() == "q"):
		return tea.Quit
	case key.Matches(msg, keys.Back):
		// Resource overlay: Esc closes and restores previous focus.
		if m.focus == paneResource {
			m.closeResourceOverlay()
			return nil
		}
		// Cancel in-flight ingest if running.
		if m.ingestRunning && m.ingestCancelFn != nil {
			m.ingestCancelFn()
			m.ingestCancelFn = nil
			m.ingestRunning = false
			m.ingestLabel = ""
			m.ingestLog = nil
			m.ingestCostEstimate = ""
			m.statusMsg = "ingest cancelled"
			return nil
		}
		m.cmdComplete = nil
		m.cmdCompleteIdx = -1
		m.paramItems = nil
		m.paramIdx = -1
		m.statusMsg = ""
		m.statusLines = nil
		m.pendingConfirm = nil
		m.pendingConfirmMsg = ""
		m.agentConfirmAction = nil
		m.agentConfirmLines = nil
		if m.populateEditing {
			m.populateEditing = false
			m.statusMsg = "populate edit cancelled"
		}
		if m.removeReviewing {
			m.removeReviewing = false
			m.statusMsg = "remove review cancelled"
		}
		// Article chat: Esc with empty input exits article chat mode.
		if m.achatMode && m.input.Value() == "" && m.focus == paneCommand {
			m.exitArticleChat()
			m.syncInputPrompt()
			m.setFocusPane(paneNav)
			return nil
		}
		// If input is already empty and not in chat mode, move focus to nav.
		if m.input.Value() == "" && !m.chatMode && !m.achatMode && m.focus == paneCommand {
			m.setFocusPane(paneNav)
			return nil
		}
		m.input.SetValue("")
		m.input.CursorEnd()
		m.pastedBlob = ""
		m.syncInputHeight()
		// In chat mode, Esc always returns focus to command input — never exits chat.
		// Use /exit or q to leave chat.
		// In chat mode, Esc always returns focus to command input — never exits chat.
		// Use /exit or q to leave chat.
		m.focus = paneCommand
		m.cursorVisible = true
		return nil
	case key.Matches(msg, keys.Scratch):
		m.toggleScratch()
		return nil
	case key.Matches(msg, keys.AskX):
		m.toggleAskX()
		return nil
	case key.Matches(msg, keys.Preview):
		m.togglePreview()
		return nil

	case key.Matches(msg, keys.CorrectInput):
		if !m.correcting && strings.TrimSpace(m.input.Value()) != "" {
			m.correcting = true
			m.statusMsg = "correcting…"
			m.statusErr = false
			// Strip command prefix (e.g. "/scratch ", "//") so the LLM only sees prose.
			text := m.input.Value()
			m.correctionPrefix = ""
			if strings.HasPrefix(text, "//") {
				m.correctionPrefix = "//"
				text = text[2:]
			} else if strings.HasPrefix(text, "/") {
				if idx := strings.Index(text, " "); idx >= 0 {
					m.correctionPrefix = text[:idx+1]
					text = text[idx+1:]
				}
			}
			return doCorrection(text, m.cfg)
		}
		return nil

	case key.Matches(msg, keys.Refresh):
		if m.svc == nil {
			return nil
		}
		var batch []tea.Cmd
		switch m.activeTab {
		case tabLibrary:
			switch m.navSubTab {
			case navSubTabArticles:
				batch = append(batch, loadNav(m.svc))
			case navSubTabCollections:
				m.collectionsLoaded = false
				batch = append(batch, loadCollectionsTree(m.svc))
			case navSubTabWorkspaces:
				m.workspacesLoaded = false
				batch = append(batch, loadWorkspaces(m.svc))
			}
			batch = append(batch, m.triggerContentLoad())
		case tabAgent:
			m.agentRunsLoaded = false
			batch = append(batch, loadAgentRuns(m.cfg.AgentPath))
		case tabStats:
			m.statsLoaded = false
			batch = append(batch, loadStats(m.svc))
		}
		m.statusMsg = "↻ refreshed"
		return tea.Batch(batch...)

	case key.Matches(msg, keys.FocusNav):
		m.setFocusPane(paneNav)
		return nil
	case key.Matches(msg, keys.FocusContent):
		m.setFocusPane(paneContent)
		return nil
	case key.Matches(msg, keys.FocusTabBar):
		m.setFocusPane(paneTabBar)
		return nil

	case key.Matches(msg, keys.PaneNext):
		// If param picker active, Tab fills selected param into input.
		if len(m.paramItems) > 0 && m.paramIdx >= 0 {
			m.acceptParam()
			return nil
		}
		// If completions active, Tab accepts the selected command.
		if len(m.cmdComplete) > 0 {
			m.acceptCompletion()
			return nil
		}
		// Tab cycles forward: Nav → Content → Split (if open) → Nav.
		splitOpen := m.previewOpen || m.scratchOpen || m.askxOpen
		switch {
		case m.focus == paneNav:
			m.setFocusPane(paneContent)
		case m.focus == paneContent && !m.splitPaneFocused() && splitOpen:
			clog.Debugf("tab: content → split pane")
			m.focusSplitPane()
		default:
			m.setFocusPane(paneNav)
		}
		return nil
	case key.Matches(msg, keys.PanePrev):
		// Shift+Tab cycles backward: Nav → Split (if open) → Content → Nav.
		splitOpen := m.previewOpen || m.scratchOpen || m.askxOpen
		switch {
		case m.focus == paneNav && splitOpen:
			clog.Debugf("shift-tab: nav → split pane")
			m.focusSplitPane()
		case m.focus == paneContent && m.splitPaneFocused():
			clog.Debugf("shift-tab: split pane → content")
			m.unfocusSplitPane()
		default:
			m.setFocusPane(paneNav)
		}
		return nil
	}

	// Pane-specific keys
	switch m.focus {
	case paneTabBar:
		return m.handleTabBarKey(msg)
	case paneNav:
		return m.handleNavKey(msg)
	case paneContent:
		return m.handleContentKey(msg)
	case paneCommand:
		return m.handleCommandKey(msg)
	case paneStatus:
		return m.handleStatusKey(msg)
	case paneResource:
		return m.handleResourceKey(msg)
	case paneNavSubTab:
		return m.handleNavSubTabKey(msg)
	}

	return nil
}

// setFocusPane switches focus to the given pane and resets related state.
func (m *Model) setFocusPane(p focusPane) {
	clog.Debugf("setFocusPane: %v → %v (previewFocused=%v scratchFocused=%v)", m.focus, p, m.previewFocused, m.scratchFocused)
	m.focus = p
	m.scratchFocused = false
	m.askxFocused = false
	m.achatFocused = false
	m.previewFocused = false
	if p == paneCommand {
		m.cursorVisible = true
	}
	if m.chatMode {
		m.rebuildChatLines(m.chatBuildWidth())
		if p == paneContent {
			if n := m.chatBoxCount(); n > 0 {
				m.chatBoxCursor = n - 1
			}
			m.chatScroll = m.chatTotalLines()
		}
	}
	if m.achatMode && p == paneContent {
		m.achatFocused = true
		m.rebuildArticleChatLines(m.achatBuildWidth())
		if n := m.achatBoxCount(); n > 0 {
			m.achatBoxCursor = n - 1
		}
	}
}

// splitPaneFocused reports whether the currently visible split pane has focus.
func (m *Model) splitPaneFocused() bool {
	return m.previewFocused || m.scratchFocused || m.askxFocused
}

// focusSplitPane gives focus to whichever split pane is currently open.
// Assumes at least one split pane is open (mutually exclusive in practice).
func (m *Model) focusSplitPane() {
	m.focus = paneContent
	switch {
	case m.previewOpen:
		m.previewFocused = true
	case m.scratchOpen:
		m.scratchFocused = true
	case m.askxOpen:
		m.askxFocused = true
	}
}

// unfocusSplitPane clears split-pane focus, leaving focus on paneContent (main area).
func (m *Model) unfocusSplitPane() {
	m.previewFocused = false
	m.scratchFocused = false
	m.askxFocused = false
	// m.focus stays paneContent — main content area retains keyboard focus.
}

// handleTabBarKey handles keys when the top tab bar has focus.
// ←/→ cycle top-level tabs; ↓ or Enter drops focus to nav pane.
// j/k and all other nav keys are intentionally ignored.
func (m *Model) handleTabBarKey(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, keys.ContentTabPrev):
		if m.achatMode {
			m.exitArticleChat()
		}
		if m.chatMode {
			m.exitChatMode()
		}
		m.activeTab = (m.activeTab - 1 + tabCount) % tabCount
	case key.Matches(msg, keys.ContentTabNext):
		if m.achatMode {
			m.exitArticleChat()
		}
		if m.chatMode {
			m.exitChatMode()
		}
		m.activeTab = (m.activeTab + 1) % tabCount
	case key.Matches(msg, keys.NavDown), key.Matches(msg, keys.Select):
		m.focus = paneNavSubTab
	}
	return nil
}

// handleNavSubTabKey handles keys when the nav sub-tab bar has focus.
// ↑ goes to the top tab bar; ↓/Enter drops into the nav list; ←/→ switch sub-tabs.
func (m *Model) handleNavSubTabKey(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, keys.NavUp):
		m.setFocusPane(paneTabBar)
	case key.Matches(msg, keys.NavDown), key.Matches(msg, keys.Select):
		m.setFocusPane(paneNav)
	case key.Matches(msg, keys.ContentTabPrev):
		return m.navLeft()
	case key.Matches(msg, keys.ContentTabNext):
		return m.navRight()
	}
	return nil
}

func (m *Model) handleNavKey(msg tea.KeyMsg) tea.Cmd {
	// Feed-specific operations in the Agent Feeds sub-tab.
	if m.activeTab == tabAgent && m.agentSubTab == agentSubTabFeeds && msg.Type == tea.KeyRunes {
		switch msg.String() {
		case "a":
			m.openEditorForFeed(-1)
			return nil
		case "e":
			if len(m.agentFeeds) > 0 {
				m.openEditorForFeed(m.agentFeedsCursor)
			}
			return nil
		case "d":
			if len(m.agentFeeds) > 0 {
				return toggleAgentFeed(m.cfg.AgentPath, m.agentFeedsCursor)
			}
			return nil
		case "D":
			if len(m.agentFeeds) > 0 {
				name := m.agentFeeds[m.agentFeedsCursor].Name
				if name == "" {
					name = m.agentFeeds[m.agentFeedsCursor].URL
				}
				idx := m.agentFeedsCursor
				m.askConfirm(fmt.Sprintf("delete %q? yes/no", name), func() tea.Cmd {
					return deleteAgentFeed(m.cfg.AgentPath, idx)
				})
			}
			return nil
		}
	}
	switch {
	case key.Matches(msg, keys.ContentTabPrev):
		return m.navLeft()
	case key.Matches(msg, keys.ContentTabNext):
		return m.navRight()
	case key.Matches(msg, keys.NavUp):
		if m.navAtTop() {
			m.setFocusPane(paneNavSubTab)
			return nil
		}
		if m.activeTab == tabAgent {
			return m.agentNavCursorUp()
		}
		return m.navCursorUp()
	case key.Matches(msg, keys.NavDown):
		if m.activeTab == tabAgent {
			return m.agentNavCursorDown()
		}
		return m.navCursorDown()
	case key.Matches(msg, keys.PageUp):
		return m.navPageUp()
	case key.Matches(msg, keys.PageDown):
		return m.navPageDown()
	case key.Matches(msg, keys.Home):
		return m.navHome()
	case key.Matches(msg, keys.End):
		return m.navEnd()
	case key.Matches(msg, keys.Expand):
		return m.navToggleExpand()
	case key.Matches(msg, keys.Select):
		if m.activeTab == tabAgent {
			m.setFocusPane(paneContent)
			return nil
		}
		return m.navSelect()
	case key.Matches(msg, keys.MarkRead):
		return m.cmdMarkRead()
	case key.Matches(msg, keys.MarkUnread):
		return m.cmdMarkUnread()
	case key.Matches(msg, keys.ToggleFav):
		if m.navSubTab == navSubTabWorkspaces {
			return m.cmdTogglePin()
		}
		return m.cmdToggleFavorite()
	case key.Matches(msg, keys.Delete):
		switch m.navSubTab {
		case navSubTabWorkspaces:
			row := m.selectedWsRow()
			if row != nil {
				switch row.kind {
				case wsRowResource, wsRowResourceDir:
					m.cmdResourceRemove(row.resourceName)
					return nil
				case wsRowOutcome:
					m.cmdOutcomeRemove(row.outcomeName)
					return nil
				case wsRowScratch:
					m.cmdClearScratch(row.wsIdx)
					return nil
				case wsRowArticle:
					return m.cmdDeleteArticle()
				case wsRowWorkspace:
					return m.cmdDeleteWorkspace()
				case wsRowAtticArticle:
					m.cmdRemoveFromAtticArticle(row)
					return nil
				case wsRowAtticCollection:
					m.cmdRemoveFromAtticCollection(row)
					return nil
				default:
					return nil
				}
			}
			return m.cmdDeleteWorkspace()
		case navSubTabCollections:
			return m.cmdDeleteCollection()
		default:
			return m.cmdDeleteArticle()
		}
	case key.Matches(msg, keys.Open):
		if m.navSubTab == navSubTabWorkspaces {
			row := m.selectedWsRow()
			if row != nil {
				if row.kind == wsRowScratch {
					m.openScratchPaneForRow(row)
					return nil
				}
				if row.kind == wsRowResource || row.kind == wsRowOutcome {
					return m.openWsFileExternal()
				}
			}
		}
		return m.openCurrentURL()
	case key.Matches(msg, keys.View):
		if m.navSubTab == navSubTabWorkspaces {
			row := m.selectedWsRow()
			if row != nil {
				if row.kind == wsRowScratch {
					return m.openScratchOverlay(row.wsIdx)
				}
				if row.kind == wsRowResource || row.kind == wsRowOutcome {
					return m.viewWsFileInOverlay()
				}
			}
		}
		return m.openArticleOverlay(m.selectedNavItem())
	case msg.String() == "U":
		if m.navSubTab == navSubTabWorkspaces {
			row := m.selectedWsRow()
			if row != nil && row.wsIdx >= 0 && row.wsIdx < len(m.workspaceItems) {
				switch row.kind {
				case wsRowArticle:
					m.cmdUnlinkArticle(row)
					return nil
				case wsRowCollection:
					m.cmdUnlinkCollection(row)
					return nil
				}
			}
		}
	case msg.String() == "e":
		if m.navSubTab == navSubTabWorkspaces {
			row := m.selectedWsRow()
			if row != nil && (row.kind == wsRowResource || row.kind == wsRowOutcome || row.kind == wsRowScratch) {
				editor := os.Getenv("EDITOR")
				if editor == "" {
					m.setStatusError("$EDITOR is not set")
					return nil
				}
				path := m.wsFilePathForRow(row)
				if path == "" {
					return nil
				}
				name := row.resourceName
				if row.kind == wsRowOutcome {
					name = row.outcomeName
				} else if row.kind == wsRowScratch {
					name = storefs.ScratchName(m.workspaceItems[row.wsIdx].name)
				}
				m.openEditorInTerminal(editor, path, name)
				return nil
			}
		}
	case key.Matches(msg, keys.Attic):
		if m.navSubTab == navSubTabWorkspaces {
			row := m.selectedWsRow()
			if row != nil && row.wsIdx >= 0 && row.wsIdx < len(m.workspaceItems) {
				switch row.kind {
				case wsRowArticle:
					if row.colSlug == "" { // only direct workspace articles
						m.cmdAtticArticle(row)
					}
					return nil
				case wsRowCollection:
					m.cmdAtticCollection(row)
					return nil
				}
			}
		}
	case key.Matches(msg, keys.UnAttic):
		if m.navSubTab == navSubTabWorkspaces {
			row := m.selectedWsRow()
			if row != nil && row.wsIdx >= 0 && row.wsIdx < len(m.workspaceItems) {
				switch row.kind {
				case wsRowAtticArticle:
					m.cmdUnAtticArticle(row)
					return nil
				case wsRowAtticCollection:
					m.cmdUnAtticCollection(row)
					return nil
				}
			}
		}
	case msg.String() == "!":
		if m.navSubTab == navSubTabWorkspaces {
			m.wsToggleFocus()
			return nil
		}
	case msg.String() == "c":
		if m.navSubTab == navSubTabArticles || m.navSubTab == navSubTabCollections {
			return m.cmdArticleChat()
		}
	case key.Matches(msg, keys.Command):
		m.focus = paneCommand
		m.cursorVisible = true
		m.input.SetValue("/")
		m.input.CursorEnd()
		m.updateCompletions()
	case key.Matches(msg, keys.Help):
		m.setStatusLines(m.helpLines("keys"))
	}
	return nil
}

// navLeft handles ← in the nav pane — cycles sub-tabs.
func (m *Model) navLeft() tea.Cmd {
	switch m.activeTab {
	case tabLibrary:
		return m.switchNavSubTab((m.navSubTab - 1 + navSubTabCount) % navSubTabCount)
	case tabAgent:
		m.agentSubTab = (m.agentSubTab - 1 + agentSubTabCount) % agentSubTabCount
		return nil
	case tabStats:
		m.statsSubTab = (m.statsSubTab - 1 + statsSubTabCount) % statsSubTabCount
		return nil
	default:
		if m.chatMode {
			m.exitChatMode()
		}
		m.activeTab = (m.activeTab - 1 + tabCount) % tabCount
		return nil
	}
}

// navRight handles → in the nav pane — cycles sub-tabs.
func (m *Model) navRight() tea.Cmd {
	switch m.activeTab {
	case tabLibrary:
		return m.switchNavSubTab((m.navSubTab + 1) % navSubTabCount)
	case tabAgent:
		m.agentSubTab = (m.agentSubTab + 1) % agentSubTabCount
		return nil
	case tabStats:
		m.statsSubTab = (m.statsSubTab + 1) % statsSubTabCount
		return nil
	default:
		if m.chatMode {
			m.exitChatMode()
		}
		m.activeTab = (m.activeTab + 1) % tabCount
		return nil
	}
}

// navAtTop returns true when the nav cursor is already at the first item,
// so pressing UP should transfer focus to the tab bar instead.
func (m *Model) navAtTop() bool {
	if m.activeTab == tabAgent {
		switch m.agentSubTab {
		case agentSubTabFeeds:
			return m.agentFeedsCursor == 0
		default:
			return m.agentRunsCursor == 0
		}
	}
	switch m.navSubTab {
	case navSubTabArticles:
		return m.navCursor == 0
	case navSubTabCollections:
		return m.navRowCursor == 0
	case navSubTabWorkspaces:
		if m.wsSearchActive() {
			return m.navCursor == 0
		}
		return m.wsCursor == 0
	}
	return false
}

// navCursorUp moves the cursor up in the active sub-tab.
func (m *Model) navCursorUp() tea.Cmd {
	switch m.navSubTab {
	case navSubTabArticles:
		if m.navCursor > 0 {
			m.navCursor--
			m.clampNavScroll()
			return m.triggerContentLoad()
		}
	case navSubTabCollections:
		if m.navRowCursor > 0 {
			m.navRowCursor--
			m.clampNavRowScroll()
			return m.triggerCollectionContentLoad()
		}
	case navSubTabWorkspaces:
		if m.wsSearchActive() {
			if m.navCursor > 0 {
				m.navCursor--
				m.clampNavScroll()
				slog.Debug("navCursorUp: ws search mode", "navCursor", m.navCursor)
				return m.triggerContentLoad()
			}
			return nil
		}
		if m.wsCursor > 0 {
			m.wsCursor--
			m.clampWsScroll()
			m.maybeReloadScratch()
			m.maybeCloseAskX()
			m.maybeUpdatePreview()
			return m.triggerWorkspaceChatLoad()
		}
	}
	return nil
}

// navCursorDown moves the cursor down in the active sub-tab.
func (m *Model) navCursorDown() tea.Cmd {
	switch m.navSubTab {
	case navSubTabArticles:
		if m.navCursor < len(m.navItems)-1 {
			m.navCursor++
			m.clampNavScroll()
			return m.triggerContentLoad()
		}
	case navSubTabCollections:
		if m.navRowCursor < len(m.navRows)-1 {
			m.navRowCursor++
			m.clampNavRowScroll()
			return m.triggerCollectionContentLoad()
		}
	case navSubTabWorkspaces:
		if m.wsSearchActive() {
			if m.navCursor < len(m.navItems)-1 {
				m.navCursor++
				m.clampNavScroll()
				slog.Debug("navCursorDown: ws search mode", "navCursor", m.navCursor)
				return m.triggerContentLoad()
			}
			return nil
		}
		if m.wsCursor < len(m.wsRows)-1 {
			m.wsCursor++
			m.clampWsScroll()
			m.maybeReloadScratch()
			m.maybeCloseAskX()
			m.maybeUpdatePreview()
			return m.triggerWorkspaceChatLoad()
		}
	}
	return nil
}

// agentNavCursorUp moves the cursor up in the active Agent sub-tab.
func (m *Model) agentNavCursorUp() tea.Cmd {
	switch m.agentSubTab {
	case agentSubTabRuns:
		if m.agentRunsCursor > 0 {
			m.agentRunsCursor--
			m.clampAgentRunsScroll()
			return m.triggerAgentRunDetail()
		}
	case agentSubTabFeeds:
		if m.agentFeedsCursor > 0 {
			m.agentFeedsCursor--
			m.clampAgentFeedsScroll()
			m.resetFeedDetailState()
		}
	}
	return nil
}

// agentNavCursorDown moves the cursor down in the active Agent sub-tab.
func (m *Model) agentNavCursorDown() tea.Cmd {
	switch m.agentSubTab {
	case agentSubTabRuns:
		if m.agentRunsCursor < len(m.agentRuns)-1 {
			m.agentRunsCursor++
			m.clampAgentRunsScroll()
			return m.triggerAgentRunDetail()
		}
	case agentSubTabFeeds:
		if m.agentFeedsCursor < len(m.agentFeeds)-1 {
			m.agentFeedsCursor++
			m.clampAgentFeedsScroll()
			m.resetFeedDetailState()
		}
	}
	return nil
}

// triggerAgentRunDetail loads the decisions file for the currently selected run
// if it hasn't been loaded yet. For decisions-type runs, loads the source daily
// run's decisions file (via SourceRunID) and queries ingested articles by agent_run_id.
func (m *Model) triggerAgentRunDetail() tea.Cmd {
	if m.agentRunsCursor < 0 || m.agentRunsCursor >= len(m.agentRuns) {
		return nil
	}
	rec := m.agentRuns[m.agentRunsCursor]
	runID := rec.RunID

	// For decisions-type runs, load the source daily run's decisions file.
	fileID := runID
	if rec.RunType == "decisions" {
		if rec.SourceRunID != "" {
			fileID = rec.SourceRunID
		} else {
			// Legacy run (predates SourceRunID): find the most recent daily run
			// before this one — that's the run whose decisions file was processed.
			// m.agentRuns is in reverse-chronological order, so scan forward from cursor.
			for i := m.agentRunsCursor + 1; i < len(m.agentRuns); i++ {
				if m.agentRuns[i].RunType == "daily" || m.agentRuns[i].RunType == "" {
					fileID = m.agentRuns[i].RunID
					break
				}
			}
		}
	}

	var cmds []tea.Cmd

	// Reload decisions file only when the source file changes.
	if fileID != m.agentRunDecisionsID {
		m.agentRunDecisionsID = fileID
		m.agentRunDecisions = agentpkg.DecisionsFile{}
		m.agentFeedExpanded = nil
		m.agentContentCursor = 0
		m.agentContentScroll = 0
		cmds = append(cmds, loadAgentDecisions(m.cfg.AgentPath, fileID))
	}

	// Reload ingested articles only when the decisions run itself changes.
	// Tracked separately from fileID so re-visiting the same run doesn't re-fetch.
	if rec.RunType == "decisions" && m.agentRunIngestedID != runID && m.agentRunIngestedErr == "" {
		m.agentRunIngested = nil
		m.agentRunIngestedID = ""
		cmds = append(cmds, loadAgentRunIngested(m.svc, runID))
	}

	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// clampAgentRunsScroll keeps agentRunsScroll within bounds so the cursor is visible.
// handleAgentContentKey handles key events in the Agent tab content pane.
func (m *Model) handleAgentContentKey(msg tea.KeyMsg) tea.Cmd {
	// Feed run history content pane.
	if m.agentSubTab == agentSubTabFeeds {
		return m.handleFeedDetailKey(msg)
	}

	var rows []agentDetailRow
	switch m.agentSubTab {
	case agentSubTabRuns:
		rows = m.buildAgentDecisionRows()
	default:
		return nil
	}

	// Find navigable rows (feed headers and article rows).
	var navIdx []int
	for i, r := range rows {
		if r.kind == agentRowFeed || r.kind == agentRowArticle {
			navIdx = append(navIdx, i)
		}
	}
	total := len(navIdx)
	if total == 0 {
		return nil
	}

	viewH := m.contentViewHeight()

	switch {
	case key.Matches(msg, keys.NavUp):
		if m.agentContentCursor > 0 {
			m.agentContentCursor--
			m.clampAgentContentScroll(viewH, rows, navIdx)
		}
	case key.Matches(msg, keys.NavDown):
		if m.agentContentCursor < total-1 {
			m.agentContentCursor++
			m.clampAgentContentScroll(viewH, rows, navIdx)
		}
	case msg.Type == tea.KeyRunes && msg.String() == "g", key.Matches(msg, keys.Home):
		m.agentContentCursor = 0
		m.agentContentScroll = 0
	case msg.Type == tea.KeyRunes && msg.String() == "G", key.Matches(msg, keys.End):
		m.agentContentCursor = total - 1
		m.clampAgentContentScroll(viewH, rows, navIdx)
	case key.Matches(msg, keys.Expand), msg.Type == tea.KeyEnter:
		// Toggle expand on the feed header at cursor.
		if m.agentContentCursor < len(navIdx) {
			row := rows[navIdx[m.agentContentCursor]]
			if row.kind == agentRowFeed {
				if m.agentFeedExpanded == nil {
					m.agentFeedExpanded = make(map[int]bool)
				}
				m.agentFeedExpanded[row.feedIdx] = !m.agentFeedExpanded[row.feedIdx]
			}
		}
	case key.Matches(msg, keys.Open):
		// Open article URL in browser.
		if m.agentContentCursor < len(navIdx) {
			row := rows[navIdx[m.agentContentCursor]]
			if row.kind == agentRowArticle && row.url != "" {
				return openInChrome(row.url)
			}
		}
	case key.Matches(msg, keys.Select), msg.Type == tea.KeyRunes && msg.String() == "v":
		// View ingested article in the Library viewer (only if status == "done").
		if m.agentContentCursor < len(navIdx) {
			row := rows[navIdx[m.agentContentCursor]]
			if row.kind == agentRowArticle && row.status == "done" && row.url != "" {
				return m.navigateToArticleByURL(row.url)
			}
		}
	case msg.Type == tea.KeyRunes && msg.String() == "a":
		// Ingest: set action "+" on selected article.
		if m.agentContentCursor < len(navIdx) {
			row := rows[navIdx[m.agentContentCursor]]
			if row.kind == agentRowArticle && row.status != "done" {
				m.agentRunDecisions.Feeds[row.itemFeedIdx].Items[row.itemIdx].Action = "+"
				m.statusMsg = "✓ queued for ingest"
				m.saveAgentDecisions()
			}
		}
	case msg.Type == tea.KeyRunes && msg.String() == "s":
		// Skip: set action "-" on selected article.
		if m.agentContentCursor < len(navIdx) {
			row := rows[navIdx[m.agentContentCursor]]
			if row.kind == agentRowArticle && row.status != "done" {
				m.agentRunDecisions.Feeds[row.itemFeedIdx].Items[row.itemIdx].Action = "-"
				m.statusMsg = "– skipped"
				m.saveAgentDecisions()
			}
		}
	}
	return nil
}

// handleFeedDetailKey handles key events in the feed run history content pane.
func (m *Model) handleFeedDetailKey(msg tea.KeyMsg) tea.Cmd {
	rows := m.buildFeedDetailRows()
	matched := m.matchedRunsForFeed()

	var navIdx []int
	for i, r := range rows {
		if r.kind == feedDetailRowRun || r.kind == feedDetailRowArticle {
			navIdx = append(navIdx, i)
		}
	}
	total := len(navIdx)
	if total == 0 {
		return nil
	}

	// Approximate viewH: total content height minus card lines.
	// Card height is variable; use a reasonable estimate for clamping.
	viewH := m.contentViewHeight() - 10
	if viewH < 4 {
		viewH = 4
	}

	switch {
	case key.Matches(msg, keys.NavUp):
		if m.agentFeedDetailCursor > 0 {
			m.agentFeedDetailCursor--
			m.clampAgentFeedDetailScroll(viewH, rows, navIdx)
		}
	case key.Matches(msg, keys.NavDown):
		if m.agentFeedDetailCursor < total-1 {
			m.agentFeedDetailCursor++
			m.clampAgentFeedDetailScroll(viewH, rows, navIdx)
		}
	case msg.Type == tea.KeyRunes && msg.String() == "g", key.Matches(msg, keys.Home):
		m.agentFeedDetailCursor = 0
		m.agentFeedDetailScroll = 0
	case msg.Type == tea.KeyRunes && msg.String() == "G", key.Matches(msg, keys.End):
		m.agentFeedDetailCursor = total - 1
		m.clampAgentFeedDetailScroll(viewH, rows, navIdx)
	case key.Matches(msg, keys.Expand), msg.Type == tea.KeyEnter:
		if m.agentFeedDetailCursor < len(navIdx) {
			row := rows[navIdx[m.agentFeedDetailCursor]]
			if row.kind == feedDetailRowRun {
				if m.agentFeedRunExpanded == nil {
					m.agentFeedRunExpanded = make(map[int]bool)
				}
				nowExpanded := !m.agentFeedRunExpanded[row.runIdx]
				m.agentFeedRunExpanded[row.runIdx] = nowExpanded
				// Load decisions on first expand.
				if nowExpanded {
					if m.agentFeedRunDecisions == nil {
						m.agentFeedRunDecisions = make(map[string]agentpkg.DecisionsFile)
					}
					if _, alreadyLoaded := m.agentFeedRunDecisions[row.fileID]; !alreadyLoaded {
						if row.runIdx < len(matched) {
							_ = matched // already have fileID from row
						}
						return loadAgentFeedRunDecisions(m.cfg.AgentPath, row.fileID)
					}
				}
			}
		}
	case key.Matches(msg, keys.Open):
		if m.agentFeedDetailCursor < len(navIdx) {
			row := rows[navIdx[m.agentFeedDetailCursor]]
			if row.kind == feedDetailRowArticle && row.url != "" {
				return openInChrome(row.url)
			}
		}
	case key.Matches(msg, keys.Select), msg.Type == tea.KeyRunes && msg.String() == "v":
		if m.agentFeedDetailCursor < len(navIdx) {
			row := rows[navIdx[m.agentFeedDetailCursor]]
			if row.kind == feedDetailRowArticle && (row.status == "done" || row.verdict == "ingest") && row.url != "" {
				return m.navigateToArticleByURL(row.url)
			}
		}
	}
	return nil
}

// saveAgentDecisions writes the in-memory decisions file back to disk.
func (m *Model) saveAgentDecisions() {
	if m.agentRunDecisionsID == "" {
		return
	}
	path := filepath.Join(m.cfg.AgentPath, "decisions-"+m.agentRunDecisionsID+".json")
	if err := agentpkg.WriteDecisionsFile(path, m.agentRunDecisions); err != nil {
		m.setStatusError("✗ save decisions: " + err.Error())
	}
}

func (m *Model) clampAgentContentScroll(viewH int, rows []agentDetailRow, navIdx []int) {
	// Build display line height for each nav position.
	// Feed rows: 2 lines (header + stats). Article rows: 2 if reason present, else 1.
	lineH := make([]int, len(navIdx))
	for pos, ri := range navIdx {
		r := rows[ri]
		switch r.kind {
		case agentRowFeed:
			lineH[pos] = 2
		case agentRowArticle:
			if r.reason != "" {
				lineH[pos] = 2
			} else {
				lineH[pos] = 1
			}
		default:
			lineH[pos] = 1
		}
	}

	// Scroll up: cursor moved above scroll window.
	if m.agentContentCursor < m.agentContentScroll {
		m.agentContentScroll = m.agentContentCursor
		return
	}

	// Scroll down: count display lines from scroll to cursor; if cursor is off-screen, advance scroll.
	lines := 0
	for pos := m.agentContentScroll; pos < len(navIdx); pos++ {
		if pos == m.agentContentCursor {
			break // cursor is visible if we haven't exceeded viewH yet
		}
		lines += lineH[pos]
		if lines >= viewH {
			// cursor is off-screen; advance scroll by one and retry
			m.agentContentScroll++
			m.clampAgentContentScroll(viewH, rows, navIdx)
			return
		}
	}
}

func (m *Model) clampAgentRunsScroll() {
	h := m.navPaneHeight() - 2 // subtract sub-tab bar + blank line
	if h < 1 {
		h = 1
	}
	if m.agentRunsCursor < m.agentRunsScroll {
		m.agentRunsScroll = m.agentRunsCursor
	}
	if m.agentRunsCursor >= m.agentRunsScroll+h {
		m.agentRunsScroll = m.agentRunsCursor - h + 1
	}
}

func (m *Model) clampAgentFeedsScroll() {
	h := m.navPaneHeight() - 2 // subtract sub-tab bar + blank line
	if h < 1 {
		h = 1
	}
	if m.agentFeedsCursor < m.agentFeedsScroll {
		m.agentFeedsScroll = m.agentFeedsCursor
	}
	if m.agentFeedsCursor >= m.agentFeedsScroll+h {
		m.agentFeedsScroll = m.agentFeedsCursor - h + 1
	}
}

func (m *Model) resetFeedDetailState() {
	m.agentFeedDetailCursor = 0
	m.agentFeedDetailScroll = 0
	m.agentFeedRunExpanded = nil
}

func (m *Model) clampAgentFeedDetailScroll(viewH int, rows []feedDetailRow, navIdx []int) {
	// Compute display line height for each nav position.
	lineH := make([]int, len(navIdx))
	for pos, ri := range navIdx {
		r := rows[ri]
		switch r.kind {
		case feedDetailRowRun:
			lineH[pos] = 2 // arrow + name line + stats line
		case feedDetailRowArticle:
			if r.reason != "" {
				lineH[pos] = 2
			} else {
				lineH[pos] = 1
			}
		}
	}
	// Scroll up: cursor above scroll → set scroll to cursor.
	if m.agentFeedDetailCursor < m.agentFeedDetailScroll {
		m.agentFeedDetailScroll = m.agentFeedDetailCursor
		return
	}
	// Scroll down: advance scroll until cursor is visible.
	lines := 0
	for pos := m.agentFeedDetailScroll; pos < len(navIdx); pos++ {
		if pos == m.agentFeedDetailCursor {
			break
		}
		lines += lineH[pos]
		if lines >= viewH {
			m.agentFeedDetailScroll++
			m.clampAgentFeedDetailScroll(viewH, rows, navIdx)
			return
		}
	}
}

// navPageUp scrolls the nav pane up by one page.
func (m *Model) navPageUp() tea.Cmd {
	h := m.navPaneHeight()
	switch m.navSubTab {
	case navSubTabArticles:
		m.navCursor -= h
		if m.navCursor < 0 {
			m.navCursor = 0
		}
		m.clampNavScroll()
		return m.triggerContentLoad()
	case navSubTabCollections:
		m.navRowCursor -= h
		if m.navRowCursor < 0 {
			m.navRowCursor = 0
		}
		m.clampNavRowScroll()
		return m.triggerCollectionContentLoad()
	case navSubTabWorkspaces:
		if m.wsSearchActive() {
			m.navCursor -= h
			if m.navCursor < 0 {
				m.navCursor = 0
			}
			m.clampNavScroll()
			return m.triggerContentLoad()
		}
		m.wsCursor -= h
		if m.wsCursor < 0 {
			m.wsCursor = 0
		}
		m.clampWsScroll()
		m.maybeReloadScratch()
		m.maybeCloseAskX()
		m.maybeUpdatePreview()
	}
	return nil
}

// navPageDown scrolls the nav pane down by one page.
func (m *Model) navPageDown() tea.Cmd {
	h := m.navPaneHeight()
	switch m.navSubTab {
	case navSubTabArticles:
		m.navCursor += h
		if m.navCursor >= len(m.navItems) {
			m.navCursor = len(m.navItems) - 1
		}
		if m.navCursor < 0 {
			m.navCursor = 0
		}
		m.clampNavScroll()
		return m.triggerContentLoad()
	case navSubTabCollections:
		m.navRowCursor += h
		if m.navRowCursor >= len(m.navRows) {
			m.navRowCursor = len(m.navRows) - 1
		}
		if m.navRowCursor < 0 {
			m.navRowCursor = 0
		}
		m.clampNavRowScroll()
		return m.triggerCollectionContentLoad()
	case navSubTabWorkspaces:
		if m.wsSearchActive() {
			m.navCursor += h
			if m.navCursor >= len(m.navItems) {
				m.navCursor = len(m.navItems) - 1
			}
			if m.navCursor < 0 {
				m.navCursor = 0
			}
			m.clampNavScroll()
			return m.triggerContentLoad()
		}
		m.wsCursor += h
		if m.wsCursor >= len(m.wsRows) {
			m.wsCursor = len(m.wsRows) - 1
		}
		if m.wsCursor < 0 {
			m.wsCursor = 0
		}
		m.clampWsScroll()
		m.maybeReloadScratch()
		m.maybeCloseAskX()
		m.maybeUpdatePreview()
	}
	return nil
}

// navHome jumps the nav cursor to the first item.
func (m *Model) navHome() tea.Cmd {
	switch m.navSubTab {
	case navSubTabArticles:
		m.navCursor = 0
		m.clampNavScroll()
		return m.triggerContentLoad()
	case navSubTabCollections:
		m.navRowCursor = 0
		m.clampNavRowScroll()
		return m.triggerCollectionContentLoad()
	case navSubTabWorkspaces:
		if m.wsSearchActive() {
			m.navCursor = 0
			m.clampNavScroll()
			return m.triggerContentLoad()
		}
		m.wsCursor = 0
		m.clampWsScroll()
		m.maybeReloadScratch()
		m.maybeCloseAskX()
		m.maybeUpdatePreview()
	}
	return nil
}

// navEnd jumps the nav cursor to the last item.
func (m *Model) navEnd() tea.Cmd {
	switch m.navSubTab {
	case navSubTabArticles:
		if len(m.navItems) > 0 {
			m.navCursor = len(m.navItems) - 1
			m.clampNavScroll()
			return m.triggerContentLoad()
		}
	case navSubTabCollections:
		if len(m.navRows) > 0 {
			m.navRowCursor = len(m.navRows) - 1
			m.clampNavRowScroll()
			return m.triggerCollectionContentLoad()
		}
	case navSubTabWorkspaces:
		if m.wsSearchActive() {
			if len(m.navItems) > 0 {
				m.navCursor = len(m.navItems) - 1
				m.clampNavScroll()
				return m.triggerContentLoad()
			}
			return nil
		}
		if len(m.wsRows) > 0 {
			m.wsCursor = len(m.wsRows) - 1
			m.clampWsScroll()
			m.maybeReloadScratch()
			m.maybeCloseAskX()
			m.maybeUpdatePreview()
		}
	}
	return nil
}

// navToggleExpand toggles expand/collapse on a collection header (Space key).
func (m *Model) navToggleExpand() tea.Cmd {
	if m.navSubTab == navSubTabWorkspaces {
		m.wsToggleExpand()
		return nil
	}
	if m.navSubTab != navSubTabCollections || m.navRowCursor >= len(m.navRows) {
		return nil
	}
	row := m.navRows[m.navRowCursor]
	if row.kind != rowCollection {
		return nil
	}
	if row.expanded {
		return m.collapseCollection(m.navRowCursor)
	}
	return m.expandCollection(m.navRowCursor)
}

// navSelect handles Enter in the nav pane.
func (m *Model) navSelect() tea.Cmd {
	switch m.navSubTab {
	case navSubTabArticles:
		if len(m.navItems) > 0 {
			return m.openArticleOverlay(m.selectedNavItem())
		}
	case navSubTabCollections:
		if m.navRowCursor >= len(m.navRows) {
			return nil
		}
		row := m.navRows[m.navRowCursor]
		if row.kind == rowCollection {
			return m.navToggleExpand()
		}
		if row.kind == rowArticle {
			return m.openArticleOverlay(m.selectedNavItem())
		}
	case navSubTabWorkspaces:
		if m.wsSearchActive() {
			slog.Debug("navSelect: ws search mode", "navCursor", m.navCursor, "items", len(m.navItems))
			if m.navCursor >= 0 && m.navCursor < len(m.navItems) {
				return m.openArticleOverlay(m.selectedNavItem())
			}
			return nil
		}
		if m.wsCursor < 0 || m.wsCursor >= len(m.wsRows) {
			return nil
		}
		row := m.wsRows[m.wsCursor]
		switch row.kind {
		case wsRowWorkspace:
			// Enter on workspace → load history (engine init deferred to first message).
			if row.wsIdx >= 0 && row.wsIdx < len(m.workspaceItems) {
				ws := m.workspaceItems[row.wsIdx]
				return m.loadChatHistoryCmd(ws.name, true)
			}
		case wsRowArticle:
			return m.openArticleOverlay(m.selectedNavItem())
		case wsRowCollection:
			m.wsToggleExpand()
		case wsRowResourceGroup, wsRowOutcomeGroup, wsRowResourceDir, wsRowAtticGroup:
			m.wsToggleExpand()
		case wsRowResource:
			if strings.HasSuffix(row.resourceName, ".url") {
				path := m.wsFilePathForRow(&row)
				if rawURL := readURLStub(path); rawURL != "" {
					return openInChrome(rawURL)
				}
			}
			return m.openWorkspaceFile(row.wsIdx, "resources", row.resourceName)
		case wsRowOutcome:
			return m.openWorkspaceFile(row.wsIdx, "outcomes", row.outcomeName)
		case wsRowScratch:
			return m.openScratchOverlay(row.wsIdx)
		}
	}
	return nil
}

// switchNavSubTab switches to the given Library nav sub-tab.
func (m *Model) switchNavSubTab(sub navSubTab) tea.Cmd {
	if m.achatMode {
		m.exitArticleChat()
	}
	if m.chatMode && sub != navSubTabWorkspaces {
		m.exitChatMode()
	}
	m.maybeCloseAskX()
	m.navSubTab = sub
	m.navRowCursor = 0
	m.navRowScroll = 0
	m.navCursor = 0
	m.navScroll = 0
	if sub == navSubTabCollections && !m.collectionsLoaded && m.svc != nil {
		return loadCollectionsTree(m.svc)
	}
	if sub == navSubTabWorkspaces && m.svc != nil {
		if !m.workspacesLoaded {
			return loadWorkspaces(m.svc)
		}
		// Already loaded — trigger history load for first workspace immediately.
		return m.triggerWorkspaceChatLoad()
	}
	return nil
}

// expandCollection starts an async load of articles for a collapsed collection header.
func (m *Model) expandCollection(rowIdx int) tea.Cmd {
	if rowIdx < 0 || rowIdx >= len(m.navRows) {
		return nil
	}
	row := m.navRows[rowIdx]
	if row.kind != rowCollection || row.expanded || m.svc == nil {
		return nil
	}
	m.statusMsg = "loading " + row.colSlug + "…"
	return loadCollectionArticlesCmd(m.svc, m.navItemsAll, row.colSlug, rowIdx)
}

// collapseCollection removes child article rows from an expanded collection.
func (m *Model) collapseCollection(rowIdx int) tea.Cmd {
	if rowIdx < 0 || rowIdx >= len(m.navRows) || m.navRows[rowIdx].kind != rowCollection {
		return nil
	}
	m.navRows[rowIdx].expanded = false
	// Remove consecutive indented article children after the header.
	i := rowIdx + 1
	for i < len(m.navRows) && m.navRows[i].kind == rowArticle && m.navRows[i].indented {
		i++
	}
	m.navRows = append(m.navRows[:rowIdx+1], m.navRows[i:]...)
	if m.navRowCursor > rowIdx {
		m.navRowCursor = rowIdx
	}
	m.clampNavRowScroll()
	return nil
}


// triggerCollectionContentLoad loads content for the article under navRowCursor.
// triggerWorkspaceChatLoad loads chat history if cursor is on a workspace row.
func (m *Model) triggerWorkspaceChatLoad() tea.Cmd {
	if m.wsCursor < 0 || m.wsCursor >= len(m.wsRows) {
		return nil
	}
	row := m.wsRows[m.wsCursor]
	if row.kind != wsRowWorkspace {
		return nil
	}
	if row.wsIdx < 0 || row.wsIdx >= len(m.workspaceItems) {
		return nil
	}
	return m.loadChatHistoryCmd(m.workspaceItems[row.wsIdx].name, false)
}

func (m *Model) triggerCollectionContentLoad() tea.Cmd {
	if m.navRowCursor < 0 || m.navRowCursor >= len(m.navRows) {
		return nil
	}
	row := m.navRows[m.navRowCursor]
	if row.kind != rowArticle || row.item == nil || row.item.root == "" {
		return nil
	}
	m.contentLoading = true
	m.contentLines = nil
	return loadContent(row.item.root, m.cfg.PreferredStyles, m.cfg.PreferredModels)
}

// clampNavRowScroll keeps navRowCursor visible within the scroll window.
func (m *Model) clampNavRowScroll() {
	h := m.navPaneHeight()
	if h < 1 {
		h = 1
	}
	if m.navRowCursor < m.navRowScroll {
		m.navRowScroll = m.navRowCursor
	} else if m.navRowCursor >= m.navRowScroll+h {
		m.navRowScroll = m.navRowCursor - h + 1
	}
}

func (m *Model) clampWsScroll() {
	h := m.navPaneHeight()
	if h < 1 {
		h = 1
	}
	if m.wsCursor < m.wsScroll {
		m.wsScroll = m.wsCursor
	} else if m.wsCursor >= m.wsScroll+h {
		m.wsScroll = m.wsCursor - h + 1
	}
}

// wsToggleExpand toggles expand/collapse for the workspace tree row at wsCursor.
func (m *Model) wsToggleExpand() {
	if m.wsCursor < 0 || m.wsCursor >= len(m.wsRows) {
		return
	}
	row := m.wsRows[m.wsCursor]
	if row.wsIdx < 0 || row.wsIdx >= len(m.workspaceItems) {
		return
	}
	ws := &m.workspaceItems[row.wsIdx]
	switch row.kind {
	case wsRowWorkspace:
		ws.expanded = !ws.expanded
		slog.Debug("wsToggleExpand", "name", ws.name, "expanded", ws.expanded)
	case wsRowCollection:
		if ws.expandedCols == nil {
			ws.expandedCols = make(map[string]bool)
		}
		ws.expandedCols[row.colSlug] = !ws.expandedCols[row.colSlug]
	case wsRowResourceGroup:
		ws.resourcesExpanded = !ws.resourcesExpanded
	case wsRowResourceDir:
		if ws.expandedResourceDirs == nil {
			ws.expandedResourceDirs = make(map[string]bool)
		}
		ws.expandedResourceDirs[row.resourceName] = !ws.expandedResourceDirs[row.resourceName]
	case wsRowOutcomeGroup:
		ws.outcomesExpanded = !ws.outcomesExpanded
	case wsRowAtticGroup:
		ws.atticExpanded = !ws.atticExpanded
	}
	m.wsRows = m.buildWsRows()
	m.clampWsScroll()
}

// wsToggleFocus toggles solo mode for the workspace under the cursor.
// In solo mode, only the focused workspace is shown in the nav pane.
func (m *Model) wsToggleFocus() {
	if m.wsCursor < 0 || m.wsCursor >= len(m.wsRows) {
		return
	}
	row := m.wsRows[m.wsCursor]
	if row.wsIdx < 0 || row.wsIdx >= len(m.workspaceItems) {
		return
	}
	ws := m.workspaceItems[row.wsIdx]

	if m.wsFocusName != "" {
		// Unfocus: restore all workspaces.
		m.wsFocusName = ""
		m.workspaceItems = m.workspaceItemsAll
		m.wsRows = m.buildWsRows()
		// Place cursor on the previously focused workspace.
		for i, r := range m.wsRows {
			if r.kind == wsRowWorkspace && r.wsIdx >= 0 && r.wsIdx < len(m.workspaceItems) && m.workspaceItems[r.wsIdx].name == ws.name {
				m.wsCursor = i
				break
			}
		}
		m.clampWsScroll()
		m.statusMsg = ""
		return
	}

	// Focus: show only this workspace.
	m.wsFocusName = ws.name
	m.workspaceItems = []workspaceItem{ws}
	m.wsRows = m.buildWsRows()
	m.wsCursor = 0
	m.wsScroll = 0
	m.statusMsg = "! focused: " + ws.name
	slog.Debug("wsToggleFocus: focused", "name", ws.name)
}

// openWorkspaceFile reads a file from the workspace subdir and opens the resource overlay.
func (m *Model) openWorkspaceFile(wsIdx int, subdir, filename string) tea.Cmd {
	if wsIdx < 0 || wsIdx >= len(m.workspaceItems) {
		return nil
	}
	ws := m.workspaceItems[wsIdx]
	filePath := filepath.Join(storefs.WorkspaceDir(m.cfg.DataRoot, ws.name), subdir, filename)
	data, err := os.ReadFile(filePath)
	if err != nil {
		m.setStatusError(fmt.Sprintf("cannot read %s/%s: %v", subdir, filename, err))
		return nil
	}
	// Binary check.
	check := data
	if len(check) > 512 {
		check = check[:512]
	}
	if !utf8.Valid(check) {
		m.setStatusError(fmt.Sprintf("%q is not a text file", filename))
		return nil
	}
	const maxBytes = 200 * 1024
	if len(data) > maxBytes {
		data = append(data[:maxBytes], []byte("\n[file truncated at 200 KB]")...)
	}
	m.openResourceOverlay(filename, string(data))
	return nil
}

// openScratchOverlay reads the scratch file for the given workspace and opens it
// in the resource overlay.
func (m *Model) openScratchOverlay(wsIdx int) tea.Cmd {
	if wsIdx < 0 || wsIdx >= len(m.workspaceItems) {
		return nil
	}
	ws := m.workspaceItems[wsIdx]
	path := storefs.ScratchPath(m.cfg.DataRoot, ws.name)
	data, err := os.ReadFile(path)
	if err != nil {
		m.setStatusError(fmt.Sprintf("cannot read scratch: %v", err))
		return nil
	}
	m.openResourceOverlay(storefs.ScratchName(ws.name), string(data))
	return nil
}

// openArticleOverlay assembles article content (flash/summary/body) and opens the overlay.
func (m *Model) openArticleOverlay(item *navItem) tea.Cmd {
	if item == nil || item.root == "" {
		return nil
	}
	files := storefs.ProbeFiles(item.root)
	files.Summary = storefs.ResolveSummary(item.root, m.cfg.PreferredStyles, m.cfg.PreferredModels)
	files.Flash = storefs.ResolveFlash(item.root, m.cfg.PreferredModels)

	type part struct {
		label string
		path  string
	}
	var parts []part
	if files.Flash != "" {
		parts = append(parts, part{"Flash", files.Flash})
	}
	if files.Summary != "" {
		parts = append(parts, part{"Summary", files.Summary})
	}
	if files.Body != "" {
		parts = append(parts, part{"Body", files.Body})
	}
	if len(parts) == 0 {
		m.setStatusError("no content files available")
		return nil
	}

	var sb strings.Builder
	for i, p := range parts {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		pad := 60 - 4 - len(p.label) - 1
		if pad < 3 {
			pad = 3
		}
		sb.WriteString(fmt.Sprintf("── %s %s\n\n", p.label, strings.Repeat("─", pad)))
		data, err := os.ReadFile(p.path)
		if err != nil {
			sb.WriteString(fmt.Sprintf("[error reading %s: %v]", p.label, err))
		} else {
			sb.WriteString(strings.TrimSpace(string(data)))
		}
	}

	title := item.title
	if title == "" {
		title = item.id
	}
	if item.numID > 0 {
		title = fmt.Sprintf("ID: %d · %s", item.numID, title)
	}
	m.openResourceOverlay(title, sb.String())
	return nil
}

// selectedWsRow returns the currently selected workspace row, or nil.
func (m *Model) selectedWsRow() *wsRow {
	if m.wsCursor < 0 || m.wsCursor >= len(m.wsRows) {
		return nil
	}
	return &m.wsRows[m.wsCursor]
}

// wsFilePathForRow returns the filesystem path for a resource or outcome row.
func (m *Model) wsFilePathForRow(row *wsRow) string {
	if row.wsIdx < 0 || row.wsIdx >= len(m.workspaceItems) {
		return ""
	}
	ws := m.workspaceItems[row.wsIdx]
	switch row.kind {
	case wsRowScratch:
		return storefs.ScratchPath(m.cfg.DataRoot, ws.name)
	case wsRowResource:
		return filepath.Join(storefs.WorkspaceDir(m.cfg.DataRoot, ws.name), "resources", row.resourceName)
	case wsRowResourceDir:
		return filepath.Join(storefs.WorkspaceDir(m.cfg.DataRoot, ws.name), "resources", row.resourceName)
	case wsRowOutcome:
		return filepath.Join(storefs.WorkspaceDir(m.cfg.DataRoot, ws.name), "outcomes", row.outcomeName)
	}
	return ""
}

// openWsFileExternal opens the selected resource/outcome with the system default app.
// For .url stub files, opens the contained URL in Chrome instead.
func (m *Model) openWsFileExternal() tea.Cmd {
	row := m.selectedWsRow()
	if row == nil {
		return nil
	}
	path := m.wsFilePathForRow(row)
	if path == "" {
		return nil
	}
	if row.kind == wsRowResource && strings.HasSuffix(row.resourceName, ".url") {
		if rawURL := readURLStub(path); rawURL != "" {
			return openInChrome(rawURL)
		}
	}
	cmd := exec.Command("open", path)
	cmd.Start()
	return nil
}

// readURLStub reads the first line of a .url stub file (the URL), or "" on error.
func readURLStub(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(string(data), "\n")
	return strings.TrimSpace(line)
}

// viewWsFileInTerminal opens the selected resource/outcome in an external terminal window.
// viewWsFileInOverlay reads the selected workspace resource/outcome and opens it
// in the in-TUI resource overlay. Falls back to external terminal for binary files.
func (m *Model) viewWsFileInOverlay() tea.Cmd {
	row := m.selectedWsRow()
	if row == nil {
		return nil
	}
	path := m.wsFilePathForRow(row)
	if path == "" {
		return nil
	}
	name := row.resourceName
	if row.kind == wsRowOutcome {
		name = row.outcomeName
	}
	data, err := os.ReadFile(path)
	if err != nil {
		m.setStatusError(fmt.Sprintf("view: %v", err))
		return nil
	}
	m.openResourceOverlay(name, string(data))
	return nil
}

func (m *Model) viewWsFileInTerminal() tea.Cmd {
	row := m.selectedWsRow()
	if row == nil {
		return nil
	}
	path := m.wsFilePathForRow(row)
	if path == "" {
		return nil
	}
	name := row.resourceName
	if row.kind == wsRowOutcome {
		name = row.outcomeName
	} else if row.kind == wsRowScratch {
		name = storefs.ScratchName(m.workspaceItems[row.wsIdx].name)
	}

	pid := os.Getpid()
	scriptPath := fmt.Sprintf("%s/arc-view-%d-%s.sh", os.TempDir(), pid, name)

	script := fmt.Sprintf(
		"#!/bin/bash\ntrap 'rm -f %q' EXIT\n"+
			"# Background watcher: exit when parent arc process dies.\n"+
			"(while kill -0 %d 2>/dev/null; do sleep 1; done; kill $$ 2>/dev/null) &\n"+
			"cat %q\necho ''\nread -n1 -s -r -p '(press any key to close)'\n",
		scriptPath, pid, path,
	)
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		m.setStatusError(fmt.Sprintf("view: could not write script: %v", err))
		return nil
	}

	var appleScript string
	switch ActiveTerminal {
	case TermITerm2:
		appleScript = fmt.Sprintf(
			`tell application "iTerm2" to create window with default profile command %q`,
			scriptPath,
		)
	default:
		appleScript = fmt.Sprintf(
			`tell application "Terminal" to do script %q`,
			scriptPath,
		)
	}

	cmd := exec.Command("osascript", "-e", appleScript)
	cmd.Start()
	return nil
}

// openEditorForFeed opens $EDITOR for adding (idx < 0) or editing (idx >= 0) a feed.
// Writes a temp JSON file, waits for the editor to close in a background goroutine,
// then parses the result and saves the config. Sends agentFeedSavedMsg when done.
func (m *Model) openEditorForFeed(idx int) {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		m.setStatusError("$EDITOR is not set")
		return
	}

	var data []byte
	var err error
	if idx >= 0 && idx < len(m.agentFeeds) {
		data, err = json.MarshalIndent(m.agentFeeds[idx], "", "  ")
	} else {
		// New feed: write a full template so the user knows all fields.
		data = []byte("{\n  \"name\": \"\",\n  \"url\": \"\",\n  \"filter\": \"\",\n  \"tags\": [],\n  \"disabled\": false\n}\n")
	}
	if err != nil {
		m.setStatusError("openEditorForFeed: marshal: " + err.Error())
		return
	}

	tmp, err := os.CreateTemp("", "arc-feed-*.json")
	if err != nil {
		m.setStatusError("openEditorForFeed: " + err.Error())
		return
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		m.setStatusError("openEditorForFeed: " + err.Error())
		return
	}
	tmp.Close()

	cmd := exec.Command(editor, tmp.Name())
	if err := cmd.Start(); err != nil {
		os.Remove(tmp.Name())
		m.setStatusError("openEditorForFeed: " + err.Error())
		return
	}

	cfgPath := filepath.Join(m.cfg.AgentPath, "config.json")
	send := *m.programSend
	go func() {
		defer os.Remove(tmp.Name())
		cmd.Wait()

		raw, err := os.ReadFile(tmp.Name())
		if err != nil {
			send(agentFeedSavedMsg{err: "read temp: " + err.Error()})
			return
		}
		var updated agentpkg.FeedConfig
		if err := json.Unmarshal(raw, &updated); err != nil {
			send(agentFeedSavedMsg{err: "parse: " + err.Error()})
			return
		}
		if updated.URL == "" {
			send(agentFeedSavedMsg{err: "URL is required"})
			return
		}
		if idx < 0 {
			err = agentpkg.AddFeed(cfgPath, updated)
		} else {
			err = agentpkg.UpdateFeed(cfgPath, idx, updated)
		}
		if err != nil {
			send(agentFeedSavedMsg{err: err.Error()})
			return
		}
		cfg, err := agentpkg.LoadAgentConfig(cfgPath)
		if err != nil {
			send(agentFeedSavedMsg{err: err.Error()})
			return
		}
		send(agentFeedSavedMsg{feeds: cfg.Feeds})
	}()

	label := "new feed"
	if idx >= 0 && idx < len(m.agentFeeds) {
		label = m.agentFeeds[idx].Name
		if label == "" {
			label = m.agentFeeds[idx].URL
		}
	}
	m.setStatusLines([]string{fmt.Sprintf("opened %q in external editor — save to update config", label)})
}

// openEditorInTerminal opens $EDITOR as a detached process with a background
// goroutine that kills it when arc exits.
func (m *Model) openEditorInTerminal(editor, filePath, label string) {
	cmd := exec.Command(editor, filePath)
	if err := cmd.Start(); err != nil {
		m.setStatusError(fmt.Sprintf("edit: %v", err))
		return
	}
	// Background: wait for editor to exit, or kill it if arc dies first.
	arcPid := os.Getpid()
	go func() {
		done := make(chan struct{})
		go func() {
			cmd.Wait()
			close(done)
		}()
		for {
			select {
			case <-done:
				return
			case <-time.After(1 * time.Second):
				if err := syscall.Kill(arcPid, 0); err != nil {
					cmd.Process.Kill()
					return
				}
			}
		}
	}()
	m.setStatusLines([]string{fmt.Sprintf("opened %s in external editor", label)})
}

func (m *Model) handleContentKey(msg tea.KeyMsg) tea.Cmd {
	// Scratch pane-level shortcuts (V view, E edit) — work whenever scratch is visible.
	if m.scratchOpen && msg.Type == tea.KeyRunes {
		switch msg.String() {
		case "V":
			content, err := storefs.ReadScratch(m.cfg.DataRoot, m.scratchWorkspace())
			if err != nil {
				m.setStatusError("scratch: " + err.Error())
				return nil
			}
			if content == "" {
				m.setStatusError("scratch is empty")
				return nil
			}
			name := "scratch"
			if ws := m.scratchWorkspace(); ws != "" {
				name = ws + "/scratch"
			}
			m.openResourceOverlay(name, content)
			return nil
		case "E":
			editor := os.Getenv("EDITOR")
			if editor == "" {
				m.setStatusError("$EDITOR is not set")
				return nil
			}
			path := m.scratchFilePath()
			label := "scratch"
			if ws := m.scratchWorkspace(); ws != "" {
				label = ws + "/scratch"
			}
			m.openEditorInTerminal(editor, path, label)
			return nil
		}
	}

	// Preview pane-level shortcut (V view) — works whenever preview is visible.
	if m.previewOpen && msg.Type == tea.KeyRunes && msg.String() == "V" {
		if len(m.previewLines) == 0 {
			m.setStatusError("preview is empty")
			return nil
		}
		m.openResourceOverlay(m.previewTitle, strings.Join(m.previewLines, "\n"))
		return nil
	}

	// AskX pane-level shortcuts (V view) — work whenever askX is visible.
	if m.askxOpen && msg.Type == tea.KeyRunes && msg.String() == "V" {
		content := m.askxAsText()
		if content == "" {
			m.setStatusError("askX is empty")
			return nil
		}
		name := "askX"
		if ws := m.askxWorkspace(); ws != "" {
			name = ws + "/askX"
		}
		m.openResourceOverlay(name, content)
		return nil
	}

	// When scratch pane is focused, route scroll/view/edit keys to scratch.
	if m.scratchOpen && m.scratchFocused {
		return m.handleScratchKey(msg)
	}
	// When askX pane is focused, route keys to askX.
	if m.askxOpen && m.askxFocused {
		return m.handleAskXKey(msg)
	}
	// When preview pane is focused, route keys to preview.
	if m.previewOpen && m.previewFocused {
		return m.handlePreviewKey(msg)
	}
	if m.achatMode && m.achatFocused {
		return m.handleArticleChatContentKey(msg)
	}
	if m.chatMode {
		return m.handleChatContentKey(msg)
	}
	if m.activeTab == tabAgent {
		return m.handleAgentContentKey(msg)
	}
	total := len(m.contentLines)
	viewH := m.contentViewHeight()

	switch {
	case msg.Type == tea.KeyRunes && msg.String() == "g", key.Matches(msg, keys.Home):
		m.contentLineCursor = 0
		m.contentScroll = 0
	case msg.Type == tea.KeyRunes && msg.String() == "G", key.Matches(msg, keys.End):
		if total > 0 {
			m.contentLineCursor = total - 1
		}
		m.scrollContentToCursor(viewH)
	case key.Matches(msg, keys.NavUp):
		if m.contentLineCursor > 0 {
			m.contentLineCursor--
			m.scrollContentToCursor(viewH)
		}
	case key.Matches(msg, keys.NavDown):
		if m.contentLineCursor < total-1 {
			m.contentLineCursor++
			m.scrollContentToCursor(viewH)
		}
	case key.Matches(msg, keys.PageUp):
		step := viewH / 2
		m.contentLineCursor -= step
		if m.contentLineCursor < 0 {
			m.contentLineCursor = 0
		}
		m.scrollContentToCursor(viewH)
	case key.Matches(msg, keys.PageDown):
		step := viewH / 2
		m.contentLineCursor += step
		if m.contentLineCursor >= total {
			m.contentLineCursor = total - 1
		}
		m.scrollContentToCursor(viewH)
	case key.Matches(msg, keys.ContentTabNext):
		return m.cycleContentTab(1)
	case key.Matches(msg, keys.ContentTabPrev):
		return m.cycleContentTab(-1)
	case key.Matches(msg, keys.Open):
		return m.openCurrentURL()
	case key.Matches(msg, keys.ToggleFav):
		return m.cmdToggleFavorite()
	case msg.Type == tea.KeyRunes:
		switch msg.String() {
		case "s":
			return m.cmdContentTTS()
		case "[":
			return m.cmdContentTTSAdjustRate(-20)
		case "]":
			return m.cmdContentTTSAdjustRate(+20)
		}
	}
	return nil
}

// handleChatContentKey handles keys in the content pane during chat mode.
// j/k navigate between boxes; v/x/s act on the selected box.
// PgUp/PgDn/Home/End scroll the view.
func (m *Model) handleChatContentKey(msg tea.KeyMsg) tea.Cmd {
	chatViewH := m.chatViewHeight()
	if chatViewH < 1 {
		chatViewH = 1
	}

	numBoxes := m.chatBoxCount()

	// Box navigation and per-box operations (boxed view is always active here).
	switch {
	case msg.Type == tea.KeyRunes:
		switch msg.String() {
		case "v":
			if numBoxes > 0 {
				m.cmdChatCollapseBox(m.chatBoxCursor)
			}
			return nil
		case "#":
			if numBoxes > 0 {
				return m.cmdChatCommentBox(m.chatBoxCursor)
			}
			return nil
		case "x":
			if numBoxes > 0 {
				return m.cmdChatDeleteBox(m.chatBoxCursor)
			}
			return nil
		case "s":
			return m.cmdChatTTS()
		case "[":
			return m.cmdChatTTSAdjustRate(-20)
		case "]":
			return m.cmdChatTTSAdjustRate(+20)
		}
	case key.Matches(msg, keys.NavUp):
		if m.chatBoxCursor > 0 {
			m.chatBoxCursor--
			m.chatAutoScroll = false
			m.scrollToChatBox(m.chatBoxCursor, chatViewH)
		}
		return nil
	case key.Matches(msg, keys.NavDown):
		if m.chatBoxCursor < numBoxes-1 {
			m.chatBoxCursor++
			m.chatAutoScroll = m.chatBoxCursor >= numBoxes-1
			m.scrollToChatBox(m.chatBoxCursor, chatViewH)
		}
		return nil
	}

	// Scroll operations.
	maxScroll := m.chatTotalLines() - chatViewH
	if maxScroll < 0 {
		maxScroll = 0
	}
	switch {
	case key.Matches(msg, keys.PageUp):
		m.chatScroll -= chatViewH
		if m.chatScroll < 0 {
			m.chatScroll = 0
		}
		m.chatAutoScroll = false
	case key.Matches(msg, keys.PageDown):
		m.chatScroll += chatViewH
		if m.chatScroll > maxScroll {
			m.chatScroll = maxScroll
		}
		if m.chatScroll >= maxScroll {
			m.chatAutoScroll = true
		}
	case key.Matches(msg, keys.Home):
		m.chatScroll = 0
		m.chatBoxCursor = 0
		m.chatAutoScroll = false
	case key.Matches(msg, keys.End):
		m.chatScroll = maxScroll
		if numBoxes > 0 {
			m.chatBoxCursor = numBoxes - 1
		}
		m.chatAutoScroll = true
	}
	return nil
}

// handleResourceKey handles keyboard input in the resource file overlay.
func (m *Model) handleResourceKey(msg tea.KeyMsg) tea.Cmd {
	viewH := m.height - 4 // top bar (2) + hint bar (2)
	if viewH < 1 {
		viewH = 1
	}
	total := len(m.resourceLines)

	switch msg.String() {
	case "ctrl+x", "q", "esc":
		m.closeResourceOverlay()
	case "g":
		m.resourceCursor = 0
		m.resourceScroll = 0
	case "G":
		if total > 0 {
			m.resourceCursor = total - 1
		}
		m.scrollResourceToCursor(viewH)
	case "k", "up":
		if m.resourceCursor > 0 {
			m.resourceCursor--
			m.scrollResourceToCursor(viewH)
		}
	case "j", "down":
		if m.resourceCursor < total-1 {
			m.resourceCursor++
			m.scrollResourceToCursor(viewH)
		}
	case "pgup", "ctrl+u":
		step := viewH / 2
		m.resourceCursor -= step
		if m.resourceCursor < 0 {
			m.resourceCursor = 0
		}
		m.scrollResourceToCursor(viewH)
	case "pgdown", "ctrl+d":
		step := viewH / 2
		m.resourceCursor += step
		if m.resourceCursor >= total {
			m.resourceCursor = total - 1
		}
		m.scrollResourceToCursor(viewH)
	case "e":
		return m.cmdResourceEdit(m.resourceName)
	case "x":
		return m.cmdResourceDeleteLine(viewH)
	case "s":
		return m.cmdResourceTTS(viewH)
	case "[":
		return m.cmdResourceTTSAdjustRate(-20, viewH)
	case "]":
		return m.cmdResourceTTSAdjustRate(+20, viewH)
	}
	return nil
}

// cmdResourceDeleteLine deletes the current line from a scratch file overlay.
func (m *Model) cmdResourceDeleteLine(viewH int) tea.Cmd {
	// Only allow deletion in scratch files.
	if !strings.HasPrefix(m.resourceName, "scratch") {
		return nil
	}
	if len(m.resourceLines) == 0 {
		return nil
	}
	// Remove the line at cursor.
	idx := m.resourceCursor
	m.resourceLines = append(m.resourceLines[:idx], m.resourceLines[idx+1:]...)
	// Write back to disk.
	path := m.scratchFilePath()
	content := strings.Join(m.resourceLines, "\n")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		m.setStatusError("delete line: " + err.Error())
		return nil
	}
	// Adjust cursor.
	if m.resourceCursor >= len(m.resourceLines) && m.resourceCursor > 0 {
		m.resourceCursor--
	}
	m.scrollResourceToCursor(viewH)
	// Refresh scratch pane if open.
	if m.scratchOpen {
		m.reloadScratchLines()
	}
	return nil
}

func (m *Model) handleCommandKey(msg tea.KeyMsg) tea.Cmd {
	// Ctrl+T: insert compact timestamp (2006-01-02 15:04).
	if msg.String() == "ctrl+t" {
		m.inputExitHistory()
		m.input.InsertString(time.Now().Format("2006-01-02 15:04"))
		m.syncInputHeight()
		m.updateCompletions()
		return nil
	}
	// Ctrl+V: read clipboard and paste.
	if msg.String() == "ctrl+v" {
		m.pasteFromClipboard()
		return nil
	}
	// Bracketed paste.
	if msg.Paste {
		m.pasteContent(string(msg.Runes))
		return nil
	}
	// Ctrl+J (Shift+Enter): insert newline.
	if msg.String() == "ctrl+j" {
		m.inputExitHistory()
		m.input.InsertString("\n")
		m.syncInputHeight()
		return nil
	}

	switch msg.Type {
	case tea.KeyTab:
		m.acceptCompletion()
	case tea.KeyUp:
		if len(m.paramItems) > 0 {
			if m.paramIdx > 0 {
				m.paramIdx--
			}
		} else if len(m.cmdComplete) > 0 {
			if m.cmdCompleteIdx > 0 {
				m.cmdCompleteIdx--
			}
		} else {
			m.inputHistoryPrev()
		}
	case tea.KeyDown:
		if len(m.paramItems) > 0 {
			if m.paramIdx < len(m.paramItems)-1 {
				m.paramIdx++
			}
		} else if len(m.cmdComplete) > 0 {
			if m.cmdCompleteIdx < len(m.cmdComplete)-1 {
				m.cmdCompleteIdx++
			}
		} else {
			m.inputHistoryNext()
		}
	case tea.KeyPgUp:
		if m.chatMode {
			chatViewH := m.chatViewHeight()
			if chatViewH < 1 {
				chatViewH = 1
			}
			m.chatScroll -= chatViewH
			if m.chatScroll < 0 {
				m.chatScroll = 0
			}
			m.chatAutoScroll = false
		}
	case tea.KeyPgDown:
		if m.chatMode {
			chatViewH := m.chatViewHeight()
			if chatViewH < 1 {
				chatViewH = 1
			}
			maxScroll := m.chatTotalLines() - chatViewH
			if maxScroll < 0 {
				maxScroll = 0
			}
			m.chatScroll += chatViewH
			if m.chatScroll > maxScroll {
				m.chatScroll = maxScroll
			}
			m.chatAutoScroll = true
		}
	case tea.KeyEnter:
		// Param picker: Enter fills selected value into input but does not execute.
		if len(m.paramItems) > 0 && m.paramIdx >= 0 {
			m.acceptParam()
			return nil
		}
		// Completion list: Enter on a no-arg command executes; on commands with args, fills like Tab.
		if len(m.cmdComplete) > 0 && m.cmdCompleteIdx >= 0 {
			c := m.cmdComplete[m.cmdCompleteIdx]
			inputVal := strings.TrimSpace(m.input.Value())
			// If input is already a full command match (e.g. "/Scratch" matches "/scratch"),
			// dispatch directly with the original input to preserve casing.
			if strings.EqualFold(inputVal, c.cmd) {
				m.cmdComplete = nil
				m.cmdCompleteIdx = -1
				m.input.SetValue("")
				m.syncInputHeight()
				return m.dispatchCommand(inputVal)
			}
			if c.arg == "" {
				// No arg needed — execute immediately.
				m.cmdComplete = nil
				m.cmdCompleteIdx = -1
				m.input.SetValue("")
				m.syncInputHeight()
				return m.dispatchCommand(c.cmd)
			}
			// Has arg — fill + space and show param picker, same as Tab.
			m.acceptCompletion()
			return nil
		}
		val := strings.TrimSpace(m.input.Value())
		m.inputSubmit()
		m.cmdComplete = nil
		m.cmdCompleteIdx = -1
		// Resolve buffered paste: use blob as the actual value.
		if m.pastedBlob != "" {
			val = strings.TrimSpace(m.pastedBlob)
			m.pastedBlob = ""
		}
		// Agent confirmation flow (multi-line block above input).
		if m.agentConfirmAction != nil {
			if val == "yes" {
				slog.Info("agent run confirmed by user")
				fn := m.agentConfirmAction
				m.agentConfirmAction = nil
				m.agentConfirmLines = nil
				// Set running state here (in Update) so it's reflected in the returned model.
				m.agentRunning = true
				m.ingestLabel = "starting…"
				m.ingestLog = nil
				return fn()
			}
			slog.Debug("agent run cancelled by user", "val", val)
			m.agentConfirmAction = nil
			m.agentConfirmLines = nil
			// Cancel any pre-created context (created in cmdAgentRun/cmdAgentRerun).
			if m.agentRunCancelFn != nil {
				m.agentRunCancelFn()
				m.agentRunCancelFn = nil
			}
			m.statusMsg = "cancelled"
			return nil
		}
		// Confirmation flow
		if m.pendingConfirm != nil {
			if val == "yes" {
				fn := m.pendingConfirm
				m.pendingConfirm = nil
				m.pendingConfirmMsg = ""
				return fn()
			}
			m.pendingConfirm = nil
			m.pendingConfirmMsg = ""
			m.statusMsg = "cancelled"
			return nil
		}
		// Populate edit flow: accept/skip/done
		if m.populateEditing {
			return m.handlePopulateEditInput(val)
		}
		// Remove review flow: remove/keep/done
		if m.removeReviewing {
			return m.handleRemoveReviewInput(val)
		}
		if val != "" {
			if m.achatMode {
				// "//" prefix → note.
				if strings.HasPrefix(val, "//") {
					raw := strings.TrimSpace(val[2:])
					return m.cmdArticleChatAddNote(raw)
				}
				if strings.HasPrefix(val, "!") {
					shellCmd := strings.TrimSpace(val[1:])
					if shellCmd != "" {
						return runShellCmd(shellCmd)
					}
				}
				if strings.HasPrefix(val, "/") {
					parts := strings.Fields(val)
					cmd := parts[0]
					arg := ""
					if len(parts) > 1 {
						arg = strings.TrimSpace(val[len(cmd)+1:])
					}
					handled, c := m.dispatchArticleChatCommand(cmd, arg)
					if handled {
						return c
					}
					// Unknown command — fall through to global dispatch.
					return m.dispatchCommand(val)
				}
				if m.achatStreaming {
					m.statusMsg = "waiting for response…"
					return nil
				}
				// Resolve implicit @b/@s/@f markers (article-specific shorthand).
				val = m.resolveArticleChatAtRefs(val)
				// Resolve @<numID> references.
				if atRefPattern.MatchString(val) {
					resolved, err := m.resolveAtRefs(val)
					if err != nil {
						m.setStatusError(err.Error())
						return nil
					}
					val = resolved
				}
				if m.achatEngine == nil {
					// Lazy init: start engine, send prompt when ready.
					m.achatPendingPrompt = val
					m.statusMsg = "initializing…"
					m.achatAutoScroll = true
					return m.startArticleChatCmd(m.achatSlug, m.achatProfile)
				}
				return m.sendArticleChatMsg(val)
			}
			if m.chatMode {
				// "//" prefix → note: stored in history, never sent to LLM.
				// Must be checked before the "/" command prefix.
				if strings.HasPrefix(val, "//") {
					raw := strings.TrimSpace(val[2:])
					return m.addChatNote(raw)
				}
				if strings.HasPrefix(val, "!") {
					shellCmd := strings.TrimSpace(val[1:])
					if shellCmd != "" {
						return runShellCmd(shellCmd)
					}
				}
				if strings.HasPrefix(val, "/") {
					return m.dispatchChatCommand(val)
				}
				if m.chatStreaming {
					m.statusMsg = "waiting for response…"
					return nil
				}
				// Resolve @<numID> references before sending to LLM.
				if atRefPattern.MatchString(val) {
					resolved, err := m.resolveAtRefs(val)
					if err != nil {
						m.setStatusError(err.Error())
						return nil
					}
					val = resolved
				}
				if m.chatEngine == nil {
					// Lazy init: queue prompt, start engine.
					m.chatPendingPrompt = val
					m.statusMsg = "initializing…"
					return m.startChatCmd(m.chatWorkspace)
				}
				return m.sendChatMsg(val)
			}
			// Resolve @<numID> references for non-slash commands.
			if !strings.HasPrefix(val, "/") && atRefPattern.MatchString(val) {
				resolved, err := m.resolveAtRefs(val)
				if err != nil {
					m.setStatusError(err.Error())
					return nil
				}
				val = resolved
			}
			return m.dispatchCommand(val)
		}
	default:
		// Delegate all other keys (runes, space, backspace, delete, arrows,
		// home, end, ctrl+u, ctrl+k, etc.) to the textarea model.
		if m.inputHistoryIdx != -1 {
			m.inputHistoryIdx = -1
			m.inputHistorySaved = ""
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.syncInputHeight()
		// Auto-insert space after '!' so the command reads "! cmd" not "!cmd".
		if m.input.Value() == "!" {
			m.input.SetValue("! ")
			m.input.CursorEnd()
		}
		m.updateCompletions()
		m.statusMsg = ""
		m.statusErr = false
		m.statusSuccess = false
		return cmd
	}
	return nil
}

// handleStatusKey handles keys when the status output pane has focus.
// j/k/↑/↓ scroll, Esc returns to command input.
func (m *Model) handleStatusKey(msg tea.KeyMsg) tea.Cmd {
	maxVisible := m.height * 30 / 100
	if maxVisible < 3 {
		maxVisible = 3
	}
	maxScroll := len(m.statusLines) - maxVisible
	if maxScroll < 0 {
		maxScroll = 0
	}

	switch {
	case key.Matches(msg, keys.NavDown):
		m.statusScroll++
		if m.statusScroll > maxScroll {
			m.statusScroll = maxScroll
		}
	case key.Matches(msg, keys.NavUp):
		m.statusScroll--
		if m.statusScroll < 0 {
			m.statusScroll = 0
		}
	case key.Matches(msg, keys.PageDown):
		m.statusScroll += maxVisible
		if m.statusScroll > maxScroll {
			m.statusScroll = maxScroll
		}
	case key.Matches(msg, keys.PageUp):
		m.statusScroll -= maxVisible
		if m.statusScroll < 0 {
			m.statusScroll = 0
		}
	}
	return nil
}

// pasteFromClipboard reads the system clipboard and pastes into the input.
func (m *Model) pasteFromClipboard() {
	out, err := exec.Command("pbpaste").Output()
	if err != nil || len(out) == 0 {
		return
	}
	m.pasteContent(string(out))
}

// copyToClipboard writes text to the system clipboard via pbcopy.
func (m *Model) copyToClipboard(text string) {
	cmd := exec.Command("pbcopy")
	cmd.Stdin = strings.NewReader(text)
	if err := cmd.Run(); err != nil {
		m.statusMsg = "copy failed: " + err.Error()
		m.statusErr = true
		return
	}
	m.statusMsg = "copied to clipboard"
	m.statusErr = false
}

// pasteContent handles pasted text: small pastes go inline, large ones are buffered.
func (m *Model) pasteContent(raw string) {
	m.inputExitHistory()
	content := strings.ReplaceAll(raw, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	content = strings.TrimRight(content, "\n")
	lines := strings.Split(content, "\n")
	if len(lines) > 20 || len([]rune(content)) > 256 {
		pre := m.input.Value()
		blob := pre + content
		m.pastedBlob = blob
		lineCount := strings.Count(content, "\n") + 1
		kb := float64(len(content)) / 1024.0
		label := fmt.Sprintf("[pasted: %d lines · %.1f KB]", lineCount, kb)
		m.input.SetValue(pre + label)
		m.input.CursorEnd()
	} else {
		m.input.InsertString(content)
	}
	m.syncInputHeight()
	m.updateCompletions()
}

// inputExitHistory exits history browsing mode, keeping the current value.
func (m *Model) inputExitHistory() {
	m.inputHistoryIdx = -1
	m.inputHistorySaved = ""
}

// inputHistoryPrev navigates to the previous (older) history entry.
func (m *Model) inputHistoryPrev() {
	if len(m.inputHistory) == 0 {
		return
	}
	if m.inputHistoryIdx == -1 {
		m.inputHistorySaved = m.input.Value()
		m.inputHistoryIdx = len(m.inputHistory) - 1
	} else if m.inputHistoryIdx > 0 {
		m.inputHistoryIdx--
	} else {
		return
	}
	m.input.SetValue(m.inputHistory[m.inputHistoryIdx])
	m.input.CursorEnd()
	m.syncInputHeight()
}

// inputHistoryNext navigates to the next (newer) history entry, or restores draft.
func (m *Model) inputHistoryNext() {
	if m.inputHistoryIdx == -1 {
		return
	}
	if m.inputHistoryIdx < len(m.inputHistory)-1 {
		m.inputHistoryIdx++
		m.input.SetValue(m.inputHistory[m.inputHistoryIdx])
	} else {
		m.input.SetValue(m.inputHistorySaved)
		m.inputHistoryIdx = -1
		m.inputHistorySaved = ""
	}
	m.input.CursorEnd()
	m.syncInputHeight()
}

// inputSubmit pushes the current value to history and clears the input.
func (m *Model) inputSubmit() {
	val := strings.TrimSpace(m.input.Value())
	if val != "" {
		m.pushHistory(val)
	}
	m.input.SetValue("")
	m.input.CursorEnd()
	m.inputHistoryIdx = -1
	m.inputHistorySaved = ""
	m.syncInputHeight()
}

// pushHistory appends val to history, deduplicating consecutive identical entries.
func (m *Model) pushHistory(val string) {
	if len(m.inputHistory) > 0 && m.inputHistory[len(m.inputHistory)-1] == val {
		return
	}
	m.inputHistory = append(m.inputHistory, val)
}

// cycleContentTab jumps contentScroll to the next/prev present section.
func (m *Model) cycleContentTab(delta int) tea.Cmd {
	cur := m.activeSection()
	// Collect present sections in display order
	order := []contentTab{ctFlash, ctSummary, ctBody, ctCards}
	var present []contentTab
	for _, ct := range order {
		if m.contentHas[ct] {
			present = append(present, ct)
		}
	}
	if len(present) == 0 {
		return nil
	}
	// Find index of current section
	idx := 0
	for i, ct := range present {
		if ct == cur {
			idx = i
			break
		}
	}
	next := present[(idx+delta+len(present))%len(present)]
	if m.contentOffsets[next] >= 0 {
		m.contentScroll = m.contentOffsets[next]
		m.contentLineCursor = m.contentOffsets[next]
	}
	return nil
}

// triggerContentLoad fires loadContent for the current nav cursor item.
func (m *Model) triggerContentLoad() tea.Cmd {
	item := m.selectedNavItem()
	if item == nil || item.root == "" {
		return nil
	}
	// Close article chat if we've navigated to a different article.
	if m.achatMode && item.id != m.achatSlug {
		m.exitArticleChat()
	}
	m.contentLoading = true
	m.contentLines = nil
	m.contentLineCursor = 0
	return loadContent(item.root, m.cfg.PreferredStyles, m.cfg.PreferredModels)
}

// navigateToArticleByURL switches to the Library/Articles view and selects the article
// matching url. Returns a content-load command if found, nil otherwise.
func (m *Model) navigateToArticleByURL(url string) tea.Cmd {
	// Find article in the unfiltered nav list.
	idx := -1
	for i, item := range m.navItemsAll {
		if item.url == url {
			idx = i
			break
		}
	}
	if idx < 0 {
		m.statusMsg = "article not found in library"
		return nil
	}
	m.activeTab = tabLibrary
	m.navSubTab = navSubTabArticles
	m.navItems = m.navItemsAll
	m.navCursor = idx
	m.navScroll = 0
	m.setFocusPane(paneNav)
	return m.triggerContentLoad()
}

// openCurrentURL opens the source URL of the current nav item in a new Chrome window.
func (m *Model) openCurrentURL() tea.Cmd {
	item := m.selectedNavItem()
	if item == nil || item.url == "" {
		return nil
	}
	return openInChrome(item.url)
}

// activeSection returns the content tab whose section is currently visible at the top
// of the content scroll position. Walks offsets in display order, returns the last
// section whose offset is ≤ contentScroll.
func (m *Model) activeSection() contentTab {
	order := []contentTab{ctFlash, ctSummary, ctBody, ctCards}
	active := ctBody // fallback
	for _, ct := range order {
		if m.contentHas[ct] {
			active = ct
			break
		}
	}
	for _, ct := range order {
		if m.contentHas[ct] && m.contentOffsets[ct] >= 0 && m.contentOffsets[ct] <= m.contentScroll {
			active = ct
		}
	}
	return active
}

// clampNavScroll adjusts navScroll so navCursor stays within the visible window.
func (m *Model) clampNavScroll() {
	visibleHeight := m.navPaneHeight()
	if visibleHeight < 1 {
		return
	}
	if m.navCursor < m.navScroll {
		m.navScroll = m.navCursor
	} else if m.navCursor >= m.navScroll+visibleHeight {
		m.navScroll = m.navCursor - visibleHeight + 1
	}
}

// updateCompletions recomputes the completion list based on the current input.
// Shows completions when input starts with "/" and contains no space.
// Shows param suggestions when input is a known command followed by a space.
func (m *Model) updateCompletions() {
	val := m.input.Value()
	m.statusLines = nil

	if !strings.HasPrefix(val, "/") {
		m.cmdComplete = nil
		m.cmdCompleteIdx = -1
		return
	}

	// Param suggestion mode: "/cmd " with optional partial arg
	if strings.Contains(val, " ") {
		m.cmdComplete = nil
		m.cmdCompleteIdx = -1
		parts := strings.SplitN(val, " ", 2)
		cmd := strings.ToLower(parts[0])
		arg := parts[1] // preserve case for display; lowercase when filtering
		all := m.paramSuggestions(cmd, arg)
		// Filter by partial last token
		partial := strings.ToLower(arg)
		// For multi-word args (e.g. "/help article "), filter on the last word
		if idx := strings.LastIndex(partial, " "); idx >= 0 {
			partial = partial[idx+1:]
		}
		var filtered []cmdCompletion
		for _, c := range all {
			if partial == "" || strings.HasPrefix(strings.ToLower(c.cmd), partial) {
				filtered = append(filtered, c)
			}
		}
		m.paramItems = filtered
		if len(filtered) > 0 {
			m.paramIdx = 0
		} else {
			m.paramIdx = -1
		}
		return
	}

	// Completion mode: "/prefix" with no space — clear any stale param items.
	m.paramItems = nil
	m.paramIdx = -1
	// Use case-sensitive matching so /S shows /Scratch but not /scratch.
	var filtered []cmdCompletion
	for _, c := range m.allCommands() {
		if strings.HasPrefix(c.cmd, val) {
			filtered = append(filtered, c)
		}
	}
	m.cmdComplete = filtered
	if m.cmdCompleteIdx >= len(filtered) {
		m.cmdCompleteIdx = len(filtered) - 1
	}
	if len(filtered) > 0 && m.cmdCompleteIdx < 0 {
		m.cmdCompleteIdx = 0
	}
}

// paramSuggestions returns candidate values for commands that take a known arg.
// arg is the partial text already typed after the command (may include spaces for /help).
func (m *Model) paramSuggestions(cmd, arg string) []cmdCompletion {
	switch cmd {
	case "/search":
		if m.activeTab == tabLibrary && m.navSubTab == navSubTabWorkspaces {
			ws := m.contextWorkspace()
			slog.Debug("paramSuggestions /search",
				"wsFocusName", m.wsFocusName,
				"contextWs", ws != nil,
				"wsCursor", m.wsCursor)
			if ws != nil {
				return []cmdCompletion{{cmd: "→", desc: "searching articles in workspace: " + ws.name}}
			}
			return []cmdCompletion{{cmd: "→", desc: "searching workspaces by name/description"}}
		}

	case "/filter":
		seen := map[string]bool{}
		var items []cmdCompletion
		for _, item := range m.navItemsAll {
			for _, tag := range item.tags {
				if !seen[tag] {
					seen[tag] = true
					items = append(items, cmdCompletion{cmd: tag})
				}
			}
		}
		return items

	case "/collection-add":
		var items []cmdCompletion
		for _, r := range m.navRows {
			if r.kind == rowCollection {
				items = append(items, cmdCompletion{cmd: r.colSlug, desc: fmt.Sprintf("%d articles", r.colCount)})
			}
		}
		return items

	case "/collection-remove":
		sel := m.selectedNavItem()
		if sel == nil {
			return nil
		}
		var items []cmdCompletion
		for _, slug := range sel.collections {
			items = append(items, cmdCompletion{cmd: slug})
		}
		return items

	case "/delete":
		sub := m.navSubTab
		if m.activeTab != tabLibrary {
			sub = navSubTabArticles
		}
		switch sub {
		case navSubTabArticles:
			items := make([]cmdCompletion, 0, len(m.navItems))
			for _, item := range m.navItems {
				items = append(items, cmdCompletion{cmd: item.id, desc: truncate(oneLine(item.title), 40)})
			}
			return items
		case navSubTabCollections:
			var items []cmdCompletion
			for _, r := range m.navRows {
				if r.kind == rowCollection {
					items = append(items, cmdCompletion{cmd: r.colSlug, desc: fmt.Sprintf("%d articles", r.colCount)})
				}
			}
			return items
		case navSubTabWorkspaces:
			items := make([]cmdCompletion, 0, len(m.workspaceItems))
			for _, ws := range m.workspaceItems {
				items = append(items, cmdCompletion{cmd: ws.name, desc: fmt.Sprintf("%da %dc", ws.articleCount, ws.collectionCount)})
			}
			return items
		}

	case "/populate", "/remove":
		// Suggest the workspace name only as the first token (before any flags).
		if strings.TrimSpace(arg) != "" {
			return nil
		}
		if m.chatMode && m.chatWorkspace != "" {
			return []cmdCompletion{{cmd: m.chatWorkspace}}
		}
		if ws := m.selectedWorkspace(); ws != nil {
			return []cmdCompletion{{cmd: ws.name}}
		}
		return nil

	case "/article":
		return []cmdCompletion{
			{cmd: "list", desc: "go to Articles sub-tab"},
			{cmd: "search", desc: "<query>  full-text search"},
			{cmd: "ingest", desc: "<url>  add a new article"},
		}

	case "/collection":
		return []cmdCompletion{
			{cmd: "list", desc: "go to Collections sub-tab"},
			{cmd: "search", desc: "<query>  filter by name/slug"},
			{cmd: "reload", desc: "refresh collections list from disk"},
		}

	case "/workspace":
		return []cmdCompletion{
			{cmd: "list", desc: "go to Workspaces sub-tab"},
			{cmd: "new", arg: "<name> [description]", desc: "create a new workspace"},
			{cmd: "delete", desc: "delete selected workspace"},
			{cmd: "rename", arg: "<name>", desc: "rename selected workspace"},
			{cmd: "describe", arg: "<text>", desc: "set workspace description"},
			{cmd: "add", arg: "article|collection <slug>", desc: "add article or collection; resets chat engine"},
			{cmd: "remove", arg: "article|collection <slug>", desc: "remove article or collection; resets chat engine"},
		}

	case "/workspace add":
		return []cmdCompletion{
			{cmd: "article", arg: "<slug>", desc: "add article to selected workspace"},
			{cmd: "collection", arg: "<slug>", desc: "add collection to selected workspace"},
		}

	case "/workspace remove":
		return []cmdCompletion{
			{cmd: "article", arg: "<slug>", desc: "remove article from selected workspace"},
			{cmd: "collection", arg: "<slug>", desc: "remove collection from selected workspace"},
		}

	case "/agent-rerun":
		return nil

	case "/mode":
		return []cmdCompletion{
			{cmd: "corpus-only", desc: "answers from workspace articles only"},
			{cmd: "corpus-first", desc: "articles first, then open knowledge"},
			{cmd: "open", desc: "no grounding — general LLM knowledge"},
		}

	case "/profile", "/model":
		var items []cmdCompletion
		for name, p := range m.cfg.Profiles {
			items = append(items, cmdCompletion{cmd: name, desc: p.Model})
		}
		return items

	case "/help":
		// Second level: "/help article " → return command entries for that group.
		trimmed := strings.TrimSpace(strings.ToLower(arg))
		for _, g := range helpGroups {
			if trimmed == g.name || strings.HasPrefix(trimmed, g.name+" ") {
				items := make([]cmdCompletion, len(g.commands))
				for i, c := range g.commands {
					name := c.cmd
					// For CLI-only entries like "arc workspace new", show just the subcommand.
					if parts := strings.Fields(name); len(parts) == 3 && parts[0] == "arc" {
						name = parts[2]
					}
					items[i] = cmdCompletion{cmd: name, desc: c.desc}
				}
				return items
			}
		}
		// First level: return group names.
		items := make([]cmdCompletion, len(helpGroups))
		for i, g := range helpGroups {
			items[i] = cmdCompletion{cmd: g.name}
		}
		return items
	}
	return nil
}

// acceptParam fills the selected param value into the input, replacing any partial last token.
func (m *Model) acceptParam() {
	if m.paramIdx < 0 || m.paramIdx >= len(m.paramItems) {
		return
	}
	val := m.paramItems[m.paramIdx].cmd
	// Find the last space in the input and replace everything after it with val.
	input := m.input.Value()
	if idx := strings.LastIndex(input, " "); idx >= 0 {
		input = input[:idx]
	}
	m.input.SetValue(input + " " + val)
	m.input.CursorEnd()
	m.paramItems = nil
	m.paramIdx = -1
}

// acceptCompletion fills the input with the selected command + space (if it takes an arg).
// Then immediately populates paramItems if the command has param suggestions.
func (m *Model) acceptCompletion() {
	if m.cmdCompleteIdx < 0 || m.cmdCompleteIdx >= len(m.cmdComplete) {
		return
	}
	c := m.cmdComplete[m.cmdCompleteIdx]
	if c.arg != "" {
		m.input.SetValue(c.cmd + " ")
	} else {
		m.input.SetValue(c.cmd)
	}
	m.input.CursorEnd()
	m.cmdComplete = nil
	m.cmdCompleteIdx = -1
	// Immediately show param picker if this command has suggestions.
	params := m.paramSuggestions(c.cmd, "")
	m.paramItems = params
	if len(params) > 0 {
		m.paramIdx = 0
	} else {
		m.paramIdx = -1
	}
}

// dispatchCommand parses and executes a submitted command string.
func (m *Model) dispatchCommand(val string) tea.Cmd {
	m.statusLines = nil
	m.statusMsg = ""
	m.statusErr = false
	m.pendingConfirm = nil
	m.pendingConfirmMsg = ""

	// Shell command — run via $SHELL -c, show output in status pane.
	if strings.HasPrefix(val, "!") {
		shellCmd := strings.TrimSpace(val[1:])
		if shellCmd == "" {
			m.statusMsg = "usage: !<command>"
			return nil
		}
		return runShellCmd(shellCmd)
	}

	parts := strings.Fields(val)
	if len(parts) == 0 {
		return nil
	}
	cmd := strings.ToLower(parts[0])
	// Preserve original formatting (newlines, whitespace) in arg
	// by stripping the command prefix instead of re-joining Fields.
	arg := ""
	if idx := strings.Index(val, parts[0]); idx >= 0 {
		rest := val[idx+len(parts[0]):]
		if trimmed := strings.TrimLeft(rest, " "); trimmed != "" {
			arg = trimmed
		}
	}

	// ── Global commands (available in any context) ──────────────────────────
	switch cmd {
	case "/config":
		m.setStatusLines(m.cmdConfigLines())
		m.focus = paneStatus
		return nil
	case "/config-view":
		m.cmdConfigView()
		return nil
	case "/config-agent-view":
		m.cmdAgentConfigView()
		return nil
	case "/config-chat-view":
		m.cmdChatConfigView()
		return nil
	case "/config-edit":
		return m.cmdConfigEdit()
	case "/config-agent-edit":
		return m.cmdAgentConfigEdit()
	case "/config-chat-edit":
		return m.cmdChatConfigEdit()
	case "/stats":
		return m.cmdStats()
	case "/models", "/profiles":
		m.setStatusLines(m.cmdModelsLines())
		return nil
	case "/log", "/logs":
		return m.cmdLog()
	case "/help":
		m.setStatusLines(m.helpLines(arg))
		return nil
	case "/scratch":
		global := parts[0] == "/Scratch"
		return m.cmdScratch(arg, global)
	case "/askx":
		global := parts[0] == "/AskX"
		return m.cmdAskX(arg, global)
	case "/article":
		return m.dispatchQualified(navSubTabArticles, arg)
	case "/collection":
		return m.dispatchQualified(navSubTabCollections, arg)
	case "/workspace":
		return m.dispatchQualified(navSubTabWorkspaces, arg)
	case "/agent-run":
		return m.cmdAgentRun(arg)
	case "/agent-rerun":
		return m.cmdAgentRerun(arg)
	case "/feed-add":
		m.openEditorForFeed(-1)
		return nil
	case "/feed-edit":
		if len(m.agentFeeds) > 0 {
			m.openEditorForFeed(m.agentFeedsCursor)
		} else {
			m.setStatusError("no feed selected")
		}
		return nil
	case "/feed-toggle":
		if len(m.agentFeeds) > 0 {
			return toggleAgentFeed(m.cfg.AgentPath, m.agentFeedsCursor)
		}
		m.setStatusError("no feed selected")
		return nil
	case "/feed-delete":
		if len(m.agentFeeds) > 0 {
			name := m.agentFeeds[m.agentFeedsCursor].Name
			if name == "" {
				name = m.agentFeeds[m.agentFeedsCursor].URL
			}
			idx := m.agentFeedsCursor
			m.askConfirm(fmt.Sprintf("delete %q? yes/no", name), func() tea.Cmd {
				return deleteAgentFeed(m.cfg.AgentPath, idx)
			})
		} else {
			m.setStatusError("no feed selected")
		}
		return nil
	}

	// ── Context-sensitive commands ──────────────────────────────────────────
	sub := m.navSubTab
	if m.activeTab != tabLibrary {
		sub = navSubTabArticles // default context outside Library
	}

	switch cmd {
	// ── Shared (multi-context) ──────────────────────────────────────────
	case "/search":
		if arg == "" {
			m.statusMsg = "usage: /search <query>"
			return nil
		}
		switch sub {
		case navSubTabWorkspaces:
			ws := m.contextWorkspace()
			slog.Debug("/search workspace dispatch",
				"wsFocusName", m.wsFocusName,
				"contextWs", ws != nil,
				"arg", arg)
			if ws != nil {
				// Cursor is within an expanded/focused workspace: search its articles.
				query, limit := parseSearchArg(arg)
				slugs := m.workspaceArticleSlugs(ws)
				slog.Debug("/search scoping to workspace", "name", ws.name, "articleCount", len(slugs))
				if m.svc == nil || len(slugs) == 0 {
					m.statusMsg = fmt.Sprintf("no articles in workspace %q", ws.name)
					return nil
				}
				m.statusMsg = "searching…"
				return cmdFTSSearch(m.svc, query, limit, slugs)
			}
			m.filterWorkspaces(arg)
			return nil
		case navSubTabCollections:
			if m.svc == nil {
				m.filterCollections(arg)
				return nil
			}
			m.statusMsg = "searching…"
			return cmdCollectionSearch(m.svc, arg)
		default: // articles
			query, limit := parseSearchArg(arg)
			if m.svc == nil {
				m.applyNavFilter("search", query)
				return nil
			}
			m.statusMsg = "searching…"
			return cmdFTSSearch(m.svc, query, limit, nil)
		}

	case "/clear":
		switch sub {
		case navSubTabWorkspaces:
			m.workspaceItems = m.workspaceItemsAll
			m.wsFocusName = ""
			m.wsRows = m.buildWsRows()
			m.wsCursor = 0
			m.wsScroll = 0
			m.navFilter = ""
			m.navItems = m.navItemsAll
			m.navCursor = 0
			m.navScroll = 0
			m.focus = paneNav
			m.statusMsg = "✓ filter cleared"
			return nil
		case navSubTabCollections:
			m.navRows = m.navRowsAll
			m.navRowCursor = 0
			m.navRowScroll = 0
			m.navFilter = ""
			m.focus = paneNav
			m.statusMsg = "✓ filter cleared"
			return nil
		default: // articles
			m.navItems = m.navItemsAll
			m.navFilter = ""
			m.navCursor = 0
			m.navScroll = 0
			m.focus = paneNav
			m.statusMsg = "✓ filter cleared"
			return m.triggerContentLoad()
		}

	case "/delete":
		switch sub {
		case navSubTabWorkspaces:
			if arg != "" {
				return m.cmdDeleteWorkspaceByName(arg)
			}
			return m.cmdDeleteWorkspace()
		case navSubTabCollections:
			if arg != "" {
				return m.cmdDeleteCollectionByName(arg)
			}
			return m.cmdDeleteCollection()
		default: // articles
			if arg != "" {
				return m.cmdDeleteArticleBySlug(arg)
			}
			return m.cmdDeleteArticle()
		}

	// ── Article-only ────────────────────────────────────────────────────
	case "/filter":
		if sub != navSubTabArticles {
			m.statusMsg = "✗ /filter is only available in Articles context"
			return nil
		}
		if arg == "" {
			m.statusMsg = "usage: /filter <tag>"
			return nil
		}
		m.applyNavFilter("tag", arg)
		return nil

	case "/tags":
		return m.cmdTags()

	case "/collections":
		return m.cmdCollections()

	case "/favorites":
		if sub != navSubTabArticles {
			m.statusMsg = "✗ /favorites is only available in Articles context"
			return nil
		}
		m.applyNavFilter("favorite", "")
		return nil

	case "/favorite":
		if sub != navSubTabArticles {
			m.statusMsg = "✗ /favorite is only available in Articles context"
			return nil
		}
		return m.cmdToggleFavorite()

	case "/open":
		if sub != navSubTabArticles {
			m.statusMsg = "✗ /open is only available in Articles context"
			return nil
		}
		return m.openCurrentURL()

	case "/read":
		if sub != navSubTabArticles {
			m.statusMsg = "✗ /read is only available in Articles context"
			return nil
		}
		return m.cmdMarkRead()

	case "/unread":
		if sub != navSubTabArticles {
			m.statusMsg = "✗ /unread is only available in Articles context"
			return nil
		}
		return m.cmdMarkUnread()

	case "/chat":
		if sub != navSubTabArticles && sub != navSubTabCollections {
			m.statusMsg = "✗ /chat requires an article selected"
			return nil
		}
		return m.cmdArticleChat()

	case "/reprocess":
		if sub != navSubTabArticles {
			m.statusMsg = "✗ /reprocess is only available in Articles context"
			return nil
		}
		return m.cmdReprocess()

	case "/collection-add":
		if sub != navSubTabArticles {
			m.statusMsg = "✗ /collection-add is only available in Articles context"
			return nil
		}
		if arg == "" {
			m.statusMsg = "usage: /collection-add <slug>"
			return nil
		}
		return m.cmdCollectionAdd(arg)

	case "/collection-remove":
		if sub != navSubTabArticles {
			m.statusMsg = "✗ /collection-remove is only available in Articles context"
			return nil
		}
		if arg == "" {
			m.statusMsg = "usage: /collection-remove <slug>"
			return nil
		}
		return m.cmdCollectionRemove(arg)

	case "/ingest":
		if arg == "" {
			m.statusMsg = "usage: /ingest <url> [--profile <name>] [--style <name>]"
			return nil
		}
		return m.cmdIngest(arg)

	// ── Workspace-only ──────────────────────────────────────────────────
	case "/new":
		if sub != navSubTabWorkspaces {
			m.statusMsg = "✗ /new is only available in Workspaces context"
			return nil
		}
		if arg == "" {
			m.statusMsg = "usage: /new <name>"
			return nil
		}
		return m.cmdNewWorkspace(arg)

	case "/rename":
		if sub != navSubTabWorkspaces {
			m.statusMsg = "✗ /rename is only available in Workspaces context"
			return nil
		}
		if arg == "" {
			m.statusMsg = "usage: /rename <new-name>"
			return nil
		}
		return m.cmdRenameWorkspace(arg)

	case "/describe":
		if sub != navSubTabWorkspaces {
			m.statusMsg = "✗ /describe is only available in Workspaces context"
			return nil
		}
		if arg == "" {
			m.statusMsg = "usage: /describe <text>"
			return nil
		}
		return m.cmdDescribeWorkspace(arg)

	case "/mode":
		if !m.chatMode {
			m.statusMsg = "✗ /mode is only available in workspace chat"
			return nil
		}
		if arg == "" {
			m.statusMsg = "grounding mode: " + m.chatGroundingMode
			return nil
		}
		if m.chatEngine != nil {
			if err := m.chatEngine.SetGroundingMode(arg); err != nil {
				m.statusMsg = "✗ " + err.Error()
				return nil
			}
		}
		m.chatGroundingMode = arg
		m.statusMsg = "grounding mode → " + arg
		return nil

	case "/profile", "/model":
		if !m.chatMode {
			m.statusMsg = "✗ /profile is only available in workspace chat"
			return nil
		}
		if arg == "" {
			active := ""
			if m.chatEngine != nil {
				active = m.chatEngine.ProfileName()
			} else if m.chatProfileOverride != "" {
				active = m.chatProfileOverride
			}
			if active != "" {
				m.statusMsg = "profile: " + active
			} else {
				m.statusMsg = "profile: (workspace default)"
			}
			return nil
		}
		if _, ok := m.cfg.Profiles[arg]; !ok {
			m.statusMsg = "✗ unknown profile: " + arg
			return nil
		}
		// Persist to workspace chat/chat.json.
		chatCfg, _ := storefs.ReadChatConfig(m.cfg.DataRoot, m.chatWorkspace)
		chatCfg.Profile = arg
		if err := storefs.WriteChatConfig(m.cfg.DataRoot, m.chatWorkspace, chatCfg); err != nil {
			m.statusMsg = "✗ save profile: " + err.Error()
			return nil
		}
		m.chatLoadedProfile = arg
		m.chatProfileOverride = arg // also set session override for immediate prompt update
		// Reset engine so it reinitializes with the new profile on next message.
		if m.chatCancelStream != nil {
			m.chatCancelStream()
			m.chatCancelStream = nil
		}
		m.chatEngine = nil
		m.chatStreaming = false
		m.syncInputPrompt()
		m.statusMsg = "profile → " + arg
		return nil

	case "/reload":
		switch sub {
		case navSubTabWorkspaces:
			return m.cmdWorkspaceReload()
		case navSubTabCollections:
			return m.cmdCollectionReload()
		default:
			m.statusMsg = "✗ /reload is only available in Workspaces or Collections context"
			return nil
		}

	case "/populate":
		if sub != navSubTabWorkspaces {
			m.statusMsg = "✗ /populate is only available in Workspaces context"
			return nil
		}
		return m.cmdPopulateWorkspace(arg)

	case "/remove":
		if sub != navSubTabWorkspaces {
			m.statusMsg = "✗ /remove is only available in Workspaces context"
			return nil
		}
		return m.cmdRemoveWorkspace(arg)

	default:
		m.statusMsg = "✗ unknown command: " + parts[0]
		return nil
	}
}

// dispatchQualified switches to the given sub-tab then executes the subcommand.
// subCmd examples: "list", "search foo", "new my-workspace".
func (m *Model) dispatchQualified(sub navSubTab, subCmd string) tea.Cmd {
	// Switch to Library tab and the right sub-tab first.
	m.activeTab = tabLibrary
	switchCmd := m.switchNavSubTab(sub)

	subCmd = strings.TrimSpace(strings.ToLower(subCmd))
	subParts := strings.Fields(subCmd)
	verb := ""
	if len(subParts) > 0 {
		verb = subParts[0]
	}
	arg := ""
	if len(subParts) > 1 {
		arg = strings.Join(subParts[1:], " ")
	}

	// After switching context, move focus to nav pane.
	m.focus = paneNav

	switch sub {
	case navSubTabArticles:
		switch verb {
		case "", "list":
			// just switching is enough
		case "search":
			if arg == "" {
				m.statusMsg = "usage: /article search <query>"
			} else {
				query, limit := parseSearchArg(arg)
				if m.svc != nil {
					m.statusMsg = "searching…"
					return tea.Batch(switchCmd, cmdFTSSearch(m.svc, query, limit, nil))
				}
				m.applyNavFilter("search", query)
			}
		case "ingest":
			if arg == "" {
				m.statusMsg = "usage: /article ingest <url>"
			} else {
				return tea.Batch(switchCmd, m.cmdIngest(arg))
			}
		default:
			m.statusMsg = "✗ unknown article command: " + verb
		}

	case navSubTabCollections:
		switch verb {
		case "", "list":
			// switching is enough
		case "search":
			if arg == "" {
				m.statusMsg = "usage: /collection search <query>"
			} else if m.svc != nil {
				m.statusMsg = "searching…"
				return tea.Batch(switchCmd, cmdCollectionSearch(m.svc, arg))
			} else {
				m.filterCollections(arg)
			}
		case "reload":
			return tea.Batch(switchCmd, m.cmdCollectionReload())
		default:
			m.statusMsg = "✗ unknown collection command: " + verb
		}

	case navSubTabWorkspaces:
		switch verb {
		case "", "list":
			// switching is enough
		case "new":
			if arg == "" {
				m.statusMsg = "usage: /workspace new <name>"
			} else {
				return tea.Batch(switchCmd, m.cmdNewWorkspace(arg))
			}
		case "delete":
			m.cmdDeleteWorkspace()
		case "rename":
			if arg == "" {
				m.statusMsg = "usage: /workspace rename <new-name>"
			} else {
				return tea.Batch(switchCmd, m.cmdRenameWorkspace(arg))
			}
		case "describe":
			if arg == "" {
				m.statusMsg = "usage: /workspace describe <text>"
			} else {
				return tea.Batch(switchCmd, m.cmdDescribeWorkspace(arg))
			}
		case "add", "remove":
			return tea.Batch(switchCmd, m.cmdWorkspaceMembership(verb, arg))
		default:
			m.statusMsg = "✗ unknown workspace command: " + verb
		}
	}

	return switchCmd
}

// contextWorkspace returns the workspace the user is currently "within" for
// search purposes. This is the workspace under the cursor if it is expanded
// (or in solo/focus mode), or nil when the cursor is on a collapsed workspace
// header (meaning the user wants to search the workspace list).
func (m *Model) contextWorkspace() *workspaceItem {
	// Solo/focus mode: wsFocusName was set via Enter.
	if m.wsFocusName != "" {
		for i := range m.workspaceItems {
			if m.workspaceItems[i].name == m.wsFocusName {
				slog.Debug("contextWorkspace: focus mode", "name", m.wsFocusName)
				return &m.workspaceItems[i]
			}
		}
	}
	// Expanded workspace: cursor is on any row that belongs to an expanded workspace.
	if m.wsCursor >= 0 && m.wsCursor < len(m.wsRows) {
		row := m.wsRows[m.wsCursor]
		if row.wsIdx >= 0 && row.wsIdx < len(m.workspaceItems) {
			ws := &m.workspaceItems[row.wsIdx]
			slog.Debug("contextWorkspace: cursor row",
				"kind", row.kind, "wsIdx", row.wsIdx,
				"name", ws.name, "expanded", ws.expanded,
				"wsFocusName", m.wsFocusName)
			if ws.expanded {
				return ws
			}
		}
	}
	return nil
}

// workspaceArticleSlugs returns all article slugs reachable from a workspace:
// direct articles plus articles belonging to any of the workspace's collections.
func (m *Model) workspaceArticleSlugs(ws *workspaceItem) []string {
	seen := make(map[string]bool)
	for _, slug := range ws.articles {
		seen[slug] = true
	}
	colSet := make(map[string]bool, len(ws.collectionSlugs))
	for _, c := range ws.collectionSlugs {
		colSet[c] = true
	}
	for _, item := range m.navItemsAll {
		if seen[item.id] {
			continue
		}
		for _, c := range item.collections {
			if colSet[c] {
				seen[item.id] = true
				break
			}
		}
	}
	slugs := make([]string, 0, len(seen))
	for s := range seen {
		slugs = append(slugs, s)
	}
	return slugs
}

// filterCollections filters navRowsAll to collections matching query (slug/name/description).
func (m *Model) filterWorkspaces(query string) {
	q := strings.ToLower(query)
	var filtered []workspaceItem
	for _, ws := range m.workspaceItemsAll {
		// Build searchable text: name, description, collection slugs, article slugs (split by -).
		searchable := strings.ToLower(ws.name + " " + ws.description)
		for _, col := range ws.collectionSlugs {
			searchable += " " + strings.ToLower(strings.ReplaceAll(col, "-", " "))
		}
		for _, slug := range ws.articles {
			searchable += " " + strings.ToLower(strings.ReplaceAll(slug, "-", " "))
		}
		if strings.Contains(searchable, q) {
			filtered = append(filtered, ws)
		}
	}
	m.workspaceItems = filtered
	m.wsRows = m.buildWsRows()
	m.wsCursor = 0
	m.wsScroll = 0
	m.focus = paneNav
	n := len(filtered)
	if n == 0 {
		m.statusMsg = fmt.Sprintf("no workspaces matching %q", query)
		m.navFilter = ""
	} else {
		m.navFilter = fmt.Sprintf("workspaces: %q · %d results  ·  /clear to reset", query, n)
		m.statusMsg = ""
	}
}

func (m *Model) filterCollections(query string) {
	q := strings.ToLower(query)
	var filtered []navRow
	for _, row := range m.navRowsAll {
		if row.kind != rowCollection {
			continue
		}
		if strings.Contains(strings.ToLower(row.colSlug), q) ||
			strings.Contains(strings.ToLower(row.colName), q) ||
			strings.Contains(strings.ToLower(row.colDesc), q) {
			filtered = append(filtered, row)
		}
	}
	m.navRows = filtered
	m.navRowCursor = 0
	m.navRowScroll = 0
	m.focus = paneNav
	n := len(filtered)
	if n == 0 {
		m.statusMsg = fmt.Sprintf("no collections matching %q", query)
		m.navFilter = ""
	} else {
		m.navFilter = fmt.Sprintf("collections: %q · %d results  ·  /clear to reset", query, n)
		m.statusMsg = ""
	}
}

// applyNavFilter filters navItems from navItemsAll by mode ("search" or "tag") and query.
func (m *Model) applyNavFilter(mode, query string) {
	q := strings.ToLower(query)
	var filtered []navItem
	for _, item := range m.navItemsAll {
		switch mode {
		case "search":
			if strings.Contains(strings.ToLower(item.title), q) ||
				strings.Contains(strings.ToLower(item.url), q) {
				filtered = append(filtered, item)
			}
		case "tag":
			for _, tag := range item.tags {
				if strings.Contains(strings.ToLower(tag), q) {
					filtered = append(filtered, item)
					break
				}
			}
		case "favorite":
			if item.favorite {
				filtered = append(filtered, item)
			}
		}
	}
	m.navItems = filtered
	m.navCursor = 0
	m.navScroll = 0
	n := len(filtered)
	if n == 0 {
		if mode == "favorite" {
			m.statusMsg = "no favorites yet — press f or * to mark an article"
		} else {
			m.statusMsg = fmt.Sprintf("no results for %q", query)
		}
		m.navFilter = ""
	} else {
		if mode == "favorite" {
			m.navFilter = fmt.Sprintf("★ favorites · %d articles  ·  /clear to reset", n)
		} else {
			m.navFilter = mode + ": " + query + " · " + fmt.Sprintf("%d", n) + " results  ·  /clear to reset"
		}
		m.statusMsg = ""
	}
}

// cmdMarkRead marks the current article as read in-memory and persists to DB.
func (m *Model) cmdMarkRead() tea.Cmd {
	item := m.selectedNavItem()
	if item == nil {
		m.statusMsg = "✗ no article selected"
		return nil
	}
	id := item.id
	item.read = true
	for i, ni := range m.navItemsAll {
		if ni.id == id {
			m.navItemsAll[i].read = true
			break
		}
	}
	m.statusMsg = "✓ marked as read"
	if m.svc == nil {
		return nil
	}
	svc := m.svc
	return func() tea.Msg {
		_ = svc.MarkRead(context.Background(), id)
		return nil
	}
}

// cmdMarkUnread marks the current article as unread in-memory and persists to DB.
func (m *Model) cmdMarkUnread() tea.Cmd {
	item := m.selectedNavItem()
	if item == nil {
		m.statusMsg = "✗ no article selected"
		return nil
	}
	id := item.id
	item.read = false
	for i, ni := range m.navItemsAll {
		if ni.id == id {
			m.navItemsAll[i].read = false
			break
		}
	}
	m.statusMsg = "✓ marked as unread"
	if m.svc == nil {
		return nil
	}
	svc := m.svc
	return func() tea.Msg {
		_ = svc.MarkUnread(context.Background(), id)
		return nil
	}
}

// cmdCollectionAdd adds the current article to the named collection.
func (m *Model) cmdCollectionAdd(collSlug string) tea.Cmd {
	item := m.selectedNavItem()
	if item == nil {
		m.statusMsg = "✗ no article selected"
		return nil
	}
	articleSlug := item.id
	// Check not already a member.
	for _, c := range item.collections {
		if c == collSlug {
			m.statusMsg = "✗ already in collection: " + collSlug
			return nil
		}
	}
	// Update in-memory.
	for i, ni := range m.navItemsAll {
		if ni.id == articleSlug {
			m.navItemsAll[i].collections = append(m.navItemsAll[i].collections, collSlug)
			break
		}
	}
	m.statusMsg = "✓ added to collection: " + collSlug
	if m.svc == nil {
		return nil
	}
	svc := m.svc
	return func() tea.Msg {
		if err := svc.AddToCollection(context.Background(), articleSlug, collSlug); err != nil {
			slog.Error("AddToCollection", "err", err)
		}
		return nil
	}
}

// cmdCollectionRemove removes the current article from the named collection.
func (m *Model) cmdCollectionRemove(collSlug string) tea.Cmd {
	item := m.selectedNavItem()
	if item == nil {
		m.statusMsg = "✗ no article selected"
		return nil
	}
	articleSlug := item.id
	// Check is a member.
	found := false
	for _, c := range item.collections {
		if c == collSlug {
			found = true
			break
		}
	}
	if !found {
		m.statusMsg = "✗ not in collection: " + collSlug
		return nil
	}
	// Update in-memory.
	for i, ni := range m.navItemsAll {
		if ni.id == articleSlug {
			cols := m.navItemsAll[i].collections
			out := cols[:0]
			for _, c := range cols {
				if c != collSlug {
					out = append(out, c)
				}
			}
			m.navItemsAll[i].collections = out
			break
		}
	}
	m.statusMsg = "✓ removed from collection: " + collSlug
	if m.svc == nil {
		return nil
	}
	svc := m.svc
	return func() tea.Msg {
		if err := svc.RemoveFromCollection(context.Background(), articleSlug, collSlug); err != nil {
			slog.Error("RemoveFromCollection", "err", err)
		}
		return nil
	}
}

// cmdToggleFavorite toggles the favorite flag on the current article.
func (m *Model) cmdToggleFavorite() tea.Cmd {
	item := m.selectedNavItem()
	if item == nil {
		m.statusMsg = "✗ no article selected"
		return nil
	}
	id := item.id
	nowFav := !item.favorite
	// Update in-memory lists.
	item.favorite = nowFav
	for i, ni := range m.navItemsAll {
		if ni.id == id {
			m.navItemsAll[i].favorite = nowFav
			break
		}
	}
	if nowFav {
		m.statusMsg = "★ marked as favorite"
	} else {
		m.statusMsg = "✓ removed from favorites"
	}
	if m.svc == nil {
		return nil
	}
	svc := m.svc
	return func() tea.Msg {
		if nowFav {
			_ = svc.MarkFavorite(context.Background(), id)
		} else {
			_ = svc.UnmarkFavorite(context.Background(), id)
		}
		return nil
	}
}

// cmdTogglePin toggles the pinned flag on the currently selected workspace.
func (m *Model) cmdTogglePin() tea.Cmd {
	row := m.selectedWsRow()
	if row == nil || row.kind != wsRowWorkspace {
		m.statusMsg = "✗ select a workspace to pin"
		return nil
	}
	wsIdx := row.wsIdx
	if wsIdx < 0 || wsIdx >= len(m.workspaceItems) {
		return nil
	}
	nowPinned := !m.workspaceItems[wsIdx].pinned
	name := m.workspaceItems[wsIdx].name
	m.workspaceItems[wsIdx].pinned = nowPinned
	// Keep workspaceItemsAll in sync.
	for i, wi := range m.workspaceItemsAll {
		if wi.name == name {
			m.workspaceItemsAll[i].pinned = nowPinned
			break
		}
	}
	if nowPinned {
		m.statusMsg = "★ workspace pinned"
	} else {
		m.statusMsg = "✓ workspace unpinned"
	}
	if m.svc == nil {
		return nil
	}
	svc := m.svc
	return func() tea.Msg {
		if nowPinned {
			_ = svc.PinWorkspace(context.Background(), name)
		} else {
			_ = svc.UnpinWorkspace(context.Background(), name)
		}
		return cmdDoneMsg{reloadWorkspaces: true}
	}
}

// cmdDeleteArticle prompts for confirmation then deletes the current article.
func (m *Model) cmdDeleteArticle() tea.Cmd {
	sel := m.selectedNavItem()
	if sel == nil {
		m.statusMsg = "✗ no article selected"
		return nil
	}
	item := *sel
	returnFocus := m.focus
	m.askConfirm(fmt.Sprintf("delete %q? (yes/N)", item.title), func() tea.Cmd {
		id := item.id
		svc := m.svc
		// Remove from in-memory lists immediately
		m.navItems = removeNavItem(m.navItems, id)
		m.navItemsAll = removeNavItem(m.navItemsAll, id)
		if m.navCursor >= len(m.navItems) {
			m.navCursor = len(m.navItems) - 1
		}
		m.statusMsg = "✓ deleted"
		m.pendingConfirm = nil
		m.pendingConfirmMsg = ""
		m.focus = returnFocus
		contentCmd := m.triggerContentLoad()
		if svc == nil {
			return contentCmd
		}
		return tea.Batch(contentCmd, func() tea.Msg {
			_ = svc.DeleteArticle(context.Background(), id)
			return nil
		})
	})
	return nil
}

// cmdTags shows all tags from navItemsAll in the status area.
func (m *Model) cmdTags() tea.Cmd {
	seen := map[string]bool{}
	var tags []string
	for _, item := range m.navItemsAll {
		for _, tag := range item.tags {
			if !seen[tag] {
				seen[tag] = true
				tags = append(tags, tag)
			}
		}
	}
	if len(tags) == 0 {
		m.statusMsg = "(no tags found)"
		return nil
	}
	lines := make([]string, 0, len(tags)+1)
	lines = append(lines, fmt.Sprintf("tags (%d):", len(tags)))
	for _, t := range tags {
		lines = append(lines, "  "+t)
	}
	m.setStatusLines(lines)
	return nil
}

// cmdCollections shows all collections in the status area.
func (m *Model) cmdCollections() tea.Cmd {
	if m.svc == nil {
		m.statusMsg = "✗ service unavailable"
		return nil
	}
	svc := m.svc
	return func() tea.Msg {
		cols, err := svc.ListCollections(context.Background())
		if err != nil {
			return cmdDoneMsg{err: err.Error()}
		}
		if len(cols) == 0 {
			return cmdDoneMsg{statusMsg: "(no collections)"}
		}
		lines := make([]string, 0, len(cols)+1)
		lines = append(lines, fmt.Sprintf("collections (%d):", len(cols)))
		for _, c := range cols {
			line := "  " + c.Slug
			if c.Description != "" {
				line += "  — " + c.Description
			}
			lines = append(lines, line)
		}
		return cmdDoneMsg{statusLines: lines}
	}
}

// filterByCollection filters nav pane to articles in the given collection.
func (m *Model) filterByCollection(name string) tea.Cmd {
	if m.svc == nil {
		m.statusMsg = "✗ service unavailable"
		return nil
	}
	svc := m.svc
	all := m.navItemsAll
	return func() tea.Msg {
		slugs, err := svc.ListCollectionArticles(context.Background(), name)
		if err != nil {
			return cmdDoneMsg{err: fmt.Sprintf("collection %q: %v", name, err)}
		}
		slugSet := map[string]bool{}
		for _, s := range slugs {
			slugSet[s] = true
		}
		var filtered []navItem
		for _, item := range all {
			if slugSet[item.id] {
				filtered = append(filtered, item)
			}
		}
		if len(filtered) == 0 {
			return cmdDoneMsg{err: fmt.Sprintf("collection %q: no articles found", name)}
		}
		return cmdDoneMsg{
			navItems:  filtered,
			navFilter: fmt.Sprintf("collection: %s · %d articles  ·  /clear to reset", name, len(filtered)),
		}
	}
}

// parseSearchArg splits a /search arg string into query and optional --limit value.
// e.g. "go concurrency --limit 50" → ("go concurrency", 50)
func parseSearchArg(arg string) (query string, limit int) {
	const flag = "--limit"
	if idx := strings.Index(arg, flag); idx != -1 {
		rest := strings.TrimSpace(arg[idx+len(flag):])
		before := strings.TrimSpace(arg[:idx])
		var n int
		if _, err := fmt.Sscanf(rest, "%d", &n); err == nil && n > 0 {
			return before, n
		}
	}
	return arg, 0
}

// cmdFTSSearch runs a full-text search via the FTS5 index and replaces nav with results.
// limit=0 uses the service default (20). slugs optionally restricts results to a set of article slugs.
func cmdFTSSearch(svc *service.Service, query string, limit int, slugs []string) tea.Cmd {
	return func() tea.Msg {
		results, err := svc.Search(context.Background(), service.SearchRequest{Query: query, Limit: limit, Slugs: slugs})
		if err != nil {
			return cmdDoneMsg{err: fmt.Sprintf("search: %v", err)}
		}
		if len(results) == 0 {
			return cmdDoneMsg{
				statusMsg: fmt.Sprintf("no results for %q", query),
				navItems:  []navItem{},
				navFilter: fmt.Sprintf("search: %q · 0 results  ·  /clear to reset", query),
			}
		}
		items := make([]navItem, len(results))
		for i, r := range results {
			a := r.Article
			tags := make([]string, len(a.Tags))
			for j, t := range a.Tags {
				tags[j] = t.Value
			}
			summaryLabel := ""
			if a.SummaryStyle != "" && a.SummaryModel != "" {
				summaryLabel = a.SummaryStyle + "/" + a.SummaryModel
			}
			items[i] = navItem{
				id:           a.ID,
				numID:        a.NumID,
				title:        a.Title,
				date:         a.IngestedAt,
				read:         a.ReadAt != nil,
				favorite:     a.FavoritedAt != nil,
				root:         a.Files.Root,
				url:          a.URL,
				tags:         tags,
				collections:  a.Collections,
				sourceType:   a.SourceType,
				author:       a.Author,
				publishedAt:  a.PublishedAt,
				feed:         a.Feed,
				agentReason:  a.AgentReason,
				qualityScore: a.QualityScore,
				summary:      summaryLabel,
				flashModel:   a.FlashModel,
			}
		}
		return cmdDoneMsg{
			navItems:  items,
			navFilter: fmt.Sprintf("search: %q · %d results  ·  /clear to reset", query, len(items)),
		}
	}
}

// cmdCollectionSearch runs an FTS5 search on collections via the service layer.
func cmdCollectionSearch(svc *service.Service, query string) tea.Cmd {
	return func() tea.Msg {
		results, err := svc.SearchCollections(context.Background(), query)
		if err != nil {
			return collectionSearchMsg{err: fmt.Sprintf("search collections: %v", err)}
		}
		return collectionSearchMsg{results: results, query: query}
	}
}

// cmdConfigLines returns formatted lines showing the resolved configuration,
// following c2's /config pattern: key settings + full profile listing.
func (m *Model) cmdConfigLines() []string {
	home, _ := os.UserHomeDir()
	cfgPath := resolveConfigPath(filepath.Join(home, ".arc", "config.jsonc"))

	row := func(label, value string) string {
		return fmt.Sprintf("  %-20s%s", label+":", value)
	}
	orNone := func(s string) string {
		if s == "" {
			return "(none)"
		}
		return s
	}

	ttsRate := m.cfg.TTSRate
	if ttsRate == 0 {
		ttsRate = 200
	}

	lines := []string{
		row("config file", cfgPath),
		row("data root", m.cfg.DataRoot),
		row("articles root", m.cfg.ArticlesRoot),
		row("db path", m.cfg.DBPath),
		row("tts voice", orNone(m.cfg.TTSVoice)),
		row("tts rate", fmt.Sprintf("%d wpm", ttsRate)),
		row("correction", orNone(m.cfg.CorrectionProfile)),
		row("askx profile", orNone(m.cfg.AskX.Profile)),
		row("preferred models", orNone(strings.Join(m.cfg.PreferredModels, ", "))),
		row("preferred styles", orNone(strings.Join(m.cfg.PreferredStyles, ", "))),
		row("log level", orNone(m.cfg.LogLevel)),
	}

	// Ingest assignments.
	lines = append(lines, "",
		"  Ingest assignments:",
		fmt.Sprintf("    summary: %s (%s)  ·  flash: %s  ·  flashcard: %s (%s)  ·  embed: %s",
			m.cfg.Ingest.SummaryProfile, orNone(m.cfg.Ingest.SummaryStyle),
			m.cfg.Ingest.FlashProfile,
			m.cfg.Ingest.FlashcardProfile, orNone(m.cfg.Ingest.FlashcardStyle),
			m.cfg.Ingest.EmbedProfile),
	)

	// Profile listing — mirrors c2's approach.
	if len(m.cfg.Profiles) > 0 {
		lines = append(lines, "", fmt.Sprintf("  Profiles (%d):", len(m.cfg.Profiles)))

		names := make([]string, 0, len(m.cfg.Profiles))
		for code := range m.cfg.Profiles {
			names = append(names, code)
		}
		sort.Strings(names)

		// Build set of active profile names for markers.
		active := map[string][]string{}
		if v := m.cfg.Ingest.SummaryProfile; v != "" {
			active[v] = append(active[v], "summary")
		}
		if v := m.cfg.Ingest.FlashProfile; v != "" {
			active[v] = append(active[v], "flash")
		}
		if v := m.cfg.Ingest.FlashcardProfile; v != "" {
			active[v] = append(active[v], "flashcard")
		}
		if v := m.cfg.Ingest.EmbedProfile; v != "" {
			active[v] = append(active[v], "embed")
		}
		if v := m.cfg.AskX.Profile; v != "" {
			active[v] = append(active[v], "askx")
		}
		if v := m.cfg.CorrectionProfile; v != "" {
			active[v] = append(active[v], "correction")
		}

		for _, code := range names {
			p := m.cfg.Profiles[code]
			parts := []string{p.Provider, p.Model}
			if p.Info.Pricing != nil {
				parts = append(parts, fmt.Sprintf("$%.2f/$%.2f", p.Info.Pricing.Input, p.Info.Pricing.Output))
			}
			if p.Info.CostTier != "" {
				parts = append(parts, p.Info.CostTier)
			}
			marker := ""
			if roles, ok := active[code]; ok {
				marker = " ← " + strings.Join(roles, ", ")
			}
			lines = append(lines, fmt.Sprintf("    %-16s%s%s", code, strings.Join(parts, ", "), marker))
		}
	}

	return lines
}

// cmdViewConfigFile reads a config file and opens it in the resource overlay.
func (m *Model) cmdViewConfigFile(path, label string) {
	resolved := resolveConfigPath(path)
	data, err := os.ReadFile(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			m.setStatusError("config file not found: " + resolved)
		} else {
			m.setStatusError("read config: " + err.Error())
		}
		return
	}
	m.openResourceOverlay(label, string(data))
}

// cmdConfigView opens the global config in the resource overlay.
func (m *Model) cmdConfigView() {
	home, _ := os.UserHomeDir()
	m.cmdViewConfigFile(filepath.Join(home, ".arc", "config.jsonc"), "config.jsonc")
}

// cmdAgentConfigView opens the agent config in the resource overlay.
func (m *Model) cmdAgentConfigView() {
	m.cmdViewConfigFile(filepath.Join(m.cfg.AgentPath, "config.jsonc"), "agent/config.jsonc")
}

// cmdChatConfigView opens the workspace chat config in the resource overlay.
func (m *Model) cmdChatConfigView() {
	if !m.chatMode {
		m.statusMsg = "✗ /config-chat-view is only available in workspace chat"
		return
	}
	path := filepath.Join(m.cfg.DataRoot, "workspaces", m.chatWorkspace, "chat", "config.jsonc")
	m.cmdViewConfigFile(path, m.chatWorkspace+"/chat/config.jsonc")
}

// editorOrError returns the $EDITOR value or sets a status error and returns "".
func (m *Model) editorOrError() string {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		m.setStatusError("$EDITOR is not set — add 'export EDITOR=<path>' to your shell config")
	}
	return editor
}

// resolveConfigPath returns path if it exists, otherwise tries the .jsonc/.json sibling.
func resolveConfigPath(base string) string {
	if _, err := os.Stat(base); err == nil {
		return base
	}
	// Try the other extension.
	ext := filepath.Ext(base)
	var alt string
	if ext == ".json" {
		alt = base[:len(base)-5] + ".jsonc"
	} else if ext == ".jsonc" {
		alt = base[:len(base)-6] + ".json"
	}
	if alt != "" {
		if _, err := os.Stat(alt); err == nil {
			return alt
		}
	}
	return base // return original even if missing — editor will create it
}

// cmdConfigEdit opens the arc config file in $EDITOR.
func (m *Model) cmdConfigEdit() tea.Cmd {
	editor := m.editorOrError()
	if editor == "" {
		return nil
	}
	home, _ := os.UserHomeDir()
	cfgPath := resolveConfigPath(filepath.Join(home, ".arc", "config.jsonc"))
	m.openEditorInTerminal(editor, cfgPath, filepath.Base(cfgPath))
	return nil
}

// cmdAgentConfigEdit opens the agent config file in $EDITOR.
func (m *Model) cmdAgentConfigEdit() tea.Cmd {
	editor := m.editorOrError()
	if editor == "" {
		return nil
	}
	cfgPath := resolveConfigPath(filepath.Join(m.cfg.AgentPath, "config.jsonc"))
	m.openEditorInTerminal(editor, cfgPath, filepath.Base(cfgPath))
	return nil
}

// cmdChatConfigEdit opens the workspace chat config file in $EDITOR.
func (m *Model) cmdChatConfigEdit() tea.Cmd {
	if !m.chatMode {
		m.statusMsg = "✗ /config-chat-edit is only available in workspace chat"
		return nil
	}
	editor := m.editorOrError()
	if editor == "" {
		return nil
	}
	cfgPath := resolveConfigPath(filepath.Join(m.cfg.DataRoot, "workspaces", m.chatWorkspace, "chat", "config.jsonc"))
	m.openEditorInTerminal(editor, cfgPath, m.chatWorkspace+"/chat/"+filepath.Base(cfgPath))
	return nil
}

// cmdStats shows library stats in the status area.
func (m *Model) cmdStats() tea.Cmd {
	if !m.statsLoaded {
		m.statusMsg = "stats not loaded yet"
		return nil
	}
	s := m.stats
	lines := []string{
		fmt.Sprintf("articles: %d  ·  unread: %d  ·  collections: %d", s.TotalArticles, s.Unread, s.TotalCollections),
		fmt.Sprintf("cost: today %s  ·  7d %s  ·  30d %s  ·  total %s",
			formatUSD(s.CostToday), formatUSD(s.CostThisWeek), formatUSD(s.CostThisMonth), formatUSD(s.CostTotal)),
	}
	// Per-model spend, sorted descending, skipping zero.
	type modelCost struct {
		model string
		usd   float64
	}
	var mc []modelCost
	for model, usd := range s.CostByModel {
		if usd > 0 {
			mc = append(mc, modelCost{model, usd})
		}
	}
	sort.Slice(mc, func(i, j int) bool { return mc[i].usd > mc[j].usd })
	for _, entry := range mc {
		lines = append(lines, fmt.Sprintf("  %-40s %s", entry.model, formatUSD(entry.usd)))
	}
	m.setStatusLines(lines)
	return nil
}

// cmdModelsLines returns formatted lines listing all LLM profiles sorted by cost tier.
func (m *Model) cmdModelsLines() []string {
	tierOrder := map[string]int{
		"local": 0, "very_low": 1, "low": 2,
		"medium": 3, "high": 4, "premium": 5,
	}

	type namedProfile struct {
		name string
		p    config.Profile
	}
	sorted := make([]namedProfile, 0, len(m.cfg.Profiles))
	for name, p := range m.cfg.Profiles {
		sorted = append(sorted, namedProfile{name, p})
	}
	sort.Slice(sorted, func(i, j int) bool {
		ti := tierOrder[sorted[i].p.Info.CostTier]
		tj := tierOrder[sorted[j].p.Info.CostTier]
		if ti != tj {
			return ti < tj
		}
		return sorted[i].name < sorted[j].name
	})

	var lines []string

	// Active assignments header.
	lines = append(lines,
		"Active profiles:",
		fmt.Sprintf("  summary: %s  ·  flash: %s  ·  flashcard: %s  ·  embed: %s",
			m.cfg.Ingest.SummaryProfile, m.cfg.Ingest.FlashProfile,
			m.cfg.Ingest.FlashcardProfile, m.cfg.Ingest.EmbedProfile),
		"",
	)

	for _, np := range sorted {
		p := np.p

		// Mark active steps.
		active := ""
		if m.cfg.Ingest.SummaryProfile == np.name {
			active += " summary"
		}
		if m.cfg.Ingest.FlashProfile == np.name {
			active += " flash"
		}
		if m.cfg.Ingest.FlashcardProfile == np.name {
			active += " flashcard"
		}
		if m.cfg.Ingest.EmbedProfile == np.name {
			active += " embed"
		}
		if active != "" {
			active = "  ←" + active
		}

		pricing := "free (local)"
		if p.Info.Pricing != nil {
			pricing = fmt.Sprintf("$%.2f/$%.2f per 1M tok", p.Info.Pricing.Input, p.Info.Pricing.Output)
		}

		line := fmt.Sprintf("%-12s  %-10s  %-36s  %-8s  %s%s",
			np.name, p.Provider, p.Model, "["+p.Info.CostTier+"]", pricing, active)
		lines = append(lines, line)
	}

	return lines
}

// cmdLog opens or closes a tail of the arc log file in a new terminal window.
// Calling it a second time writes a sentinel file that signals the tail script to exit.
func (m *Model) cmdLog() tea.Cmd {
	pid := os.Getpid()
	sentinelPath := fmt.Sprintf("%s/arc-log-stop-%d", os.TempDir(), pid)

	if m.logViewerOpen {
		_ = os.WriteFile(sentinelPath, nil, 0o644)
		m.logViewerOpen = false
		m.statusMsg = "log viewer closed"
		return nil
	}

	logPath := m.cfg.LogPath
	scriptPath := fmt.Sprintf("%s/arc-log-viewer-%d.sh", os.TempDir(), pid)

	script := fmt.Sprintf(
		"#!/bin/bash\ntrap 'rm -f %q %q' EXIT\ntail -n 200 -f %q & __t=$!\nwhile kill -0 %d 2>/dev/null && [ ! -f %q ]; do sleep 1; done\nkill $__t 2>/dev/null\n",
		scriptPath, sentinelPath, logPath, pid, sentinelPath,
	)
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		m.statusMsg = fmt.Sprintf("✗ /log: could not write script: %v", err)
		return nil
	}

	var appleScript string
	switch ActiveTerminal {
	case TermITerm2:
		appleScript = fmt.Sprintf(
			`tell application "iTerm2" to create window with default profile command %q`,
			scriptPath,
		)
	default:
		appleScript = fmt.Sprintf(
			`tell application "Terminal" to do script %q`,
			scriptPath,
		)
	}
	go exec.Command("osascript", "-e", appleScript).Run() //nolint:errcheck
	m.logViewerOpen = true
	m.statusMsg = "log viewer opened — /log again to close"
	return nil
}

// cmdViewArticle opens the selected article's flash/summary/body in an external
// terminal window using less. The viewer auto-exits when arc exits (PID poll).
func (m *Model) cmdViewArticle() tea.Cmd {
	item := m.selectedNavItem()
	if item == nil {
		m.statusMsg = "✗ no article selected"
		return nil
	}
	if item.root == "" {
		m.statusMsg = "✗ article has no local files"
		return nil
	}

	// Resolve file paths.
	files := storefs.ProbeFiles(item.root)
	files.Summary = storefs.ResolveSummary(item.root, m.cfg.PreferredStyles, m.cfg.PreferredModels)
	files.Flash = storefs.ResolveFlash(item.root, m.cfg.PreferredModels)

	// Collect files in display order: Flash → Summary → Body.
	type viewPart struct {
		label string
		path  string
	}
	var parts []viewPart
	if files.Flash != "" {
		parts = append(parts, viewPart{"Flash", files.Flash})
	}
	if files.Summary != "" {
		parts = append(parts, viewPart{"Summary", files.Summary})
	}
	if files.Body != "" {
		parts = append(parts, viewPart{"Body", files.Body})
	}
	if len(parts) == 0 {
		m.statusMsg = "✗ no content files available"
		return nil
	}

	pid := os.Getpid()
	scriptPath := fmt.Sprintf("%s/arc-view-%d-%s.sh", os.TempDir(), pid, item.id)

	// Build a script that concatenates files with labeled dividers, pipes to less,
	// and exits when the parent arc process dies.
	var catParts string
	for i, p := range parts {
		if i > 0 {
			catParts += "echo ''; "
		}
		// ── Label ────────────────────────────────
		pad := 60 - 4 - len(p.label) - 1 // 4 = "── ", 1 = " "
		if pad < 3 {
			pad = 3
		}
		catParts += fmt.Sprintf("echo '── %s %s'; echo ''; ", p.label, strings.Repeat("─", pad))
		catParts += fmt.Sprintf("cat %q; ", p.path)
	}

	script := fmt.Sprintf(
		"#!/bin/bash\ntrap 'rm -f %q' EXIT\n"+
			"# Background watcher: exit when parent arc process dies.\n"+
			"(while kill -0 %d 2>/dev/null; do sleep 1; done; kill $$ 2>/dev/null) &\n"+
			"{ %s }\necho ''\nread -n1 -s -r -p '(press any key to close)'\n",
		scriptPath, pid, catParts,
	)
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		m.statusMsg = fmt.Sprintf("✗ view: could not write script: %v", err)
		return nil
	}

	var appleScript string
	switch ActiveTerminal {
	case TermITerm2:
		appleScript = fmt.Sprintf(
			`tell application "iTerm2" to create window with default profile command %q`,
			scriptPath,
		)
	default:
		appleScript = fmt.Sprintf(
			`tell application "Terminal" to do script %q`,
			scriptPath,
		)
	}
	go exec.Command("osascript", "-e", appleScript).Run() //nolint:errcheck
	m.statusMsg = "opened viewer for " + item.id
	return nil
}

// cmdReprocess regenerates summary/flash for the current article.
func (m *Model) cmdReprocess() tea.Cmd {
	sel := m.selectedNavItem()
	if sel == nil {
		m.statusMsg = "✗ no article selected"
		return nil
	}
	if m.svc == nil {
		m.statusMsg = "✗ service unavailable"
		return nil
	}
	item := *sel
	svc := m.svc
	cfg := m.cfg
	m.statusMsg = "⠸ reprocessing " + item.id + "…"
	return func() tea.Msg {
		req := service.ReprocessRequest{
			Slug: item.id,
		}
		_ = cfg
		_, err := svc.Reprocess(context.Background(), req)
		if err != nil {
			return cmdDoneMsg{err: err.Error()}
		}
		return cmdDoneMsg{statusMsg: "✓ reprocessed " + item.id, reloadNav: false}
	}
}

// cmdIngest ingests a new article from a URL.
// arg is "<url> [--profile <name>] [--style <name>]".
func (m *Model) cmdIngest(arg string) tea.Cmd {
	if m.svc == nil {
		m.statusMsg = "✗ service unavailable"
		return nil
	}
	url, profile, style := parseIngestFlags(arg)
	if url == "" {
		m.statusMsg = "usage: /ingest <url> [--profile <name>] [--style <name>]"
		return nil
	}
	svc := m.svc
	send := *m.programSend
	ctx, cancel := context.WithCancel(context.Background())
	m.ingestCancelFn = cancel
	m.ingestRunning = true
	m.ingestLabel = "fetching…"
	m.statusMsg = ""
	return func() tea.Msg {
		start := time.Now()
		req := service.IngestRequest{
			URL:             url,
			SummaryProfile:  profile,
			FlashProfile:    profile,
			FlashcardProfile: profile,
			SummaryStyle:    style,
			Progress: func(step string) {
				send(statusUpdateMsg{text: step})
			},
			OnCostEstimate: func(nChunks int, usd float64) {
				send(ingestCostEstimateMsg{nChunks: nChunks, usd: usd})
			},
		}
		result, err := svc.Ingest(ctx, req)
		if err != nil {
			return cmdDoneMsg{err: err.Error()}
		}
		elapsed := time.Since(start).Round(time.Second)
		cost := result.Cost.TotalUSD
		msg := fmt.Sprintf("✓ %s  %s", result.Slug, elapsed)
		if cost > 0 {
			msg += fmt.Sprintf("  $%.4f", cost)
		}
		return cmdDoneMsg{
			statusMsg: msg,
			reloadNav: true,
		}
	}
}

// ── Agent commands ───────────────────────────────────────────────────────────

// cmdAgentRun prepares a fresh agent feed-scan confirmation block.
// arg may contain --dry-run and/or --focus "...".
func (m *Model) cmdAgentRun(arg string) tea.Cmd {
	slog.Debug("/agent-run invoked", "arg", arg)
	if m.agentRunning {
		slog.Debug("/agent-run rejected: already running")
		m.statusMsg = "✗ agent run already in progress"
		return nil
	}
	dryRun, focus := parseAgentRunFlags(arg)

	agentCfgPath := filepath.Join(m.cfg.AgentPath, "config.json")
	agentCfg, err := agentpkg.LoadAgentConfig(agentCfgPath)
	if err != nil {
		m.statusMsg = "✗ could not load agent config: " + err.Error()
		return nil
	}
	activeFeeds := 0
	for _, f := range agentCfg.Feeds {
		if !f.Disabled {
			activeFeeds++
		}
	}
	if activeFeeds == 0 {
		m.statusMsg = "✗ no feeds configured — add feeds to ~/.arc/agent/config.json"
		return nil
	}

	focusStr := "(none)"
	if focus != "" {
		focusStr = focus
	} else if agentCfg.Focus != "" {
		focusStr = agentCfg.Focus
	}
	dryStr := "no"
	if dryRun {
		dryStr = "yes"
	}
	filterProfile := agentCfg.FilterProfileName()
	summaryProfile := agentCfg.SummaryProfileName()

	// Create context now so cancel is stored in the model before confirmation.
	ctx, cancel := context.WithCancel(context.Background())
	m.agentRunCancelFn = cancel

	// Capture only non-model data for the closure — do NOT capture m.
	send := *m.programSend
	cfg := m.cfg
	svc := m.svc

	slog.Debug("/agent-run confirm ready",
		"active_feeds", activeFeeds, "filter", filterProfile,
		"ingest", summaryProfile, "focus", focusStr, "dry_run", dryRun)
	m.agentConfirmLines = []string{
		"  Agent run — poll all feeds",
		"",
		fmt.Sprintf("  %-12s %d active", "Feeds", activeFeeds),
		fmt.Sprintf("  %-12s %s", "Filter", filterProfile),
		fmt.Sprintf("  %-12s %s", "Ingest", summaryProfile),
		fmt.Sprintf("  %-12s %s", "Focus", focusStr),
		fmt.Sprintf("  %-12s %s", "Dry-run", dryStr),
		"",
		"  yes to confirm   Esc to cancel",
	}
	m.agentConfirmAction = func() tea.Cmd {
		return func() tea.Msg {
			slog.Info("agent run goroutine started",
				"feeds", activeFeeds, "dry_run", dryRun, "focus", focus)
			db := svc.Library().DB()
			opts := agentpkg.RunOptions{
				ArcConfig:    cfg,
				AgentCfg:     agentCfg,
				DB:           db,
				FeedStateDir: filepath.Join(cfg.AgentPath, "state"),
				RunsPath:     filepath.Join(cfg.AgentPath, "runs.jsonl"),
				DecisionsDir: cfg.AgentPath,
				DryRun:       dryRun,
				Focus:        focus,
				Status: func(slot int, txt string) {
					if slot == 0 {
						slog.Debug("agent run status", "slot", slot, "msg", txt)
						send(statusUpdateMsg{text: txt})
					}
				},
			}
			rec, err := agentpkg.RunFeeds(ctx, opts)
			if err != nil {
				slog.Error("agent run failed", "err", err)
				return agentRunDoneMsg{err: err.Error()}
			}
			slog.Info("agent run complete",
				"run_id", rec.RunID, "ingested", rec.TotalIngest,
				"maybe", rec.TotalMaybe, "skipped", rec.TotalSkip,
				"cost_usd", rec.TotalCostUSD)
			return agentRunDoneMsg{rec: rec, newRunID: rec.RunID}
		}
	}
	m.focus = paneCommand
	m.input.SetValue("")
	return nil
}

// cmdAgentRerun prepares a decisions-rerun confirmation block for the selected run.
func (m *Model) cmdAgentRerun(arg string) tea.Cmd {
	slog.Debug("/agent-rerun invoked", "arg", arg)
	if m.agentRunning {
		slog.Debug("/agent-rerun rejected: already running")
		m.statusMsg = "✗ agent run already in progress"
		return nil
	}
	dryRun, _ := parseAgentRunFlags(arg)
	// Reject any non-flag tokens.
	for _, tok := range strings.Fields(arg) {
		if tok != "--dry-run" {
			m.statusMsg = "✗ unknown argument: " + tok + "  usage: /agent-rerun [--dry-run]"
			return nil
		}
	}
	if m.agentRunsCursor < 0 || m.agentRunsCursor >= len(m.agentRuns) {
		m.statusMsg = "✗ no run selected — select a run in the Agent nav pane"
		return nil
	}
	if len(m.agentRunDecisions.Feeds) == 0 {
		m.statusMsg = "✗ no decisions file for this run"
		return nil
	}
	// Count queued items.
	queued := 0
	for _, df := range m.agentRunDecisions.Feeds {
		for _, item := range df.Items {
			if item.Action == "+" && item.Status != "done" {
				queued++
			}
		}
	}
	if queued == 0 {
		m.statusMsg = "✗ no items queued — use a/s keys to mark items for ingest"
		return nil
	}
	if m.svc == nil {
		m.statusMsg = "✗ service unavailable"
		return nil
	}

	rec := m.agentRuns[m.agentRunsCursor]
	dryStr := "no"
	if dryRun {
		dryStr = "yes"
	}
	decisionsPath := filepath.Join(m.cfg.AgentPath, "decisions-"+rec.RunID+".json")

	agentCfgPath := filepath.Join(m.cfg.AgentPath, "config.json")
	agentCfg, err := agentpkg.LoadAgentConfig(agentCfgPath)
	if err != nil {
		m.statusMsg = "✗ could not load agent config: " + err.Error()
		return nil
	}

	// Create context now so cancel is stored in the model before confirmation.
	ctx, cancel := context.WithCancel(context.Background())
	m.agentRunCancelFn = cancel

	// Capture only non-model data for the closure — do NOT capture m.
	send := *m.programSend
	cfg := m.cfg
	svc := m.svc

	slog.Debug("/agent-rerun confirm ready",
		"run_id", rec.RunID, "queued", queued,
		"decisions_path", decisionsPath, "dry_run", dryRun)
	m.agentConfirmLines = []string{
		fmt.Sprintf("  Agent rerun — decisions from %s", rec.RunID),
		"",
		fmt.Sprintf("  %-12s %d items marked for ingest", "Queued", queued),
		fmt.Sprintf("  %-12s %s", "Run date", rec.StartedAt.Local().Format("2006-01-02 15:04")),
		fmt.Sprintf("  %-12s %s", "Dry-run", dryStr),
		"",
		"  yes to confirm   Esc to cancel",
	}
	m.agentConfirmAction = func() tea.Cmd {
		return func() tea.Msg {
			slog.Info("agent rerun goroutine started",
				"decisions_path", decisionsPath, "queued", queued, "dry_run", dryRun)
			db := svc.Library().DB()
			opts := agentpkg.RunOptions{
				ArcConfig: cfg,
				AgentCfg:  agentCfg,
				DB:        db,
				RunsPath:  filepath.Join(cfg.AgentPath, "runs.jsonl"),
				DryRun:    dryRun,
				Status: func(slot int, txt string) {
					if slot == 0 {
						slog.Debug("agent rerun status", "slot", slot, "msg", txt)
						send(statusUpdateMsg{text: txt})
					}
				},
			}
			rec, err := agentpkg.RunDecisions(ctx, opts, decisionsPath)
			if err != nil {
				slog.Error("agent rerun failed", "err", err)
				return agentRunDoneMsg{err: err.Error(), isRerun: true}
			}
			slog.Info("agent rerun complete",
				"run_id", rec.RunID, "ingested", rec.TotalIngest, "cost_usd", rec.TotalCostUSD)
			return agentRunDoneMsg{rec: rec, isRerun: true}
		}
	}
	m.focus = paneCommand
	m.input.SetValue("")
	return nil
}

// parseIngestFlags parses "<url> [--profile <name>] [--style <name>]".
// The URL is the first non-flag token. Flags may appear before or after the URL.
func parseIngestFlags(arg string) (url, profile, style string) {
	parts := strings.Fields(arg)
	for i := 0; i < len(parts); i++ {
		switch parts[i] {
		case "--profile":
			if i+1 < len(parts) {
				i++
				profile = parts[i]
			}
		case "--style":
			if i+1 < len(parts) {
				i++
				style = parts[i]
			}
		default:
			if url == "" {
				url = parts[i]
			}
		}
	}
	return
}

// parseAgentRunFlags parses --dry-run and --focus "..." from a command arg string.
func parseAgentRunFlags(arg string) (dryRun bool, focus string) {
	parts := strings.Fields(arg)
	for i := 0; i < len(parts); i++ {
		switch parts[i] {
		case "--dry-run":
			dryRun = true
		case "--focus":
			if i+1 < len(parts) {
				i++
				focus = strings.Join(parts[i:], " ")
				focus = strings.Trim(focus, "\"")
				break
			}
		}
	}
	return
}

// ── Collection commands ──────────────────────────────────────────────────────

// cmdCollectionReload re-fetches the full collections list from disk.
func (m *Model) cmdCollectionReload() tea.Cmd {
	if m.svc == nil {
		m.statusMsg = "✗ service unavailable"
		return nil
	}
	m.collectionsLoaded = false
	m.statusMsg = "reloading collections…"
	return loadCollectionsTree(m.svc)
}

// cmdDeleteCollection deletes the selected collection after confirmation.
func (m *Model) cmdDeleteCollection() tea.Cmd {
	col := m.selectedCollection()
	if col == nil {
		m.statusMsg = "✗ no collection selected — cursor must be on a collection header"
		return nil
	}
	if m.svc == nil {
		m.statusMsg = "✗ service unavailable"
		return nil
	}
	slug := col.colSlug
	svc := m.svc
	m.askConfirm(fmt.Sprintf("delete collection %q? (yes/N)", slug), func() tea.Cmd {
		return func() tea.Msg {
			_, err := svc.DeleteCollection(context.Background(), slug, false)
			if err != nil {
				return cmdDoneMsg{err: err.Error()}
			}
			return cmdDoneMsg{statusMsg: "✓ deleted collection " + slug, reloadCollections: true}
		}
	})
	return nil
}

// cmdDeleteArticleBySlug deletes an article by slug (from /delete <slug>).
func (m *Model) cmdDeleteArticleBySlug(slug string) tea.Cmd {
	if m.svc == nil {
		m.statusMsg = "✗ service unavailable"
		return nil
	}
	svc := m.svc
	m.askConfirm(fmt.Sprintf("delete article %q? (yes/N)", slug), func() tea.Cmd {
		m.navItems = removeNavItem(m.navItems, slug)
		m.navItemsAll = removeNavItem(m.navItemsAll, slug)
		if m.navCursor >= len(m.navItems) {
			m.navCursor = len(m.navItems) - 1
		}
		if m.navCursor < 0 {
			m.navCursor = 0
		}
		m.clampNavScroll()
		contentCmd := m.triggerContentLoad()
		return tea.Batch(contentCmd, func() tea.Msg {
			_ = svc.DeleteArticle(context.Background(), slug)
			return nil
		})
	})
	return nil
}

// cmdDeleteCollectionByName deletes a collection by slug (from /delete <slug>).
func (m *Model) cmdDeleteCollectionByName(slug string) tea.Cmd {
	if m.svc == nil {
		m.statusMsg = "✗ service unavailable"
		return nil
	}
	svc := m.svc
	m.askConfirm(fmt.Sprintf("delete collection %q? (yes/N)", slug), func() tea.Cmd {
		return func() tea.Msg {
			_, err := svc.DeleteCollection(context.Background(), slug, false)
			if err != nil {
				return cmdDoneMsg{err: err.Error()}
			}
			return cmdDoneMsg{statusMsg: "✓ deleted collection " + slug, reloadCollections: true}
		}
	})
	return nil
}

// cmdDeleteWorkspaceByName deletes a workspace by name (from /delete <name>).
func (m *Model) cmdDeleteWorkspaceByName(name string) tea.Cmd {
	if m.svc == nil {
		m.statusMsg = "✗ service unavailable"
		return nil
	}
	svc := m.svc
	m.askConfirm(fmt.Sprintf("delete workspace %q? (yes/N)", name), func() tea.Cmd {
		return func() tea.Msg {
			if err := svc.DeleteWorkspace(context.Background(), name); err != nil {
				return cmdDoneMsg{err: err.Error()}
			}
			return cmdDoneMsg{statusMsg: "✓ deleted workspace " + name, reloadWorkspaces: true}
		}
	})
	return nil
}

// ── Workspace commands ───────────────────────────────────────────────────────

// selectedWorkspace returns the workspaceItem under the cursor, or nil.
func (m *Model) selectedWorkspace() *workspaceItem {
	if m.wsCursor < 0 || m.wsCursor >= len(m.wsRows) {
		return nil
	}
	idx := m.wsRows[m.wsCursor].wsIdx
	if idx < 0 || idx >= len(m.workspaceItems) {
		return nil
	}
	return &m.workspaceItems[idx]
}

// cmdNewWorkspace creates a new workspace.
func (m *Model) cmdNewWorkspace(arg string) tea.Cmd {
	if m.svc == nil {
		m.statusMsg = "✗ service unavailable"
		return nil
	}
	// Parse: /new <name> [description]
	parts := strings.SplitN(arg, " ", 2)
	name := parts[0]
	description := ""
	if len(parts) == 2 {
		description = strings.TrimSpace(parts[1])
	}
	svc := m.svc
	m.statusMsg = "⠸ creating workspace " + name + "…"
	return func() tea.Msg {
		if err := svc.CreateWorkspace(context.Background(), name, description); err != nil {
			return cmdDoneMsg{err: err.Error()}
		}
		msg := "✓ created workspace " + name
		if description != "" {
			msg += " — " + description
		}
		return cmdDoneMsg{statusMsg: msg, reloadWorkspaces: true}
	}
}

// cmdDeleteWorkspace deletes the selected workspace after confirmation.
func (m *Model) cmdDeleteWorkspace() tea.Cmd {
	ws := m.selectedWorkspace()
	if ws == nil {
		m.statusMsg = "✗ no workspace selected"
		return nil
	}
	if m.svc == nil {
		m.statusMsg = "✗ service unavailable"
		return nil
	}
	name := ws.name
	svc := m.svc
	m.askConfirm(fmt.Sprintf("delete workspace %q? (yes/N)", name), func() tea.Cmd {
		return func() tea.Msg {
			if err := svc.DeleteWorkspace(context.Background(), name); err != nil {
				return cmdDoneMsg{err: err.Error()}
			}
			return cmdDoneMsg{statusMsg: "✓ deleted workspace " + name, reloadWorkspaces: true}
		}
	})
	return nil
}

// cmdRenameWorkspace renames the selected workspace.
func (m *Model) cmdRenameWorkspace(newName string) tea.Cmd {
	ws := m.selectedWorkspace()
	if ws == nil {
		m.statusMsg = "✗ no workspace selected"
		return nil
	}
	if m.svc == nil {
		m.statusMsg = "✗ service unavailable"
		return nil
	}
	oldName := ws.name
	svc := m.svc
	m.statusMsg = "⠸ renaming workspace…"
	return func() tea.Msg {
		if err := svc.RenameWorkspace(context.Background(), oldName, newName); err != nil {
			return cmdDoneMsg{err: err.Error()}
		}
		return cmdDoneMsg{statusMsg: fmt.Sprintf("✓ renamed %s → %s", oldName, newName), reloadWorkspaces: true}
	}
}

// cmdDescribeWorkspace sets the description of the selected workspace.
func (m *Model) cmdDescribeWorkspace(text string) tea.Cmd {
	ws := m.selectedWorkspace()
	if ws == nil {
		m.statusMsg = "✗ no workspace selected"
		return nil
	}
	if m.svc == nil {
		m.statusMsg = "✗ service unavailable"
		return nil
	}
	name := ws.name
	svc := m.svc
	return func() tea.Msg {
		if err := svc.SetWorkspaceDescription(context.Background(), name, text); err != nil {
			return cmdDoneMsg{err: err.Error()}
		}
		return cmdDoneMsg{statusMsg: "✓ description updated for " + name, reloadWorkspaces: true}
	}
}

// cmdWorkspaceMembership handles /workspace add|remove article|collection <slug>.
// On success it resets the chat engine for the affected workspace so the next
// message picks up the updated corpus. See local/CHAT_ARCHITECTURE.md.
func (m *Model) cmdWorkspaceMembership(verb, arg string) tea.Cmd {
	ws := m.selectedWorkspace()
	if ws == nil {
		m.statusMsg = "✗ no workspace selected"
		return nil
	}
	if m.svc == nil {
		m.statusMsg = "✗ service unavailable"
		return nil
	}
	parts := strings.Fields(arg)
	if len(parts) < 2 {
		m.statusMsg = "usage: /workspace " + verb + " article|collection <slug>"
		return nil
	}
	kind := strings.ToLower(parts[0])
	slug := parts[1]
	if kind != "article" && kind != "collection" {
		m.statusMsg = "✗ specify 'article' or 'collection'"
		return nil
	}

	wsName := ws.name
	svc := m.svc
	adding := verb == "add"

	return func() tea.Msg {
		var err error
		var statusMsg string
		switch {
		case kind == "article" && adding:
			err = svc.AddArticlesToWorkspace(context.Background(), wsName, []string{slug})
			statusMsg = "✓ added article " + slug + " → " + wsName
		case kind == "article" && !adding:
			err = svc.RemoveArticlesFromWorkspace(context.Background(), wsName, []string{slug})
			statusMsg = "✓ removed article " + slug + " from " + wsName
		case kind == "collection" && adding:
			err = svc.AddCollectionsToWorkspace(context.Background(), wsName, []string{slug})
			statusMsg = "✓ added collection " + slug + " → " + wsName
		case kind == "collection" && !adding:
			err = svc.RemoveCollectionsFromWorkspace(context.Background(), wsName, []string{slug})
			statusMsg = "✓ removed collection " + slug + " from " + wsName
		}
		if err != nil {
			return cmdDoneMsg{err: err.Error()}
		}
		return cmdDoneMsg{
			statusMsg:          statusMsg,
			reloadWorkspaces:   true,
			resetChatEngine:    true,
			resetChatWorkspace: wsName,
		}
	}
}

// cmdWorkspaceReload drops the chat engine for the selected workspace so the
// next message triggers a fresh engine init (rebuilding the RAG context).
func (m *Model) cmdWorkspaceReload() tea.Cmd {
	ws := m.selectedWorkspace()
	if ws == nil {
		// In chat mode, fall back to the active chat workspace.
		if m.chatMode && m.chatWorkspace != "" {
			m.chatEngine = nil
			m.statusMsg = "✓ engine reset — will reinitialise on next message"
			return nil
		}
		m.statusMsg = "✗ no workspace selected"
		return nil
	}
	wsName := ws.name
	// Apply immediately if this is the active chat workspace.
	if m.chatMode && m.chatWorkspace == wsName {
		m.chatEngine = nil
	}
	m.statusMsg = "✓ engine reset for " + wsName + " — will reinitialise on next message"
	return nil
}

// cmdPopulateWorkspace runs LLM-assisted workspace population.
// Parses --hint and --include-collections from arg string.
func (m *Model) cmdPopulateWorkspace(arg string) tea.Cmd {
	// Resolve workspace name.
	var wsName string
	if m.chatMode && m.chatWorkspace != "" {
		wsName = m.chatWorkspace
	} else if ws := m.selectedWorkspace(); ws != nil {
		wsName = ws.name
	} else {
		m.statusMsg = "✗ no workspace selected"
		return nil
	}
	if m.svc == nil {
		m.statusMsg = "✗ service unavailable"
		return nil
	}

	// Parse arg: [workspace-name] [--hint "..."] [--include-collections] [--dry-run] [--edit]
	// First non-flag token is treated as workspace name override.
	var hint string
	var profile string
	var includeCols bool
	var dryRun bool
	var edit bool
	tokens := strings.Fields(arg)
	for i := 0; i < len(tokens); i++ {
		switch tokens[i] {
		case "--include-collections":
			includeCols = true
		case "--dry-run":
			dryRun = true
		case "--edit":
			edit = true
		case "--profile":
			if i+1 < len(tokens) {
				i++
				profile = tokens[i]
			}
		case "--hint":
			// Consume tokens until the next flag or end of input.
			var hintParts []string
			for i+1 < len(tokens) {
				i++
				if strings.HasPrefix(tokens[i], "--") {
					i-- // let the outer loop handle this flag
					break
				}
				hintParts = append(hintParts, tokens[i])
			}
			hint = strings.Trim(strings.Join(hintParts, " "), "\"'")
		default:
			// First non-flag token = workspace name (from completion).
			if !strings.HasPrefix(tokens[i], "--") {
				wsName = tokens[i]
			}
		}
	}

	svc := m.svc
	cfg := m.cfg
	m.populateRunning = true
	m.populateLabel = "populating " + wsName + "…"
	m.statusMsg = ""

	return func() tea.Msg {
		var progressLog []string
		progress := func(msg string) {
			progressLog = append(progressLog, msg)
		}

		result, err := svc.PopulateWorkspace(context.Background(), service.PopulateRequest{
			Workspace:          wsName,
			Profile:            profile,
			Hint:               hint,
			IncludeCollections: includeCols,
			Progress:           progress,
		})
		if err != nil {
			return cmdDoneMsg{err: err.Error()}
		}

		// Interactive edit: return items for one-by-one review in the input pane.
		if edit {
			var items []populateEditItem
			for _, c := range result.Collections {
				items = append(items, populateEditItem{
					slug:         c.Slug,
					display:      c.Display,
					articleCount: c.ArticleCount,
					isCollection: true,
				})
			}
			for _, a := range result.Articles {
				items = append(items, populateEditItem{
					slug:    a.Slug,
					display: a.Display,
				})
			}
			return populateEditMsg{
				workspace: wsName,
				items:     items,
				cost:      result.CostUSD,
				hint:      hint,
				log:       progressLog,
			}
		}

		// Build output lines for status pane (CLI-style).
		var lines []string
		if dryRun {
			lines = append(lines, fmt.Sprintf("populate dry-run for %s", wsName))
		} else {
			lines = append(lines, fmt.Sprintf("populate %s", wsName))
		}
		if hint != "" {
			lines = append(lines, fmt.Sprintf("hint: %s", hint))
		}
		lines = append(lines, "")
		for _, msg := range progressLog {
			lines = append(lines, "  "+msg)
		}
		if len(progressLog) > 0 {
			lines = append(lines, "")
		}

		if len(result.Collections) > 0 {
			lines = append(lines, "Collections:")
			for _, c := range result.Collections {
				if c.ArticleCount > 0 {
					lines = append(lines, fmt.Sprintf("  %s (%d articles)", c.Slug, c.ArticleCount))
				} else {
					lines = append(lines, fmt.Sprintf("  %s", c.Slug))
				}
				if c.Display != "" {
					lines = append(lines, fmt.Sprintf("  %s", c.Display))
				}
			}
			lines = append(lines, "")
		}

		if len(result.Articles) > 0 {
			lines = append(lines, "Articles:")
			for _, a := range result.Articles {
				line := fmt.Sprintf("  %s", a.Slug)
				if a.Display != "" {
					line += fmt.Sprintf("  — %s", a.Display)
				}
				lines = append(lines, line)
			}
			lines = append(lines, "")
		}

		// Apply unless dry-run.
		if !dryRun {
			colSlugs := make([]string, len(result.Collections))
			for i, c := range result.Collections {
				colSlugs[i] = c.Slug
			}
			artSlugs := make([]string, len(result.Articles))
			for i, a := range result.Articles {
				artSlugs[i] = a.Slug
			}
			if len(colSlugs) > 0 {
				_ = svc.AddCollectionsToWorkspace(context.Background(), wsName, colSlugs)
			}
			if len(artSlugs) > 0 {
				_ = svc.AddArticlesToWorkspace(context.Background(), wsName, artSlugs)
			}
			lines = append(lines, fmt.Sprintf("✓ Linked: %d collections, %d articles (cost: $%.4f)",
				len(result.Collections), len(result.Articles), result.CostUSD))
		} else {
			lines = append(lines, fmt.Sprintf("Suggested: %d collections, %d articles (cost: $%.4f)",
				len(result.Collections), len(result.Articles), result.CostUSD))
			lines = append(lines, "(dry-run — nothing linked)")
		}

		// Save full output to scratch as a single entry.
		output := strings.Join(lines, "\n") + "\n"
		_ = storefs.AppendScratch(cfg.DataRoot, wsName, output)

		return cmdDoneMsg{
			statusLines:      lines,
			reloadWorkspaces: !dryRun,
		}
	}
}

// handlePopulateEditInput processes user input during populate --edit review.
// Empty string or anything other than "n"/"q" = accept current item.
func (m *Model) handlePopulateEditInput(val string) tea.Cmd {
	val = strings.ToLower(strings.TrimSpace(val))
	switch val {
	case "n":
		// Skip — leave accepted=false, advance.
	case "q":
		// Done early — finish with what's accepted so far.
		return m.finishPopulateEdit()
	default:
		// Accept (Enter or any other input).
		m.populateEditItems[m.populateEditIdx].accepted = true
	}
	m.populateEditIdx++
	if m.populateEditIdx >= len(m.populateEditItems) {
		return m.finishPopulateEdit()
	}
	// Show next item.
	m.input.SetValue("")
	m.input.CursorEnd()
	return nil
}

// finishPopulateEdit ends the populate review, links accepted items, and shows results.
func (m *Model) finishPopulateEdit() tea.Cmd {
	m.populateEditing = false
	wsName := m.populateEditWs
	svc := m.svc
	cfg := m.cfg

	// Collect accepted items.
	var colSlugs, artSlugs []string
	for _, item := range m.populateEditItems {
		if !item.accepted {
			continue
		}
		if item.isCollection {
			colSlugs = append(colSlugs, item.slug)
		} else {
			artSlugs = append(artSlugs, item.slug)
		}
	}

	// Build status lines with ✓/– markers.
	var lines []string
	lines = append(lines, fmt.Sprintf("populate --edit %s", wsName))
	if m.populateEditHint != "" {
		lines = append(lines, fmt.Sprintf("hint: %s", m.populateEditHint))
	}
	lines = append(lines, "")
	for _, msg := range m.populateEditLog {
		lines = append(lines, "  "+msg)
	}
	if len(m.populateEditLog) > 0 {
		lines = append(lines, "")
	}

	hasCollections := false
	hasArticles := false
	for _, item := range m.populateEditItems {
		if item.isCollection {
			hasCollections = true
		} else {
			hasArticles = true
		}
	}

	if hasCollections {
		lines = append(lines, "Collections:")
		for _, item := range m.populateEditItems {
			if !item.isCollection {
				continue
			}
			marker := "✓"
			if !item.accepted {
				marker = "–"
			}
			line := fmt.Sprintf("  %s %s", marker, item.slug)
			if item.articleCount > 0 {
				line += fmt.Sprintf(" (%d articles)", item.articleCount)
			}
			lines = append(lines, line)
		}
		lines = append(lines, "")
	}

	if hasArticles {
		lines = append(lines, "Articles:")
		for _, item := range m.populateEditItems {
			if item.isCollection {
				continue
			}
			marker := "✓"
			if !item.accepted {
				marker = "–"
			}
			line := fmt.Sprintf("  %s %s", marker, item.slug)
			if item.display != "" {
				line += fmt.Sprintf("  — %s", item.display)
			}
			lines = append(lines, line)
		}
		lines = append(lines, "")
	}

	// Link accepted items.
	if svc != nil {
		if len(colSlugs) > 0 {
			_ = svc.AddCollectionsToWorkspace(context.Background(), wsName, colSlugs)
		}
		if len(artSlugs) > 0 {
			_ = svc.AddArticlesToWorkspace(context.Background(), wsName, artSlugs)
		}
	}

	lines = append(lines, fmt.Sprintf("✓ Linked: %d collections, %d articles (cost: $%.4f)",
		len(colSlugs), len(artSlugs), m.populateEditCost))

	// Save to scratch.
	output := strings.Join(lines, "\n") + "\n"
	_ = storefs.AppendScratch(cfg.DataRoot, wsName, output)

	m.setStatusLines(lines)
	m.input.SetValue("")

	// Reload workspaces since we linked items.
	if svc != nil && (len(colSlugs) > 0 || len(artSlugs) > 0) {
		m.workspacesLoaded = false
		return loadWorkspaces(svc)
	}
	return nil
}

// cmdRemoveWorkspace handles /remove — removes articles/collections from a workspace.
// Supports --article slug, --collection slug, --all-articles, --all-collections, --dry-run.
func (m *Model) cmdRemoveWorkspace(arg string) tea.Cmd {
	// Resolve workspace name.
	var wsName string
	if m.chatMode && m.chatWorkspace != "" {
		wsName = m.chatWorkspace
	} else if ws := m.selectedWorkspace(); ws != nil {
		wsName = ws.name
	} else {
		m.statusMsg = "✗ no workspace selected"
		return nil
	}
	if m.svc == nil {
		m.statusMsg = "✗ service unavailable"
		return nil
	}

	// Parse flags.
	var articles, collections []string
	var allArticles, allCollections, dryRun bool
	tokens := strings.Fields(arg)
	for i := 0; i < len(tokens); i++ {
		switch tokens[i] {
		case "--all-articles":
			allArticles = true
		case "--all-collections":
			allCollections = true
		case "--dry-run":
			dryRun = true
		case "--article":
			if i+1 < len(tokens) {
				i++
				articles = append(articles, tokens[i])
			}
		case "--collection":
			if i+1 < len(tokens) {
				i++
				collections = append(collections, tokens[i])
			}
		default:
			if !strings.HasPrefix(tokens[i], "--") {
				wsName = tokens[i]
			}
		}
	}

	cfg := m.cfg

	// --all-articles / --all-collections → interactive review in input pane.
	if allArticles || allCollections {
		var items []populateEditItem
		if allArticles {
			linked, _, _ := storefs.ListWorkspaceArticles(cfg.DataRoot, wsName)
			for _, slug := range linked {
				items = append(items, populateEditItem{slug: slug})
			}
			for _, slug := range storefs.ListAtticArticles(cfg.DataRoot, wsName) {
				items = append(items, populateEditItem{slug: slug})
			}
		}
		if allCollections {
			linked, _ := storefs.ListWorkspaceCollections(cfg.DataRoot, wsName)
			for _, slug := range linked {
				items = append(items, populateEditItem{slug: slug, isCollection: true})
			}
			for _, slug := range storefs.ListAtticCollections(cfg.DataRoot, wsName) {
				items = append(items, populateEditItem{slug: slug, isCollection: true})
			}
		}
		if len(items) == 0 {
			m.statusMsg = "✗ no items to remove"
			return nil
		}
		m.removeReviewing = true
		m.removeReviewItems = items
		m.removeReviewIdx = 0
		m.removeReviewWs = wsName
		m.removeReviewDry = dryRun
		m.focus = paneCommand
		m.cursorVisible = true
		m.input.SetValue("")
		m.input.CursorEnd()
		return nil
	}

	// Individual --article / --collection → direct removal.
	if len(articles) == 0 && len(collections) == 0 {
		m.statusMsg = "✗ specify --article, --collection, --all-articles, or --all-collections"
		return nil
	}

	if dryRun {
		total := len(articles) + len(collections)
		m.statusMsg = fmt.Sprintf("would remove %d items from %s (dry-run)", total, wsName)
		return nil
	}

	return func() tea.Msg {
		var errs []string
		removed := 0
		for _, slug := range articles {
			// Try active list first, then attic.
			if err := storefs.RemoveArticleFromWorkspace(cfg.DataRoot, wsName, slug); err == nil {
				removed++
			} else if err2 := storefs.RemoveArticleFromAttic(cfg.DataRoot, wsName, slug); err2 == nil {
				removed++
			} else {
				errs = append(errs, fmt.Sprintf("%s: not in workspace or attic", slug))
			}
		}
		for _, col := range collections {
			if err := storefs.RemoveCollectionFromWorkspace(cfg.DataRoot, wsName, col); err == nil {
				removed++
			} else if err2 := storefs.RemoveCollectionFromAttic(cfg.DataRoot, wsName, col); err2 == nil {
				removed++
			} else {
				errs = append(errs, fmt.Sprintf("%s: not in workspace or attic", col))
			}
		}
		if len(errs) > 0 {
			return cmdDoneMsg{err: strings.Join(errs, "; ")}
		}

		statusMsg := fmt.Sprintf("✓ removed %d items from %s", removed, wsName)

		// Save to scratch.
		_ = storefs.AppendScratch(cfg.DataRoot, wsName, statusMsg+"\n")

		return cmdDoneMsg{
			statusMsg:          statusMsg,
			reloadWorkspaces:   true,
			resetChatEngine:    true,
			resetChatWorkspace: wsName,
		}
	}
}

// handleRemoveReviewInput processes user input during /remove --all-* review.
// Empty string or anything other than "n"/"q" = mark for removal.
func (m *Model) handleRemoveReviewInput(val string) tea.Cmd {
	val = strings.ToLower(strings.TrimSpace(val))
	switch val {
	case "n":
		// Keep — leave accepted=false, advance.
	case "q":
		// Done early — finish with what's marked so far.
		return m.finishRemoveReview()
	default:
		// Remove (Enter or any other input).
		m.removeReviewItems[m.removeReviewIdx].accepted = true
	}
	m.removeReviewIdx++
	if m.removeReviewIdx >= len(m.removeReviewItems) {
		return m.finishRemoveReview()
	}
	// Show next item.
	m.input.SetValue("")
	m.input.CursorEnd()
	return nil
}

// finishRemoveReview ends the remove review, unlinks marked items, and shows results.
func (m *Model) finishRemoveReview() tea.Cmd {
	m.removeReviewing = false
	wsName := m.removeReviewWs
	dryRun := m.removeReviewDry
	svc := m.svc
	cfg := m.cfg

	// Collect items marked for removal.
	var colSlugs, artSlugs []string
	for _, item := range m.removeReviewItems {
		if !item.accepted {
			continue
		}
		if item.isCollection {
			colSlugs = append(colSlugs, item.slug)
		} else {
			artSlugs = append(artSlugs, item.slug)
		}
	}

	// Build status lines with ✓ (removed) / – (kept) markers.
	var lines []string
	verb := "remove"
	if dryRun {
		verb = "remove --dry-run"
	}
	lines = append(lines, fmt.Sprintf("%s %s", verb, wsName))
	lines = append(lines, "")

	hasCollections := false
	hasArticles := false
	for _, item := range m.removeReviewItems {
		if item.isCollection {
			hasCollections = true
		} else {
			hasArticles = true
		}
	}

	if hasCollections {
		lines = append(lines, "Collections:")
		for _, item := range m.removeReviewItems {
			if !item.isCollection {
				continue
			}
			marker := "✓ remove"
			if !item.accepted {
				marker = "– keep"
			}
			lines = append(lines, fmt.Sprintf("  %s  %s", marker, item.slug))
		}
		lines = append(lines, "")
	}

	if hasArticles {
		lines = append(lines, "Articles:")
		for _, item := range m.removeReviewItems {
			if item.isCollection {
				continue
			}
			marker := "✓ remove"
			if !item.accepted {
				marker = "– keep"
			}
			lines = append(lines, fmt.Sprintf("  %s  %s", marker, item.slug))
		}
		lines = append(lines, "")
	}

	total := len(colSlugs) + len(artSlugs)

	if dryRun {
		lines = append(lines, fmt.Sprintf("would remove %d items (dry-run — nothing changed)", total))
	} else if svc != nil && total > 0 {
		for _, col := range colSlugs {
			if err := storefs.RemoveCollectionFromWorkspace(cfg.DataRoot, wsName, col); err != nil {
				_ = storefs.RemoveCollectionFromAttic(cfg.DataRoot, wsName, col)
			}
		}
		for _, slug := range artSlugs {
			if err := storefs.RemoveArticleFromWorkspace(cfg.DataRoot, wsName, slug); err != nil {
				_ = storefs.RemoveArticleFromAttic(cfg.DataRoot, wsName, slug)
			}
		}
		lines = append(lines, fmt.Sprintf("✓ removed %d items from %s", total, wsName))
	} else {
		lines = append(lines, "no items removed")
	}

	// Save to scratch.
	output := strings.Join(lines, "\n") + "\n"
	_ = storefs.AppendScratch(cfg.DataRoot, wsName, output)

	m.setStatusLines(lines)
	m.input.SetValue("")

	// Reload workspaces since we removed items.
	if !dryRun && svc != nil && total > 0 {
		m.workspacesLoaded = false
		return loadWorkspaces(svc)
	}
	return nil
}

// helpGroups defines the command groups shown by /help.
// Names match the context qualifier: article, collection, workspace, keys, system.
var helpGroups = []struct {
	name     string
	commands []cmdCompletion
}{
	{"article", []cmdCompletion{
		{"/search", "<query> [--limit N]", "full-text search — use /article search from any tab"},
		{"/filter", "<tag>", "filter by tag"},
		{"/favorites", "", "show only favorited articles"},
		{"/clear", "", "clear active filter"},
		{"/open", "", "open source URL in browser"},
		{"/read", "", "mark as read"},
		{"/unread", "", "mark as unread"},
		{"/favorite", "", "toggle favorite"},
		{"/collection-add", "<slug>", "add article to a collection (picker in status pane)"},
		{"/collection-remove", "<slug>", "remove article from a collection (picker in status pane)"},
		{"/chat", "", "open article chat pane (or press c in nav)"},
		{"/delete", "", "delete current article"},
		{"/reprocess", "", "regenerate summary/flash"},
		{"/ingest", "<url> [--profile <name>] [--style <name>]", "add a new article — use /article ingest from any tab"},
	}},
	{"collection", []cmdCompletion{
		{"/search", "<query>", "filter collections by name/slug"},
		{"/clear", "", "clear active filter"},
		{"/reload", "", "refresh collections list from disk"},
		{"/chat", "", "open article chat pane (or press c in nav)"},
		{"/delete", "", "delete current collection"},
		{"arc collections create", "<slug>", "create a new collection  (CLI only)"},
		{"arc collections rename", "<old> <new>", "rename  (CLI only)"},
		{"arc collections describe", "<slug> <desc>", "set description  (CLI only)"},
		{"arc collections suggest", "[--apply]", "AI-suggest collections  (CLI only)"},
		{"arc collections read", "<slug>", "read flash/summary across collection  (CLI only)"},
	}},
	{"workspace", []cmdCompletion{
		{"/search", "<query>", "search workspaces · or articles within focused workspace"},
		{"/clear", "", "clear active filter"},
		{"/new", "<name>", "create a new workspace"},
		{"/delete", "", "delete current workspace"},
		{"/rename", "<new-name>", "rename current workspace"},
		{"/describe", "<text>", "set workspace description"},
		{"/populate", "[--hint --edit --dry-run --profile --include-collections]", "LLM-assisted article selection from library"},
		{"/remove", "[--article --collection --all-articles --all-collections --dry-run]", "remove articles/collections from workspace"},
		{"arc workspace add", "<slug>", "add articles/collections/resources  (CLI only)"},
		{"arc workspace chat", "<slug>", "start interactive chat session  (CLI only)"},
		{"arc workspace archive", "<slug>", "archive a workspace  (CLI only)"},
		{"arc workspace outcomes", "<slug>", "list or read outcomes  (CLI only)"},
		{"arc workspace system", "<slug>", "get/set system prompt  (CLI only)"},
	}},
	{"keys", []cmdCompletion{
		{"j / ↓", "", "move down"},
		{"k / ↑", "", "move up"},
		{"PgDn / ctrl+d", "", "page down"},
		{"PgUp / ctrl+u", "", "page up"},
		{"g / Home", "", "go to top"},
		{"G / End", "", "go to bottom"},
		{"enter", "", "select / expand / collapse"},
		{"space", "", "expand / collapse"},
		{"esc", "", "back / dismiss"},
		{"tab", "", "next pane"},
		{"shift+tab", "", "previous pane"},
		{"alt+1/2/3", "", "jump to nav / content / tab bar"},
		{"l / →", "", "next content tab (Body/Summary/Flash/Cards)"},
		{"h / ←", "", "previous content tab"},
		{"c", "", "open article chat (Articles / Collections nav)"},
		{"r", "", "mark article as read"},
		{"u", "", "mark article as unread"},
		{"f/*", "", "toggle favorite"},
		{"o", "", "open source URL in browser"},
		{"v", "", "view article in external terminal"},
		{"D", "", "delete current item"},
		{"U", "", "unlink article/collection from workspace"},
		{"a", "", "move article/collection to attic"},
		{"b", "", "restore article/collection from attic"},
		{"/", "", "open command input"},
		{"↑ / ↓", "", "recall command history (in command pane)"},
		{"?", "", "show key bindings"},
		{"q / ctrl+c", "", "quit"},
	}},
	{"agent", []cmdCompletion{
		{"/agent-run", "[--dry-run] [--focus \"...\"]", "fresh feed scan — poll all feeds, filter, ingest"},
		{"/agent-rerun", "[--dry-run]", "process decisions for the selected run (mark items with a/s first)"},
		{"/feed-add", "", "add a new feed (opens $EDITOR with template)"},
		{"/feed-edit", "", "edit selected feed in $EDITOR"},
		{"/feed-toggle", "", "toggle selected feed enabled/disabled"},
		{"/feed-delete", "", "delete selected feed (with confirmation)"},
	}},
	{"system", []cmdCompletion{
		{"/scratch", "[msg]", "workspace-local scratch (append / toggle)"},
		{"/Scratch", "[msg]", "global scratch (append / toggle)"},
		{"/askX", "[--profile <name>] <prompt>", "workspace-local LLM query"},
		{"/AskX", "[--profile <name>] <prompt>", "global LLM query (same as Ctrl+X)"},
		{"/profile", "[name]", "show or switch LLM profile for this chat session"},
		{"/config", "", "show resolved configuration"},
		{"/config-view", "", "view config.jsonc in overlay"},
		{"/config-edit", "", "open config.jsonc in $EDITOR"},
		{"/config-agent-view", "", "view agent/config.jsonc in overlay"},
		{"/config-agent-edit", "", "open agent/config.jsonc in $EDITOR"},
		{"/config-chat-view", "", "view workspace chat/config.jsonc in overlay"},
		{"/config-chat-edit", "", "open workspace chat/config.jsonc in $EDITOR"},
		{"/tags", "", "list all tags"},
		{"/stats", "", "show library stats"},
		{"/models", "", "list available LLM profiles"},
		{"/log", "", "open/close debug log tail"},
	}},
}

// helpLines returns context-sensitive help for the active tab.
// arg="" shows all groups; "article" shows article commands;
// "article /read" shows just the /read entry.
func (m *Model) helpLines(arg string) []string {
	if m.activeTab != tabLibrary {
		return []string{"no commands available for this tab"}
	}

	renderGroup := func(g struct {
		name     string
		commands []cmdCompletion
	}) []string {
		// Check if this group has both TUI and CLI-only entries.
		hasTUI, hasCLI := false, false
		for _, c := range g.commands {
			if strings.HasPrefix(c.cmd, "arc ") {
				hasCLI = true
			} else {
				hasTUI = true
			}
		}
		showLegend := hasTUI && hasCLI

		header := g.name + ":"
		if showLegend {
			header += "  (/ = TUI command · no slash = CLI only: arc " + g.name + " <cmd>)"
		}
		lines := []string{header}
		for _, c := range g.commands {
			displayCmd := c.cmd
			// For CLI-only entries like "arc collections create", show just the subcommand.
			if parts := strings.Fields(displayCmd); len(parts) >= 3 && parts[0] == "arc" {
				displayCmd = parts[2]
			}
			synopsis := displayCmd
			if c.arg != "" {
				synopsis += " " + c.arg
			}
			// Strip "(CLI only)" from desc when legend is shown — it's redundant.
			desc := c.desc
			if showLegend {
				desc = strings.TrimSuffix(strings.TrimSpace(strings.Replace(desc, "  (CLI only)", "", 1)), "(CLI only)")
			}
			lines = append(lines, fmt.Sprintf("  %-24s  %s", synopsis, desc))
		}
		return lines
	}

	arg = strings.TrimSpace(strings.ToLower(arg))

	// No arg — show all groups.
	if arg == "" {
		var lines []string
		for i, g := range helpGroups {
			if i > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, renderGroup(g)...)
		}
		return lines
	}

	// Two-level: "article /read" — show just that command entry.
	parts := strings.SplitN(arg, " ", 2)
	groupName := parts[0]
	cmdFilter := ""
	if len(parts) == 2 {
		cmdFilter = strings.TrimSpace(parts[1])
	}

	for _, g := range helpGroups {
		if g.name == groupName {
			if cmdFilter == "" {
				return renderGroup(g)
			}
			// Filter to matching command(s).
			var lines []string
			for _, c := range g.commands {
				displayCmd := c.cmd
				if parts := strings.Fields(displayCmd); len(parts) >= 3 && parts[0] == "arc" {
					displayCmd = parts[2]
				}
				if strings.HasPrefix(displayCmd, cmdFilter) {
					synopsis := displayCmd
					if c.arg != "" {
						synopsis += " " + c.arg
					}
					lines = append(lines, fmt.Sprintf("  %-24s  %s", synopsis, c.desc))
				}
			}
			if len(lines) == 0 {
				return []string{fmt.Sprintf("no commands matching %q in %q", cmdFilter, groupName)}
			}
			return lines
		}
	}

	groupNames := make([]string, len(helpGroups))
	for i, g := range helpGroups {
		groupNames[i] = g.name
	}
	return []string{fmt.Sprintf("unknown group %q — available: %s", groupName, strings.Join(groupNames, ", "))}
}

// removeNavItem removes the item with the given id from a slice.
func removeNavItem(items []navItem, id string) []navItem {
	out := items[:0]
	for _, item := range items {
		if item.id != id {
			out = append(out, item)
		}
	}
	return out
}

// handleMouse handles mouse press, release, and motion events.
func (m *Model) handleMouse(msg tea.MouseMsg) tea.Cmd {
	divCol := m.dividerCol()

	// Resource overlay captures all mouse events: wheel scrolls, clicks are swallowed.
	if m.focus == paneResource {
		if msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown {
			delta := 1
			if msg.Button == tea.MouseButtonWheelUp {
				delta = -1
			}
			viewH := m.height - 4
			if viewH < 1 {
				viewH = 1
			}
			m.resourceCursor += delta
			if m.resourceCursor < 0 {
				m.resourceCursor = 0
			}
			if m.resourceCursor >= len(m.resourceLines) {
				m.resourceCursor = len(m.resourceLines) - 1
			}
			m.scrollResourceToCursor(viewH)
		}
		return nil
	}

	// Mouse wheel scrolling.
	if msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown {
		delta := 1
		if msg.Button == tea.MouseButtonWheelUp {
			delta = -1
		}

		// Status area — scrolls when statusLines visible, regardless of position.
		if len(m.statusLines) > 0 {
			maxVisible := m.height * 30 / 100
			if maxVisible < 3 {
				maxVisible = 3
			}
			m.statusScroll += delta
			if m.statusScroll < 0 {
				m.statusScroll = 0
			}
			maxScroll := len(m.statusLines) - maxVisible
			if maxScroll < 0 {
				maxScroll = 0
			}
			if m.statusScroll > maxScroll {
				m.statusScroll = maxScroll
			}
			return nil
		}

		// Scratch/AskX/Preview pane wheel (bottom of content pane).
		if msg.X > m.dividerCol() && (m.scratchOpen || m.askxOpen || m.previewOpen) {
			splitStartRow := m.splitPaneStartRow()
			if msg.Y >= splitStartRow {
				mainH := m.mainAreaHeight()
				splitH := mainH / 3
				if splitH < 3 {
					splitH = 3
				}
				viewH := splitH - 1
				if m.scratchOpen {
					m.scratchScroll += delta
					if m.scratchScroll < 0 {
						m.scratchScroll = 0
					}
					maxScroll := len(m.scratchLines) - viewH
					if maxScroll < 0 {
						maxScroll = 0
					}
					if m.scratchScroll > maxScroll {
						m.scratchScroll = maxScroll
					}
				} else if m.askxOpen {
					m.askxScroll += delta
					if m.askxScroll < 0 {
						m.askxScroll = 0
					}
					maxScroll := len(m.askxDisplayLines) - viewH
					if maxScroll < 0 {
						maxScroll = 0
					}
					if m.askxScroll > maxScroll {
						m.askxScroll = maxScroll
					}
				} else if m.previewOpen {
					m.previewScroll += delta
					if m.previewScroll < 0 {
						m.previewScroll = 0
					}
					maxScroll := len(m.previewLines) - viewH
					if maxScroll < 0 {
						maxScroll = 0
					}
					if m.previewScroll > maxScroll {
						m.previewScroll = maxScroll
					}
				}
				return nil
			}
		}

		// Nav pane wheel (left of divider).
		if msg.X < m.dividerCol() {
			if m.navSubTab == navSubTabCollections {
				m.navRowScroll += delta
				if m.navRowScroll < 0 {
					m.navRowScroll = 0
				}
				max := len(m.navRows) - m.navPaneHeight()
				if max < 0 {
					max = 0
				}
				if m.navRowScroll > max {
					m.navRowScroll = max
				}
				if m.navRowCursor < m.navRowScroll {
					m.navRowCursor = m.navRowScroll
				} else if m.navRowCursor >= m.navRowScroll+m.navPaneHeight() {
					m.navRowCursor = m.navRowScroll + m.navPaneHeight() - 1
				}
				return m.triggerCollectionContentLoad()
			}
			m.navScroll += delta
			if m.navScroll < 0 {
				m.navScroll = 0
			}
			max := len(m.navItems) - m.navPaneHeight()
			if max < 0 {
				max = 0
			}
			if m.navScroll > max {
				m.navScroll = max
			}
			// Keep cursor within visible window.
			if m.navCursor < m.navScroll {
				m.navCursor = m.navScroll
			} else if m.navCursor >= m.navScroll+m.navPaneHeight() {
				m.navCursor = m.navScroll + m.navPaneHeight() - 1
			}
			return m.triggerContentLoad()
		}

		// Content pane wheel (right of divider).
		if msg.X > m.dividerCol() {
			if m.chatMode {
				chatViewH := m.chatViewHeight()
				if chatViewH < 1 {
					chatViewH = 1
				}
				m.chatScroll += delta
				if m.chatScroll < 0 {
					m.chatScroll = 0
				}
				maxScroll := m.chatTotalLines() - chatViewH
				if maxScroll < 0 {
					maxScroll = 0
				}
				if m.chatScroll > maxScroll {
					m.chatScroll = maxScroll
				}
				m.chatAutoScroll = m.chatScroll >= maxScroll
				return nil
			}
			m.contentScroll += delta
			if m.contentScroll < 0 {
				m.contentScroll = 0
			}
			maxScroll := len(m.contentLines) - m.contentViewHeight()
			if maxScroll < 0 {
				maxScroll = 0
			}
			if m.contentScroll > maxScroll {
				m.contentScroll = maxScroll
			}
			return nil
		}
	}

	switch msg.Action {
	case tea.MouseActionPress:
		if msg.Button == tea.MouseButtonLeft {
			// Click on tab bar (row 0).
			if msg.Y == 0 {
				if t := tabBarHitTest(msg.X); t >= 0 {
					if m.chatMode && t != tabLibrary {
						m.exitChatMode()
					}
					m.activeTab = t
					m.focus = paneTabBar
					m.previewFocused = false
				}
				return nil
			}
			// Click on nav sub-tab bar (first row of main area = topBarHeight).
			subTabRow := topBarHeight
			if msg.Y == subTabRow && msg.X < m.dividerCol() {
				m.previewFocused = false
				switch m.activeTab {
				case tabLibrary:
					if sub := navSubTabHitTest(msg.X); sub >= 0 {
						m.focus = paneNav
						return m.switchNavSubTab(sub)
					}
				case tabAgent:
					if sub := agentNavSubTabHitTest(msg.X); sub >= 0 {
						m.focus = paneNav
						m.agentSubTab = sub
					}
				case tabStats:
					if sub := statsNavSubTabHitTest(msg.X); sub >= 0 {
						m.focus = paneNav
						m.statsSubTab = sub
					}
				}
				return nil
			}
			// Check command input row first — it spans full width.
			cmdRow := m.height - hintBarHeight - m.completionCount() - statusSepHeight - cmdBarHeight
			if msg.Y == cmdRow {
				m.focus = paneCommand
				m.previewFocused = false
				m.cursorVisible = true
				return nil
			}
			// Clicking on or within 1 col of the divider starts a drag.
			if msg.X >= divCol-1 && msg.X <= divCol+1 {
				m.dragging = true
				m.dragCol = msg.X
				return nil
			}
			// Click in nav pane (left of divider) — focus nav, update cursor row.
			if msg.X < divCol {
				clog.Debugf("mouse: nav click, clearing previewFocused=%v", m.previewFocused)
				m.focus = paneNav
				m.scratchFocused = false
				m.previewFocused = false
				if cmd := m.clickNavRow(msg.Y); cmd != nil {
					return cmd
				}
				return nil
			}
			// Click in content pane.
			m.focus = paneContent
			// Check if click is in the scratch or askX region.
			splitOpen := m.scratchOpen || m.askxOpen || m.previewOpen || m.achatMode
			if splitOpen {
				splitStartRow := m.splitPaneStartRow()
				if msg.Y >= splitStartRow {
					if m.scratchOpen {
						m.scratchFocused = true
					}
					if m.askxOpen {
						m.askxFocused = true
					}
					if m.previewOpen {
						m.previewFocused = true
					}
					if m.achatMode {
						m.achatFocused = true
					}
					return nil
				}
			}
			m.setFocusPane(paneContent)
		}

	case tea.MouseActionMotion:
		if m.dragging {
			newW := msg.X
			if newW < 10 {
				newW = 10
			}
			if newW > m.width-10 {
				newW = m.width - 10
			}
			m.navWidthOverride = newW
		}

	case tea.MouseActionRelease:
		m.dragging = false
	}

	return nil
}

// clickNavRow moves navCursor to the item at the given terminal row (0-based Y).
// Library: content starts at topBarHeight + 3 (top bar + sub-tab bar + blank).
// Other tabs: topBarHeight + 2 (top bar + label).
// In Library tabs, row 0 is the scratch row.
func (m *Model) clickNavRow(y int) tea.Cmd {
	contentStartRow := topBarHeight + 2
	if m.activeTab == tabLibrary {
		contentStartRow = topBarHeight + 3
	}
	switch m.navSubTab {
	case navSubTabArticles:
		idx := m.navScroll + (y - contentStartRow)
		if idx >= 0 && idx < len(m.navItems) {
			m.navCursor = idx
			return m.triggerContentLoad()
		}
	case navSubTabCollections:
		idx := m.navRowScroll + (y - contentStartRow)
		if idx >= 0 && idx < len(m.navRows) {
			m.navRowCursor = idx
			return m.triggerCollectionContentLoad()
		}
	case navSubTabWorkspaces:
		idx := m.wsScroll + (y - contentStartRow)
		if idx >= 0 && idx < len(m.wsRows) {
			m.wsCursor = idx
			m.maybeReloadScratch()
			m.maybeCloseAskX()
			m.maybeUpdatePreview()
			row := m.wsRows[idx]
			switch row.kind {
			case wsRowWorkspace:
				// Click on workspace → load history (engine init deferred to first message).
				if row.wsIdx >= 0 && row.wsIdx < len(m.workspaceItems) {
					ws := m.workspaceItems[row.wsIdx]
					return m.loadChatHistoryCmd(ws.name, true)
				}
			case wsRowCollection:
				m.wsToggleExpand()
			case wsRowResourceGroup, wsRowResourceDir:
				m.wsToggleExpand()
			case wsRowOutcomeGroup:
				m.wsToggleExpand()
			case wsRowResource:
				return m.openWorkspaceFile(row.wsIdx, "resources", row.resourceName)
			case wsRowOutcome:
				return m.openWorkspaceFile(row.wsIdx, "outcomes", row.outcomeName)
			}
		}
	}
	return nil
}

// navPaneHeight returns usable item lines in the nav pane (excluding scratch row when open).
func (m *Model) navPaneHeight() int {
	// fixed: top bar (2) + split sep (1) + cmd (1) + status sep (1) + status bar (1) = 6
	// plus completions expanding upward
	// Library tab: 2 header rows (sub-tab bar + blank) + optional scratch row
	// Other tabs: 1 header row (label)
	overhead := 1
	if m.activeTab == tabLibrary {
		overhead = 2 // sub-tab bar + blank
	}
	h := m.height - 6 - m.completionCount() - overhead
	if h < 1 {
		h = 1
	}
	return h
}

// =============================================================================
// Shell command execution (! prefix)
// =============================================================================

type shellDoneMsg struct {
	cmd      string
	output   string
	exitCode int
}

// runShellCmd executes cmd via the user's login shell with a 30s timeout and returns shellDoneMsg.
func runShellCmd(cmd string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "sh"
		}
		c := exec.CommandContext(ctx, shell, "-i", "-c", cmd)
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		out, err := c.CombinedOutput()
		exitCode := 0
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			} else {
				exitCode = 1
				out = append(out, []byte("\n"+err.Error())...)
			}
		}
		return shellDoneMsg{cmd: cmd, output: string(out), exitCode: exitCode}
	}
}

// contentViewHeight returns the number of lines available for scrollable content.
// Layout: header (4 lines) + sep + tab strip + sep = 7 fixed lines in content pane.
func (m *Model) contentViewHeight() int {
	mainH := m.height - 6 - m.completionCount()
	// Subtract scratch split if open.
	if m.scratchOpen {
		scratchH := mainH / 3
		if scratchH < 3 {
			scratchH = 3
		}
		mainH -= scratchH
		if mainH < 3 {
			mainH = 3
		}
	}
	h := mainH - contentHeaderLines(m.selectedNavItem())
	if h < 1 {
		h = 1
	}
	return h
}

// ── Scratch ─────────────────────────────────────────────────────────────────

// scratchWorkspace returns the workspace name to use for scratch operations.
// Returns "" (global scratch) if no workspace is active or scratchGlobal is set.
func (m *Model) scratchWorkspace() string {
	if m.scratchGlobal {
		return ""
	}
	// Nav cursor workspace takes priority — it reflects what the user is looking at.
	if m.navSubTab == navSubTabWorkspaces {
		if ws := m.selectedWorkspace(); ws != nil {
			return ws.name
		}
	}
	// Fall back to chatWorkspace when not on workspaces tab (e.g. articles tab with active chat).
	if m.chatMode && m.chatWorkspace != "" {
		return m.chatWorkspace
	}
	return ""
}

// toggleScratch toggles the global scratch pane. When opening, pre-fills input with "/scratch ".
// Always opens global scratch regardless of cursor context.
func (m *Model) toggleScratch() {
	if m.scratchOpen {
		m.closeScratch()
		return
	}
	// Mutual exclusion: close askX and preview if open.
	if m.askxOpen {
		m.closeAskX()
	}
	if m.previewOpen {
		m.closePreview()
	}
	m.scratchGlobal = true
	m.scratchOpen = true
	m.reloadScratchLines()
	m.scratchScrollToBottom()
	// Don't move focus or pre-fill input — let the user stay where they are.
	// m.focus = paneCommand
	// m.cursorVisible = true
	// m.input.SetValue("/scratch ")
	// m.input.CursorEnd()
	// m.cmdComplete = nil
	// m.cmdCompleteIdx = -1
	// m.paramItems = nil
	// m.paramIdx = -1
}

// cmdScratch handles /scratch [msg]. Empty msg toggles pane; non-empty appends.
// global=true targets the global scratch; global=false uses workspace-local.
func (m *Model) cmdScratch(msg string, global bool) tea.Cmd {
	if msg == "" {
		// Toggle pane visibility.
		if m.scratchOpen {
			m.closeScratch()
		} else {
			if m.askxOpen {
				m.closeAskX()
			}
			if m.previewOpen {
				m.closePreview()
			}
			m.scratchGlobal = global
			m.scratchOpen = true
			m.reloadScratchLines()
			m.scratchScrollToBottom()
			if m.chatMode {
				m.chatScroll = m.chatTotalLines()
			}
		}
		return nil
	}
	// Append message.
	if !m.scratchOpen {
		m.scratchGlobal = global
	}
	ws := m.scratchWorkspace()
	if err := storefs.AppendScratch(m.cfg.DataRoot, ws, msg); err != nil {
		m.setStatusError("scratch: " + err.Error())
		return nil
	}
	m.reloadScratchLines()
	if !m.scratchOpen {
		if m.askxOpen {
			m.closeAskX()
		}
		if m.previewOpen {
			m.closePreview()
		}
		m.scratchOpen = true
	}
	// Auto-scroll to bottom.
	m.scratchScrollToBottom()
	m.statusMsg = "✓ added to scratch"
	return nil
}

// reloadScratchLines reads the scratch file and caches lines + blocks for rendering.
// Uses scratchWorkspace() unless scratchGlobal is set (always "").
// triggerWorkspaceReload synchronously reloads workspace data, preserving expand state.
func (m *Model) triggerWorkspaceReload() {
	if m.svc == nil {
		return
	}
	infos, err := m.svc.ListWorkspaces(context.Background(), false)
	if err != nil {
		return
	}
	old := make(map[string]*workspaceItem, len(m.workspaceItems))
	for i := range m.workspaceItems {
		old[m.workspaceItems[i].name] = &m.workspaceItems[i]
	}
	items := make([]workspaceItem, len(infos))
	for i, w := range infos {
		items[i] = workspaceItem{
			name:            w.Name,
			description:     w.Description,
			status:          w.Status,
			createdAt:       w.CreatedAt,
			articleCount:    w.ArticleCount,
			collectionCount: w.CollectionCount,
			resourceCount:   w.ResourceCount,
			outcomeCount:    w.OutcomeCount,
			hasSystem:       w.HasSystem,
			hasHistory:      w.HasHistory,
			chatProfile:     w.ChatConfig.Profile,
			chatStrategy:    w.ChatConfig.Strategy,
			articles:        w.Articles,
			collectionSlugs: w.CollectionSlugs,
			resources:       w.ResourceNames,
			resourceDirs:    w.ResourceDirs,
			outcomes:        w.OutcomeNames,
			atticArticles:   w.AtticArticles,
			atticCollections: w.AtticCollectionSlugs,
			expandedCols:         make(map[string]bool),
			expandedResourceDirs: make(map[string]bool),
		}
		if prev, ok := old[items[i].name]; ok {
			items[i].expanded = prev.expanded
			items[i].expandedCols = prev.expandedCols
			items[i].resourcesExpanded = prev.resourcesExpanded
			items[i].expandedResourceDirs = prev.expandedResourceDirs
			items[i].outcomesExpanded = prev.outcomesExpanded
			items[i].atticExpanded = prev.atticExpanded
		}
	}
	m.workspaceItemsAll = items
	if m.wsFocusName != "" {
		var focused []workspaceItem
		for _, ws := range items {
			if ws.name == m.wsFocusName {
				focused = append(focused, ws)
				break
			}
		}
		if len(focused) > 0 {
			m.workspaceItems = focused
		} else {
			m.workspaceItems = items
		}
	} else {
		m.workspaceItems = items
	}
	m.wsRows = m.buildWsRows()
	m.clampWsScroll()
}

func (m *Model) reloadScratchLines() {
	ws := ""
	if !m.scratchGlobal {
		ws = m.scratchWorkspace()
	}
	m.scratchLoadedWs = ws
	content, err := storefs.ReadScratch(m.cfg.DataRoot, ws)
	if err != nil {
		m.scratchLines = []string{"(error reading scratch: " + err.Error() + ")"}
		m.scratchBlocks = nil
		return
	}
	if content == "" {
		m.scratchLines = []string{"(empty scratch — use /scratch <msg> to add notes)"}
		m.scratchBlocks = nil
		return
	}
	// Word-wrap lines to content pane width (scratch has no horizontal scroll).
	w := m.width - m.navWidth() - 1
	if w < 10 {
		w = 10
	}

	rawLines := splitLines(content)
	var wrapped []string
	var blocks []scratchBlock

	for _, raw := range rawLines {
		startIdx := len(wrapped)
		wlines := wordWrap(raw, w)
		if len(wlines) == 0 {
			wlines = []string{""}
		}
		wrapped = append(wrapped, wlines...)
		endIdx := len(wrapped) - 1

		isSep := strings.HasPrefix(raw, "----------")
		isBullet := strings.HasPrefix(raw, "• ")

		if isSep {
			blocks = append(blocks, scratchBlock{
				startLine: startIdx,
				endLine:   endIdx,
				text:      raw,
				isSep:     true,
			})
		} else if isBullet {
			blocks = append(blocks, scratchBlock{
				startLine: startIdx,
				endLine:   endIdx,
				text:      strings.TrimPrefix(raw, "• "),
			})
		} else if raw == "" {
			// blank lines — not a block, just spacing
		} else {
			// Continuation of previous block (e.g. multi-line pasted note).
			if len(blocks) > 0 && !blocks[len(blocks)-1].isSep {
				blocks[len(blocks)-1].endLine = endIdx
				blocks[len(blocks)-1].text += "\n" + raw
			} else {
				// No preceding block to continue — standalone block.
				blocks = append(blocks, scratchBlock{
					startLine: startIdx,
					endLine:   endIdx,
					text:      raw,
				})
			}
		}
	}

	m.scratchLines = wrapped
	m.scratchBlocks = blocks

	// Clamp block cursor.
	if m.scratchBlockCursor >= len(blocks) {
		m.scratchBlockCursor = len(blocks) - 1
	}
	if m.scratchBlockCursor < 0 {
		m.scratchBlockCursor = 0
	}
	// Skip separator if cursor landed on one.
	m.scratchBlockCursorSkipSep(1)
}

// maybeReloadScratch reloads the scratch pane if the cursor moved to a different workspace.
// No-op when scratch was opened as global (via Ctrl+L).
func (m *Model) maybeReloadScratch() {
	if !m.scratchOpen || m.scratchGlobal {
		return
	}
	ws := m.scratchWorkspace()
	if ws == m.scratchLoadedWs {
		return
	}
	m.reloadScratchLines()
	m.scratchScrollToBottom()
}

// maybeCloseAskX closes the workspace-local askX pane when the cursor moves away.
// No-op when askX is global (opened via Ctrl+X) or not open.
func (m *Model) maybeCloseAskX() {
	if !m.askxOpen || m.askxGlobal {
		return
	}
	m.closeAskX()
}

// scratchBlockCursorSkipSep advances the block cursor past date separators.
// dir should be +1 or -1 to indicate search direction.
func (m *Model) scratchBlockCursorSkipSep(dir int) {
	for m.scratchBlockCursor >= 0 && m.scratchBlockCursor < len(m.scratchBlocks) {
		if !m.scratchBlocks[m.scratchBlockCursor].isSep {
			return
		}
		m.scratchBlockCursor += dir
	}
	// If we ran off the end, search the other direction.
	if dir > 0 {
		m.scratchBlockCursor = len(m.scratchBlocks) - 1
	} else {
		m.scratchBlockCursor = 0
	}
	for m.scratchBlockCursor >= 0 && m.scratchBlockCursor < len(m.scratchBlocks) {
		if !m.scratchBlocks[m.scratchBlockCursor].isSep {
			return
		}
		m.scratchBlockCursor -= dir
	}
}

// scratchScrollToBottom scrolls the scratch pane to the bottom and moves cursor to last block.
func (m *Model) scratchScrollToBottom() {
	viewH := m.scratchViewH()
	total := m.scratchTotalVLines()
	maxScroll := total - viewH
	if maxScroll < 0 {
		maxScroll = 0
	}
	m.scratchScroll = maxScroll
	// Move block cursor to last selectable block.
	if len(m.scratchBlocks) > 0 {
		m.scratchBlockCursor = len(m.scratchBlocks) - 1
		m.scratchBlockCursorSkipSep(-1)
	}
}

// openScratchPaneForRow toggles the scratch split pane for the workspace of the given row.
// If scratch is already open for this workspace, closes it. Otherwise opens it,
// pre-fills /scratch in input, and focuses command pane.
func (m *Model) openScratchPaneForRow(row *wsRow) {
	if row == nil || row.wsIdx < 0 || row.wsIdx >= len(m.workspaceItems) {
		return
	}
	ws := m.workspaceItems[row.wsIdx]
	// Toggle off if already open for this workspace.
	if m.scratchOpen && m.scratchLoadedWs == ws.name {
		m.closeScratch()
		return
	}
	// Close existing scratch if open for a different workspace.
	if m.scratchOpen {
		m.closeScratch()
	}
	// Close askX and preview if open (mutual exclusion).
	if m.askxOpen {
		m.closeAskX()
	}
	if m.previewOpen {
		m.closePreview()
	}
	// Move cursor to this row's workspace so scratchWorkspace() resolves correctly.
	m.wsCursor = m.wsRowIndexForScratch(row.wsIdx)
	m.clampWsScroll()
	m.scratchOpen = true
	m.reloadScratchLines()
	m.scratchScrollToBottom()
	// Don't move focus or pre-fill input — let the user stay where they are.
	// m.focus = paneCommand
	// m.cursorVisible = true
	// m.input.SetValue("/scratch ")
	// m.input.CursorEnd()
	// m.cmdComplete = nil
	// m.cmdCompleteIdx = -1
	// m.paramItems = nil
	// m.paramIdx = -1
}

// wsRowIndexForScratch finds the wsRow index for the scratch row of the given workspace.
func (m *Model) wsRowIndexForScratch(wsIdx int) int {
	for i, r := range m.wsRows {
		if r.kind == wsRowScratch && r.wsIdx == wsIdx {
			return i
		}
	}
	return m.wsCursor // fallback: don't move
}

// cmdClearScratch clears the scratch file for the given workspace, with confirmation.
func (m *Model) cmdClearScratch(wsIdx int) {
	if wsIdx < 0 || wsIdx >= len(m.workspaceItems) {
		return
	}
	ws := m.workspaceItems[wsIdx]
	cfg := m.cfg
	prompt := fmt.Sprintf("clear scratch for workspace %q? (yes/N)", ws.name)
	m.askConfirm(prompt, func() tea.Cmd {
		return func() tea.Msg {
			if err := storefs.ClearScratch(cfg.DataRoot, ws.name); err != nil {
				return cmdDoneMsg{err: "clear scratch: " + err.Error()}
			}
			return cmdDoneMsg{statusMsg: fmt.Sprintf("✓ scratch cleared for %q", ws.name)}
		}
	})
}

// closeScratch closes the scratch pane.
func (m *Model) closeScratch() {
	m.scratchOpen = false
	m.scratchFocused = false
	m.scratchScroll = 0
	m.scratchLines = nil
	m.scratchBlocks = nil
	m.scratchBlockCursor = 0
	m.scratchCollapsed = nil
	m.scratchLoadedWs = ""
	m.scratchGlobal = false
	m.clearScratchInput()
}

// clearScratchInput clears the command input if it starts with "/scratch" or "/Scratch".
func (m *Model) clearScratchInput() {
	if strings.HasPrefix(m.input.Value(), "/scratch") || strings.HasPrefix(m.input.Value(), "/Scratch") {
		m.input.SetValue("")
		m.input.CursorEnd()
		m.syncInputHeight()
	}
}

// scratchFilePath returns the file path for the current scratch file.
func (m *Model) scratchFilePath() string {
	return storefs.ScratchPath(m.cfg.DataRoot, m.scratchWorkspace())
}

// handleScratchKey handles keys when the scratch pane is focused (in content pane).
// j/k navigate between blocks; s speaks, v opens overlay, d deletes the selected block.
func (m *Model) handleScratchKey(msg tea.KeyMsg) tea.Cmd {
	numBlocks := m.scratchSelectableCount()
	viewH := m.scratchViewH()

	switch {
	case msg.Type == tea.KeyRunes:
		switch msg.String() {
		case "s":
			return m.cmdScratchTTS()
		case "v":
			if len(m.scratchBlocks) > 0 {
				m.cmdScratchCollapseBlock(m.scratchBlockCursor)
			}
		case "x":
			return m.cmdScratchDeleteBlock()
		case "e":
			// Edit scratch file in $EDITOR.
			editor := os.Getenv("EDITOR")
			if editor == "" {
				m.setStatusError("$EDITOR is not set")
				return nil
			}
			path := m.scratchFilePath()
			ws := m.scratchWorkspace()
			label := "scratch"
			if ws != "" {
				label = ws + "/scratch"
			}
			m.openEditorInTerminal(editor, path, label)
		case "[":
			return m.cmdScratchTTSAdjustRate(-20)
		case "]":
			return m.cmdScratchTTSAdjustRate(+20)
		}
		return nil
	case key.Matches(msg, keys.NavUp):
		m.scratchBlockPrev()
		m.scrollToScratchBlock(viewH)
		return nil
	case key.Matches(msg, keys.NavDown):
		m.scratchBlockNext()
		m.scrollToScratchBlock(viewH)
		return nil
	case key.Matches(msg, keys.PageUp):
		for i := 0; i < viewH && m.scratchBlockCursor > 0; i++ {
			m.scratchBlockPrev()
		}
		m.scrollToScratchBlock(viewH)
	case key.Matches(msg, keys.PageDown):
		for i := 0; i < viewH && m.scratchBlockCursor < len(m.scratchBlocks)-1; i++ {
			m.scratchBlockNext()
		}
		m.scrollToScratchBlock(viewH)
	case key.Matches(msg, keys.Home):
		m.scratchBlockCursor = 0
		m.scratchBlockCursorSkipSep(1)
		m.scrollToScratchBlock(viewH)
	case key.Matches(msg, keys.End):
		if numBlocks > 0 {
			m.scratchBlockCursor = len(m.scratchBlocks) - 1
			m.scratchBlockCursorSkipSep(-1)
		}
		m.scrollToScratchBlock(viewH)
	case key.Matches(msg, keys.Back):
		// Esc unfocuses scratch, returns to content pane.
		m.scratchFocused = false
	case key.Matches(msg, keys.Command):
		m.focus = paneCommand
		m.cursorVisible = true
		if m.scratchGlobal {
			m.input.SetValue("/Scratch ")
		} else {
			m.input.SetValue("/scratch ")
		}
		m.input.CursorEnd()
	}
	return nil
}

// scratchSelectableCount returns the number of non-separator scratch blocks.
func (m *Model) scratchSelectableCount() int {
	n := 0
	for _, b := range m.scratchBlocks {
		if !b.isSep {
			n++
		}
	}
	return n
}

// scratchViewH returns the viewable height of the scratch pane content (excluding header).
func (m *Model) scratchViewH() int {
	mainH := m.height - 6 - m.completionCount()
	scratchH := mainH / 3
	if scratchH < 3 {
		scratchH = 3
	}
	return scratchH - 1
}

// scratchBlockPrev moves the block cursor to the previous selectable block.
func (m *Model) scratchBlockPrev() {
	c := m.scratchBlockCursor - 1
	for c >= 0 {
		if !m.scratchBlocks[c].isSep {
			m.scratchBlockCursor = c
			return
		}
		c--
	}
}

// scratchBlockNext moves the block cursor to the next selectable block.
func (m *Model) scratchBlockNext() {
	c := m.scratchBlockCursor + 1
	for c < len(m.scratchBlocks) {
		if !m.scratchBlocks[c].isSep {
			m.scratchBlockCursor = c
			return
		}
		c++
	}
}


// cmdScratchCollapseBlock toggles the collapsed state of block at blockIdx.
func (m *Model) cmdScratchCollapseBlock(blockIdx int) {
	if m.scratchCollapsed == nil {
		m.scratchCollapsed = make(map[int]bool)
	}
	m.scratchCollapsed[blockIdx] = !m.scratchCollapsed[blockIdx]
}

// buildScratchVLines builds the virtual display list for the scratch boxed view.
// Only the selected block gets a border; all others render as plain text.
// Returns nil when not in boxed mode (scratch not focused).
func (m Model) buildScratchVLines() []scratchVLine {
	if !m.scratchFocused || !m.scratchOpen || m.focus != paneContent {
		return nil
	}
	if len(m.scratchBlocks) == 0 {
		return nil
	}

	var vlines []scratchVLine
	for i, blk := range m.scratchBlocks {
		selected := i == m.scratchBlockCursor && !m.selectionMode
		collapsed := m.scratchCollapsed != nil && m.scratchCollapsed[i]

		if blk.isSep {
			// Date separator: render as plain line(s), never boxed.
			for li := blk.startLine; li <= blk.endLine; li++ {
				vlines = append(vlines, scratchVLine{isSep: true, lineIdx: li, blockIdx: i})
			}
			continue
		}

		totalLines := blk.endLine - blk.startLine + 1

		if selected {
			vlines = append(vlines, scratchVLine{isBoxTop: true, lineIdx: -1, blockIdx: i, isSelected: true})

			// Header with hints.
			expandHint := "v expand"
			if collapsed {
				expandHint = "v collapse"
			}
			hintsStr := expandHint + " · s speak · e edit · x delete"
			vlines = append(vlines, scratchVLine{isHeader: true, metaText: hintsStr, lineIdx: -1, blockIdx: i, isSelected: true})

			if collapsed {
				limit := blk.startLine + 1
				if limit > blk.endLine+1 {
					limit = blk.endLine + 1
				}
				for li := blk.startLine; li < limit; li++ {
					vlines = append(vlines, scratchVLine{lineIdx: li, blockIdx: i, isSelected: true})
				}
				if totalLines > 1 {
					vlines = append(vlines, scratchVLine{
						isEllipsis: true,
						metaText:   fmt.Sprintf("... (%d more lines)", totalLines-1),
						lineIdx:    -1, blockIdx: i, isSelected: true,
					})
				}
			} else {
				for li := blk.startLine; li <= blk.endLine; li++ {
					vlines = append(vlines, scratchVLine{lineIdx: li, blockIdx: i, isSelected: true})
				}
			}

			vlines = append(vlines, scratchVLine{isBoxBottom: true, lineIdx: -1, blockIdx: i, isSelected: true})
		} else {
			if collapsed {
				limit := blk.startLine + 1
				if limit > blk.endLine+1 {
					limit = blk.endLine + 1
				}
				for li := blk.startLine; li < limit; li++ {
					vlines = append(vlines, scratchVLine{lineIdx: li, blockIdx: i})
				}
				if totalLines > 1 {
					vlines = append(vlines, scratchVLine{
						isEllipsis: true,
						metaText:   fmt.Sprintf("... (%d more lines)", totalLines-1),
						lineIdx:    -1, blockIdx: i,
					})
				}
			} else {
				for li := blk.startLine; li <= blk.endLine; li++ {
					vlines = append(vlines, scratchVLine{lineIdx: li, blockIdx: i})
				}
			}
		}
	}
	return vlines
}

// scratchTotalVLines returns the total number of virtual lines for the scratch pane.
func (m *Model) scratchTotalVLines() int {
	if vlines := m.buildScratchVLines(); vlines != nil {
		return len(vlines)
	}
	return len(m.scratchLines)
}

// scrollToScratchBlock adjusts scratchScroll so that the selected block is visible
// using the virtual line list.
func (m *Model) scrollToScratchBlock(viewH int) {
	vlines := m.buildScratchVLines()
	if len(vlines) == 0 {
		return
	}
	first, last := -1, -1
	for i, vl := range vlines {
		if vl.blockIdx == m.scratchBlockCursor {
			if first == -1 {
				first = i
			}
			last = i
		}
	}
	if first == -1 {
		return
	}
	if first >= m.scratchScroll && last < m.scratchScroll+viewH {
		return
	}
	m.scratchScroll = first
	maxScroll := len(vlines) - viewH
	if maxScroll < 0 {
		maxScroll = 0
	}
	if m.scratchScroll > maxScroll {
		m.scratchScroll = maxScroll
	}
}

// cmdScratchDeleteBlock deletes the selected block from the scratch file.
func (m *Model) cmdScratchDeleteBlock() tea.Cmd {
	if m.scratchBlockCursor < 0 || m.scratchBlockCursor >= len(m.scratchBlocks) {
		return nil
	}
	blk := m.scratchBlocks[m.scratchBlockCursor]
	if blk.isSep {
		return nil
	}

	// Read raw file, find and remove the block line.
	ws := m.scratchWorkspace()
	content, err := storefs.ReadScratch(m.cfg.DataRoot, ws)
	if err != nil {
		m.setStatusError("delete: " + err.Error())
		return nil
	}
	rawLines := splitLines(content)
	// Match either bulleted or legacy (raw) form.
	// For multi-line blocks, find the first line then skip continuations.
	textLines := strings.Split(blk.text, "\n")
	bulletTarget := "• " + textLines[0]
	var newLines []string
	found := false
	skipping := false
	for _, l := range rawLines {
		if skipping {
			// Continue skipping until we hit a new bullet or separator.
			if strings.HasPrefix(l, "• ") || strings.HasPrefix(l, "----------") {
				skipping = false
				newLines = append(newLines, l)
			}
			continue
		}
		if !found && (l == bulletTarget || (len(textLines) == 1 && l == blk.text)) {
			found = true
			if len(textLines) > 1 {
				skipping = true
			}
			continue // skip this line
		}
		newLines = append(newLines, l)
	}
	if !found {
		m.setStatusError("block not found in file")
		return nil
	}

	// Write back.
	path := m.scratchFilePath()
	newContent := strings.Join(newLines, "\n")
	if len(newLines) > 0 {
		newContent += "\n"
	}
	if err := os.WriteFile(path, []byte(newContent), 0644); err != nil {
		m.setStatusError("delete: " + err.Error())
		return nil
	}

	// Shift collapsed keys.
	if m.scratchCollapsed != nil {
		newCollapsed := make(map[int]bool)
		for k, v := range m.scratchCollapsed {
			if k < m.scratchBlockCursor {
				newCollapsed[k] = v
			} else if k > m.scratchBlockCursor {
				newCollapsed[k-1] = v
			}
		}
		m.scratchCollapsed = newCollapsed
	}

	m.reloadScratchLines()
	m.statusMsg = "✓ deleted block"
	return nil
}

// cmdScratchTTS speaks the selected scratch block via TTS.
func (m *Model) cmdScratchTTS() tea.Cmd {
	if m.ttsPlayer.Playing() {
		m.stopTTS()
		m.statusMsg = ""
		return nil
	}

	if m.scratchBlockCursor < 0 || m.scratchBlockCursor >= len(m.scratchBlocks) {
		m.statusMsg = "nothing to speak"
		return nil
	}
	blk := m.scratchBlocks[m.scratchBlockCursor]
	if blk.isSep || blk.text == "" {
		m.statusMsg = "nothing to speak"
		return nil
	}

	text := tts.Strip(blk.text)
	m.contentTTSText = blk.text
	playFn := m.ttsPlayer.Play(text)
	m.ttsGen = m.ttsPlayer.Gen()
	m.ttsCurrentText = text

	return func() tea.Msg {
		done := playFn()
		return ttsDoneMsg{err: done.Err, gen: done.Gen}
	}
}

// cmdScratchTTSAdjustRate adjusts TTS rate while speaking a scratch block.
func (m *Model) cmdScratchTTSAdjustRate(delta int) tea.Cmd {
	if !m.ttsPlayer.Playing() || m.contentTTSText == "" {
		return nil
	}
	newRate := m.cfg.TTSRate + delta
	if m.cfg.TTSRate == 0 {
		newRate = 200 + delta
	}
	if newRate < 80 {
		newRate = 80
	}
	if newRate > 500 {
		newRate = 500
	}
	m.cfg.TTSRate = newRate
	m.ttsPlayer.SetRate(newRate)

	text := tts.Strip(m.contentTTSText)
	m.ttsPlayer.Stop()
	playFn := m.ttsPlayer.Play(text)
	m.ttsGen = m.ttsPlayer.Gen()
	m.ttsCurrentText = text

	return func() tea.Msg {
		done := playFn()
		return ttsDoneMsg{err: done.Err, gen: done.Gen}
	}
}

// ── Input correction (Ctrl+G) ────────────────────────────────────────────────

const defaultCorrectionPrompt = "Correct the spelling and grammar of the following text. " +
	"Return only the corrected text with no explanations, no quotes, and no additional commentary."

// doCorrection sends the input text to an LLM for spelling/grammar correction.
func doCorrection(text string, cfg config.Config) tea.Cmd {
	return func() tea.Msg {
		// Resolve which profile to use.
		profileCode := cfg.CorrectionProfile
		if profileCode == "" {
			profileCode = "oai-mini"
		}
		prof, ok := cfg.Profiles[profileCode]
		if !ok {
			// Try any available profile.
			for code, p := range cfg.Profiles {
				profileCode = code
				prof = p
				ok = true
				break
			}
		}
		if !ok {
			return correctionDoneMsg{err: fmt.Errorf("no LLM profiles configured")}
		}

		// Resolve system prompt.
		systemPrompt := cfg.CorrectionPrompt
		if systemPrompt == "" {
			systemPrompt = defaultCorrectionPrompt
		}

		apiKey := correctionResolveAPIKey(prof.Provider)
		prov, err := llm.New(llm.ProviderConfig{
			Provider: prof.Provider,
			Model:    prof.Model,
			Host:     prof.Host,
			APIKey:   apiKey,
			Think:    prof.Think,
		})
		if err != nil {
			return correctionDoneMsg{err: fmt.Errorf("correction: %w", err)}
		}

		msgs := []llm.Message{
			{Role: llm.RoleUser, Content: text},
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		response, _, err := prov.Chat(ctx, systemPrompt, msgs)
		if err != nil {
			return correctionDoneMsg{err: fmt.Errorf("correction: %w", err)}
		}
		return correctionDoneMsg{text: strings.TrimSpace(response)}
	}
}

// correctionResolveAPIKey returns the API key for the given provider from env vars.
func correctionResolveAPIKey(provider string) string {
	switch strings.ToLower(provider) {
	case "anthropic":
		for _, k := range []string{"ARC_ANTHROPIC_API_KEY", "ANTHROPIC_API_KEY"} {
			if v := strings.TrimSpace(os.Getenv(k)); v != "" {
				return v
			}
		}
	case "openai":
		for _, k := range []string{"ARC_OPENAI_API_KEY", "OPENAI_API_KEY"} {
			if v := strings.TrimSpace(os.Getenv(k)); v != "" {
				return v
			}
		}
	}
	return ""
}

// ── TTS ─────────────────────────────────────────────────────────────────────

// cmdChatTTS plays or stops TTS for the selected chat box.
// Uses the same paragraph-block queue as the resource overlay: each block is
// spoken in turn, the line cursor advances with playback, and rate changes
// restart only the current block.
func (m *Model) cmdChatTTS() tea.Cmd {
	// Toggle: if already playing, stop.
	if m.ttsPlayer.Playing() {
		m.stopTTS()
		m.chatAutoScroll = false
		m.statusMsg = ""
		return nil
	}

	// Guard: refuse on the actively-streaming box.
	infos := m.chatBoxInfos()
	if m.chatBoxCursor < 0 || m.chatBoxCursor >= len(infos) {
		m.statusMsg = "nothing to speak"
		return nil
	}
	if m.chatStreaming && m.chatBoxCursor == len(infos)-1 {
		m.statusMsg = "cannot speak while streaming"
		return nil
	}

	// Find this box's line range in chatDisplayLines (same logic as buildChatVLines).
	dl := m.chatDisplayLines
	type boxBound struct{ start, end int }
	var bounds []boxBound
	for i, cl := range dl {
		if cl.role == chatLineUser && (i == 0 || dl[i-1].role != chatLineUser) {
			bounds = append(bounds, boxBound{i, len(dl)})
			if len(bounds) > 1 {
				bounds[len(bounds)-2].end = i
			}
		} else if cl.role == chatLineNote && (i == 0 || dl[i-1].role != chatLineNote) {
			bounds = append(bounds, boxBound{i, len(dl)})
			if len(bounds) > 1 {
				bounds[len(bounds)-2].end = i
			}
		}
	}
	if m.chatBoxCursor >= len(bounds) {
		m.statusMsg = "nothing to speak"
		return nil
	}
	b := bounds[m.chatBoxCursor]
	// Trim trailing blank lines.
	trimEnd := b.end
	for trimEnd > b.start && dl[trimEnd-1].role == chatLineBlank {
		trimEnd--
	}

	// Extract plain text from the box's display lines.
	boxLines := make([]string, trimEnd-b.start)
	for i := b.start; i < trimEnd; i++ {
		boxLines[i-b.start] = dl[i].text
	}

	blocks := buildResourceTTSBlocks(boxLines, 0)
	if len(blocks) == 0 {
		m.statusMsg = "nothing to speak"
		return nil
	}

	// Offset cursorLine values from box-relative to absolute chatDisplayLines index.
	base := b.start
	for i := range blocks {
		blocks[i].cursorLine += base
	}

	m.chatTTSBoxIdx = m.chatBoxCursor
	m.chatTTSCursor = blocks[0].cursorLine
	m.chatTTSText = blocks[0].text
	m.chatTTSQueue = blocks[1:]
	m.chatAutoScroll = false

	viewH := m.height - 4
	if viewH < 1 {
		viewH = 1
	}
	m.scrollToChatTTSLine(viewH)

	text := tts.Strip(m.chatTTSText)
	m.ttsCurrentText = text
	playFn := m.ttsPlayer.Play(text)
	m.ttsGen = m.ttsPlayer.Gen()
	m.statusMsg = ""

	return func() tea.Msg {
		done := playFn()
		return ttsDoneMsg{err: done.Err, gen: done.Gen}
	}
}

// scrollToChatTTSLine adjusts m.chatScroll so that the current TTS cursor line
// (m.chatTTSCursor, an absolute index into chatDisplayLines) is visible.
func (m *Model) scrollToChatTTSLine(viewH int) {
	vlines := m.buildChatVLines()
	if vlines == nil {
		// Flat mode: chatScroll is an offset into chatDisplayLines.
		if m.chatTTSCursor < m.chatScroll {
			m.chatScroll = m.chatTTSCursor
		} else if m.chatTTSCursor >= m.chatScroll+viewH {
			m.chatScroll = m.chatTTSCursor - viewH + 1
		}
		return
	}
	// Boxed mode: chatScroll is an offset into vlines.
	// Find the vline whose contentIdx matches m.chatTTSCursor.
	for vi, vl := range vlines {
		if vl.contentIdx == m.chatTTSCursor {
			if vi < m.chatScroll {
				m.chatScroll = vi
			} else if vi >= m.chatScroll+viewH {
				m.chatScroll = vi - viewH + 1
			}
			return
		}
	}
}

// cmdChatTTSAdjustRate changes the TTS rate and restarts playback of the
// current chat block only. No-op if not playing.
func (m *Model) cmdChatTTSAdjustRate(delta int) tea.Cmd {
	if !m.ttsPlayer.Playing() || m.chatTTSText == "" {
		return nil
	}

	newRate := m.cfg.TTSRate + delta
	if m.cfg.TTSRate == 0 {
		newRate = 200 + delta
	}
	if newRate < 80 {
		newRate = 80
	}
	if newRate > 500 {
		newRate = 500
	}
	m.cfg.TTSRate = newRate
	m.ttsPlayer.SetRate(newRate)
	m.ttsPlayer.Stop()

	text := tts.Strip(m.chatTTSText)
	playFn := m.ttsPlayer.Play(text)
	m.ttsGen = m.ttsPlayer.Gen()
	m.ttsCurrentText = text

	return func() tea.Msg {
		done := playFn()
		return ttsDoneMsg{err: done.Err, gen: done.Gen}
	}
}

// ── Preview TTS ─────────────────────────────────────────────────────────────

// cmdPreviewTTS plays or stops TTS for the preview pane, using the same
// paragraph-block queue as the resource overlay.
func (m *Model) cmdPreviewTTS() tea.Cmd {
	if m.ttsPlayer.Playing() {
		m.stopTTS()
		m.statusMsg = ""
		return nil
	}

	blocks := buildResourceTTSBlocks(m.previewLines, m.previewLineCursor)
	if len(blocks) == 0 {
		m.statusMsg = "nothing to speak"
		return nil
	}

	m.previewLineCursor = blocks[0].cursorLine
	m.previewTTSText = blocks[0].text
	m.previewTTSQueue = blocks[1:]

	viewH := m.previewViewH()
	m.scrollPreviewToCursor(viewH)

	text := tts.Strip(m.previewTTSText)
	m.ttsCurrentText = text
	playFn := m.ttsPlayer.Play(text)
	m.ttsGen = m.ttsPlayer.Gen()
	m.statusMsg = ""

	return func() tea.Msg {
		done := playFn()
		return ttsDoneMsg{err: done.Err, gen: done.Gen}
	}
}

// scrollPreviewToCursor adjusts m.previewScroll so that m.previewLineCursor is visible.
func (m *Model) scrollPreviewToCursor(viewH int) {
	if m.previewLineCursor < m.previewScroll {
		m.previewScroll = m.previewLineCursor
	} else if m.previewLineCursor >= m.previewScroll+viewH {
		m.previewScroll = m.previewLineCursor - viewH + 1
	}
}

// cmdPreviewTTSAdjustRate changes the TTS rate and restarts playback of the
// current preview block only. No-op if not playing.
func (m *Model) cmdPreviewTTSAdjustRate(delta int) tea.Cmd {
	if !m.ttsPlayer.Playing() || m.previewTTSText == "" {
		return nil
	}

	newRate := m.cfg.TTSRate + delta
	if m.cfg.TTSRate == 0 {
		newRate = 200 + delta
	}
	if newRate < 80 {
		newRate = 80
	}
	if newRate > 500 {
		newRate = 500
	}
	m.cfg.TTSRate = newRate
	m.ttsPlayer.SetRate(newRate)
	m.ttsPlayer.Stop()

	text := tts.Strip(m.previewTTSText)
	playFn := m.ttsPlayer.Play(text)
	m.ttsGen = m.ttsPlayer.Gen()
	m.ttsCurrentText = text

	return func() tea.Msg {
		done := playFn()
		return ttsDoneMsg{err: done.Err, gen: done.Gen}
	}
}

// ── Resource TTS ────────────────────────────────────────────────────────────

// buildResourceTTSBlocks splits resource lines into paragraph-level TTS blocks
// starting from fromLine. Each block tracks the last source line index so the
// cursor can follow along during playback.
func buildResourceTTSBlocks(lines []string, fromLine int) []resourceTTSBlock {
	var blocks []resourceTTSBlock
	var current []string
	var lastIdx int

	flush := func() {
		joined := strings.TrimSpace(strings.Join(current, " "))
		if joined != "" && tts.Strip(joined) != "" {
			blocks = append(blocks, resourceTTSBlock{text: joined, cursorLine: lastIdx})
		}
		current = current[:0]
	}

	for i := fromLine; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])

		if trimmed == "" {
			flush()
			continue
		}

		isHeading := strings.HasPrefix(trimmed, "#")
		isList := len(trimmed) > 0 && (trimmed[0] == '-' || trimmed[0] == '*' ||
			(trimmed[0] >= '0' && trimmed[0] <= '9'))
		isCodeFence := strings.HasPrefix(trimmed, "```")

		if isHeading || isCodeFence || isList {
			flush()
			lastIdx = i
			current = append(current, trimmed)
			flush()
			continue
		}

		lastIdx = i
		current = append(current, trimmed)

		last := trimmed[len(trimmed)-1]
		if last == '?' || last == '!' {
			flush()
		}
	}
	flush()
	return blocks
}

// cmdResourceTTS plays or stops TTS from the current cursor in the resource overlay.
func (m *Model) cmdResourceTTS(viewH int) tea.Cmd {
	if m.ttsPlayer.Playing() {
		m.stopTTS()
		return nil
	}

	blocks := buildResourceTTSBlocks(m.resourceLines, m.resourceCursor)
	if len(blocks) == 0 {
		m.statusMsg = "nothing to speak"
		return nil
	}

	m.resourceTTSQueue = blocks[1:]
	m.resourceCursor = blocks[0].cursorLine
	m.resourceTTSText = blocks[0].text
	m.scrollResourceToCursor(viewH)

	text := tts.Strip(m.resourceTTSText)
	playFn := m.ttsPlayer.Play(text)
	m.ttsGen = m.ttsPlayer.Gen()
	m.ttsCurrentText = text

	return func() tea.Msg {
		done := playFn()
		return ttsDoneMsg{err: done.Err, gen: done.Gen}
	}
}

// cmdResourceTTSAdjustRate changes the TTS rate and restarts playback of the
// current resource block. No-op if not playing.
func (m *Model) cmdResourceTTSAdjustRate(delta, viewH int) tea.Cmd {
	if !m.ttsPlayer.Playing() || m.resourceTTSText == "" {
		return nil
	}

	newRate := m.cfg.TTSRate + delta
	if m.cfg.TTSRate == 0 {
		newRate = 200 + delta
	}
	if newRate < 80 {
		newRate = 80
	}
	if newRate > 500 {
		newRate = 500
	}
	m.cfg.TTSRate = newRate
	m.ttsPlayer.SetRate(newRate)

	m.ttsPlayer.Stop()

	text := tts.Strip(m.resourceTTSText)
	playFn := m.ttsPlayer.Play(text)
	m.ttsGen = m.ttsPlayer.Gen()
	m.ttsCurrentText = text

	return func() tea.Msg {
		done := playFn()
		return ttsDoneMsg{err: done.Err, gen: done.Gen}
	}
}

// ── Content pane TTS ────────────────────────────────────────────────────────

// cmdContentTTS plays or stops TTS from the current scroll position in the content pane.
func (m *Model) cmdContentTTS() tea.Cmd {
	if m.ttsPlayer.Playing() {
		m.stopTTS()
		m.statusMsg = ""
		return nil
	}

	if len(m.contentLines) == 0 {
		m.statusMsg = "nothing to speak"
		return nil
	}

	blocks := buildResourceTTSBlocks(m.contentLines, m.contentLineCursor)
	if len(blocks) == 0 {
		m.statusMsg = "nothing to speak"
		return nil
	}

	m.contentTTSQueue = blocks[1:]
	m.contentLineCursor = blocks[0].cursorLine
	viewH := m.contentViewHeight()
	m.scrollContentToCursor(viewH)
	m.contentTTSText = blocks[0].text

	text := tts.Strip(m.contentTTSText)
	playFn := m.ttsPlayer.Play(text)
	m.ttsGen = m.ttsPlayer.Gen()
	m.ttsCurrentText = text

	return func() tea.Msg {
		done := playFn()
		return ttsDoneMsg{err: done.Err, gen: done.Gen}
	}
}

// scrollContentToCursor adjusts m.contentScroll so that m.contentLineCursor is visible.
func (m *Model) scrollContentToCursor(viewH int) {
	if m.contentLineCursor < m.contentScroll {
		m.contentScroll = m.contentLineCursor
	} else if m.contentLineCursor >= m.contentScroll+viewH {
		m.contentScroll = m.contentLineCursor - viewH + 1
	}
}

// cmdContentTTSAdjustRate changes the TTS rate and restarts playback of the
// current content block. No-op if not playing.
func (m *Model) cmdContentTTSAdjustRate(delta int) tea.Cmd {
	if !m.ttsPlayer.Playing() || m.contentTTSText == "" {
		return nil
	}

	newRate := m.cfg.TTSRate + delta
	if m.cfg.TTSRate == 0 {
		newRate = 200 + delta
	}
	if newRate < 80 {
		newRate = 80
	}
	if newRate > 500 {
		newRate = 500
	}
	m.cfg.TTSRate = newRate
	m.ttsPlayer.SetRate(newRate)

	m.ttsPlayer.Stop()

	text := tts.Strip(m.contentTTSText)
	playFn := m.ttsPlayer.Play(text)
	m.ttsGen = m.ttsPlayer.Gen()
	m.ttsCurrentText = text

	return func() tea.Msg {
		done := playFn()
		return ttsDoneMsg{err: done.Err, gen: done.Gen}
	}
}

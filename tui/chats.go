package tui

import (
	"fmt"
	"os"
	"strings"
	"time"

	storefs "github.com/jrniemiec/arc/store/fs"
)

// cmdChatsArchive syncs pending AskX + article chat messages into chat_archive.jsonl.
func (m *Model) cmdChatsArchive() {
	res, err := m.svc.SyncChats()
	if err != nil {
		m.setStatusError("chats-archive: " + err.Error())
		return
	}
	if res.Messages == 0 {
		m.setStatusLines([]string{"chats-archive: nothing new to archive"})
		return
	}
	m.setStatusLines([]string{fmt.Sprintf(
		"chats-archive: archived %d message(s) from %d source(s)",
		res.Messages, res.Sources,
	)})
}

// cmdChatsHistory opens the full-screen archive viewer overlay, scrolled to the bottom.
func (m *Model) cmdChatsHistory() {
	entries, err := storefs.ReadAllArchiveEntries(m.cfg.DataRoot)
	if err != nil {
		m.setStatusError("chats-history: " + err.Error())
		return
	}
	if len(entries) == 0 {
		m.setStatusLines([]string{"chats-history: archive is empty — run /chats-archive first"})
		return
	}
	lines := renderArchiveLines(entries)
	m.openResourceOverlay("chat archive", strings.Join(lines, "\n"))
	// Scroll to bottom so the most recent content is visible.
	last := len(m.resourceLines) - 1
	if last < 0 {
		last = 0
	}
	m.resourceCursor = last
	contentH := m.height - 4
	if contentH < 1 {
		contentH = 1
	}
	// Position cursor at ~2/3 down the viewport so there's context above it.
	scroll := last - (contentH * 2 / 3)
	if scroll < 0 {
		scroll = 0
	}
	m.resourceScroll = scroll
}

// cmdChatsExport renders the archive to a file and opens it in $EDITOR.
// flag: "" = use config default, "--md" = markdown, "--text" = plain text.
func (m *Model) cmdChatsExport(flag string) {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		m.setStatusError("chats-export: $EDITOR is not set")
		return
	}

	entries, err := storefs.ReadAllArchiveEntries(m.cfg.DataRoot)
	if err != nil {
		m.setStatusError("chats-export: " + err.Error())
		return
	}
	if len(entries) == 0 {
		m.setStatusLines([]string{"chats-export: archive is empty — run /chats-archive first"})
		return
	}

	// Determine format.
	format := m.cfg.ChatExportFormat
	if format == "" {
		format = "text"
	}
	switch flag {
	case "--md", "--markdown":
		format = "markdown"
	case "--text":
		format = "text"
	}

	// Render and write.
	dateStr := time.Now().Format("2006-01-02")
	var content, ext string
	if format == "markdown" {
		content = renderArchiveMarkdown(entries, dateStr)
		ext = ".md"
	} else {
		content = renderArchiveText(entries, dateStr)
		ext = ".txt"
	}

	outPath := fmt.Sprintf("%s/chat_export_%s%s", m.cfg.DataRoot, dateStr, ext)
	if err := os.WriteFile(outPath, []byte(content), 0644); err != nil {
		m.setStatusError("chats-export: write file: " + err.Error())
		return
	}

	m.setStatusLines([]string{fmt.Sprintf("chats-export: wrote %s — opening in $EDITOR", outPath)})
	m.openEditorInTerminal(editor, outPath, "chat export")
}

// renderArchiveLines renders archive entries as lines for the paneResource overlay.
// Oldest first. Notes are prefixed with "Note:".
func renderArchiveLines(entries []storefs.ArchiveEntry) []string {
	var lines []string

	for _, e := range entries {
		if e.Type == "archive-run" {
			lines = append(lines, "", archiveRunHeader(e.ArchivedAt), "")
			continue
		}
		lines = append(lines, entryHeader(e), "")
		for _, msg := range e.Messages {
			switch msg.Role {
			case "user":
				lines = append(lines, "You: "+msg.Content, "")
			case "assistant":
				lines = append(lines, "Assistant: "+msg.Content, "")
			case "note":
				lines = append(lines, "📌 "+msg.Content, "")
			}
		}
	}
	return lines
}

// renderArchiveText renders archive entries as plain text for export.
func renderArchiveText(entries []storefs.ArchiveEntry, dateStr string) string {
	var sb strings.Builder
	sb.WriteString("Arc Chat Archive\n")
	sb.WriteString("Exported: " + dateStr + "\n")

	for _, e := range entries {
		if e.Type == "archive-run" {
			sb.WriteString("\n" + archiveRunHeader(e.ArchivedAt) + "\n")
			continue
		}
		sb.WriteString("\n== " + entryHeaderText(e) + " ==\n\n")
		for _, msg := range e.Messages {
			switch msg.Role {
			case "user":
				sb.WriteString("You: " + msg.Content + "\n\n")
			case "assistant":
				sb.WriteString("Assistant: " + msg.Content + "\n\n")
			case "note":
				sb.WriteString("📌 " + msg.Content + "\n\n")
			}
		}
	}
	return sb.String()
}

// renderArchiveMarkdown renders archive entries as Markdown for export.
func renderArchiveMarkdown(entries []storefs.ArchiveEntry, dateStr string) string {
	var sb strings.Builder
	sb.WriteString("# Arc Chat Archive\n")
	sb.WriteString("Exported: " + dateStr + "\n")

	for _, e := range entries {
		if e.Type == "archive-run" {
			sb.WriteString("\n---\n*" + archiveRunHeader(e.ArchivedAt) + "*\n\n")
			continue
		}
		sb.WriteString("\n## " + entryHeaderText(e) + "\n\n")
		for _, msg := range e.Messages {
			switch msg.Role {
			case "user":
				sb.WriteString("**You:** " + msg.Content + "\n\n")
			case "assistant":
				sb.WriteString("**Assistant:** " + msg.Content + "\n\n")
			case "note":
				sb.WriteString("> 📌 " + msg.Content + "\n\n")
			}
		}
		sb.WriteString("---\n")
	}
	return sb.String()
}

// entryHeader returns the display header line for the overlay view.
func entryHeader(e storefs.ArchiveEntry) string {
	return "── " + entryHeaderText(e) + " ──"
}

// entryHeaderText returns a concise label for the entry (used in both formats).
func entryHeaderText(e storefs.ArchiveEntry) string {
	timeRange := formatTimeRange(e.DateFrom, e.DateTo)
	if e.Type == "article" {
		label := e.Title
		if label == "" {
			label = e.Slug
		}
		return "Article: " + label + " — " + timeRange
	}
	return "AskX — " + timeRange
}

// archiveRunHeader returns a visual delimiter for an archive-run entry.
func archiveRunHeader(ts time.Time) string {
	label := "Archive run: " + ts.Local().Format("Jan 2, 2006 15:04")
	pad := strings.Repeat("=", 10)
	return pad + " " + label + " " + pad
}

func formatTimeRange(from, to time.Time) string {
	if from.IsZero() {
		return "unknown"
	}
	date := from.Local().Format("Jan 2, 2006")
	t1 := from.Local().Format("15:04")
	if to.IsZero() || to.Equal(from) {
		return date + " " + t1
	}
	// Same day: "Jan 2, 2006 09:00–09:45"
	if from.Local().Format("2006-01-02") == to.Local().Format("2006-01-02") {
		return date + " " + t1 + "–" + to.Local().Format("15:04")
	}
	return date + " " + t1 + " – " + to.Local().Format("Jan 2, 2006 15:04")
}

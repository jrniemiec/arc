// Package clog provides a thin wrapper around slog with lumberjack rotation.
// Call Init once from main before any logging. All packages log via the slog
// default logger so Init affects the whole process.
package clog

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
)

const maxSizeMB = 50

var (
	writer   *lumberjack.Logger
	isNew    bool       // true when the log file was absent or empty at Init time
	logLevel slog.Level // level set by Init
	verbose  bool       // true when --verbose is active
)

// ParseLevel converts a string (debug|info|warn|error) to a slog.Level.
// Returns slog.LevelInfo and false if the string is unrecognised.
func ParseLevel(s string) (slog.Level, bool) {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug, true
	case "info":
		return slog.LevelInfo, true
	case "warn", "warning":
		return slog.LevelWarn, true
	case "error":
		return slog.LevelError, true
	default:
		return slog.LevelInfo, false
	}
}

// Init opens path for append with automatic rotation and sets it as the slog
// default handler at the given level. When verb is true, a second handler
// mirrors debug-level output to stderr. Safe to call multiple times.
func Init(path string, level slog.Level, verb bool) {
	fi, err := os.Stat(path)
	// Treat the file as new if absent, empty, or a tiny leftover fragment.
	isNew = err != nil || fi.Size() == 0
	if err == nil && fi.Size() > 0 && fi.Size() < 32 {
		_ = os.Truncate(path, 0)
		isNew = true
	}

	verbose = verb

	// When verbose, force file logger to debug so it captures everything.
	fileLevel := level
	if verbose && fileLevel > slog.LevelDebug {
		fileLevel = slog.LevelDebug
	}

	logLevel = fileLevel
	writer = &lumberjack.Logger{
		Filename:   path,
		MaxSize:    maxSizeMB,
		MaxBackups: 2,
		Compress:   false,
	}

	fileHandler := slog.NewTextHandler(writer, &slog.HandlerOptions{Level: fileLevel})

	if verbose {
		stderrHandler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})
		slog.SetDefault(slog.New(&multiHandler{handlers: []slog.Handler{fileHandler, stderrHandler}}))
	} else {
		slog.SetDefault(slog.New(fileHandler))
	}
}

// IsNew reports whether the log file was absent or empty when Init was called.
func IsNew() bool { return isNew }

// IsDebug reports whether the log level is DEBUG or lower.
func IsDebug() bool { return logLevel <= slog.LevelDebug }

// IsVerbose reports whether --verbose mode is active.
func IsVerbose() bool { return verbose }

// Raw writes text directly to the log file without slog quoting or escaping.
// label appears as a header so the block is easy to find in the log.
// When verbose mode is active, also writes to stderr.
func Raw(label, text string) {
	if writer == nil {
		return
	}
	ts := time.Now().Format("2006-01-02T15:04:05.000Z07:00")
	block := fmt.Sprintf("\n%s [RAW] %s\n%s\n", ts, label, text)
	_, _ = writer.Write([]byte(block))
	if verbose {
		fmt.Fprint(os.Stderr, block)
	}
}

// Structured variants — preferred for new code.
func Debug(msg string, args ...any) { slog.Debug(msg, args...) }
func Info(msg string, args ...any)  { slog.Info(msg, args...) }
func Warn(msg string, args ...any)  { slog.Warn(msg, args...) }
func Error(msg string, args ...any) { slog.Error(msg, args...) }

// Printf-style variants — used by existing call sites.
func Debugf(format string, args ...any) { slog.Debug(fmt.Sprintf(format, args...)) }
func Infof(format string, args ...any)  { slog.Info(fmt.Sprintf(format, args...)) }
func Warnf(format string, args ...any)  { slog.Warn(fmt.Sprintf(format, args...)) }
func Errorf(format string, args ...any) { slog.Error(fmt.Sprintf(format, args...)) }

// multiHandler fans out log records to multiple slog.Handlers.
type multiHandler struct {
	handlers []slog.Handler
}

func (m *multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range m.handlers {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (m *multiHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, h := range m.handlers {
		if h.Enabled(ctx, r.Level) {
			if err := h.Handle(ctx, r); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	handlers := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		handlers[i] = h.WithAttrs(attrs)
	}
	return &multiHandler{handlers: handlers}
}

func (m *multiHandler) WithGroup(name string) slog.Handler {
	handlers := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		handlers[i] = h.WithGroup(name)
	}
	return &multiHandler{handlers: handlers}
}

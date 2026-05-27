// Package clog provides a thin wrapper around slog with lumberjack rotation.
// Call Init once from main before any logging. All packages log via the slog
// default logger so Init affects the whole process.
package clog

import (
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
	isNew    bool        // true when the log file was absent or empty at Init time
	logLevel slog.Level  // level set by Init
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
// default handler at the given level. Safe to call multiple times.
func Init(path string, level slog.Level) {
	fi, err := os.Stat(path)
	// Treat the file as new if absent, empty, or a tiny leftover fragment.
	isNew = err != nil || fi.Size() == 0
	if err == nil && fi.Size() > 0 && fi.Size() < 32 {
		_ = os.Truncate(path, 0)
		isNew = true
	}

	logLevel = level
	writer = &lumberjack.Logger{
		Filename:   path,
		MaxSize:    maxSizeMB,
		MaxBackups: 2,
		Compress:   false,
	}
	h := slog.NewTextHandler(writer, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(h))
}

// IsNew reports whether the log file was absent or empty when Init was called.
func IsNew() bool { return isNew }

// IsDebug reports whether the log level is DEBUG or lower.
func IsDebug() bool { return logLevel <= slog.LevelDebug }

// Raw writes text directly to the log file without slog quoting or escaping.
// label appears as a header so the block is easy to find in the log.
func Raw(label, text string) {
	if writer == nil {
		return
	}
	ts := time.Now().Format("2006-01-02T15:04:05.000Z07:00")
	block := fmt.Sprintf("\n%s [RAW] %s\n%s\n", ts, label, text)
	_, _ = writer.Write([]byte(block))
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

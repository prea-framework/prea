// Package logger is the framework logger: leveled and structured (log/slog).
// It starts at info, in text; Configure changes it at startup.
package logger

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// Level order: Debug < Info < Success < Warn < Error.
const (
	LevelDebug   = slog.LevelDebug
	LevelInfo    = slog.LevelInfo
	LevelSuccess = slog.Level(2)
	LevelWarn    = slog.LevelWarn
	LevelError   = slog.LevelError
)

// Log is the logger behind the package functions. It is also slog's default,
// so log.Printf and slog.Info from any library end up here.
var Log *slog.Logger

func init() {
	Configure("info", "text")
}

// Configure replaces Log (and slog's default) with one writing to stdout.
// level: debug | info | success | warn | error. format: text | json.
// Text is colored when stdout is a terminal, unless NO_COLOR is set.
func Configure(level, format string) error {
	l, err := ParseLevel(level)
	if err != nil {
		return err
	}
	if format != "" && format != "text" && format != "json" {
		return fmt.Errorf("logger: invalid format %q (text or json)", format)
	}
	var w io.Writer = os.Stdout
	if format != "json" && colorTerminal() {
		w = colorWriter{os.Stdout}
	}
	Log = New(w, l, format == "json")
	slog.SetDefault(Log)
	return nil
}

// New builds a logger; use it in tests or to log somewhere else.
func New(w io.Writer, level slog.Level, json bool) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level, ReplaceAttr: levelName}
	if json {
		return slog.New(slog.NewJSONHandler(w, opts))
	}
	return slog.New(slog.NewTextHandler(w, opts))
}

func ParseLevel(name string) (slog.Level, error) {
	switch strings.ToLower(name) {
	case "debug":
		return LevelDebug, nil
	case "", "info":
		return LevelInfo, nil
	case "success":
		return LevelSuccess, nil
	case "warn":
		return LevelWarn, nil
	case "error":
		return LevelError, nil
	}
	return 0, fmt.Errorf("logger: invalid level %q (debug, info, success, warn or error)", name)
}

func levelName(_ []string, a slog.Attr) slog.Attr {
	if a.Key == slog.LevelKey && a.Value.Any() == LevelSuccess {
		a.Value = slog.StringValue("SUCCESS")
	}
	return a
}

func colorTerminal() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// ANSI colors of each level; the time is dimmed.
var levelColors = map[string]string{
	"DEBUG":   "\x1b[90m",
	"INFO":    "\x1b[32m",
	"SUCCESS": "\x1b[1;32m",
	"WARN":    "\x1b[33m",
	"ERROR":   "\x1b[31m",
}

const dim, reset = "\x1b[2m", "\x1b[0m"

// colorWriter colors the time and the level of each line the text handler
// writes, which always start with time=... level=....
type colorWriter struct{ w io.Writer }

func (c colorWriter) Write(p []byte) (int, error) {
	line := p
	var out []byte
	if bytes.HasPrefix(line, []byte("time=")) {
		end := bytes.IndexByte(line, ' ') + 1
		out = append(append(append(out, dim...), line[:end-1]...), reset+" "...)
		line = line[end:]
	}
	if level, rest, ok := bytes.Cut(line, []byte(" ")); ok && bytes.HasPrefix(level, []byte("level=")) {
		if color, ok := levelColors[string(level[len("level="):])]; ok {
			out = append(append(append(out, color...), level...), reset+" "...)
			line = rest
		}
	}
	if _, err := c.w.Write(append(out, line...)); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Args are key/value pairs: logger.Info("user created", "id", u.ID).

func Debug(msg string, args ...any)   { Log.Debug(msg, args...) }
func Info(msg string, args ...any)    { Log.Info(msg, args...) }
func Success(msg string, args ...any) { Log.Log(context.Background(), LevelSuccess, msg, args...) }
func Warn(msg string, args ...any)    { Log.Warn(msg, args...) }
func Error(msg string, args ...any)   { Log.Error(msg, args...) }

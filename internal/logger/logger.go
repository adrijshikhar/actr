package logger

import (
	"io"
	"log/slog"
	"os"
	"sync"
)

var (
	mu      sync.RWMutex
	handler *slog.Logger
)

func init() {
	Init(false, os.Stderr)
}

func Init(debug bool, out io.Writer) {
	if out == nil {
		out = io.Discard
	}

	mu.Lock()
	defer mu.Unlock()

	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}

	opts := &slog.HandlerOptions{
		Level: level,
	}
	handler = slog.New(slog.NewTextHandler(out, opts))
}

func Debug(msg string, args ...any) {
	mu.RLock()
	h := handler
	mu.RUnlock()
	if h != nil {
		h.Debug(msg, args...)
	}
}

func Info(msg string, args ...any) {
	mu.RLock()
	h := handler
	mu.RUnlock()
	if h != nil {
		h.Info(msg, args...)
	}
}

func Warn(msg string, args ...any) {
	mu.RLock()
	h := handler
	mu.RUnlock()
	if h != nil {
		h.Warn(msg, args...)
	}
}

func Error(msg string, args ...any) {
	mu.RLock()
	h := handler
	mu.RUnlock()
	if h != nil {
		h.Error(msg, args...)
	}
}

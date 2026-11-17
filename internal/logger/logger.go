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
	defer mu.RUnlock()
	handler.Debug(msg, args...)
}

func Info(msg string, args ...any) {
	mu.RLock()
	defer mu.RUnlock()
	handler.Info(msg, args...)
}

func Warn(msg string, args ...any) {
	mu.RLock()
	defer mu.RUnlock()
	handler.Warn(msg, args...)
}

func Error(msg string, args ...any) {
	mu.RLock()
	defer mu.RUnlock()
	handler.Error(msg, args...)
}

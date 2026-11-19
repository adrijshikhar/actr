package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/adrijshikhar/actr/internal/demo"
	"github.com/adrijshikhar/actr/internal/history"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "demo-runner error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	sockPath := "/tmp/actr-demo.sock"
	if len(os.Args) > 1 {
		sockPath = filepath.Clean(os.Args[1])
	}

	var dbPath string
	var cleanupDir string
	if len(os.Args) > 2 {
		dbPath = filepath.Clean(os.Args[2])
	} else if env := os.Getenv("ACTR_DB_PATH"); env != "" {
		dbPath = env
	} else {
		tmpDir, err := os.MkdirTemp("", "actr-demo-db-*")
		if err != nil {
			return fmt.Errorf("failed to create demo temp dir: %w", err)
		}
		cleanupDir = tmpDir
		dbPath = filepath.Join(tmpDir, "demo_history.db")
	}
	if cleanupDir != "" {
		defer func() { _ = os.RemoveAll(cleanupDir) }()
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	var store history.Store
	if s, err := history.NewSQLiteStore(dbPath); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to initialize history store at %s: %v\n", dbPath, err)
	} else {
		store = s
		defer func() { _ = s.Close() }()
		if err := demo.SeedHistoryStore(store); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to seed history store: %v\n", err)
		}
	}

	server, err := demo.StartServerWithStore(ctx, sockPath, store)
	if err != nil {
		return fmt.Errorf("failed to start demo server: %w", err)
	}
	defer func() { _ = server.Stop() }()

	fmt.Printf("Demo runner daemon listening on %s (PID %d)\n", sockPath, os.Getpid())
	fmt.Printf("Historical runs seeded in: %s\n", dbPath)
	fmt.Printf("View dashboard in another terminal with:\n  actr monitor --sock %s\n", sockPath)

	<-ctx.Done()
	fmt.Println("\nShutting down demo runner...")
	return nil
}

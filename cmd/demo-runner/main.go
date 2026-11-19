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
			fmt.Fprintf(os.Stderr, "failed to create demo temp dir: %v\n", err)
			os.Exit(1)
		}
		cleanupDir = tmpDir
		dbPath = filepath.Join(tmpDir, "demo_history.db")
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
	if cleanupDir != "" {
		defer func() { _ = os.RemoveAll(cleanupDir) }()
	}

	server, err := demo.StartServerWithStore(ctx, sockPath, store)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to start demo server: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = server.Stop() }()

	fmt.Printf("Demo runner daemon listening on %s (PID %d)\n", sockPath, os.Getpid())
	fmt.Printf("Historical runs seeded in: %s\n", dbPath)
	fmt.Printf("View dashboard in another terminal with:\n  actr monitor --sock %s\n", sockPath)

	<-ctx.Done()
	fmt.Println("\nShutting down demo runner...")
}

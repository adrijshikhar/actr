package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/adrijshikhar/actr/internal/config"
	"github.com/adrijshikhar/actr/internal/demo"
	"github.com/adrijshikhar/actr/internal/history"
)

func main() {
	sockPath := "/tmp/actr-demo.sock"
	if len(os.Args) > 1 {
		sockPath = filepath.Clean(os.Args[1])
	}

	runsDir := filepath.Join(config.DefaultConfigDir(), "runs")
	if len(os.Args) > 2 {
		runsDir = filepath.Clean(os.Args[2])
	} else if env := os.Getenv("ACTR_RUNS_DIR"); env != "" {
		runsDir = env
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	server, err := demo.StartServer(ctx, sockPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to start demo server: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = server.Stop() }()

	store, err := history.NewDiskStore(runsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to initialize history store at %s: %v\n", runsDir, err)
	} else {
		if err := demo.SeedHistoryStore(store); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to seed history store: %v\n", err)
		} else {
			server.WithHistoryStore(store)
		}
	}

	fmt.Printf("Demo runner daemon listening on %s (PID %d)\n", sockPath, os.Getpid())
	fmt.Printf("Historical runs seeded in: %s\n", runsDir)
	fmt.Printf("View dashboard in another terminal with:\n  actr monitor --sock %s --runs-dir %s\n", sockPath, runsDir)

	<-ctx.Done()
	fmt.Println("\nShutting down demo runner...")
}

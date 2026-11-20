package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/adrijshikhar/actr/internal/config"
	"github.com/adrijshikhar/actr/internal/demo"
	"github.com/adrijshikhar/actr/internal/history"
	"github.com/adrijshikhar/actr/internal/monitor"
	"github.com/adrijshikhar/actr/internal/tui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
)

var runProgram = func(m tea.Model, opts ...tea.ProgramOption) error {
	p := tea.NewProgram(m, opts...)
	_, err := p.Run()
	return err
}

func newMonitorCmd() *cobra.Command {
	var (
		sockPath      string
		configDirFlag string
		runsDirFlag   string
		demoFlag      bool
		mouseFlag     bool
	)

	cmd := &cobra.Command{
		Use:   "monitor",
		Short: "Launch interactive terminal dashboard for actr",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfgDir := resolveConfigDir(configDirFlag, cmd.Flags().Changed("config-dir"))

			var (
				store      history.Store
				tmpDemoDir string
			)
			defer func() {
				if store != nil {
					_ = store.Close()
				}
				if tmpDemoDir != "" {
					_ = os.RemoveAll(tmpDemoDir)
				}
			}()

			if demoFlag {
				var err error
				tmpDemoDir, err = os.MkdirTemp("", "actr-demo-*")
				if err != nil {
					return fmt.Errorf("create demo temp dir: %w", err)
				}
				s, err := history.NewSQLiteStore(filepath.Join(tmpDemoDir, "demo_history.db"))
				if err != nil {
					return fmt.Errorf("create demo sqlite store: %w", err)
				}
				store = s
				if err := demo.SeedHistoryStore(store); err != nil {
					return fmt.Errorf("seed demo history store: %w", err)
				}
			} else {
				dbPath := filepath.Join(cfgDir, "history.db")
				if runsDirFlag != "" {
					if strings.HasSuffix(runsDirFlag, ".db") {
						dbPath = runsDirFlag
					} else {
						dbPath = filepath.Join(runsDirFlag, "history.db")
					}
				}
				if s, err := history.NewSQLiteStore(dbPath); err == nil {
					store = s
				}
			}

			var srv *monitor.Server
			if demoFlag {
				tmpSock := filepath.Join(os.TempDir(), fmt.Sprintf("actr-demo-%d.sock", os.Getpid()))
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()

				var err error
				srv, err = demo.StartServerWithStore(ctx, tmpSock, store)
				if err != nil {
					return fmt.Errorf("start demo server: %w", err)
				}
				defer func() {
					_ = srv.Stop()
					_ = os.Remove(tmpSock)
				}()
				sockPath = tmpSock
			} else if sockPath == "" {
				sockPath = filepath.Join(cfgDir, "daemon.sock")
			}

			isDaemonRunning := false
			if _, err := os.Stat(sockPath); err == nil {
				conn, dialErr := net.DialTimeout("unix", sockPath, 200*time.Millisecond)
				if dialErr == nil {
					_ = conn.Close()
					isDaemonRunning = true
				}
			}

			var model *tui.DashboardModel
			if demoFlag || isDaemonRunning {
				model = tui.NewDashboardModel(sockPath)
				if store != nil {
					model.SetHistoryStore(store)
				}
			} else {
				// Offline mode fallback: load historical runs directly from sqlite store
				if store == nil {
					dbPath := filepath.Join(cfgDir, "history.db")
					if runsDirFlag != "" {
						if strings.HasSuffix(runsDirFlag, ".db") {
							dbPath = runsDirFlag
						} else {
							dbPath = filepath.Join(runsDirFlag, "history.db")
						}
					}
					s, err := history.NewSQLiteStore(dbPath)
					if err != nil {
						return fmt.Errorf("initialize history store at %s: %w", dbPath, err)
					}
					store = s
				}
				snap, err := tui.HydrateSnapshotFromStore(store, 50)
				if err != nil {
					return fmt.Errorf("hydrate historical runs from store: %w", err)
				}
				model = tui.NewDashboardModel(sockPath)
				model.SetHistoryStore(store)
				model.SetOffline(true)
				model.ApplySnapshot(snap)
			}

			model.AutoFocusRunningStep()
			opts := []tea.ProgramOption{tea.WithAltScreen()}
			if mouseFlag {
				model.SetMouseEnabled(true)
				opts = append(opts, tea.WithMouseCellMotion())
			}
			if err := runProgram(model, opts...); err != nil {
				return fmt.Errorf("dashboard execution: %w", err)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&sockPath, "sock", "", "path to daemon unix socket")
	cmd.Flags().StringVar(&configDirFlag, "config-dir", config.DefaultConfigDir(), "path to directory containing runner configuration and daemon socket")
	cmd.Flags().StringVar(&runsDirFlag, "runs-dir", "", "path to runs history directory or database file (defaults to <config-dir>/history.db)")
	cmd.Flags().BoolVar(&demoFlag, "demo", false, "launch monitor with simulated live workflow execution and telemetry")
	cmd.Flags().BoolVar(&mouseFlag, "mouse", false, "enable mouse tracking mode (note: in most terminals, this disables native text selection)")
	_ = cmd.MarkFlagFilename("sock")
	_ = cmd.MarkFlagDirname("config-dir")
	_ = cmd.MarkFlagDirname("runs-dir")
	return cmd
}

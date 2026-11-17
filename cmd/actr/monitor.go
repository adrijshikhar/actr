package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
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
	)

	cmd := &cobra.Command{
		Use:   "monitor",
		Short: "Launch interactive terminal dashboard for actr",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfgDir := resolveConfigDir(configDirFlag, cmd.Flags().Changed("config-dir"))

			var srv *monitor.Server
			if demoFlag {
				tmpSock := filepath.Join(os.TempDir(), fmt.Sprintf("actr-demo-%d.sock", os.Getpid()))
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()

				var err error
				srv, err = demo.StartServer(ctx, tmpSock)
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

			runsDir := runsDirFlag
			if runsDir == "" {
				runsDir = filepath.Join(cfgDir, "runs")
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
				if store, err := history.NewDiskStore(runsDir); err == nil {
					if demoFlag {
						_ = demo.SeedHistoryStore(store)
						if srv != nil {
							srv.WithHistoryStore(store)
						}
					}
					model.SetHistoryStore(store)
				}
			} else {
				// Offline mode fallback: load historical runs directly from disk store
				store, err := history.NewDiskStore(runsDir)
				if err != nil {
					return fmt.Errorf("initialize history store at %s: %w", runsDir, err)
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
			if err := runProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion()); err != nil {
				return fmt.Errorf("dashboard execution: %w", err)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&sockPath, "sock", "", "path to daemon unix socket")
	cmd.Flags().StringVar(&configDirFlag, "config-dir", config.DefaultConfigDir(), "path to directory containing runner configuration and daemon socket")
	cmd.Flags().StringVar(&runsDirFlag, "runs-dir", "", "path to runs history directory (defaults to <config-dir>/runs)")
	cmd.Flags().BoolVar(&demoFlag, "demo", false, "launch monitor with simulated live workflow execution and telemetry")
	_ = cmd.MarkFlagFilename("sock")
	_ = cmd.MarkFlagDirname("config-dir")
	_ = cmd.MarkFlagDirname("runs-dir")
	return cmd
}

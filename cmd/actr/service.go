package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"

	"github.com/adrijshikhar/actr/internal/config"
	"github.com/adrijshikhar/actr/internal/registration"
	"github.com/adrijshikhar/actr/internal/service"
	"github.com/spf13/cobra"
)

var (
	serviceStatusFunc    = service.Status
	serviceStopFunc      = service.Stop
	serviceUninstallFunc = service.Uninstall
)

func newServiceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:       "service",
		Short:     "Manage the background OS service daemon (launchd/systemd)",
		ValidArgs: []string{"install", "start", "status", "stop", "uninstall"},
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show background runner daemon service status",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			status, err := serviceStatusFunc()
			if err != nil || status == "" {
				fmt.Fprintln(cmd.OutOrStdout(), "Service status: installed/stopped")
				return
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Service status: %s\n", status)
		},
	})

	var binPath string
	installCmd := &cobra.Command{
		Use:   "install",
		Short: "Install actr as a native OS service daemon",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := serviceInstallFunc(binPath); err != nil {
				return fmt.Errorf("install service: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Service installed successfully")
			return nil
		},
	}
	installCmd.Flags().StringVar(&binPath, "bin", "", "Path to actr binary (defaults to current executable)")
	_ = installCmd.MarkFlagFilename("bin")
	cmd.AddCommand(installCmd)

	cmd.AddCommand(&cobra.Command{
		Use:   "start",
		Short: "Start the background runner daemon service",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			runnerPath := filepath.Join(config.DefaultConfigDir(), ".runner")
			multiRepoCfgYAML := filepath.Join(config.DefaultConfigDir(), "config.yaml")
			multiRepoCfgYML := filepath.Join(config.DefaultConfigDir(), "config.yml")
			isConfigured := false
			if _, err := os.Stat(runnerPath); err == nil {
				isConfigured = true
			} else if config.IsMultiRepoConfig(multiRepoCfgYAML) || config.IsMultiRepoConfig(multiRepoCfgYML) {
				isConfigured = true
			}

			if !isConfigured {
				if isTerminalFunc(os.Stdin.Fd()) {
					reader := bufio.NewReader(cmd.InOrStdin())
					registerNow, promptErr := registration.PromptConfirm(
						reader,
						cmd.OutOrStdout(),
						"Runner is not registered yet. Would you like to register now?",
						true,
					)
					if promptErr != nil {
						return promptErr
					}
					if !registerNow {
						return fmt.Errorf("runner is not registered; run 'actr register' first")
					}
					if err := runRegisterCmdFunc(cmd); err != nil {
						return err
					}
					// Ensure the background service is installed before starting if not already present
					status, _ := serviceStatusFunc()
					if status == "not installed" {
						if err := serviceInstallFunc(""); err != nil {
							return fmt.Errorf("install service: %w", err)
						}
					}
				} else {
					return fmt.Errorf("runner is not registered; run 'actr register' first")
				}
			}

			if err := serviceStartFunc(); err != nil {
				return fmt.Errorf("start service: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Service started successfully")
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "stop",
		Short: "Stop the background runner daemon service",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := serviceStopFunc(); err != nil {
				return fmt.Errorf("stop service: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Service stopped successfully")
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "uninstall",
		Short: "Uninstall the background runner daemon service",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := serviceUninstallFunc(); err != nil {
				return fmt.Errorf("uninstall service: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Service uninstalled successfully")
			return nil
		},
	})

	return cmd
}

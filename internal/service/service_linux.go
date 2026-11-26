//go:build linux

package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/adrijshikhar/actr/internal/config"
)

const (
	ServiceUnitName       = "actr.service"
	defaultCommandTimeout = 10 * time.Second
)

var (
	unitPathFunc = defaultUnitPath
	execCommand  = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, name, args...)
	}
)

func defaultUnitPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get user home directory: %w", err)
	}
	return filepath.Join(home, ".config", "systemd", "user", ServiceUnitName), nil
}

// Install writes the systemd user unit file and reloads systemd.
func Install(binPath string, configDirs ...string) error {
	resolvedBin, err := resolveBinPath(binPath)
	if err != nil {
		return err
	}

	unitPath, err := unitPathFunc()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(unitPath), 0755); err != nil {
		return fmt.Errorf("create systemd directory: %w", err)
	}

	workDir := config.DefaultConfigDir()
	if len(configDirs) > 0 && configDirs[0] != "" {
		if abs, err := filepath.Abs(configDirs[0]); err == nil {
			workDir = abs
		} else {
			workDir = configDirs[0]
		}
	}

	unitContent := GenerateLinuxUnit(resolvedBin, workDir)
	if err := os.WriteFile(unitPath, []byte(unitContent), 0600); err != nil {
		return fmt.Errorf("write systemd unit: %w", err)
	}

	ctxReload, cancelReload := context.WithTimeout(context.Background(), defaultCommandTimeout)
	defer cancelReload()
	if out, err := execCommand(ctxReload, "systemctl", "--user", "daemon-reload").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl daemon-reload failed: %s: %w", strings.TrimSpace(string(out)), err)
	}

	ctxEnable, cancelEnable := context.WithTimeout(context.Background(), defaultCommandTimeout)
	defer cancelEnable()
	if out, err := execCommand(ctxEnable, "systemctl", "--user", "enable", ServiceUnitName).CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl enable failed: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// Status checks the status of the systemd service.
func Status() (string, error) {
	unitPath, err := unitPathFunc()
	if err != nil {
		return "", err
	}

	if _, err := os.Stat(unitPath); errors.Is(err, os.ErrNotExist) {
		return "not installed", nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultCommandTimeout)
	defer cancel()
	out, err := execCommand(ctx, "systemctl", "--user", "is-active", ServiceUnitName).Output()
	if err == nil && strings.TrimSpace(string(out)) == "active" {
		return "running", nil
	}

	return "installed/stopped", nil
}

// Start starts the systemd service.
func Start() error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultCommandTimeout)
	defer cancel()
	cmd := execCommand(ctx, "systemctl", "--user", "start", ServiceUnitName)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl start failed: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// Stop stops the systemd service.
func Stop() error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultCommandTimeout)
	defer cancel()
	cmd := execCommand(ctx, "systemctl", "--user", "stop", ServiceUnitName)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl stop failed: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// Uninstall stops, disables, and removes the systemd unit file.
func Uninstall() error {
	_ = Stop()
	ctxDisable, cancelDisable := context.WithTimeout(context.Background(), defaultCommandTimeout)
	defer cancelDisable()
	_ = execCommand(ctxDisable, "systemctl", "--user", "disable", ServiceUnitName).Run()

	unitPath, err := unitPathFunc()
	if err != nil {
		return err
	}
	if err := os.Remove(unitPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove unit file: %w", err)
	}
	ctxReload, cancelReload := context.WithTimeout(context.Background(), defaultCommandTimeout)
	defer cancelReload()
	_ = execCommand(ctxReload, "systemctl", "--user", "daemon-reload").Run()
	return nil
}

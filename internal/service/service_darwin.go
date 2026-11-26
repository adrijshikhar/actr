//go:build darwin

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
	ServiceLabel          = "com.github.actr"
	defaultCommandTimeout = 10 * time.Second
)

var (
	plistPathFunc = defaultPlistPath
	execCommand   = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, name, args...)
	}
)

func defaultPlistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get user home directory: %w", err)
	}
	return filepath.Join(home, "Library", "LaunchAgents", ServiceLabel+".plist"), nil
}

// Install writes the launchd plist to the user's LaunchAgents directory.
func Install(binPath string, configDirs ...string) error {
	resolvedBin, err := resolveBinPath(binPath)
	if err != nil {
		return err
	}

	plistPath, err := plistPathFunc()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(plistPath), 0755); err != nil {
		return fmt.Errorf("create launchd directory: %w", err)
	}

	workDir := config.DefaultConfigDir()
	if len(configDirs) > 0 && configDirs[0] != "" {
		if abs, err := filepath.Abs(configDirs[0]); err == nil {
			workDir = abs
		} else {
			workDir = configDirs[0]
		}
	}

	plistContent := GenerateDarwinPlist(resolvedBin, workDir)
	if err := os.WriteFile(plistPath, []byte(plistContent), 0600); err != nil {
		return fmt.Errorf("write launchd plist: %w", err)
	}
	return nil
}

// Status checks the status of the launchd service.
func Status() (string, error) {
	plistPath, err := plistPathFunc()
	if err != nil {
		return "", err
	}

	if _, err := os.Stat(plistPath); errors.Is(err, os.ErrNotExist) {
		return "not installed", nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultCommandTimeout)
	defer cancel()
	out, err := execCommand(ctx, "launchctl", "list", ServiceLabel).Output()
	if err == nil && len(out) > 0 && strings.Contains(string(out), "\"PID\" =") {
		return "running", nil
	}

	return "installed/stopped", nil
}

// Start loads the launchd agent.
func Start() error {
	plistPath, err := plistPathFunc()
	if err != nil {
		return err
	}
	if _, err := os.Stat(plistPath); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("service plist not found at %s (run install first)", plistPath)
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultCommandTimeout)
	defer cancel()
	cmd := execCommand(ctx, "launchctl", "load", "-w", plistPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl load failed: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// Stop unloads the launchd agent.
func Stop() error {
	plistPath, err := plistPathFunc()
	if err != nil {
		return err
	}
	if _, err := os.Stat(plistPath); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("service plist not found at %s", plistPath)
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultCommandTimeout)
	defer cancel()
	cmd := execCommand(ctx, "launchctl", "unload", plistPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl unload failed: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// Uninstall unloads and removes the launchd plist.
func Uninstall() error {
	_ = Stop()
	plistPath, err := plistPathFunc()
	if err != nil {
		return err
	}
	if err := os.Remove(plistPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove plist file: %w", err)
	}
	return nil
}

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestServiceCommandHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	ctx := context.Background()

	code := dispatchWithContext(ctx, []string{"service", "--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0 from service --help, got %d. stderr: %s", code, stderr.String())
	}

	out := stdout.String()
	for _, sub := range []string{"status", "install", "start", "stop", "uninstall"} {
		if !strings.Contains(out, sub) {
			t.Errorf("expected subcommand %q in service help output, got: %s", sub, out)
		}
	}
}

func TestServiceStatusCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	ctx := context.Background()

	code := dispatchWithContext(ctx, []string{"service", "status"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0 from service status, got %d. stderr: %s", code, stderr.String())
	}

	out := stdout.String()
	if !strings.Contains(out, "Service status:") {
		t.Errorf("expected 'Service status:' in output, got: %s", out)
	}
}

func TestServiceStart_UnregisteredNonInteractive(t *testing.T) {
	origIsTerminal := isTerminalFunc
	origStart := serviceStartFunc
	t.Cleanup(func() {
		isTerminalFunc = origIsTerminal
		serviceStartFunc = origStart
	})

	isTerminalFunc = func(fd uintptr) bool { return false }
	serviceStartCalled := false
	serviceStartFunc = func() error {
		serviceStartCalled = true
		return nil
	}

	tempDir := t.TempDir()
	t.Setenv("ACT_RUNNER_CONFIG_DIR", tempDir)

	var stdout, stderr bytes.Buffer
	ctx := context.Background()

	code := dispatchWithContext(ctx, []string{"service", "start"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit code when runner is not registered in non-interactive mode")
	}

	if serviceStartCalled {
		t.Fatalf("serviceStartFunc should not have been called when unconfigured")
	}

	combined := stdout.String() + "\n" + stderr.String()
	expectedErr := "runner is not registered; run 'actr register' first"
	if !strings.Contains(combined, expectedErr) {
		t.Fatalf("expected error %q, got: %s", expectedErr, combined)
	}
}

func TestServiceStart_UnregisteredInteractivePromptDecline(t *testing.T) {
	origIsTerminal := isTerminalFunc
	origStart := serviceStartFunc
	t.Cleanup(func() {
		isTerminalFunc = origIsTerminal
		serviceStartFunc = origStart
	})

	isTerminalFunc = func(fd uintptr) bool { return true }
	serviceStartCalled := false
	serviceStartFunc = func() error {
		serviceStartCalled = true
		return nil
	}

	tempDir := t.TempDir()
	t.Setenv("ACT_RUNNER_CONFIG_DIR", tempDir)

	var stdout, stderr bytes.Buffer
	ctx := context.Background()
	stdin := strings.NewReader("n\n")

	code := dispatchWithContextAndIn(ctx, stdin, []string{"service", "start"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit code when registration is declined")
	}

	if serviceStartCalled {
		t.Fatalf("serviceStartFunc should not have been called when registration was declined")
	}

	out := stdout.String()
	expectedPrompt := "Runner is not registered yet. Would you like to register now? [Y/n]"
	if !strings.Contains(out, expectedPrompt) {
		t.Fatalf("expected prompt %q in stdout, got: %s", expectedPrompt, out)
	}

	combined := out + "\n" + stderr.String()
	expectedErr := "runner is not registered; run 'actr register' first"
	if !strings.Contains(combined, expectedErr) {
		t.Fatalf("expected error %q, got: %s", expectedErr, combined)
	}
}

func TestServiceStart_UnregisteredInteractivePromptAccept(t *testing.T) {
	origIsTerminal := isTerminalFunc
	origStart := serviceStartFunc
	origRunRegister := runRegisterCmdFunc
	origServiceInstall := serviceInstallFunc
	origServiceStatus := serviceStatusFunc
	t.Cleanup(func() {
		isTerminalFunc = origIsTerminal
		serviceStartFunc = origStart
		serviceInstallFunc = origServiceInstall
		serviceStatusFunc = origServiceStatus
		runRegisterCmdFunc = origRunRegister
	})

	isTerminalFunc = func(fd uintptr) bool { return true }
	serviceStatusFunc = func() (string, error) {
		return "not installed", nil
	}
	serviceStartCalled := false
	serviceStartFunc = func() error {
		serviceStartCalled = true
		return nil
	}
	serviceInstallCalled := false
	serviceInstallFunc = func(bin string, configDirs ...string) error {
		serviceInstallCalled = true
		return nil
	}

	tempDir := t.TempDir()
	t.Setenv("ACT_RUNNER_CONFIG_DIR", tempDir)

	registerCalled := false
	runRegisterCmdFunc = func(cmd *cobra.Command, cfgDir ...string) error {
		registerCalled = true
		return os.WriteFile(filepath.Join(tempDir, ".runner"), []byte("{}"), 0600)
	}

	var stdout, stderr bytes.Buffer
	ctx := context.Background()
	stdin := strings.NewReader("y\n")

	code := dispatchWithContextAndIn(ctx, stdin, []string{"service", "start"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0 when registration accepted, got %d. stderr: %s", code, stderr.String())
	}

	if !registerCalled {
		t.Fatalf("expected registration command to be executed")
	}

	if !serviceInstallCalled {
		t.Fatalf("expected serviceInstallFunc to be executed after registration")
	}

	if !serviceStartCalled {
		t.Fatalf("expected serviceStartFunc to be executed after registration")
	}

	out := stdout.String()
	expectedPrompt := "Runner is not registered yet. Would you like to register now? [Y/n]"
	if !strings.Contains(out, expectedPrompt) {
		t.Fatalf("expected prompt %q in stdout, got: %s", expectedPrompt, out)
	}
	if !strings.Contains(out, "Service started successfully") {
		t.Fatalf("expected 'Service started successfully' in stdout, got: %s", out)
	}
}

func TestServiceStart_RegisteredProceeds(t *testing.T) {
	origStart := serviceStartFunc
	t.Cleanup(func() {
		serviceStartFunc = origStart
	})

	serviceStartCalled := false
	serviceStartFunc = func() error {
		serviceStartCalled = true
		return nil
	}

	tempDir := t.TempDir()
	t.Setenv("ACT_RUNNER_CONFIG_DIR", tempDir)
	if err := os.WriteFile(filepath.Join(tempDir, ".runner"), []byte("{}"), 0600); err != nil {
		t.Fatalf("write .runner: %v", err)
	}

	var stdout, stderr bytes.Buffer
	ctx := context.Background()

	code := dispatchWithContext(ctx, []string{"service", "start"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0 when already registered, got %d. stderr: %s", code, stderr.String())
	}

	if !serviceStartCalled {
		t.Fatalf("expected serviceStartFunc to be executed")
	}

	out := stdout.String()
	if strings.Contains(out, "Would you like to register now?") {
		t.Fatalf("did not expect registration prompt when already registered, got: %s", out)
	}
	if !strings.Contains(out, "Service started successfully") {
		t.Fatalf("expected 'Service started successfully' in stdout, got: %s", out)
	}
}

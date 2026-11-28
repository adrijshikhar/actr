//go:build darwin

package service

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDarwinInstallAndStatus(t *testing.T) {
	tmpDir := t.TempDir()
	origFunc := plistPathFunc
	origExec := execCommand
	defer func() {
		plistPathFunc = origFunc
		execCommand = origExec
	}()

	fakePlist := filepath.Join(tmpDir, "com.github.actr.plist")
	plistPathFunc = func() (string, error) {
		return fakePlist, nil
	}
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "false")
	}

	// Status before install
	status, err := Status()
	if err != nil {
		t.Fatalf("unexpected error checking status: %v", err)
	}
	if status != "not installed" {
		t.Fatalf("expected 'not installed', got: %s", status)
	}

	// Install
	if err := Install("/usr/local/bin/actr"); err != nil {
		t.Fatalf("install failed: %v", err)
	}

	// Check file was written
	if _, err := os.Stat(fakePlist); err != nil {
		t.Fatalf("plist file was not created: %v", err)
	}

	// Status after install (not running since mock)
	status, err = Status()
	if err != nil {
		t.Fatalf("unexpected error checking status after install: %v", err)
	}
	if status != "installed/stopped" {
		t.Fatalf("expected 'installed/stopped', got: %s", status)
	}

	// Uninstall
	if err := Uninstall(); err != nil {
		t.Fatalf("uninstall failed: %v", err)
	}
	if _, err := os.Stat(fakePlist); !os.IsNotExist(err) {
		t.Fatalf("expected plist to be deleted after uninstall")
	}
}

func TestDarwinStatus_RunningAndStopped(t *testing.T) {
	tmpDir := t.TempDir()
	origFunc := plistPathFunc
	origExec := execCommand
	defer func() {
		plistPathFunc = origFunc
		execCommand = origExec
	}()

	fakePlist := filepath.Join(tmpDir, "com.github.actr.plist")
	plistPathFunc = func() (string, error) {
		return fakePlist, nil
	}
	if err := os.WriteFile(fakePlist, []byte("plist"), 0644); err != nil {
		t.Fatal(err)
	}

	// Mock execCommand to simulate running (with PID)
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "echo", "{\n\t\"PID\" = 12345;\n\t\"Label\" = \"com.github.actr\";\n}")
	}
	status, err := Status()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "running" {
		t.Fatalf("expected running, got %s", status)
	}

	// Mock execCommand to simulate stopped/crashed (without PID)
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "echo", "{\n\t\"LastExitStatus\" = 256;\n\t\"Label\" = \"com.github.actr\";\n}")
	}
	status, err = Status()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "installed/stopped" {
		t.Fatalf("expected installed/stopped, got %s", status)
	}
}

func TestDarwinStart_UnloadsThenLoads(t *testing.T) {
	tmpDir := t.TempDir()
	origFunc := plistPathFunc
	origExec := execCommand
	defer func() {
		plistPathFunc = origFunc
		execCommand = origExec
	}()

	fakePlist := filepath.Join(tmpDir, "com.github.actr.plist")
	plistPathFunc = func() (string, error) {
		return fakePlist, nil
	}
	if err := os.WriteFile(fakePlist, []byte("<plist/>"), 0600); err != nil {
		t.Fatal(err)
	}

	var calls [][]string
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string{name}, args...))
		if len(args) > 0 && args[0] == "unload" {
			return exec.CommandContext(ctx, "false")
		}
		return exec.CommandContext(ctx, "true")
	}

	if err := Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("expected 2 launchctl calls (unload + load), got %d: %v", len(calls), calls)
	}
	if calls[0][0] != "launchctl" || calls[0][1] != "unload" {
		t.Fatalf("expected first call to unload, got %v", calls[0])
	}
	if calls[1][0] != "launchctl" || calls[1][1] != "load" || calls[1][2] != "-w" {
		t.Fatalf("expected second call to load -w, got %v", calls[1])
	}
}

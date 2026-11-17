//go:build darwin

package service

import (
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
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("false")
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
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("echo", "{\n\t\"PID\" = 12345;\n\t\"Label\" = \"com.github.actr\";\n}")
	}
	status, err := Status()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "running" {
		t.Fatalf("expected running, got %s", status)
	}

	// Mock execCommand to simulate stopped/crashed (without PID)
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("echo", "{\n\t\"LastExitStatus\" = 256;\n\t\"Label\" = \"com.github.actr\";\n}")
	}
	status, err = Status()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "installed/stopped" {
		t.Fatalf("expected installed/stopped, got %s", status)
	}
}

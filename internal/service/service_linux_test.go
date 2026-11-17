//go:build linux

package service

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLinuxInstallAndStatus(t *testing.T) {
	tmpDir := t.TempDir()
	origFunc := unitPathFunc
	defer func() { unitPathFunc = origFunc }()

	fakeUnit := filepath.Join(tmpDir, "actr.service")
	unitPathFunc = func() (string, error) {
		return fakeUnit, nil
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
	if _, err := os.Stat(fakeUnit); err != nil {
		t.Fatalf("unit file was not created: %v", err)
	}

	// Status after install
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
	if _, err := os.Stat(fakeUnit); !os.IsNotExist(err) {
		t.Fatalf("expected unit to be deleted after uninstall")
	}
}

package service

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestServiceManifestGeneration(t *testing.T) {
	manifest := GenerateDarwinPlist("/usr/local/bin/actr", "/Users/user/.actr")
	if !strings.Contains(manifest, "com.github.actr") || !strings.Contains(manifest, "/usr/local/bin/actr") || !strings.Contains(manifest, "--config-dir") {
		t.Fatalf("invalid launchd plist: %s", manifest)
	}

	unit := GenerateLinuxUnit("/usr/local/bin/actr", "/home/user/.actr")
	if !strings.Contains(unit, "[Service]") || !strings.Contains(unit, "ExecStart=/usr/local/bin/actr run --config-dir /home/user/.actr") {
		t.Fatalf("invalid systemd unit: %s", unit)
	}
}

func TestResolveBinPath(t *testing.T) {
	path, err := resolveBinPath("/custom/bin/actr")
	if err != nil || path != "/custom/bin/actr" {
		t.Fatalf("expected /custom/bin/actr, got %s (err: %v)", path, err)
	}

	relPath, err := resolveBinPath("./bin/actr")
	if err != nil || !filepath.IsAbs(relPath) {
		t.Fatalf("expected absolute path for relative input, got %s (err: %v)", relPath, err)
	}

	autoPath, err := resolveBinPath("")
	if err != nil || autoPath == "" || !filepath.IsAbs(autoPath) {
		t.Fatalf("expected resolved autoPath, got %s (err: %v)", autoPath, err)
	}
}

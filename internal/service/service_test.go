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
	if !strings.Contains(unit, "[Service]") || !strings.Contains(unit, "/usr/local/bin/actr") || !strings.Contains(unit, "--config-dir") {
		t.Fatalf("invalid systemd unit: %s", unit)
	}
}

func TestServiceTemplates_EscapeSpecialCharactersAndSpaces(t *testing.T) {
	plist := GenerateDarwinPlist("/opt/dir & <actr>/actr", "/tmp/work<bad>")
	if !strings.Contains(plist, "&lt;actr&gt;") || !strings.Contains(plist, "&amp;") || strings.Contains(plist, "<bad>") {
		t.Errorf("expected XML escaping in plist, got: %s", plist)
	}

	unit := GenerateLinuxUnit(`/opt/my dir/actr`, `/var/lib/actr%h`+"\nExecStartPre=/bin/evil")
	if !strings.Contains(unit, `"/opt/my dir/actr"`) || !strings.Contains(unit, `%%h`) || strings.Contains(unit, "\nExecStartPre=/bin/evil") {
		t.Errorf("expected escaping and newline stripping in systemd unit, got: %s", unit)
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

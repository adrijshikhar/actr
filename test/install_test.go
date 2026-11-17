package test

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func createArchive(t *testing.T, binPath, archivePath string) string {
	t.Helper()
	tarFile, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("failed to create tar file: %v", err)
	}
	defer func() { _ = tarFile.Close() }()

	gw := gzip.NewWriter(tarFile)
	defer func() { _ = gw.Close() }()

	tw := tar.NewWriter(gw)
	defer func() { _ = tw.Close() }()

	binData, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatalf("failed to read binary data: %v", err)
	}

	header := &tar.Header{
		Name: "actr",
		Mode: 0755,
		Size: int64(len(binData)),
	}
	if err := tw.WriteHeader(header); err != nil {
		t.Fatalf("failed to write header: %v", err)
	}
	if _, err := tw.Write(binData); err != nil {
		t.Fatalf("failed to write binary content: %v", err)
	}

	_ = tw.Close()
	_ = gw.Close()
	_ = tarFile.Close()

	// Compute sha256
	f, err := os.Open(archivePath)
	if err != nil {
		t.Fatalf("failed to open archive for sha: %v", err)
	}
	defer func() { _ = f.Close() }()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		t.Fatalf("failed to hash archive: %v", err)
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

func TestInstaller_CleanInstallAndCompletions(t *testing.T) {
	tempDir := t.TempDir()
	tempBin := filepath.Join(tempDir, "actr_source")

	// 1. Build test binary
	cmd := exec.Command("go", "build", "-o", tempBin, "../cmd/actr")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build test actr: %v, out: %s", err, string(out))
	}

	// 2. Package tarball matching installer naming convention
	version := "1.2.3"
	osName := runtime.GOOS
	archName := runtime.GOARCH
	tarballName := fmt.Sprintf("actr_%s_%s_%s.tar.gz", version, osName, archName)
	archivePath := filepath.Join(tempDir, tarballName)
	hash := createArchive(t, tempBin, archivePath)

	checksumContent := fmt.Sprintf("%s  %s\n", hash, tarballName)

	// 3. Start mock HTTP server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + tarballName:
			http.ServeFile(w, r, archivePath)
		case "/checksums.txt":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = fmt.Fprint(w, checksumContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	// 4. Run install.sh in sandbox
	installDest := filepath.Join(tempDir, "target_bin")
	fakeHome := filepath.Join(tempDir, "fake_home")
	_ = os.MkdirAll(installDest, 0755)
	_ = os.MkdirAll(fakeHome, 0755)

	installScript, err := filepath.Abs("../install.sh")
	if err != nil {
		t.Fatalf("failed to resolve install.sh: %v", err)
	}

	installCmd := exec.Command("/bin/sh", installScript)
	installCmd.Env = append(os.Environ(),
		"ACTR_VERSION=v"+version,
		"ACTR_DOWNLOAD_BASE_URL="+server.URL,
		"ACTR_INSTALL_DIR="+installDest,
		"HOME="+fakeHome,
	)

	out, err := installCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("installer execution failed: %v\nOutput:\n%s", err, string(out))
	}

	// 5. Verify binary installed and executable
	installedBin := filepath.Join(installDest, "actr")
	info, err := os.Stat(installedBin)
	if err != nil {
		t.Fatalf("expected binary at %s, not found: %v", installedBin, err)
	}
	if info.Mode()&0111 == 0 {
		t.Fatalf("expected binary to be executable, got mode: %v", info.Mode())
	}

	// Verify binary executes
	verOut, err := exec.Command(installedBin, "-v").CombinedOutput()
	if err != nil {
		t.Fatalf("installed binary execution failed: %v", err)
	}
	if !strings.Contains(string(verOut), "actr version") {
		t.Fatalf("unexpected version output: %s", string(verOut))
	}

	// 6. Verify shell completions installed
	zshComp := filepath.Join(fakeHome, ".zsh/completions/_actr")
	if data, err := os.ReadFile(zshComp); err != nil || !strings.Contains(string(data), "#compdef actr") {
		t.Fatalf("zsh completion file invalid or not created: %v", err)
	}

	bashComp := filepath.Join(fakeHome, ".local/share/bash-completion/completions/actr")
	if data, err := os.ReadFile(bashComp); err != nil || !strings.Contains(string(data), "complete -o default") {
		t.Fatalf("bash completion file invalid or not created: %v", err)
	}

	fishComp := filepath.Join(fakeHome, ".config/fish/completions/actr.fish")
	if data, err := os.ReadFile(fishComp); err != nil || !strings.Contains(string(data), "complete -c actr") {
		t.Fatalf("fish completion file invalid or not created: %v", err)
	}
}

func TestInstaller_ChecksumMismatchRejection(t *testing.T) {
	tempDir := t.TempDir()
	tempBin := filepath.Join(tempDir, "actr_source")

	cmd := exec.Command("go", "build", "-o", tempBin, "../cmd/actr")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build test actr: %v, out: %s", err, string(out))
	}

	version := "1.2.3"
	osName := runtime.GOOS
	archName := runtime.GOARCH
	tarballName := fmt.Sprintf("actr_%s_%s_%s.tar.gz", version, osName, archName)
	archivePath := filepath.Join(tempDir, tarballName)
	_ = createArchive(t, tempBin, archivePath)

	corruptChecksum := "baaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaad"
	checksumContent := fmt.Sprintf("%s  %s\n", corruptChecksum, tarballName)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + tarballName:
			http.ServeFile(w, r, archivePath)
		case "/checksums.txt":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = fmt.Fprint(w, checksumContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	installDest := filepath.Join(tempDir, "target_bin")
	fakeHome := filepath.Join(tempDir, "fake_home")
	_ = os.MkdirAll(installDest, 0755)
	_ = os.MkdirAll(fakeHome, 0755)

	installScript, err := filepath.Abs("../install.sh")
	if err != nil {
		t.Fatalf("failed to resolve install.sh: %v", err)
	}

	installCmd := exec.Command("/bin/sh", installScript)
	installCmd.Env = append(os.Environ(),
		"ACTR_VERSION=v"+version,
		"ACTR_DOWNLOAD_BASE_URL="+server.URL,
		"ACTR_INSTALL_DIR="+installDest,
		"HOME="+fakeHome,
	)

	out, err := installCmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected installer to fail with checksum mismatch, but it succeeded!\nOutput:\n%s", string(out))
	}

	if !strings.Contains(string(out), "Checksum verification failed") {
		t.Fatalf("expected error message to contain 'Checksum verification failed', got:\n%s", string(out))
	}

	installedBin := filepath.Join(installDest, "actr")
	if _, err := os.Stat(installedBin); err == nil {
		t.Fatalf("binary should not have been installed on checksum failure!")
	}
}

func TestInstaller_MissingChecksumsTxtFailsClosed(t *testing.T) {
	tempDir := t.TempDir()
	tempBin := filepath.Join(tempDir, "actr_source")

	cmd := exec.Command("go", "build", "-o", tempBin, "../cmd/actr")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build test actr: %v, out: %s", err, string(out))
	}

	version := "1.2.3"
	osName := runtime.GOOS
	archName := runtime.GOARCH
	tarballName := fmt.Sprintf("actr_%s_%s_%s.tar.gz", version, osName, archName)
	archivePath := filepath.Join(tempDir, tarballName)
	_ = createArchive(t, tempBin, archivePath)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + tarballName:
			http.ServeFile(w, r, archivePath)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	installDest := filepath.Join(tempDir, "target_bin")
	fakeHome := filepath.Join(tempDir, "fake_home")
	_ = os.MkdirAll(installDest, 0755)
	_ = os.MkdirAll(fakeHome, 0755)

	installScript, err := filepath.Abs("../install.sh")
	if err != nil {
		t.Fatalf("failed to resolve install.sh: %v", err)
	}

	installCmd := exec.Command("/bin/sh", installScript)
	installCmd.Env = append(os.Environ(),
		"ACTR_VERSION=v"+version,
		"ACTR_DOWNLOAD_BASE_URL="+server.URL,
		"ACTR_INSTALL_DIR="+installDest,
		"HOME="+fakeHome,
	)

	out, err := installCmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected installer to fail when checksums.txt is missing, but it succeeded!\nOutput:\n%s", string(out))
	}

	if !strings.Contains(string(out), "Failed to download checksums.txt") {
		t.Fatalf("expected error message to contain 'Failed to download checksums.txt', got:\n%s", string(out))
	}

	installedBin := filepath.Join(installDest, "actr")
	if _, err := os.Stat(installedBin); err == nil {
		t.Fatalf("binary should not have been installed when checksums.txt is missing!")
	}
}

func TestInstaller_SkipChecksumOptOut(t *testing.T) {
	tempDir := t.TempDir()
	tempBin := filepath.Join(tempDir, "actr_source")

	cmd := exec.Command("go", "build", "-o", tempBin, "../cmd/actr")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build test actr: %v, out: %s", err, string(out))
	}

	version := "1.2.3"
	osName := runtime.GOOS
	archName := runtime.GOARCH
	tarballName := fmt.Sprintf("actr_%s_%s_%s.tar.gz", version, osName, archName)
	archivePath := filepath.Join(tempDir, tarballName)
	_ = createArchive(t, tempBin, archivePath)

	// Server does NOT serve checksums.txt
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + tarballName:
			http.ServeFile(w, r, archivePath)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	installDest := filepath.Join(tempDir, "target_bin")
	fakeHome := filepath.Join(tempDir, "fake_home")
	_ = os.MkdirAll(installDest, 0755)
	_ = os.MkdirAll(fakeHome, 0755)

	installScript, err := filepath.Abs("../install.sh")
	if err != nil {
		t.Fatalf("failed to resolve install.sh: %v", err)
	}

	installCmd := exec.Command("/bin/sh", installScript)
	installCmd.Env = append(os.Environ(),
		"ACTR_VERSION=v"+version,
		"ACTR_DOWNLOAD_BASE_URL="+server.URL,
		"ACTR_INSTALL_DIR="+installDest,
		"HOME="+fakeHome,
		"ACTR_INSECURE_SKIP_CHECKSUM=1",
	)

	out, err := installCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("expected installer to succeed with ACTR_INSECURE_SKIP_CHECKSUM=1, failed: %v\nOutput:\n%s", err, string(out))
	}

	if !strings.Contains(string(out), "skipping checksum verification") {
		t.Fatalf("expected warning message about skipping checksum verification, got:\n%s", string(out))
	}

	installedBin := filepath.Join(installDest, "actr")
	if _, err := os.Stat(installedBin); err != nil {
		t.Fatalf("binary should have been installed with ACTR_INSECURE_SKIP_CHECKSUM=1, not found: %v", err)
	}
}

func TestInstaller_MissingChecksumEntryFailsClosed(t *testing.T) {
	tempDir := t.TempDir()
	tempBin := filepath.Join(tempDir, "actr_source")

	cmd := exec.Command("go", "build", "-o", tempBin, "../cmd/actr")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build test actr: %v, out: %s", err, string(out))
	}

	version := "1.2.3"
	osName := runtime.GOOS
	archName := runtime.GOARCH
	tarballName := fmt.Sprintf("actr_%s_%s_%s.tar.gz", version, osName, archName)
	archivePath := filepath.Join(tempDir, tarballName)
	_ = createArchive(t, tempBin, archivePath)

	// Checksums file exists but does NOT contain entry for this tarball
	checksumContent := "abcdef1234567890  some_other_archive.tar.gz\n"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + tarballName:
			http.ServeFile(w, r, archivePath)
		case "/checksums.txt":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = fmt.Fprint(w, checksumContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	installDest := filepath.Join(tempDir, "target_bin")
	fakeHome := filepath.Join(tempDir, "fake_home")
	_ = os.MkdirAll(installDest, 0755)
	_ = os.MkdirAll(fakeHome, 0755)

	installScript, err := filepath.Abs("../install.sh")
	if err != nil {
		t.Fatalf("failed to resolve install.sh: %v", err)
	}

	installCmd := exec.Command("/bin/sh", installScript)
	installCmd.Env = append(os.Environ(),
		"ACTR_VERSION=v"+version,
		"ACTR_DOWNLOAD_BASE_URL="+server.URL,
		"ACTR_INSTALL_DIR="+installDest,
		"HOME="+fakeHome,
	)

	out, err := installCmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected installer to fail when checksum entry is missing, but it succeeded!\nOutput:\n%s", string(out))
	}

	if !strings.Contains(string(out), "Checksum entry for "+tarballName+" not found in checksums.txt") {
		t.Fatalf("expected error message to contain 'Checksum entry for ... not found in checksums.txt', got:\n%s", string(out))
	}

	installedBin := filepath.Join(installDest, "actr")
	if _, err := os.Stat(installedBin); err == nil {
		t.Fatalf("binary should not have been installed when checksum entry is missing!")
	}
}

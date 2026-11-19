package tui

import (
	"bytes"
	"encoding/base64"
	"os/exec"
	"strings"
	"testing"
)

func TestCopyText_OSC52(t *testing.T) {
	oldWriter := clipboardWriter
	oldGOOS := currentGOOS
	defer func() {
		clipboardWriter = oldWriter
		currentGOOS = oldGOOS
	}()

	currentGOOS = "linux" // Ensure pbcopy is not invoked
	var buf bytes.Buffer
	clipboardWriter = &buf

	input := "Hello, actr terminal clipboard!\nLine 2 with special chars: ⚡ 🚀"
	err := CopyText(input)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	out := buf.String()
	prefix := "\x1b]52;c;"
	suffix := "\x07"
	if !strings.HasPrefix(out, prefix) {
		t.Fatalf("expected OSC 52 prefix %q, got %q", prefix, out)
	}
	if !strings.HasSuffix(out, suffix) {
		t.Fatalf("expected OSC 52 suffix %q, got %q", suffix, out)
	}

	payload := strings.TrimSuffix(strings.TrimPrefix(out, prefix), suffix)
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatalf("failed to decode base64 payload %q: %v", payload, err)
	}
	if string(decoded) != input {
		t.Fatalf("decoded payload mismatch: expected %q, got %q", input, string(decoded))
	}
}

func TestCopyText_OSC52_EmptyString(t *testing.T) {
	oldWriter := clipboardWriter
	oldGOOS := currentGOOS
	defer func() {
		clipboardWriter = oldWriter
		currentGOOS = oldGOOS
	}()

	currentGOOS = "linux"
	var buf bytes.Buffer
	clipboardWriter = &buf

	err := CopyText("")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	expected := "\x1b]52;c;\x07"
	if buf.String() != expected {
		t.Fatalf("expected %q, got %q", expected, buf.String())
	}
}

func TestCopyText_DarwinPbcopy(t *testing.T) {
	oldWriter := clipboardWriter
	oldCmd := execCommand
	oldGOOS := currentGOOS
	defer func() {
		clipboardWriter = oldWriter
		execCommand = oldCmd
		currentGOOS = oldGOOS
	}()

	var buf bytes.Buffer
	clipboardWriter = &buf
	currentGOOS = "darwin"

	var invokedCommand string
	execCommand = func(name string, arg ...string) *exec.Cmd {
		invokedCommand = name
		// Return a harmless command that consumes stdin and succeeds
		return exec.Command("cat")
	}

	testPayload := "darwin clipboard content"
	err := CopyText(testPayload)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	if invokedCommand != "pbcopy" {
		t.Fatalf("expected command 'pbcopy', got %q", invokedCommand)
	}

	// Verify OSC 52 was also emitted
	if !strings.Contains(buf.String(), "\x1b]52;c;") {
		t.Fatalf("expected OSC 52 sequence in buffer, got %q", buf.String())
	}
}

func TestCopyText_GracefulFailure(t *testing.T) {
	oldWriter := clipboardWriter
	oldCmd := execCommand
	oldGOOS := currentGOOS
	defer func() {
		clipboardWriter = oldWriter
		execCommand = oldCmd
		currentGOOS = oldGOOS
	}()

	clipboardWriter = nil // No writer
	currentGOOS = "darwin"

	// Mock execCommand to return a command that fails or non-existent binary
	execCommand = func(name string, arg ...string) *exec.Cmd {
		return exec.Command("/path/to/definitely/nonexistent/pbcopy/tool")
	}

	// Should not panic and should return nil gracefully
	err := CopyText("fallback ignore test")
	if err != nil {
		t.Fatalf("expected nil error on missing tool, got %v", err)
	}
}

func TestClipboardCopy_Alias(t *testing.T) {
	oldWriter := clipboardWriter
	oldGOOS := currentGOOS
	defer func() {
		clipboardWriter = oldWriter
		currentGOOS = oldGOOS
	}()

	currentGOOS = "linux"
	var buf bytes.Buffer
	clipboardWriter = &buf

	err := ClipboardCopy("alias test")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	if !strings.Contains(buf.String(), "\x1b]52;c;") {
		t.Fatalf("expected OSC 52 sequence, got %q", buf.String())
	}
}

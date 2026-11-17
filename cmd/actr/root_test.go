package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRootCommandHelpAndVersion(t *testing.T) {
	SetVersion("1.0.0")

	var stdout, stderr bytes.Buffer
	ctx := context.Background()

	code := dispatchWithContext(ctx, []string{"--version"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected code 0, got %d. stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "1.0.0") {
		t.Fatalf("expected version output '1.0.0', got: %s", stdout.String())
	}

	stdout.Reset()
	code = dispatchWithContext(ctx, []string{"--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected code 0, got %d", code)
	}
	if !strings.Contains(stdout.String(), "actr") {
		t.Fatalf("expected help description, got: %s", stdout.String())
	}
}

func TestRootCommand_InvalidFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	ctx := context.Background()

	code := dispatchWithContext(ctx, []string{"--invalid-flag-that-does-not-exist"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit code for invalid flag, got %d", code)
	}
}

func TestVersionAccessors(t *testing.T) {
	orig := GetVersion()
	defer SetVersion(orig)

	SetVersion("2.3.4")
	if got := GetVersion(); got != "2.3.4" {
		t.Fatalf("expected 2.3.4, got %s", got)
	}
}

func TestDispatchWithContext_DebugFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	ctx := context.Background()

	code := dispatchWithContext(ctx, []string{"--debug", "--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected code 0 with debug flag, got %d", code)
	}
}

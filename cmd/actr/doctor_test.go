package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestDoctorCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	ctx := context.Background()

	code := dispatchWithContext(ctx, []string{"doctor"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0 from doctor, got %d. stderr: %s", code, stderr.String())
	}

	out := stdout.String()
	if !strings.Contains(out, "OS & Architecture") {
		t.Errorf("expected OS & Architecture in doctor output, got: %s", out)
	}
	if !strings.Contains(out, "Git") {
		t.Errorf("expected Git in doctor output, got: %s", out)
	}
}

//go:build darwin

package power

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

func TestDarwinInhibitor_UsesCaffeinateWithDaemonPIDAndReleases(t *testing.T) {
	origExec := execCommand
	defer func() { execCommand = origExec }()

	var capturedName string
	var capturedArgs []string
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		capturedName = name
		capturedArgs = append([]string(nil), args...)
		return exec.CommandContext(ctx, "sleep", "30")
	}

	inh := newPlatformInhibitor().(*darwinInhibitor)
	if err := inh.Acquire(); err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}

	if capturedName != "/usr/bin/caffeinate" {
		t.Fatalf("expected /usr/bin/caffeinate, got %q", capturedName)
	}

	joined := strings.Join(capturedArgs, " ")
	expectedPID := strconv.Itoa(os.Getpid())
	if !strings.Contains(joined, "-i") || !strings.Contains(joined, "-s") || !strings.Contains(joined, fmt.Sprintf("-w %s", expectedPID)) {
		t.Fatalf("unexpected caffeinate flags: %v", capturedArgs)
	}
	if strings.Contains(joined, "-d") {
		t.Fatalf("did not expect display sleep flag -d in %v", capturedArgs)
	}

	if err := inh.Release(); err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	inh.mu.Lock()
	active := inh.active
	inh.mu.Unlock()
	if active {
		t.Fatalf("expected inhibitor to be inactive after Release")
	}
}

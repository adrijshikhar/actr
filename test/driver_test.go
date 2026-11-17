package test

import (
	"bytes"
	"context"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/adrijshikhar/actr/internal/driver"
	"github.com/adrijshikhar/actr/internal/driver/host"
)

func TestHostDriver_ProcessGroupKilledOnCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping process group signal test on windows")
	}

	d := host.NewDriver()
	tmpDir := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	// Script spawns a background sleep and prints its PID, then sleeps itself.
	// Both processes belong to the new process group created by host driver.
	script := `
sleep 30 &
CHILD_PID=$!
echo "CHILD_PID=$CHILD_PID"
sleep 30
`
	spec := &driver.StepSpec{
		ID:               "step-kill-pgid",
		Name:             "Kill PGID test",
		WorkingDirectory: tmpDir,
		Script:           script,
		Shell:            "sh",
	}

	var stdout, stderr bytes.Buffer
	res, err := d.ExecuteStep(ctx, spec, &stdout, &stderr)
	if err == nil && res.ExitCode == 0 {
		t.Fatal("expected step to be cancelled/fail, but got success")
	}

	out := stdout.String()
	var childPID int
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "CHILD_PID=") {
			pidStr := strings.TrimPrefix(line, "CHILD_PID=")
			pid, parseErr := strconv.Atoi(pidStr)
			if parseErr == nil && pid > 0 {
				childPID = pid
				break
			}
		}
	}

	if childPID == 0 {
		t.Fatalf("could not extract child PID from stdout:\n%s\nstderr:\n%s", out, stderr.String())
	}

	// Give the OS a moment to reap the killed process
	time.Sleep(100 * time.Millisecond)

	// Verify child process was killed along with the process group
	checkCmd := exec.Command("kill", "-0", strconv.Itoa(childPID))
	if err := checkCmd.Run(); err == nil {
		// Clean up leaked process if test failed
		_ = exec.Command("kill", "-9", strconv.Itoa(childPID)).Run()
		t.Fatalf("expected child process %d to be killed by process group cancellation, but it is still running", childPID)
	}
}

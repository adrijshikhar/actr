package test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBinaryBuildAndSmoke(t *testing.T) {
	binPath := filepath.Join(t.TempDir(), "actr")
	cmd := exec.Command("go", "build", "-o", binPath, "../cmd/actr")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build failed: %v, out: %s", err, string(out))
	}

	out, err := exec.Command(binPath, "--help").Output()
	if err != nil {
		t.Fatalf("binary execution failed: %v", err)
	}

	if !strings.Contains(string(out), "actr") {
		t.Fatalf("unexpected help text: %s", string(out))
	}

	runOut, err := exec.Command(binPath, "run", "--help").Output()
	if err != nil {
		t.Fatalf("run --help failed: %v", err)
	}

	for _, flag := range []string{"--limit-cpu", "--limit-memory", "--limit-pids", "--driver"} {
		if !strings.Contains(string(runOut), flag) {
			t.Errorf("expected run --help to contain %q, got: %s", flag, string(runOut))
		}
	}
}

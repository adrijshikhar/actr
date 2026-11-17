package diagnostics

import (
	"testing"
)

func TestDoctorChecks(t *testing.T) {
	results := RunAllChecks()
	if len(results) == 0 {
		t.Fatal("expected doctor checks to return results")
	}

	foundGit := false
	for _, res := range results {
		if res.Name == "Git" {
			foundGit = true
			if !res.Passed {
				t.Logf("git check warning: %s", res.Message)
			}
		}
	}
	if !foundGit {
		t.Fatal("expected Git check in doctor results")
	}
}

func TestCheckSystemAlias(t *testing.T) {
	res1 := RunAllChecks()
	res2 := CheckSystem()
	if len(res1) != len(res2) {
		t.Fatalf("expected CheckSystem() to return same count as RunAllChecks(), got %d vs %d", len(res2), len(res1))
	}
}

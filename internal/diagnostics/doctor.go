package diagnostics

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

type CheckResult struct {
	Name    string
	Passed  bool
	Message string
}

func RunAllChecks() []CheckResult {
	return []CheckResult{
		checkOS(),
		checkGit(),
		checkNode(),
	}
}

// CheckSystem is an alias for RunAllChecks.
func CheckSystem() []CheckResult {
	return RunAllChecks()
}

func checkOS() CheckResult {
	return CheckResult{
		Name:    "OS & Architecture",
		Passed:  true,
		Message: fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
	}
}

func checkGit() CheckResult {
	path, err := exec.LookPath("git")
	if err != nil {
		return CheckResult{Name: "Git", Passed: false, Message: "git not found in PATH"}
	}
	out, err := exec.Command(path, "--version").Output()
	if err != nil {
		return CheckResult{Name: "Git", Passed: false, Message: fmt.Sprintf("git execution failed: %v", err)}
	}
	return CheckResult{Name: "Git", Passed: true, Message: strings.TrimSpace(string(out))}
}

func checkNode() CheckResult {
	path, err := exec.LookPath("node")
	if err != nil {
		return CheckResult{Name: "Node.js (for JS actions)", Passed: false, Message: "node not found in PATH (install via sdkman/nvm/brew for JS actions)"}
	}
	out, err := exec.Command(path, "--version").Output()
	if err != nil {
		return CheckResult{Name: "Node.js (for JS actions)", Passed: false, Message: fmt.Sprintf("node execution failed: %v", err)}
	}
	return CheckResult{Name: "Node.js (for JS actions)", Passed: true, Message: strings.TrimSpace(string(out))}
}

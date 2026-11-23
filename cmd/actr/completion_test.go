package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adrijshikhar/actr/internal/config"
)

func TestCompletion_Commands(t *testing.T) {
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	cmd := newRootCmd(out, errOut)

	cmd.SetArgs([]string{"__complete", "ser"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := out.String()
	if !strings.Contains(output, "service") {
		t.Errorf("expected 'service' in completion output, got:\n%s", output)
	}
}

func TestCompletion_ServiceSubcommands(t *testing.T) {
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	cmd := newRootCmd(out, errOut)

	cmd.SetArgs([]string{"__complete", "service", ""})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := out.String()
	expectedSubs := []string{"install", "start", "status", "stop", "uninstall"}
	for _, sub := range expectedSubs {
		if !strings.Contains(output, sub) {
			t.Errorf("expected '%s' in service completion output, got:\n%s", sub, output)
		}
	}
}

func TestCompletion_RegisterFlags(t *testing.T) {
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	cmd := newRootCmd(out, errOut)

	cmd.SetArgs([]string{"__complete", "register", "--"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := out.String()
	expectedFlags := []string{"--url", "--token", "--name", "--labels", "--config-dir", "--work-folder", "--force"}
	for _, flag := range expectedFlags {
		if !strings.Contains(output, flag) {
			t.Errorf("expected '%s' in register flag completion, got:\n%s", flag, output)
		}
	}
}

func TestCompletion_ShellGeneration(t *testing.T) {
	shells := []struct {
		shell string
		check string
	}{
		{"zsh", "#compdef actr"},
		{"bash", "complete -o default"},
		{"fish", "complete -c actr"},
	}

	for _, tt := range shells {
		t.Run(tt.shell, func(t *testing.T) {
			out := &bytes.Buffer{}
			errOut := &bytes.Buffer{}
			cmd := newRootCmd(out, errOut)

			cmd.SetArgs([]string{"completion", tt.shell})
			if err := cmd.Execute(); err != nil {
				t.Fatalf("completion %s error: %v", tt.shell, err)
			}

			if !strings.Contains(out.String(), tt.check) {
				t.Errorf("expected shell completion script to contain %q", tt.check)
			}
		})
	}
}

func TestCompletion_PowershellAndInvalid(t *testing.T) {
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	cmd := newRootCmd(out, errOut)

	cmd.SetArgs([]string{"completion", "powershell"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected powershell completion error: %v", err)
	}
	if !strings.Contains(out.String(), "Register-ArgumentCompleter") {
		t.Errorf("expected powershell completion script to contain Register-ArgumentCompleter, got:\n%s", out.String())
	}

	cmd.SetArgs([]string{"completion", "invalid-shell"})
	if err := cmd.Execute(); err == nil {
		t.Fatalf("expected error for invalid shell, got nil")
	}
}

func TestCompletion_RunAndRegisterFlags(t *testing.T) {
	expectedLabels := []string{"self-hosted", "macOS", "ARM64", "linux", "x64"}

	t.Run("run command does not have dangling labels or name flags", func(t *testing.T) {
		runCmd := newRunCmd()
		if runCmd.Flags().Lookup("labels") != nil {
			t.Errorf("expected no labels flag on run cmd")
		}
		if runCmd.Flags().Lookup("name") != nil {
			t.Errorf("expected no name flag on run cmd")
		}
	})

	t.Run("register --labels completion", func(t *testing.T) {
		out := &bytes.Buffer{}
		errOut := &bytes.Buffer{}
		cmd := newRootCmd(out, errOut)

		cmd.SetArgs([]string{"__complete", "register", "--labels", ""})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		output := out.String()
		for _, label := range expectedLabels {
			if !strings.Contains(output, label) {
				t.Errorf("expected label %q in register --labels completion, got:\n%s", label, output)
			}
		}
	})

	t.Run("name completion from configs on register command", func(t *testing.T) {
		tmpDir := t.TempDir()

		// 1. Setup single runner config in tmpDir
		runnerCfg := &config.RunnerConfig{
			AgentName: "test-runner-alpha",
		}
		if err := config.SaveRunnerConfig(tmpDir, runnerCfg); err != nil {
			t.Fatalf("failed to save runner config: %v", err)
		}

		// 2. Setup multi-repo config in tmpDir
		supCfg := &config.SupervisorConfig{
			Version: "1",
			Global: config.GlobalConfig{
				MaxConcurrentJobs: 2,
			},
			Repos: map[string]config.RepoConfig{
				"repo-worker-beta": {
					URL: "https://github.com/org/repo",
				},
			},
		}
		if err := config.SaveSupervisorConfig(filepath.Join(tmpDir, "config.yaml"), supCfg); err != nil {
			t.Fatalf("failed to save supervisor config: %v", err)
		}

		// Test on register command
		out := &bytes.Buffer{}
		errOut := &bytes.Buffer{}
		cmd := newRootCmd(out, errOut)
		cmd.SetArgs([]string{"__complete", "register", "--config-dir", tmpDir, "--name", ""})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		output := out.String()
		if !strings.Contains(output, "test-runner-alpha") {
			t.Errorf("expected 'test-runner-alpha' in register --name completion, got:\n%s", output)
		}
		if !strings.Contains(output, "repo-worker-beta") {
			t.Errorf("expected 'repo-worker-beta' in register --name completion, got:\n%s", output)
		}

		// Test prefix filtering on register --name
		out.Reset()
		errOut.Reset()
		cmd = newRootCmd(out, errOut)
		cmd.SetArgs([]string{"__complete", "register", "--config-dir", tmpDir, "--name", "repo-"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		output = out.String()
		if !strings.Contains(output, "repo-worker-beta") {
			t.Errorf("expected 'repo-worker-beta' in register --name prefix completion, got:\n%s", output)
		}
		if strings.Contains(output, "test-runner-alpha") {
			t.Errorf("unexpected 'test-runner-alpha' in register --name prefix completion, got:\n%s", output)
		}
	})

	t.Run("name completion discovers config from current working directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		supCfg := &config.SupervisorConfig{
			Version: "1",
			Repos: map[string]config.RepoConfig{
				"local-repo-worker": {
					URL: "https://github.com/org/local-repo",
				},
			},
		}
		if err := config.SaveSupervisorConfig(filepath.Join(tmpDir, "config.yaml"), supCfg); err != nil {
			t.Fatalf("failed to save supervisor config: %v", err)
		}

		origWd, err := os.Getwd()
		if err != nil {
			t.Fatalf("failed to get working dir: %v", err)
		}
		if err := os.Chdir(tmpDir); err != nil {
			t.Fatalf("failed to chdir to tmpDir: %v", err)
		}
		t.Cleanup(func() { _ = os.Chdir(origWd) })

		out := &bytes.Buffer{}
		errOut := &bytes.Buffer{}
		cmd := newRootCmd(out, errOut)
		cmd.SetArgs([]string{"__complete", "register", "--name", ""})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		output := out.String()
		if !strings.Contains(output, "local-repo-worker") {
			t.Errorf("expected 'local-repo-worker' discovered from cwd config.yaml, got:\n%s", output)
		}
	})

	t.Run("labels prefix and comma filtering", func(t *testing.T) {
		out := &bytes.Buffer{}
		errOut := &bytes.Buffer{}
		cmd := newRootCmd(out, errOut)

		cmd.SetArgs([]string{"__complete", "register", "--labels", "mac"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		output := out.String()
		if !strings.Contains(output, "macOS") {
			t.Errorf("expected 'macOS' in register --labels prefix completion, got:\n%s", output)
		}
		if strings.Contains(output, "linux") {
			t.Errorf("unexpected 'linux' in register --labels prefix completion, got:\n%s", output)
		}

		// Multi-value comma autocompletion
		out.Reset()
		errOut.Reset()
		cmd = newRootCmd(out, errOut)
		cmd.SetArgs([]string{"__complete", "register", "--labels", "self-hosted,m"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		output = out.String()
		if !strings.Contains(output, "self-hosted,macOS") {
			t.Errorf("expected 'self-hosted,macOS' in comma completion, got:\n%s", output)
		}

		// Trailing comma completes all options prefixed
		out.Reset()
		errOut.Reset()
		cmd = newRootCmd(out, errOut)
		cmd.SetArgs([]string{"__complete", "register", "--labels", "self-hosted,"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		output = out.String()
		if !strings.Contains(output, "self-hosted,ARM64") || !strings.Contains(output, "self-hosted,macOS") {
			t.Errorf("expected prefixed labels after trailing comma, got:\n%s", output)
		}
	})

	t.Run("filesystem directives", func(t *testing.T) {
		regCmd := newRegisterCmd()
		if regCmd.Flags().Lookup("config-dir") == nil {
			t.Error("expected config-dir flag on register")
		}
		if regCmd.Flags().Lookup("work-folder") == nil {
			t.Error("expected work-folder flag on register")
		}

		runCmd := newRunCmd()
		if runCmd.Flags().Lookup("config-dir") == nil {
			t.Error("expected config-dir flag on run")
		}
		if runCmd.Flags().Lookup("runs-dir") == nil {
			t.Error("expected runs-dir flag on run")
		}

		monCmd := newMonitorCmd()
		if monCmd.Flags().Lookup("sock") == nil {
			t.Error("expected sock flag on monitor")
		}

		srvCmd := newServiceCmd()
		for _, sub := range srvCmd.Commands() {
			if sub.Name() == "install" {
				if sub.Flags().Lookup("bin") == nil {
					t.Error("expected bin flag on service install")
				}
			}
		}
	})
}

func TestCompletion_Install(t *testing.T) {
	tempHome := t.TempDir()
	brewPrefix := filepath.Join(tempHome, "homebrew")
	siteFunctions := filepath.Join(brewPrefix, "share", "zsh", "site-functions")
	if err := os.MkdirAll(siteFunctions, 0755); err != nil {
		t.Fatalf("failed to create fake site-functions: %v", err)
	}

	// Test path resolution for Homebrew Zsh
	targetPath, err := resolveInstallPath("zsh", tempHome, brewPrefix)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := filepath.Join(siteFunctions, "_actr")
	if targetPath != expected {
		t.Errorf("expected %q, got %q", expected, targetPath)
	}

	// Test path resolution for fallback Zsh (no brew)
	zshFallback, err := resolveInstallPath("zsh", tempHome, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectedZshFallback := filepath.Join(tempHome, ".zsh", "completions", "_actr")
	if zshFallback != expectedZshFallback {
		t.Errorf("expected %q, got %q", expectedZshFallback, zshFallback)
	}

	// Test path resolution for Fish
	fishPath, err := resolveInstallPath("fish", tempHome, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectedFish := filepath.Join(tempHome, ".config", "fish", "completions", "actr.fish")
	if fishPath != expectedFish {
		t.Errorf("expected %q, got %q", expectedFish, fishPath)
	}

	// Test path resolution for Bash
	bashPath, err := resolveInstallPath("bash", tempHome, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectedBash := filepath.Join(tempHome, ".local", "share", "bash-completion", "completions", "actr")
	if bashPath != expectedBash {
		t.Errorf("expected %q, got %q", expectedBash, bashPath)
	}

	// Test case-insensitivity in resolveInstallPath (e.g. "ZSH", "Fish", "BASH")
	zshUpper, err := resolveInstallPath("ZSH", tempHome, "")
	if err != nil || zshUpper != expectedZshFallback {
		t.Errorf("expected %q for ZSH, got %q (err: %v)", expectedZshFallback, zshUpper, err)
	}
	fishMixed, err := resolveInstallPath("Fish", tempHome, "")
	if err != nil || fishMixed != expectedFish {
		t.Errorf("expected %q for Fish, got %q (err: %v)", expectedFish, fishMixed, err)
	}
	bashUpper, err := resolveInstallPath("BASH", tempHome, "")
	if err != nil || bashUpper != expectedBash {
		t.Errorf("expected %q for BASH, got %q (err: %v)", expectedBash, bashUpper, err)
	}

	// Test unsupported shell
	if _, err := resolveInstallPath("unknown", tempHome, ""); err == nil {
		t.Errorf("expected error for unsupported shell, got nil")
	}

	// Test execute install command with explicit argument
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	cmd := newRootCmd(out, errOut)
	cmd.SetArgs([]string{"completion", "install", "fish"})

	origResolver := completionTargetResolver
	t.Cleanup(func() { completionTargetResolver = origResolver })
	completionTargetResolver = func(shell string) (string, error) {
		return filepath.Join(tempHome, "actr.fish"), nil
	}

	if err := cmd.Execute(); err != nil {
		t.Fatalf("completion install error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(tempHome, "actr.fish"))
	if err != nil {
		t.Fatalf("failed to read installed completion file: %v", err)
	}
	if !strings.Contains(string(data), "complete -c actr") {
		t.Errorf("expected fish completion content, got:\n%s", string(data))
	}
	if !strings.Contains(out.String(), "Installed fish completion") {
		t.Errorf("expected success message in stdout, got:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Restart your shell or run: source") {
		t.Errorf("expected reload instructions in fish install output, got:\n%s", out.String())
	}

	// Test execute install command with auto-detection from SHELL
	t.Setenv("SHELL", "/bin/zsh")
	out.Reset()
	errOut.Reset()
	completionTargetResolver = func(shell string) (string, error) {
		if shell != "zsh" {
			t.Errorf("expected auto-detected shell zsh, got %q", shell)
		}
		return filepath.Join(tempHome, "_actr"), nil
	}

	cmd = newRootCmd(out, errOut)
	cmd.SetArgs([]string{"completion", "install"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("completion install auto-detect error: %v", err)
	}

	zshData, err := os.ReadFile(filepath.Join(tempHome, "_actr"))
	if err != nil {
		t.Fatalf("failed to read installed zsh completion file: %v", err)
	}
	if !strings.Contains(string(zshData), "#compdef actr") {
		t.Errorf("expected zsh completion content, got:\n%s", string(zshData))
	}
	if !strings.Contains(out.String(), "autoload -Uz compinit && compinit") {
		t.Errorf("expected zsh compinit instructions in stdout, got:\n%s", out.String())
	}

	// Test atomic cleanup when completion generation fails
	t.Run("atomic cleanup on generateCompletion error", func(t *testing.T) {
		failDir := t.TempDir()
		failPath := filepath.Join(failDir, "partial.tmp")
		completionTargetResolver = func(shell string) (string, error) {
			return failPath, nil
		}

		failOut := &bytes.Buffer{}
		failErrOut := &bytes.Buffer{}
		failCmd := newRootCmd(failOut, failErrOut)
		failCmd.SetArgs([]string{"completion", "install", "unsupported"})
		runErr := failCmd.Execute()
		if runErr == nil {
			t.Fatal("expected error installing unsupported shell, got nil")
		}
		if _, statErr := os.Stat(failPath); !os.IsNotExist(statErr) {
			t.Errorf("expected partial file %s to be cleaned up, but it still exists (err: %v)", failPath, statErr)
		}
	})

	// Test execute install command without SHELL and without arg
	t.Setenv("SHELL", "")
	cmd = newRootCmd(out, errOut)
	cmd.SetArgs([]string{"completion", "install"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error when SHELL is empty and no args provided, got nil")
	}

	// Test detectBrewPrefix with environment variable
	t.Setenv("HOMEBREW_PREFIX", "/custom/homebrew")
	if prefix := detectBrewPrefix(); prefix != "/custom/homebrew" {
		t.Errorf("expected /custom/homebrew, got %q", prefix)
	}
}


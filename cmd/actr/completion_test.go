package main

import (
	"bytes"
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

func TestCompletion_RunAndRegisterFlags(t *testing.T) {
	expectedLabels := []string{"self-hosted", "macOS", "ARM64", "linux", "x64"}

	t.Run("run --labels completion", func(t *testing.T) {
		out := &bytes.Buffer{}
		errOut := &bytes.Buffer{}
		cmd := newRootCmd(out, errOut)

		cmd.SetArgs([]string{"__complete", "run", "--labels", ""})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		output := out.String()
		for _, label := range expectedLabels {
			if !strings.Contains(output, label) {
				t.Errorf("expected label %q in run --labels completion, got:\n%s", label, output)
			}
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

	t.Run("name completion from configs", func(t *testing.T) {
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

		// Test on run command
		out := &bytes.Buffer{}
		errOut := &bytes.Buffer{}
		cmd := newRootCmd(out, errOut)
		cmd.SetArgs([]string{"__complete", "run", "--config-dir", tmpDir, "--name", ""})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		output := out.String()
		if !strings.Contains(output, "test-runner-alpha") {
			t.Errorf("expected 'test-runner-alpha' in run --name completion, got:\n%s", output)
		}
		if !strings.Contains(output, "repo-worker-beta") {
			t.Errorf("expected 'repo-worker-beta' in run --name completion, got:\n%s", output)
		}

		// Test on register command
		out.Reset()
		errOut.Reset()
		cmd = newRootCmd(out, errOut)
		cmd.SetArgs([]string{"__complete", "register", "--config-dir", tmpDir, "--name", ""})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		output = out.String()
		if !strings.Contains(output, "test-runner-alpha") {
			t.Errorf("expected 'test-runner-alpha' in register --name completion, got:\n%s", output)
		}
		if !strings.Contains(output, "repo-worker-beta") {
			t.Errorf("expected 'repo-worker-beta' in register --name completion, got:\n%s", output)
		}

		// Test prefix filtering on run --name
		out.Reset()
		errOut.Reset()
		cmd = newRootCmd(out, errOut)
		cmd.SetArgs([]string{"__complete", "run", "--config-dir", tmpDir, "--name", "repo-"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		output = out.String()
		if !strings.Contains(output, "repo-worker-beta") {
			t.Errorf("expected 'repo-worker-beta' in run --name prefix completion, got:\n%s", output)
		}
		if strings.Contains(output, "test-runner-alpha") {
			t.Errorf("unexpected 'test-runner-alpha' in run --name prefix completion, got:\n%s", output)
		}
	})

	t.Run("labels prefix filtering", func(t *testing.T) {
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

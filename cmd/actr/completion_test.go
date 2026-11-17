package main

import (
	"bytes"
	"strings"
	"testing"
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

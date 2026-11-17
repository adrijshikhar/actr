package action_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/adrijshikhar/actr/internal/action"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildActionEnvironment_DualKeyAndSanitization(t *testing.T) {
	manifest := &action.ActionManifest{
		Name: "Test Action",
		Inputs: map[string]action.ActionInput{
			"go-version": {
				Description: "The Go version to download",
				Default:     "1.21",
			},
			"cache dependency path": {
				Description: "Path to dependency file",
				Default:     "go.sum",
			},
			"check-latest": {
				Description: "Check for latest version",
				Default:     "false",
			},
			"my-custom input": {
				Description: "Mixed hyphen and space",
				Default:     "default-val",
			},
		},
	}

	actCtx := &action.ActionContext{
		StepID: "step-1",
		Inputs: map[string]string{
			"go-version":      "1.24",
			"my-custom input": "overridden-val",
		},
		Manifest: manifest,
		Env: map[string]string{
			"EXISTING_ENV": "existing_value",
		},
		WorkspaceDir:   "/tmp/workspace",
		TempDir:        "/tmp/runner_temp",
		ToolCacheDir:   "/tmp/runner_tool_cache",
		StatePath:      "/tmp/state.txt",
		PathFilePath:   "/tmp/path.txt",
		EnvFilePath:    "/tmp/env.txt",
		OutputFilePath: "/tmp/output.txt",
		ActionDir:      "/tmp/actions/setup-go",
	}

	env := action.BuildActionEnvironment(actCtx)

	// Verify existing env is preserved
	assert.Equal(t, "existing_value", env["EXISTING_ENV"])

	// Dual-key verification for "go-version" (overridden value "1.24")
	// Exact @actions/core form: INPUT_GO-VERSION
	assert.Equal(t, "1.24", env["INPUT_GO-VERSION"], "exact key INPUT_GO-VERSION must match override")
	// Shell-sanitized form: INPUT_GO_VERSION
	assert.Equal(t, "1.24", env["INPUT_GO_VERSION"], "sanitized key INPUT_GO_VERSION must match override")

	// Dual-key verification for "cache dependency path" (from manifest default)
	assert.Equal(t, "go.sum", env["INPUT_CACHE_DEPENDENCY_PATH"])

	// Dual-key verification for "check-latest" (from manifest default)
	assert.Equal(t, "false", env["INPUT_CHECK-LATEST"])
	assert.Equal(t, "false", env["INPUT_CHECK_LATEST"])

	// Dual-key verification for "my-custom input"
	// Exact form: space -> _ (hyphen kept)
	assert.Equal(t, "overridden-val", env["INPUT_MY-CUSTOM_INPUT"])
	// Shell-sanitized form: space -> _, hyphen -> _
	assert.Equal(t, "overridden-val", env["INPUT_MY_CUSTOM_INPUT"])

	// GitHub special file command environment variables
	assert.Equal(t, "step-1", env["GITHUB_ACTION"])
	assert.Equal(t, "/tmp/workspace", env["GITHUB_WORKSPACE"])
	assert.Equal(t, "/tmp/runner_temp", env["RUNNER_TEMP"])
	assert.Equal(t, "/tmp/runner_tool_cache", env["RUNNER_TOOL_CACHE"])
	assert.Equal(t, "/tmp/runner_tool_cache", env["AGENT_TOOLSDIRECTORY"])
	assert.Equal(t, "/tmp/state.txt", env["GITHUB_STATE"])
	assert.Equal(t, "/tmp/path.txt", env["GITHUB_PATH"])
	assert.Equal(t, "/tmp/env.txt", env["GITHUB_ENV"])
	assert.Equal(t, "/tmp/output.txt", env["GITHUB_OUTPUT"])
	assert.Equal(t, "/tmp/actions/setup-go", env["GITHUB_ACTION_PATH"])
}

func TestBuildActionEnvironment_DefaultInputsMerging(t *testing.T) {
	manifest := &action.ActionManifest{
		Inputs: map[string]action.ActionInput{
			"version": {
				Default: "v1.0.0",
			},
			"token": {
				Default: "secret-token",
			},
			"empty-default": {
				Default: "",
			},
		},
	}

	actCtx := &action.ActionContext{
		Manifest: manifest,
		Inputs: map[string]string{
			"token":     "user-provided-token",
			"new-input": "new-value",
		},
	}

	env := action.BuildActionEnvironment(actCtx)

	// Manifest default applied when not overridden
	assert.Equal(t, "v1.0.0", env["INPUT_VERSION"])
	// User input overrides manifest default
	assert.Equal(t, "user-provided-token", env["INPUT_TOKEN"])
	// User input not in manifest is still passed
	assert.Equal(t, "new-value", env["INPUT_NEW-INPUT"])
	assert.Equal(t, "new-value", env["INPUT_NEW_INPUT"])
	// Empty default in manifest without override shouldn't create empty key if unset
	_, hasEmpty := env["INPUT_EMPTY-DEFAULT"]
	assert.False(t, hasEmpty, "empty default without override should not produce an input key")
}

func TestBuildActionEnvironment_NilSafety(t *testing.T) {
	// Nil context
	envNil := action.BuildActionEnvironment(nil)
	assert.NotNil(t, envNil)
	assert.Empty(t, envNil)

	// Empty context
	emptyCtx := &action.ActionContext{}
	envEmpty := action.BuildActionEnvironment(emptyCtx)
	assert.NotNil(t, envEmpty)
	assert.Contains(t, envEmpty, "GITHUB_ACTION")
}

func TestExecuteNodeAction_Success(t *testing.T) {
	detector := action.NewHostNodeDetector("")
	nodePath, err := detector.Detect(context.Background())
	if err != nil {
		t.Skipf("Node.js runtime not found on host, skipping: %v", err)
	}

	tempDir := t.TempDir()
	workspaceDir := filepath.Join(tempDir, "workspace")
	require.NoError(t, os.MkdirAll(workspaceDir, 0755))

	outputFile := filepath.Join(tempDir, "output.txt")
	require.NoError(t, os.WriteFile(outputFile, nil, 0644))

	scriptPath := filepath.Join(tempDir, "index.js")
	scriptContent := `
const fs = require('fs');
const outPath = process.env.GITHUB_OUTPUT;
fs.appendFileSync(outPath, "greeting=hello " + process.env['INPUT_WHO-TO-GREET'] + "\n");
console.log("LOG: Greeting delivered to " + process.env['INPUT_WHO_TO_GREET']);
process.stderr.write("STDERR: Everything normal\n");
`
	require.NoError(t, os.WriteFile(scriptPath, []byte(scriptContent), 0644))

	actCtx := &action.ActionContext{
		StepID:         "greet-step",
		WorkspaceDir:   workspaceDir,
		TempDir:        tempDir,
		OutputFilePath: outputFile,
		Inputs: map[string]string{
			"who-to-greet": "world",
		},
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err = action.ExecuteNodeAction(ctx, nodePath, scriptPath, actCtx, &stdout, &stderr)
	require.NoError(t, err)

	assert.Contains(t, stdout.String(), "LOG: Greeting delivered to world")
	assert.Contains(t, stderr.String(), "STDERR: Everything normal")

	outData, err := os.ReadFile(outputFile)
	require.NoError(t, err)
	assert.Equal(t, "greeting=hello world\n", string(outData))
}

func TestExecuteNodeAction_NonZeroExit(t *testing.T) {
	detector := action.NewHostNodeDetector("")
	nodePath, err := detector.Detect(context.Background())
	if err != nil {
		t.Skipf("Node.js runtime not found on host, skipping: %v", err)
	}

	tempDir := t.TempDir()
	scriptPath := filepath.Join(tempDir, "fail.js")
	scriptContent := `
console.error("fatal failure");
process.exit(42);
`
	require.NoError(t, os.WriteFile(scriptPath, []byte(scriptContent), 0644))

	actCtx := &action.ActionContext{
		StepID:       "fail-step",
		WorkspaceDir: tempDir,
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err = action.ExecuteNodeAction(ctx, nodePath, scriptPath, actCtx, &stdout, &stderr)
	require.Error(t, err)

	var exitErr *exec.ExitError
	require.True(t, errors.As(err, &exitErr), "expected *exec.ExitError")
	assert.Equal(t, 42, exitErr.ExitCode())
	assert.Contains(t, stderr.String(), "fatal failure")
}

func TestExecuteNodeAction_InvalidArgs(t *testing.T) {
	ctx := context.Background()

	err := action.ExecuteNodeAction(ctx, "", "script.js", nil, nil, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "node path")

	err = action.ExecuteNodeAction(ctx, "node", "", nil, nil, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "script path")

	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	err = action.ExecuteNodeAction(canceledCtx, "node", "script.js", nil, nil, nil)
	assert.ErrorIs(t, err, context.Canceled)
}

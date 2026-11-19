package action_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/adrijshikhar/actr/internal/action"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createMockNodeScript(t *testing.T, dir, filename string, succeeds bool) string {
	t.Helper()
	path := filepath.Join(dir, filename)
	var content string
	if succeeds {
		content = "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then\n  echo \"v20.10.0\"\n  exit 0\nfi\nexit 1\n"
	} else {
		content = "#!/bin/sh\nexit 1\n"
	}
	err := os.WriteFile(path, []byte(content), 0755)
	require.NoError(t, err)
	return path
}

func TestHostNodeDetector_InterfaceCompliance(t *testing.T) {
	var _ action.NodeDetector = (*action.HostNodeDetector)(nil)
}

func TestHostNodeDetector_CustomPath(t *testing.T) {
	tmpDir := t.TempDir()
	mockNode := createMockNodeScript(t, tmpDir, "node", true)

	detector := action.NewHostNodeDetector(mockNode)
	path, err := detector.Detect(context.Background())
	require.NoError(t, err)
	assert.Equal(t, mockNode, path)
}

func TestHostNodeDetector_CustomPath_NonExistent(t *testing.T) {
	nonExistentPath := filepath.Join(t.TempDir(), "non-existent-node")
	detector := action.NewHostNodeDetector(nonExistentPath)
	path, err := detector.Detect(context.Background())
	require.Error(t, err)
	assert.Empty(t, path)
	assert.Contains(t, err.Error(), "not found")
}

func TestHostNodeDetector_CustomPath_FailsVerification(t *testing.T) {
	tmpDir := t.TempDir()
	badNode := createMockNodeScript(t, tmpDir, "bad-node", false)

	detector := action.NewHostNodeDetector(badNode)
	path, err := detector.Detect(context.Background())
	require.Error(t, err)
	assert.Empty(t, path)
}

func TestHostNodeDetector_EnvVar(t *testing.T) {
	tmpDir := t.TempDir()
	mockNode := createMockNodeScript(t, tmpDir, "env-node", true)

	t.Setenv("ACTR_NODE_PATH", mockNode)
	detector := action.NewHostNodeDetector("")
	path, err := detector.Detect(context.Background())
	require.NoError(t, err)
	assert.Equal(t, mockNode, path)
}

func TestHostNodeDetector_EnvVar_InvalidFallsBack(t *testing.T) {
	t.Setenv("ACTR_NODE_PATH", filepath.Join(t.TempDir(), "non-existent-node"))
	detector := action.NewHostNodeDetector("")
	path, err := detector.Detect(context.Background())
	if err != nil {
		t.Skipf("Node.js is not installed on this host: %v", err)
	}
	assert.NotEmpty(t, path)
	assert.NotContains(t, path, "non-existent-node")
}

func TestHostNodeDetector_DetectHostNode(t *testing.T) {
	// If host has node available, Detect should find it
	detector := action.NewHostNodeDetector("")
	path, err := detector.Detect(context.Background())
	if err != nil {
		t.Skipf("Node.js is not installed on this host: %v", err)
	}
	assert.NotEmpty(t, path)
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.False(t, info.IsDir())
}

func TestHostNodeDetector_ContextCanceled(t *testing.T) {
	tmpDir := t.TempDir()
	mockNode := createMockNodeScript(t, tmpDir, "node", true)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	detector := action.NewHostNodeDetector(mockNode)
	_, err := detector.Detect(ctx)
	require.Error(t, err)
}

package action_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/adrijshikhar/actr/internal/action"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseManifest(t *testing.T) {
	t.Run("valid node20 action manifest", func(t *testing.T) {
		yamlData := []byte(`
name: 'Setup Node'
author: 'GitHub'
description: 'Setup a Node.js environment'
inputs:
  node-version:
    description: 'Version Spec of the version to use'
    required: false
    default: '20.x'
    deprecationMessage: 'Consider upgrading to node 22'
  check-latest:
    description: 'Check latest version'
    required: true
outputs:
  node-version:
    description: 'The installed node version'
    value: '${{ steps.install.outputs.version }}'
runs:
  using: 'node20'
  pre: 'dist/pre.js'
  pre-if: 'always()'
  main: 'dist/index.js'
  post: 'dist/post.js'
  post-if: 'success()'
`)

		manifest, err := action.ParseManifest(yamlData)
		require.NoError(t, err)
		require.NotNil(t, manifest)

		assert.Equal(t, "Setup Node", manifest.Name)
		assert.Equal(t, "GitHub", manifest.Author)
		assert.Equal(t, "Setup a Node.js environment", manifest.Description)

		// Check inputs
		require.Contains(t, manifest.Inputs, "node-version")
		assert.Equal(t, "Version Spec of the version to use", manifest.Inputs["node-version"].Description)
		assert.False(t, manifest.Inputs["node-version"].Required)
		assert.Equal(t, "20.x", manifest.Inputs["node-version"].Default)
		assert.Equal(t, "Consider upgrading to node 22", manifest.Inputs["node-version"].DeprecationMessage)

		require.Contains(t, manifest.Inputs, "check-latest")
		assert.True(t, manifest.Inputs["check-latest"].Required)

		// Check outputs
		require.Contains(t, manifest.Outputs, "node-version")
		assert.Equal(t, "The installed node version", manifest.Outputs["node-version"].Description)
		assert.Equal(t, "${{ steps.install.outputs.version }}", manifest.Outputs["node-version"].Value)

		// Check runs
		assert.Equal(t, "node20", manifest.Runs.Using)
		assert.Equal(t, "dist/pre.js", manifest.Runs.Pre)
		assert.Equal(t, "always()", manifest.Runs.PreIf)
		assert.Equal(t, "dist/index.js", manifest.Runs.Main)
		assert.Equal(t, "dist/post.js", manifest.Runs.Post)
		assert.Equal(t, "success()", manifest.Runs.PostIf)
	})

	t.Run("non-string default inputs unmarshaled as strings", func(t *testing.T) {
		yamlData := []byte(`
name: 'Action With Non-String Defaults'
description: 'Tests boolean and integer defaults'
inputs:
  no-cache:
    required: false
    default: false
  cache-ttl:
    required: false
    default: 3600
  feature-flag:
    required: false
    default: true
runs:
  using: 'node20'
  main: 'dist/index.js'
`)
		manifest, err := action.ParseManifest(yamlData)
		require.NoError(t, err)
		require.NotNil(t, manifest)

		require.Contains(t, manifest.Inputs, "no-cache")
		assert.Equal(t, "false", manifest.Inputs["no-cache"].Default)

		require.Contains(t, manifest.Inputs, "cache-ttl")
		assert.Equal(t, "3600", manifest.Inputs["cache-ttl"].Default)

		require.Contains(t, manifest.Inputs, "feature-flag")
		assert.Equal(t, "true", manifest.Inputs["feature-flag"].Default)
	})

	t.Run("missing runs.using returns error", func(t *testing.T) {
		yamlData := []byte(`
name: 'Broken Action'
description: 'Action without using'
runs:
  main: 'index.js'
`)
		manifest, err := action.ParseManifest(yamlData)
		assert.Error(t, err)
		assert.Nil(t, manifest)
		assert.Contains(t, err.Error(), "runs.using")
	})

	t.Run("empty YAML returns error", func(t *testing.T) {
		manifest, err := action.ParseManifest([]byte(""))
		assert.Error(t, err)
		assert.Nil(t, manifest)
	})

	t.Run("malformed YAML returns error", func(t *testing.T) {
		yamlData := []byte(`
name: [unterminated list
runs:
  using: node20
`)
		manifest, err := action.ParseManifest(yamlData)
		assert.Error(t, err)
		assert.Nil(t, manifest)
	})
}

func TestActionInput_NullDefaultIsEmpty(t *testing.T) {
	raw := []byte("runs:\n  using: node20\n  main: dist/index.js\ninputs:\n  tok:\n    default: null\n  tilde:\n    default: ~\n")
	m, err := action.ParseManifest(raw)
	require.NoError(t, err)
	assert.Equal(t, "", m.Inputs["tok"].Default)
	assert.Equal(t, "", m.Inputs["tilde"].Default)
}

func TestParseManifestFile_RejectsSymlinkOutsideDir(t *testing.T) {
	tmpDir := t.TempDir()
	outsideDir := t.TempDir()

	outsideManifest := filepath.Join(outsideDir, "action.yml")
	err := os.WriteFile(outsideManifest, []byte("runs:\n  using: node20\n  main: index.js\n"), 0o600)
	require.NoError(t, err)

	symlinkPath := filepath.Join(tmpDir, "action.yml")
	err = os.Symlink(outsideManifest, symlinkPath)
	require.NoError(t, err)

	manifest, err := action.ParseManifestFile(tmpDir)
	assert.Error(t, err)
	assert.Nil(t, manifest)
	assert.ErrorIs(t, err, action.ErrPathTraversal)
}

func TestParseManifestFile(t *testing.T) {
	t.Run("prefers action.yml over action.yaml", func(t *testing.T) {
		tmpDir := t.TempDir()

		ymlContent := []byte(`
name: 'Action from YML'
runs:
  using: 'node20'
  main: 'index.js'
`)
		yamlContent := []byte(`
name: 'Action from YAML'
runs:
  using: 'node20'
  main: 'index.js'
`)

		err := os.WriteFile(filepath.Join(tmpDir, "action.yml"), ymlContent, 0o600)
		require.NoError(t, err)
		err = os.WriteFile(filepath.Join(tmpDir, "action.yaml"), yamlContent, 0o600)
		require.NoError(t, err)

		manifest, err := action.ParseManifestFile(tmpDir)
		require.NoError(t, err)
		require.NotNil(t, manifest)
		assert.Equal(t, "Action from YML", manifest.Name)
	})

	t.Run("falls back to action.yaml if action.yml does not exist", func(t *testing.T) {
		tmpDir := t.TempDir()

		yamlContent := []byte(`
name: 'Action from YAML only'
runs:
  using: 'composite'
`)

		err := os.WriteFile(filepath.Join(tmpDir, "action.yaml"), yamlContent, 0o600)
		require.NoError(t, err)

		manifest, err := action.ParseManifestFile(tmpDir)
		require.NoError(t, err)
		require.NotNil(t, manifest)
		assert.Equal(t, "Action from YAML only", manifest.Name)
		assert.Equal(t, "composite", manifest.Runs.Using)
	})

	t.Run("returns error when neither file exists", func(t *testing.T) {
		tmpDir := t.TempDir()

		manifest, err := action.ParseManifestFile(tmpDir)
		assert.Error(t, err)
		assert.Nil(t, manifest)
		assert.Contains(t, err.Error(), "action manifest not found")
	})

	t.Run("returns error when directory does not exist", func(t *testing.T) {
		manifest, err := action.ParseManifestFile(filepath.Join(t.TempDir(), "nonexistent"))
		assert.Error(t, err)
		assert.Nil(t, manifest)
	})
}

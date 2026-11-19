package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadSupervisorConfig_Valid(t *testing.T) {
	yamlContent := `version: "1"
global:
  max_concurrent_jobs: 2
  default_driver: host
  retention:
    max_runs: 50
    max_disk_mb: 500
  docker:
    socket: "/var/run/docker.sock"
    auto_pull: true

repos:
  aim:
    url: https://github.com/adrijshikhar/aim
    config_dir: repos/aim
    max_concurrent_jobs: 1
    driver: host
    limits:
      cpu: "4.0"
      memory: "8GB"
      timeout: "25m"
      pids: 512
  cxstatusline:
    url: https://github.com/adrijshikhar/cxstatusline
    max_concurrent_jobs: 1
    limits:
      cpu: 2
      memory: "3GiB"
      timeout: "15m"
`
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	err := os.WriteFile(cfgPath, []byte(yamlContent), 0644)
	require.NoError(t, err)

	cfg, err := LoadSupervisorConfig(cfgPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, "1", cfg.Version)
	assert.Equal(t, 2, cfg.Global.MaxConcurrentJobs)
	assert.Equal(t, "host", cfg.Global.DefaultDriver)
	assert.Equal(t, 50, cfg.Global.Retention.MaxRuns)
	assert.Equal(t, int64(500), cfg.Global.Retention.MaxDiskMB)
	assert.Equal(t, "/var/run/docker.sock", cfg.Global.Docker.Socket)
	assert.True(t, cfg.Global.Docker.AutoPull)
	require.Len(t, cfg.Repos, 2)

	aimRepo := cfg.Repos["aim"]
	assert.Equal(t, "https://github.com/adrijshikhar/aim", aimRepo.URL)
	expectedAimDir := filepath.Join(tmpDir, "repos", "aim")
	assert.Equal(t, expectedAimDir, aimRepo.ConfigDir)
	assert.Equal(t, 1, aimRepo.MaxConcurrentJobs)
	assert.Equal(t, "host", aimRepo.Driver)

	aimLimits, err := aimRepo.ParseLimits()
	require.NoError(t, err)
	assert.Equal(t, 4.0, aimLimits.CPU)
	assert.Equal(t, int64(8000000000), aimLimits.Memory)
	assert.Equal(t, 25*time.Minute, aimLimits.Timeout)
	assert.Equal(t, int64(512), aimLimits.Pids)

	cxRepo := cfg.Repos["cxstatusline"]
	assert.Equal(t, "https://github.com/adrijshikhar/cxstatusline", cxRepo.URL)
	expectedCxDir := filepath.Join(tmpDir, "repos", "cxstatusline")
	assert.Equal(t, expectedCxDir, cxRepo.ConfigDir)
	assert.Equal(t, 1, cxRepo.MaxConcurrentJobs)

	cxLimits, err := cxRepo.ParseLimits()
	require.NoError(t, err)
	assert.Equal(t, 2.0, cxLimits.CPU)
	assert.Equal(t, int64(3*1024*1024*1024), cxLimits.Memory)
	assert.Equal(t, 15*time.Minute, cxLimits.Timeout)
}

func TestLoadSupervisorConfig_Defaults(t *testing.T) {
	yamlContent := `version: "1"
repos:
  minimal:
    url: https://github.com/example/minimal
  abs_dir:
    url: https://github.com/example/abs
    config_dir: /custom/absolute/path
`
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	err := os.WriteFile(cfgPath, []byte(yamlContent), 0644)
	require.NoError(t, err)

	cfg, err := LoadSupervisorConfig(cfgPath)
	require.NoError(t, err)

	// Global default
	assert.Equal(t, 2, cfg.Global.MaxConcurrentJobs)

	// Minimal repo defaults
	minimal := cfg.Repos["minimal"]
	assert.Equal(t, 1, minimal.MaxConcurrentJobs)
	assert.Equal(t, filepath.Join(tmpDir, "repos", "minimal"), minimal.ConfigDir)
	limits, err := minimal.ParseLimits()
	require.NoError(t, err)
	assert.Equal(t, float64(0), limits.CPU)
	assert.Equal(t, int64(0), limits.Memory)
	assert.Equal(t, int64(0), limits.Pids)
	assert.Equal(t, time.Duration(0), limits.Timeout)

	// Absolute config_dir remains unchanged
	absRepo := cfg.Repos["abs_dir"]
	assert.Equal(t, "/custom/absolute/path", absRepo.ConfigDir)
}

func TestLoadSupervisorConfig_ValidationErrors(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"missing version", `global: {max_concurrent_jobs: 2}`},
		{"wrong version", `version: "2"` + "\n" + `global: {max_concurrent_jobs: 2}` + "\n" + `repos: {a: {url: "http"}}`},
		{"empty repos", `version: "1"` + "\n" + `global: {max_concurrent_jobs: 2}` + "\n" + `repos: {}`},
		{"missing repos key", `version: "1"` + "\n" + `global: {max_concurrent_jobs: 2}`},
		{"negative timeout", `version: "1"` + "\n" + `global: {max_concurrent_jobs: 1}` + "\n" + `repos: {a: {url: "http", limits: {timeout: "-5m"}}}`},
		{"invalid timeout format", `version: "1"` + "\n" + `repos: {a: {url: "http", limits: {timeout: "invalid"}}}`},
		{"negative pids", `version: "1"` + "\n" + `repos: {a: {url: "http", limits: {pids: -1}}}`},
		{"invalid cpu", `version: "1"` + "\n" + `repos: {a: {url: "http", limits: {cpu: "invalid"}}}`},
		{"negative cpu", `version: "1"` + "\n" + `repos: {a: {url: "http", limits: {cpu: -2}}}`},
		{"invalid memory", `version: "1"` + "\n" + `repos: {a: {url: "http", limits: {memory: "not_a_size"}}}`},
		{"negative memory", `version: "1"` + "\n" + `repos: {a: {url: "http", limits: {memory: "-10MB"}}}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			cfgPath := filepath.Join(tmpDir, "config.yaml")
			err := os.WriteFile(cfgPath, []byte(tc.content), 0644)
			require.NoError(t, err)

			_, err = LoadSupervisorConfig(cfgPath)
			assert.Error(t, err, "expected error for case: %s", tc.name)
		})
	}

	t.Run("non-existent file", func(t *testing.T) {
		_, err := LoadSupervisorConfig("/non/existent/path/config.yaml")
		assert.Error(t, err)
	})

	t.Run("corrupted yaml", func(t *testing.T) {
		tmpDir := t.TempDir()
		cfgPath := filepath.Join(tmpDir, "config.yaml")
		_ = os.WriteFile(cfgPath, []byte("{not valid yaml: ["), 0644)
		_, err := LoadSupervisorConfig(cfgPath)
		assert.Error(t, err)
	})
}

func TestIsMultiRepoConfig(t *testing.T) {
	tmpDir := t.TempDir()

	// Valid multi-repo config
	validPath := filepath.Join(tmpDir, "valid.yaml")
	err := os.WriteFile(validPath, []byte(`version: "1"`+"\n"+`repos:`+"\n"+`  repo1:`+"\n"+`    url: http`), 0644)
	require.NoError(t, err)
	assert.True(t, IsMultiRepoConfig(validPath))

	// Single-runner config (no version: "1" and no repos)
	singleRunnerPath := filepath.Join(tmpDir, "single.yaml")
	err = os.WriteFile(singleRunnerPath, []byte(`runner:`+"\n"+`  name: test-runner`+"\n"+`  work_folder: _work`), 0644)
	require.NoError(t, err)
	assert.False(t, IsMultiRepoConfig(singleRunnerPath))

	// Version not "1"
	wrongVersionPath := filepath.Join(tmpDir, "wrong_ver.yaml")
	err = os.WriteFile(wrongVersionPath, []byte(`version: "2"`+"\n"+`repos:`+"\n"+`  repo1:`+"\n"+`    url: http`), 0644)
	require.NoError(t, err)
	assert.False(t, IsMultiRepoConfig(wrongVersionPath))

	// Empty repos
	emptyReposPath := filepath.Join(tmpDir, "empty_repos.yaml")
	err = os.WriteFile(emptyReposPath, []byte(`version: "1"`+"\n"+`repos: {}`), 0644)
	require.NoError(t, err)
	assert.False(t, IsMultiRepoConfig(emptyReposPath))

	// Non-existent file
	assert.False(t, IsMultiRepoConfig(filepath.Join(tmpDir, "does_not_exist.yaml")))

	// Corrupted yaml
	badYamlPath := filepath.Join(tmpDir, "bad.yaml")
	err = os.WriteFile(badYamlPath, []byte("{ bad yaml: ["), 0644)
	require.NoError(t, err)
	assert.False(t, IsMultiRepoConfig(badYamlPath))
}

func TestRepoConfig_ParseLimits_EdgeCases(t *testing.T) {
	var nilRepo *RepoConfig
	lim, err := nilRepo.ParseLimits()
	assert.NoError(t, err)
	assert.Nil(t, lim)

	repo := &RepoConfig{
		Limits: LimitsYAMLConfig{
			CPU: "500m",
		},
	}
	lim, err = repo.ParseLimits()
	require.NoError(t, err)
	assert.Equal(t, 0.5, lim.CPU)

	repoFloat := &RepoConfig{
		Limits: LimitsYAMLConfig{
			CPU: 1.5,
		},
	}
	limFloat, err := repoFloat.ParseLimits()
	require.NoError(t, err)
	assert.Equal(t, 1.5, limFloat.CPU)
}

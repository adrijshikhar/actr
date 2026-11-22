package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSaveAndLoadConfig(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &RunnerConfig{
		AgentID:    101,
		AgentName:  "test-runner-1",
		PoolID:     1,
		ServerURL:  "https://broker.actions.githubusercontent.com",
		GitHubURL:  "https://github.com/adrijshikhar/cxstatusline",
		WorkFolder: "_work",
	}

	creds := &Credentials{
		ClientID:  "client-uuid-1234",
		IDToken:   "id-token-abc",
		SessionID: "session-uuid-5678",
	}

	if err := SaveRunnerConfig(tmpDir, cfg); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	if err := SaveCredentials(tmpDir, creds); err != nil {
		t.Fatalf("failed to save credentials: %v", err)
	}

	// Verify file permissions (credentials must be 0600)
	credPath := filepath.Join(tmpDir, ".credentials")
	info, err := os.Stat(credPath)
	if err != nil {
		t.Fatalf("stat failed on credentials: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("expected credentials permissions 0600, got %o", info.Mode().Perm())
	}

	// Verify .runner file permissions (should be 0644)
	cfgPath := filepath.Join(tmpDir, ".runner")
	cfgInfo, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatalf("stat failed on runner config: %v", err)
	}
	if cfgInfo.Mode().Perm() != 0644 {
		t.Fatalf("expected runner config permissions 0644, got %o", cfgInfo.Mode().Perm())
	}

	loadedCfg, err := LoadRunnerConfig(tmpDir)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}
	if loadedCfg.AgentID != cfg.AgentID ||
		loadedCfg.AgentName != cfg.AgentName ||
		loadedCfg.PoolID != cfg.PoolID ||
		loadedCfg.ServerURL != cfg.ServerURL ||
		loadedCfg.GitHubURL != cfg.GitHubURL ||
		loadedCfg.WorkFolder != cfg.WorkFolder {
		t.Fatalf("mismatched loaded config: %+v", loadedCfg)
	}

	loadedCreds, err := LoadCredentials(tmpDir)
	if err != nil {
		t.Fatalf("failed to load credentials: %v", err)
	}
	if loadedCreds.ClientID != creds.ClientID ||
		loadedCreds.IDToken != creds.IDToken ||
		loadedCreds.SessionID != creds.SessionID {
		t.Fatalf("mismatched loaded credentials: %+v", loadedCreds)
	}
}

func TestLoadConfigErrors(t *testing.T) {
	tmpDir := t.TempDir()

	// Missing file
	if _, err := LoadRunnerConfig(tmpDir); err == nil {
		t.Fatalf("expected error loading missing runner config, got nil")
	}
	if _, err := LoadCredentials(tmpDir); err == nil {
		t.Fatalf("expected error loading missing credentials, got nil")
	}

	// Corrupted JSON
	if err := os.WriteFile(filepath.Join(tmpDir, ".runner"), []byte("{bad json"), 0644); err != nil {
		t.Fatalf("failed to write bad config: %v", err)
	}
	if _, err := LoadRunnerConfig(tmpDir); err == nil {
		t.Fatalf("expected error loading corrupted runner config, got nil")
	}

	if err := os.WriteFile(filepath.Join(tmpDir, ".credentials"), []byte("{bad json"), 0600); err != nil {
		t.Fatalf("failed to write bad credentials: %v", err)
	}
	if _, err := LoadCredentials(tmpDir); err == nil {
		t.Fatalf("expected error loading corrupted credentials, got nil")
	}
}

func TestSaveCredentialsEnforcesPermissions(t *testing.T) {
	tmpDir := t.TempDir()
	credPath := filepath.Join(tmpDir, ".credentials")

	// Pre-create file with permissive mode 0666
	if err := os.WriteFile(credPath, []byte("{}"), 0666); err != nil {
		t.Fatalf("failed to pre-create credentials file: %v", err)
	}

	creds := &Credentials{
		ClientID:  "client-1",
		IDToken:   "token-1",
		SessionID: "sess-1",
	}

	if err := SaveCredentials(tmpDir, creds); err != nil {
		t.Fatalf("failed to save credentials: %v", err)
	}

	info, err := os.Stat(credPath)
	if err != nil {
		t.Fatalf("stat failed: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("expected permissions to be tightened to 0600, got %o", info.Mode().Perm())
	}
}

func TestSaveConfigCreatesDir(t *testing.T) {
	tmpDir := filepath.Join(t.TempDir(), "nested", "config", "dir")

	cfg := &RunnerConfig{
		AgentID: 101,
	}
	if err := SaveRunnerConfig(tmpDir, cfg); err != nil {
		t.Fatalf("failed to save config in non-existent dir: %v", err)
	}

	creds := &Credentials{
		ClientID: "client-1",
	}
	credDir := filepath.Join(t.TempDir(), "nested", "creds", "dir")
	if err := SaveCredentials(credDir, creds); err != nil {
		t.Fatalf("failed to save credentials in non-existent dir: %v", err)
	}
}

func TestConfigV2AndCredentialsData(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &RunnerConfig{
		AgentID:     202,
		AgentName:   "v2-runner",
		PoolID:      1,
		PoolName:    "Default",
		ServerURL:   "https://pipelines.actions.githubusercontent.com",
		GitHubURL:   "https://github.com/adrijshikhar/actr",
		WorkFolder:  "_work",
		UseV2Flow:   true,
		ServerURLV2: "https://broker.actions.githubusercontent.com/",
	}

	creds := &Credentials{
		Scheme:                  "OAuth",
		ClientID:                "v2-client-id",
		AuthorizationURL:        "https://broker.actions.githubusercontent.com/token",
		RequireFipsCryptography: true,
		Data: &CredentialsData{
			ClientID:                "v2-client-id",
			AuthorizationURL:        "https://broker.actions.githubusercontent.com/token",
			RequireFipsCryptography: true,
		},
	}

	if err := SaveRunnerConfig(tmpDir, cfg); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}
	if err := SaveCredentials(tmpDir, creds); err != nil {
		t.Fatalf("failed to save credentials: %v", err)
	}

	loadedCfg, err := LoadRunnerConfig(tmpDir)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}
	if !loadedCfg.UseV2Flow || loadedCfg.ServerURLV2 != cfg.ServerURLV2 || loadedCfg.PoolName != "Default" {
		t.Fatalf("mismatched loaded v2 config: %+v", loadedCfg)
	}

	loadedCreds, err := LoadCredentials(tmpDir)
	if err != nil {
		t.Fatalf("failed to load credentials: %v", err)
	}
	if loadedCreds.Scheme != "OAuth" ||
		loadedCreds.ClientID != "v2-client-id" ||
		loadedCreds.AuthorizationURL != "https://broker.actions.githubusercontent.com/token" ||
		!loadedCreds.RequireFipsCryptography ||
		loadedCreds.Data == nil ||
		loadedCreds.Data.ClientID != "v2-client-id" {
		t.Fatalf("mismatched loaded credentials: %+v", loadedCreds)
	}
}

func TestDefaultConfigDir(t *testing.T) {
	// Test ACTR_CONFIG_DIR env override
	t.Setenv("ACTR_CONFIG_DIR", "/custom/actr/dir")
	t.Setenv("ACT_RUNNER_CONFIG_DIR", "/fallback/dir")
	assert.Equal(t, "/custom/actr/dir", DefaultConfigDir())

	// Test ACT_RUNNER_CONFIG_DIR fallback
	t.Setenv("ACTR_CONFIG_DIR", "")
	t.Setenv("ACT_RUNNER_CONFIG_DIR", "/custom/runner/dir")
	assert.Equal(t, "/custom/runner/dir", DefaultConfigDir())

	// Test default home expansion
	t.Setenv("ACTR_CONFIG_DIR", "")
	t.Setenv("ACT_RUNNER_CONFIG_DIR", "")
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	expected := filepath.Join(home, ".actr")
	assert.Equal(t, expected, DefaultConfigDir())
}

func TestLoadYAMLConfig(t *testing.T) {
	tmpDir := t.TempDir()
	yamlPath := filepath.Join(tmpDir, "config.yaml")

	yamlContent := `
runner:
  name: "m5-pro"
  work_folder: "_work"

limits:
  cpu: "2.0"
  memory: "4GiB"
  pids: 512
  timeout: "30m"

driver:
  default: "host"
  docker:
    socket: "/var/run/docker.sock"
    auto_pull: true
    image_mappings:
      ubuntu-latest: "catthehacker/ubuntu:act-latest"
      node-20: "node:20-bookworm"

retention:
  max_runs: 25
  max_disk_mb: 150
`
	err := os.WriteFile(yamlPath, []byte(yamlContent), 0644)
	require.NoError(t, err)

	cfg, err := LoadYAMLConfig(yamlPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, "m5-pro", cfg.Runner.Name)
	assert.Equal(t, "_work", cfg.Runner.WorkFolder)
	assert.Equal(t, "2.0", cfg.Limits.CPU)
	assert.Equal(t, "4GiB", cfg.Limits.Memory)
	assert.Equal(t, int64(512), cfg.Limits.Pids)
	assert.Equal(t, "30m", cfg.Limits.Timeout)
	assert.Equal(t, "host", cfg.Driver.Default)
	assert.Equal(t, "/var/run/docker.sock", cfg.Driver.Docker.Socket)
	assert.True(t, cfg.Driver.Docker.AutoPull)
	assert.Equal(t, "catthehacker/ubuntu:act-latest", cfg.Driver.Docker.ImageMappings["ubuntu-latest"])
	assert.Equal(t, "node:20-bookworm", cfg.Driver.Docker.ImageMappings["node-20"])
	assert.Equal(t, 25, cfg.Retention.MaxRuns)
	assert.Equal(t, int64(150), cfg.Retention.MaxDiskMB)

	// Test ParseLimits
	limits, err := cfg.ParseLimits()
	require.NoError(t, err)
	require.NotNil(t, limits)
	assert.Equal(t, 2.0, limits.CPU)
	assert.Equal(t, int64(4*1024*1024*1024), limits.Memory)
	assert.Equal(t, int64(512), limits.Pids)
	assert.Equal(t, 30*time.Minute, limits.Timeout)
}

func TestLoadYAMLConfig_WithVault(t *testing.T) {
	tmpDir := t.TempDir()
	yamlPath := filepath.Join(tmpDir, "config_with_vault.yaml")

	yamlContent := `
runner:
  name: "vault-runner"
  work_folder: "_work"

vault:
  enabled: true
  trusted_only: true
  providers:
    - type: "keychain"
      label: "com.github.actr.custom"
    - type: "encrypted-file"
      path: "/tmp/custom-vault.enc"
      auth:
        passphrase: "secret-passphrase"
    - type: "hashicorp-vault"
      address: "https://vault.example.com:8200"
      path: "secret/data/actr"
      auth:
        token: "s.sampletoken"
    - type: "aws-secrets-manager"
      region: "us-east-1"
      prefix: "actr/"
`
	err := os.WriteFile(yamlPath, []byte(yamlContent), 0644)
	require.NoError(t, err)

	cfg, err := LoadYAMLConfig(yamlPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, "vault-runner", cfg.Runner.Name)
	assert.True(t, cfg.Vault.Enabled)
	assert.True(t, cfg.Vault.TrustedOnly)
	require.Len(t, cfg.Vault.Providers, 4)

	assert.Equal(t, "keychain", cfg.Vault.Providers[0].Type)
	assert.Equal(t, "com.github.actr.custom", cfg.Vault.Providers[0].Label)

	assert.Equal(t, "encrypted-file", cfg.Vault.Providers[1].Type)
	assert.Equal(t, "/tmp/custom-vault.enc", cfg.Vault.Providers[1].Path)
	assert.Equal(t, "secret-passphrase", cfg.Vault.Providers[1].Auth["passphrase"])

	assert.Equal(t, "hashicorp-vault", cfg.Vault.Providers[2].Type)
	assert.Equal(t, "https://vault.example.com:8200", cfg.Vault.Providers[2].Address)
	assert.Equal(t, "secret/data/actr", cfg.Vault.Providers[2].Path)
	assert.Equal(t, "s.sampletoken", cfg.Vault.Providers[2].Auth["token"])

	assert.Equal(t, "aws-secrets-manager", cfg.Vault.Providers[3].Type)
	assert.Equal(t, "us-east-1", cfg.Vault.Providers[3].Region)
	assert.Equal(t, "actr/", cfg.Vault.Providers[3].Prefix)
}

func TestLoadYAMLConfig_Errors(t *testing.T) {
	tmpDir := t.TempDir()

	// Non-existent file
	_, err := LoadYAMLConfig(filepath.Join(tmpDir, "missing.yaml"))
	assert.Error(t, err)

	// Malformed YAML
	badPath := filepath.Join(tmpDir, "bad.yaml")
	err = os.WriteFile(badPath, []byte("runner:\n  name: [unclosed"), 0644)
	require.NoError(t, err)

	_, err = LoadYAMLConfig(badPath)
	assert.Error(t, err)
}

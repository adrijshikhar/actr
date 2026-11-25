package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type RunnerConfig struct {
	AgentID     int64  `json:"agentId"`
	AgentName   string `json:"agentName"`
	PoolID      int64  `json:"poolId"`
	PoolName    string `json:"poolName,omitempty"`
	ServerURL   string `json:"serverUrl"`
	GitHubURL   string `json:"gitHubUrl"`
	WorkFolder  string `json:"workFolder"`
	UseV2Flow   bool   `json:"useV2Flow,omitempty"`
	ServerURLV2 string `json:"serverUrlV2,omitempty"`
}

type CredentialsData struct {
	ClientID                string `json:"clientId,omitempty"`
	AuthorizationURL        string `json:"authorizationUrl,omitempty"`
	RequireFipsCryptography bool   `json:"requireFipsCryptography,omitempty"`
}

func (cd *CredentialsData) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if cid, ok := raw["clientId"]; ok {
		if err := json.Unmarshal(cid, &cd.ClientID); err != nil {
			return fmt.Errorf("unmarshal clientId: %w", err)
		}
	}
	if auth, ok := raw["authorizationUrl"]; ok {
		if err := json.Unmarshal(auth, &cd.AuthorizationURL); err != nil {
			return fmt.Errorf("unmarshal authorizationUrl: %w", err)
		}
	}
	if fips, ok := raw["requireFipsCryptography"]; ok {
		var b bool
		if err := json.Unmarshal(fips, &b); err == nil {
			cd.RequireFipsCryptography = b
		} else {
			var s string
			if err := json.Unmarshal(fips, &s); err != nil {
				return fmt.Errorf("unmarshal requireFipsCryptography: %w", err)
			}
			cd.RequireFipsCryptography = strings.EqualFold(s, "true")
		}
	}
	return nil
}

type Credentials struct {
	Scheme                  string           `json:"scheme,omitempty"`
	Data                    *CredentialsData `json:"data,omitempty"`
	ClientID                string           `json:"clientId,omitempty"`
	IDToken                 string           `json:"idToken,omitempty"`
	SessionID               string           `json:"sessionId,omitempty"`
	AuthorizationURL        string           `json:"authorizationUrl,omitempty"`
	RequireFipsCryptography bool             `json:"requireFipsCryptography,omitempty"`
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	closed := false
	cleanup := true
	defer func() {
		if cleanup {
			if !closed {
				_ = tmp.Close()
			}
			_ = os.Remove(tmpName)
		}
	}()

	if err := tmp.Chmod(mode); err != nil {
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	closed = true
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename temp file: %w", err)
	}
	cleanup = false
	return nil
}

func SaveRunnerConfig(dir string, cfg *RunnerConfig) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	path := filepath.Join(dir, ".runner")
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal runner config: %w", err)
	}
	if err := writeFileAtomic(path, data, 0600); err != nil {
		return fmt.Errorf("write runner config: %w", err)
	}
	return nil
}

func LoadRunnerConfig(dir string) (*RunnerConfig, error) {
	path := filepath.Join(dir, ".runner")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read runner config: %w", err)
	}
	var cfg RunnerConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("unmarshal runner config: %w", err)
	}
	return &cfg, nil
}

func SaveCredentials(dir string, creds *Credentials) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	path := filepath.Join(dir, ".credentials")
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal credentials: %w", err)
	}
	if err := writeFileAtomic(path, data, 0600); err != nil {
		return fmt.Errorf("write credentials: %w", err)
	}
	return nil
}

func LoadCredentials(dir string) (*Credentials, error) {
	path := filepath.Join(dir, ".credentials")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read credentials: %w", err)
	}
	var creds Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("unmarshal credentials: %w", err)
	}
	if creds.Data != nil {
		if creds.ClientID == "" {
			creds.ClientID = creds.Data.ClientID
		}
		if creds.AuthorizationURL == "" {
			creds.AuthorizationURL = creds.Data.AuthorizationURL
		}
		if !creds.RequireFipsCryptography && creds.Data.RequireFipsCryptography {
			creds.RequireFipsCryptography = creds.Data.RequireFipsCryptography
		}
	} else if creds.ClientID != "" || creds.AuthorizationURL != "" {
		creds.Data = &CredentialsData{
			ClientID:                creds.ClientID,
			AuthorizationURL:        creds.AuthorizationURL,
			RequireFipsCryptography: creds.RequireFipsCryptography,
		}
	}
	return &creds, nil
}

// DefaultConfigDir returns the default directory for runner configuration and credentials.
// Precedence: ACTR_CONFIG_DIR > ACT_RUNNER_CONFIG_DIR > ~/.actr > current working directory.
func DefaultConfigDir() string {
	if env := os.Getenv("ACTR_CONFIG_DIR"); env != "" {
		if abs, err := filepath.Abs(env); err == nil {
			return abs
		}
		return env
	}
	if env := os.Getenv("ACT_RUNNER_CONFIG_DIR"); env != "" {
		if abs, err := filepath.Abs(env); err == nil {
			return abs
		}
		return env
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(home, ".actr")
}

type DockerDriverConfig struct {
	Socket        string            `json:"socket,omitempty" yaml:"socket,omitempty"`
	AutoPull      bool              `json:"auto_pull,omitempty" yaml:"auto_pull,omitempty"`
	ImageMappings map[string]string `json:"image_mappings,omitempty" yaml:"image_mappings,omitempty"`
}

type DriverConfig struct {
	Default string             `json:"default,omitempty" yaml:"default,omitempty"`
	Docker  DockerDriverConfig `json:"docker,omitempty" yaml:"docker,omitempty"`
}

type RetentionYAMLConfig struct {
	MaxRuns   int   `json:"max_runs,omitempty" yaml:"max_runs,omitempty"`
	MaxDiskMB int64 `json:"max_disk_mb,omitempty" yaml:"max_disk_mb,omitempty"`
}

type VaultProviderYAMLConfig struct {
	Type    string            `json:"type" yaml:"type"`
	Label   string            `json:"label,omitempty" yaml:"label,omitempty"`
	Path    string            `json:"path,omitempty" yaml:"path,omitempty"`
	Address string            `json:"address,omitempty" yaml:"address,omitempty"`
	Region  string            `json:"region,omitempty" yaml:"region,omitempty"`
	Prefix  string            `json:"prefix,omitempty" yaml:"prefix,omitempty"`
	Auth    map[string]string `json:"auth,omitempty" yaml:"auth,omitempty"`
}

type VaultYAMLConfig struct {
	Enabled     bool                      `json:"enabled" yaml:"enabled"`
	TrustedOnly bool                      `json:"trusted_only" yaml:"trusted_only"`
	Providers   []VaultProviderYAMLConfig `json:"providers" yaml:"providers"`
}

type RunnerYAMLConfig struct {
	Runner struct {
		Name       string `json:"name" yaml:"name"`
		WorkFolder string `json:"work_folder" yaml:"work_folder"`
	} `json:"runner" yaml:"runner"`
	Limits struct {
		CPU     string `json:"cpu" yaml:"cpu"`
		Memory  string `json:"memory" yaml:"memory"`
		Pids    int64  `json:"pids" yaml:"pids"`
		Timeout string `json:"timeout" yaml:"timeout"`
	} `json:"limits" yaml:"limits"`
	Driver    DriverConfig        `json:"driver" yaml:"driver"`
	Retention RetentionYAMLConfig `json:"retention,omitempty" yaml:"retention,omitempty"`
	Vault     VaultYAMLConfig     `json:"vault,omitempty" yaml:"vault,omitempty"`
}

func (c *RunnerYAMLConfig) ParseLimits() (*LimitsConfig, error) {
	if c == nil {
		return nil, nil
	}
	cpu, err := ParseCPU(c.Limits.CPU)
	if err != nil {
		return nil, fmt.Errorf("parse limits cpu: %w", err)
	}
	mem, err := ParseMemory(c.Limits.Memory)
	if err != nil {
		return nil, fmt.Errorf("parse limits memory: %w", err)
	}
	if c.Limits.Pids < 0 {
		return nil, fmt.Errorf("limits pids cannot be negative: %d", c.Limits.Pids)
	}
	var timeout time.Duration
	if strings.TrimSpace(c.Limits.Timeout) != "" {
		timeout, err = time.ParseDuration(c.Limits.Timeout)
		if err != nil {
			return nil, fmt.Errorf("parse limits timeout: %w", err)
		}
		if timeout < 0 {
			return nil, fmt.Errorf("limits timeout cannot be negative: %v", timeout)
		}
	}
	return &LimitsConfig{
		CPU:     cpu,
		Memory:  mem,
		Pids:    c.Limits.Pids,
		Timeout: timeout,
	}, nil
}

func LoadYAMLConfig(path string) (*RunnerYAMLConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read yaml config: %w", err)
	}
	var cfg RunnerYAMLConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("unmarshal yaml config: %w", err)
	}
	return &cfg, nil
}

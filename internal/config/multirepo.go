package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type LimitsYAMLConfig struct {
	CPU     any    `json:"cpu" yaml:"cpu"`
	Memory  string `json:"memory" yaml:"memory"`
	Pids    int64  `json:"pids" yaml:"pids"`
	Timeout string `json:"timeout" yaml:"timeout"`
}

type RepoConfig struct {
	URL               string           `json:"url" yaml:"url"`
	ConfigDir         string           `json:"config_dir,omitempty" yaml:"config_dir,omitempty"`
	MaxConcurrentJobs int              `json:"max_concurrent_jobs,omitempty" yaml:"max_concurrent_jobs,omitempty"`
	Driver            string           `json:"driver,omitempty" yaml:"driver,omitempty"`
	Limits            LimitsYAMLConfig `json:"limits" yaml:"limits"`
}

func (r *RepoConfig) ParseLimits() (*LimitsConfig, error) {
	if r == nil {
		return nil, nil
	}
	var cpuStr string
	if r.Limits.CPU != nil {
		cpuStr = fmt.Sprintf("%v", r.Limits.CPU)
	}
	cpu, err := ParseCPU(cpuStr)
	if err != nil {
		return nil, fmt.Errorf("parse repo limits cpu: %w", err)
	}
	mem, err := ParseMemory(r.Limits.Memory)
	if err != nil {
		return nil, fmt.Errorf("parse repo limits memory: %w", err)
	}
	if r.Limits.Pids < 0 {
		return nil, fmt.Errorf("limits pids cannot be negative: %d", r.Limits.Pids)
	}
	var timeout time.Duration
	if strings.TrimSpace(r.Limits.Timeout) != "" {
		timeout, err = time.ParseDuration(r.Limits.Timeout)
		if err != nil {
			return nil, fmt.Errorf("parse repo limits timeout: %w", err)
		}
		if timeout < 0 {
			return nil, fmt.Errorf("limits timeout cannot be negative: %v", timeout)
		}
	}
	return &LimitsConfig{
		CPU:     cpu,
		Memory:  mem,
		Pids:    r.Limits.Pids,
		Timeout: timeout,
	}, nil
}

type GlobalConfig struct {
	MaxConcurrentJobs int                 `json:"max_concurrent_jobs" yaml:"max_concurrent_jobs"`
	DefaultDriver     string              `json:"default_driver" yaml:"default_driver"`
	Retention         RetentionYAMLConfig `json:"retention" yaml:"retention"`
	Docker            DockerDriverConfig  `json:"docker,omitempty" yaml:"docker,omitempty"`
}

type SupervisorConfig struct {
	Version string                `json:"version" yaml:"version"`
	Global  GlobalConfig          `json:"global" yaml:"global"`
	Repos   map[string]RepoConfig `json:"repos" yaml:"repos"`
}

// IsMultiRepoConfig inspects the YAML file at configPath and returns true if it
// represents a multi-repo supervisor config (version: "1" and non-empty repos map).
func IsMultiRepoConfig(configPath string) bool {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return false
	}
	var raw struct {
		Version string         `yaml:"version"`
		Repos   map[string]any `yaml:"repos"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return false
	}
	return raw.Version == "1" && len(raw.Repos) > 0
}

// LoadSupervisorConfig parses and validates a multi-repo supervisor configuration file.
func LoadSupervisorConfig(configPath string) (*SupervisorConfig, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("read supervisor config: %w", err)
	}

	var cfg SupervisorConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("unmarshal supervisor config: %w", err)
	}

	if cfg.Version != "1" {
		return nil, fmt.Errorf("invalid supervisor config version %q; expected \"1\"", cfg.Version)
	}
	if cfg.Global.MaxConcurrentJobs <= 0 {
		cfg.Global.MaxConcurrentJobs = 2
	}
	if len(cfg.Repos) == 0 {
		return nil, fmt.Errorf("invalid supervisor config: 'repos' map must not be empty")
	}

	baseDir := filepath.Dir(configPath)
	for alias, repo := range cfg.Repos {
		if repo.MaxConcurrentJobs <= 0 {
			repo.MaxConcurrentJobs = 1
		}
		if repo.ConfigDir == "" {
			repo.ConfigDir = filepath.Join(baseDir, "repos", alias)
		} else if !filepath.IsAbs(repo.ConfigDir) {
			repo.ConfigDir = filepath.Join(baseDir, repo.ConfigDir)
		}
		if _, err := repo.ParseLimits(); err != nil {
			return nil, fmt.Errorf("repo %q: %w", alias, err)
		}
		cfg.Repos[alias] = repo
	}

	return &cfg, nil
}

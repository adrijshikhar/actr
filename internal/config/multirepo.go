package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var repoAliasRegexp = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

// ValidateRepoAlias ensures a repository alias is a safe single path-component identifier.
func ValidateRepoAlias(alias string) error {
	if alias == "" {
		return fmt.Errorf("repo alias must not be empty")
	}
	if strings.Contains(alias, "..") || strings.ContainsAny(alias, `/\`) {
		return fmt.Errorf("invalid repo alias %q: path traversal characters are not allowed", alias)
	}
	if !repoAliasRegexp.MatchString(alias) {
		return fmt.Errorf("invalid repo alias %q: must match ^[a-zA-Z0-9][a-zA-Z0-9._-]*$", alias)
	}
	return nil
}

// verifyDirContained checks that targetDir does not escape absBaseDir via traversal or symlinks.
// Note: Explicit absolute config_dir paths configured by an administrator are trusted.
func verifyDirContained(absBaseDir, targetDir string) error {
	absTarget, err := filepath.Abs(targetDir)
	if err != nil {
		return fmt.Errorf("resolve repo config_dir: %w", err)
	}
	rel, err := filepath.Rel(absBaseDir, absTarget)
	if err != nil {
		return fmt.Errorf("repo config_dir containment check failed: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("repo config_dir %q escapes config base directory %q", targetDir, absBaseDir)
	}

	if evalBase, err := filepath.EvalSymlinks(absBaseDir); err == nil {
		if evalTarget, err := filepath.EvalSymlinks(absTarget); err == nil {
			symRel, err := filepath.Rel(evalBase, evalTarget)
			if err != nil || symRel == ".." || strings.HasPrefix(symRel, ".."+string(filepath.Separator)) || filepath.IsAbs(symRel) {
				return fmt.Errorf("repo config_dir %q escapes config base directory %q", targetDir, absBaseDir)
			}
		}
	}
	return nil
}

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
	Vault             VaultYAMLConfig  `json:"vault,omitempty" yaml:"vault,omitempty"`
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
	absBaseDir, err := filepath.Abs(baseDir)
	if err != nil {
		return nil, fmt.Errorf("resolve config base dir: %w", err)
	}
	for alias, repo := range cfg.Repos {
		if err := ValidateRepoAlias(alias); err != nil {
			return nil, fmt.Errorf("repo %q: %w", alias, err)
		}
		if repo.MaxConcurrentJobs <= 0 {
			repo.MaxConcurrentJobs = 1
		}
		if repo.ConfigDir == "" {
			repo.ConfigDir = filepath.Join(baseDir, "repos", alias)
			if err := verifyDirContained(absBaseDir, repo.ConfigDir); err != nil {
				return nil, fmt.Errorf("repo %q: %w", alias, err)
			}
		} else if !filepath.IsAbs(repo.ConfigDir) {
			repo.ConfigDir = filepath.Join(baseDir, repo.ConfigDir)
			if err := verifyDirContained(absBaseDir, repo.ConfigDir); err != nil {
				return nil, fmt.Errorf("repo %q: %w", alias, err)
			}
		}
		if _, err := repo.ParseLimits(); err != nil {
			return nil, fmt.Errorf("repo %q: %w", alias, err)
		}
		cfg.Repos[alias] = repo
	}

	return &cfg, nil
}

// SaveSupervisorConfig writes the supervisor configuration to configPath atomically.
func SaveSupervisorConfig(configPath string, cfg *SupervisorConfig) error {
	if cfg != nil {
		for alias := range cfg.Repos {
			if err := ValidateRepoAlias(alias); err != nil {
				return fmt.Errorf("invalid repository alias %q: %w", alias, err)
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
		return fmt.Errorf("create supervisor config dir: %w", err)
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal supervisor config: %w", err)
	}
	if err := writeFileAtomic(configPath, data, 0600); err != nil {
		return fmt.Errorf("write supervisor config: %w", err)
	}
	return nil
}

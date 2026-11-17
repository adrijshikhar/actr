package action

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ActionRuns defines the execution configuration for an action.
type ActionRuns struct {
	Using  string `yaml:"using"`
	Main   string `yaml:"main"`
	Pre    string `yaml:"pre"`
	PreIf  string `yaml:"pre-if"`
	Post   string `yaml:"post"`
	PostIf string `yaml:"post-if"`
}

// ActionInput defines an input parameter accepted by an action.
type ActionInput struct {
	Description        string `yaml:"description"`
	Required           bool   `yaml:"required"`
	Default            string `yaml:"default"`
	DeprecationMessage string `yaml:"deprecationMessage"`
}

// ActionOutput defines an output parameter produced by an action.
type ActionOutput struct {
	Description string `yaml:"description"`
	Value       string `yaml:"value"`
}

// ActionManifest represents the parsed action.yml or action.yaml file.
type ActionManifest struct {
	Name        string                  `yaml:"name"`
	Author      string                  `yaml:"author"`
	Description string                  `yaml:"description"`
	Inputs      map[string]ActionInput  `yaml:"inputs"`
	Outputs     map[string]ActionOutput `yaml:"outputs"`
	Runs        ActionRuns              `yaml:"runs"`
}

// ParseManifest parses raw YAML bytes into an ActionManifest.
// It verifies that runs.using is specified.
func ParseManifest(data []byte) (*ActionManifest, error) {
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, errors.New("empty action manifest")
	}

	var manifest ActionManifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("failed to parse action manifest: %w", err)
	}

	if strings.TrimSpace(manifest.Runs.Using) == "" {
		return nil, errors.New("invalid action manifest: runs.using is required")
	}

	return &manifest, nil
}

// ParseManifestFile looks for action.yml or action.yaml in dir and parses it.
// action.yml takes precedence over action.yaml.
func ParseManifestFile(dir string) (*ActionManifest, error) {
	candidates := []string{"action.yml", "action.yaml"}

	for _, filename := range candidates {
		targetPath := filepath.Join(dir, filename)
		info, err := os.Stat(targetPath)
		if err == nil && !info.IsDir() {
			data, err := os.ReadFile(targetPath)
			if err != nil {
				return nil, fmt.Errorf("failed to read action manifest %s: %w", targetPath, err)
			}
			return ParseManifest(data)
		}
	}

	return nil, fmt.Errorf("action manifest not found in directory: %s", dir)
}

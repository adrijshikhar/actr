package action

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
)

// NodeDetector defines an interface for detecting a Node.js executable.
type NodeDetector interface {
	Detect(ctx context.Context) (string, error)
}

// HostNodeDetector discovers a suitable Node.js runtime on the host machine.
type HostNodeDetector struct {
	CustomPath string
}

// NewHostNodeDetector creates a new HostNodeDetector with an optional custom path.
func NewHostNodeDetector(customPath string) *HostNodeDetector {
	return &HostNodeDetector{
		CustomPath: customPath,
	}
}

// Detect discovers a Node.js binary on the host based on priority:
// 1. CustomPath (if specified)
// 2. $ACTR_NODE_PATH environment variable
// 3. exec.LookPath("node")
// 4. Common version managers and standard system paths
func (d *HostNodeDetector) Detect(ctx context.Context) (string, error) {
	if ctx.Err() != nil {
		return "", ctx.Err()
	}

	// Priority 1: CustomPath
	if d.CustomPath != "" {
		if err := verifyNodeBinary(ctx, d.CustomPath); err != nil {
			return "", err
		}
		return d.CustomPath, nil
	}

	var candidates []string

	// Priority 2: $ACTR_NODE_PATH
	if envPath := os.Getenv("ACTR_NODE_PATH"); envPath != "" {
		candidates = append(candidates, envPath)
	}

	// Priority 3: exec.LookPath("node")
	if lookPath, err := exec.LookPath("node"); err == nil {
		candidates = append(candidates, lookPath)
	}

	// Priority 4: Search version managers and known standard paths
	homeDir, _ := os.UserHomeDir()
	if homeDir != "" {
		// fnm: ~/.local/state/fnm_multishells/*/bin/node
		fnmMatches, _ := filepath.Glob(filepath.Join(homeDir, ".local", "state", "fnm_multishells", "*", "bin", "node"))
		sort.Sort(sort.Reverse(sort.StringSlice(fnmMatches)))
		candidates = append(candidates, fnmMatches...)

		// nvm: ~/.nvm/versions/node/*/bin/node
		nvmMatches, _ := filepath.Glob(filepath.Join(homeDir, ".nvm", "versions", "node", "*", "bin", "node"))
		sort.Sort(sort.Reverse(sort.StringSlice(nvmMatches)))
		candidates = append(candidates, nvmMatches...)

		// asdf: ~/.asdf/shims/node
		candidates = append(candidates, filepath.Join(homeDir, ".asdf", "shims", "node"))

		// mise: ~/.local/share/mise/shims/node
		candidates = append(candidates, filepath.Join(homeDir, ".local", "share", "mise", "shims", "node"))
	}

	// Standard system paths
	candidates = append(candidates,
		"/opt/homebrew/bin/node",
		"/usr/local/bin/node",
		"/usr/bin/node",
	)

	seen := make(map[string]bool)
	for _, candidate := range candidates {
		if candidate == "" || seen[candidate] {
			continue
		}
		seen[candidate] = true

		if ctx.Err() != nil {
			return "", ctx.Err()
		}

		if err := verifyNodeBinary(ctx, candidate); err == nil {
			return candidate, nil
		}
	}

	return "", errors.New("no valid Node.js runtime found on host (checked ACTR_NODE_PATH, PATH, version managers, and standard locations)")
}

// verifyNodeBinary ensures that the candidate path exists, is executable,
// and successfully executes "--version".
func verifyNodeBinary(ctx context.Context, path string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("node binary not found at %s: %w", path, err)
	}
	if info.IsDir() {
		return fmt.Errorf("node path %s is a directory", path)
	}
	if runtime.GOOS != "windows" && info.Mode()&0111 == 0 {
		return fmt.Errorf("node binary at %s is not executable", path)
	}

	cmd := exec.CommandContext(ctx, path, "--version")
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("node binary at %s failed verification (--version): %w", path, err)
	}
	return nil
}

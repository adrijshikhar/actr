package action

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ActionContext encapsulates the execution environment and metadata for an action step.
type ActionContext struct {
	StepID         string
	ActionDir      string
	Manifest       *ActionManifest
	Inputs         map[string]string
	Env            map[string]string
	WorkspaceDir   string
	TempDir        string
	ToolCacheDir   string
	StatePath      string
	PathFilePath   string
	EnvFilePath    string
	OutputFilePath string
}

// BuildActionEnvironment constructs the execution environment map for an action step,
// including exact and shell-sanitized input keys for @actions/core compatibility,
// default values from the manifest, and GitHub special command file paths.
func BuildActionEnvironment(actCtx *ActionContext) map[string]string {
	env := make(map[string]string)
	if actCtx == nil {
		return env
	}

	// 1. Copy starting environment
	for k, v := range actCtx.Env {
		env[k] = v
	}

	// 2. Resolve inputs: defaults from manifest merged with actCtx.Inputs overrides
	effectiveInputs := make(map[string]string)
	if actCtx.Manifest != nil && actCtx.Manifest.Inputs != nil {
		for name, inputDef := range actCtx.Manifest.Inputs {
			if inputDef.Default != "" {
				effectiveInputs[name] = inputDef.Default
			}
		}
	}
	if actCtx.Inputs != nil {
		for name, val := range actCtx.Inputs {
			effectiveInputs[name] = val
		}
	}

	// 3. Emit dual keys for @actions/core and shell compatibility
	for name, val := range effectiveInputs {
		exactKey, sanitizedKey := FormatActionInputKeys(name)
		env[exactKey] = val
		env[sanitizedKey] = val
	}

	// 4. GitHub special command file and context environment variables
	env["GITHUB_ACTION"] = actCtx.StepID
	env["GITHUB_WORKSPACE"] = actCtx.WorkspaceDir
	env["RUNNER_TEMP"] = actCtx.TempDir
	env["GITHUB_STATE"] = actCtx.StatePath
	env["GITHUB_PATH"] = actCtx.PathFilePath
	env["GITHUB_ENV"] = actCtx.EnvFilePath
	env["GITHUB_OUTPUT"] = actCtx.OutputFilePath

	if actCtx.ToolCacheDir != "" {
		env["RUNNER_TOOL_CACHE"] = actCtx.ToolCacheDir
		env["AGENT_TOOLSDIRECTORY"] = actCtx.ToolCacheDir
	}

	if actCtx.ActionDir != "" {
		env["GITHUB_ACTION_PATH"] = actCtx.ActionDir
	}

	return env
}

// FormatActionInputKeys returns the exact @actions/core form and shell-sanitized form of an input name.
// Exact form: INPUT_ + strings.ToUpper(strings.ReplaceAll(name, " ", "_"))
// Sanitized form: INPUT_ + strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(name, " ", "_"), "-", "_"))
func FormatActionInputKeys(name string) (exactKey string, sanitizedKey string) {
	spaced := strings.ReplaceAll(name, " ", "_")
	exactKey = "INPUT_" + strings.ToUpper(spaced)
	sanitized := strings.ReplaceAll(spaced, "-", "_")
	sanitizedKey = "INPUT_" + strings.ToUpper(sanitized)
	return exactKey, sanitizedKey
}

// ExecuteNodeAction executes a JavaScript action using the specified Node.js binary.
// It sets the working directory, builds the action environment, and streams stdout/stderr.
func ExecuteNodeAction(ctx context.Context, nodePath string, scriptPath string, actCtx *ActionContext, stdout, stderr io.Writer) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if strings.TrimSpace(nodePath) == "" {
		return errors.New("node path is required")
	}
	if strings.TrimSpace(scriptPath) == "" {
		return errors.New("script path is required")
	}

	cmd := exec.CommandContext(ctx, nodePath, scriptPath)
	if actCtx != nil && actCtx.WorkspaceDir != "" {
		cmd.Dir = actCtx.WorkspaceDir
	}

	cmd.Stdout = stdout
	cmd.Stderr = stderr
	setProcessGroup(cmd)
	cmd.Cancel = func() error {
		return killProcessGroup(cmd)
	}
	cmd.WaitDelay = 5 * time.Second

	// Layer sanitized host environment with built action environment
	cmd.Env = SanitizeHostEnvironment(os.Environ())
	if actCtx != nil {
		actionEnv := BuildActionEnvironment(actCtx)
		for k, v := range actionEnv {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
		}
	}

	return cmd.Run()
}

// SanitizeHostEnvironment filters sensitive environment variables (such as host tokens,
// SSH agent sockets, cloud provider credentials, and dynamic loader injection flags)
// to prevent third-party marketplace actions from accessing host credentials.
func SanitizeHostEnvironment(environ []string) []string {
	sensitiveKeys := map[string]bool{
		"GITHUB_TOKEN":              true,
		"GH_TOKEN":                  true,
		"AWS_ACCESS_KEY_ID":         true,
		"AWS_SECRET_ACCESS_KEY":     true,
		"AWS_SESSION_TOKEN":         true,
		"AWS_SECURITY_TOKEN":        true,
		"SSH_AUTH_SOCK":             true,
		"NODE_OPTIONS":              true,
		"LD_PRELOAD":                true,
		"LD_LIBRARY_PATH":           true,
		"DYLD_INSERT_LIBRARIES":     true,
		"DYLD_LIBRARY_PATH":         true,
		"HOMEBREW_GITHUB_API_TOKEN": true,
	}

	sanitized := make([]string, 0, len(environ))
	for _, envVar := range environ {
		idx := strings.IndexByte(envVar, '=')
		if idx <= 0 {
			continue
		}
		key := strings.ToUpper(envVar[:idx])
		if sensitiveKeys[key] {
			continue
		}
		sanitized = append(sanitized, envVar)
	}
	return sanitized
}

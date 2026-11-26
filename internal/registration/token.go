package registration

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/url"
	"os/exec"
	"strings"
)

// DefaultGHRunner executes a command using the local `gh` binary.
func DefaultGHRunner(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		errOutput := strings.TrimSpace(stderr.String())
		if errOutput == "" {
			errOutput = strings.TrimSpace(stdout.String())
		}
		if errOutput != "" {
			return "", fmt.Errorf("%s: %w", errOutput, err)
		}
		return "", err
	}
	return stdout.String(), nil
}

// FetchRegistrationTokenGH calls the GitHub CLI (`gh api`) to obtain a runner registration token.
func FetchRegistrationTokenGH(ctx context.Context, target *TargetInfo, ghRunner func(ctx context.Context, args ...string) (string, error)) (string, error) {
	if target == nil {
		return "", fmt.Errorf("target is nil")
	}
	if ghRunner == nil {
		ghRunner = DefaultGHRunner
	}

	var apiPath string
	if target.Scope == ScopeOrg {
		apiPath = fmt.Sprintf("/orgs/%s/actions/runners/registration-token", url.PathEscape(target.Owner))
	} else {
		apiPath = fmt.Sprintf("/repos/%s/%s/actions/runners/registration-token", url.PathEscape(target.Owner), url.PathEscape(target.Repo))
	}

	args := []string{"api", "--hostname", target.Host, "--method", "POST", apiPath, "--jq", ".token"}
	out, err := ghRunner(ctx, args...)
	if err != nil {
		return "", fmt.Errorf("gh api: %w", err)
	}

	token := strings.TrimSpace(out)
	if token == "" {
		return "", fmt.Errorf("empty token received from gh CLI")
	}
	return token, nil
}

// GetRunnerSettingsURL returns the web URL to add/register a new self-hosted runner.
func GetRunnerSettingsURL(target *TargetInfo) string {
	if target == nil {
		return ""
	}
	host := target.Host
	if ip := net.ParseIP(host); ip != nil && ip.To4() == nil && !strings.HasPrefix(host, "[") {
		host = fmt.Sprintf("[%s]", host)
	}
	if target.Scope == ScopeOrg {
		return fmt.Sprintf("https://%s/organizations/%s/settings/actions/runners/new", host, target.Owner)
	}
	return fmt.Sprintf("https://%s/%s/%s/settings/actions/runners/new", host, target.Owner, target.Repo)
}

package registration

import (
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"regexp"
	"strings"
)

type ScopeType string

const (
	ScopeRepo ScopeType = "repo"
	ScopeOrg  ScopeType = "org"
)

type TargetInfo struct {
	RawURL string
	WebURL string
	Host   string
	Owner  string
	Repo   string
	Scope  ScopeType
}

var scpPattern = regexp.MustCompile(`^(?:[\w-]+@)?([^:]+):([^/]+)/(.+?)(?:\.git)?$`)

func ParseTargetURL(raw string) (*TargetInfo, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, fmt.Errorf("empty URL provided")
	}

	// 1. Try SCP-like SSH syntax: git@github.com:owner/repo(.git)?
	if matches := scpPattern.FindStringSubmatch(trimmed); len(matches) == 4 {
		host := matches[1]
		owner := matches[2]
		repo := strings.TrimRight(strings.TrimSuffix(strings.TrimRight(matches[3], "/"), ".git"), "/")
		return &TargetInfo{
			RawURL: trimmed,
			WebURL: fmt.Sprintf("https://%s/%s/%s", host, owner, repo),
			Host:   host,
			Owner:  owner,
			Repo:   repo,
			Scope:  ScopeRepo,
		}, nil
	}

	// Support scheme-less input like "github.com/owner/repo"
	if !strings.Contains(trimmed, "://") && !strings.Contains(trimmed, "@") && !strings.Contains(trimmed, ":") {
		trimmed = "https://" + trimmed
	}

	// 2. Standard URL parse: https:// or ssh://
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("parse url: %w", err)
	}

	host := parsed.Host
	if host == "" {
		return nil, fmt.Errorf("invalid repository url: missing host in %q", trimmed)
	}
	// Strip userinfo if present in host
	if strings.Contains(host, "@") {
		parts := strings.Split(host, "@")
		host = parts[len(parts)-1]
	}
	// Strip port if standard default port or ssh
	if parsed.Scheme == "ssh" || strings.HasSuffix(host, ":22") || strings.HasSuffix(host, ":443") || strings.HasSuffix(host, ":80") {
		host = strings.Split(host, ":")[0]
	}

	pathParts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(pathParts) == 0 || pathParts[0] == "" {
		return nil, fmt.Errorf("invalid repository url: missing path in %q", trimmed)
	}

	if len(pathParts) == 1 {
		owner := strings.TrimSuffix(pathParts[0], ".git")
		return &TargetInfo{
			RawURL: trimmed,
			WebURL: fmt.Sprintf("https://%s/%s", host, owner),
			Host:   host,
			Owner:  owner,
			Repo:   "",
			Scope:  ScopeOrg,
		}, nil
	}

	owner := pathParts[0]
	repo := strings.TrimSuffix(pathParts[1], ".git")

	return &TargetInfo{
		RawURL: trimmed,
		WebURL: fmt.Sprintf("https://%s/%s/%s", host, owner, repo),
		Host:   host,
		Owner:  owner,
		Repo:   repo,
		Scope:  ScopeRepo,
	}, nil
}

func DefaultGitRunner(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func DetectGitRemote(ctx context.Context, dir string, gitRunner func(ctx context.Context, dir string, args ...string) (string, error)) (*TargetInfo, error) {
	if gitRunner == nil {
		gitRunner = DefaultGitRunner
	}

	out, err := gitRunner(ctx, dir, "remote", "get-url", "origin")
	if err != nil {
		// Fallback to git config --get remote.origin.url
		out, err = gitRunner(ctx, dir, "config", "--get", "remote.origin.url")
		if err != nil {
			return nil, fmt.Errorf("no git remote 'origin' found in %s: %w", dir, err)
		}
	}

	return ParseTargetURL(strings.TrimSpace(out))
}

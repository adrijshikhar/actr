package registration

import (
	"context"
	"fmt"
	"net"
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

var scpPattern = regexp.MustCompile(`^(?:[\w.-]+@)?([^:]+):([^/]+)/(.+?)(?:\.git)?$`)

func isValidTargetHost(h string) bool {
	if h == "" || strings.HasPrefix(h, "-") || strings.Contains(h, "..") {
		return false
	}
	cleanIP := strings.Trim(h, "[]")
	if net.ParseIP(cleanIP) != nil {
		return true
	}
	if strings.ContainsAny(h, "/\\ \t\r\n?#@:;{}|^~`") {
		return false
	}
	return true
}

func isValidTargetIdentifier(s string) bool {
	if s == "" || strings.HasPrefix(s, "-") || strings.Contains(s, "..") {
		return false
	}
	unescaped, err := url.PathUnescape(s)
	if err == nil && (strings.Contains(unescaped, "..") || strings.HasPrefix(unescaped, "-")) {
		return false
	}
	if strings.ContainsAny(s, "/\\ \t\r\n?#@:;{}|^~`[]") {
		return false
	}
	return true
}

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
		if !isValidTargetHost(host) || !isValidTargetIdentifier(owner) || !isValidTargetIdentifier(repo) {
			return nil, fmt.Errorf("invalid scp git url target: %q", trimmed)
		}
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

	// 2. Standard URL parse: https:// or ssh:// or http://
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("parse url: %w", err)
	}

	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" && scheme != "ssh" && scheme != "git" && scheme != "git+ssh" && scheme != "ssh+git" {
		return nil, fmt.Errorf("unsupported url scheme %q in %q", parsed.Scheme, trimmed)
	}

	hostname := parsed.Hostname()
	if hostname == "" {
		return nil, fmt.Errorf("invalid repository url: missing host in %q", trimmed)
	}
	if !isValidTargetHost(hostname) {
		return nil, fmt.Errorf("invalid repository url: invalid host %q", hostname)
	}

	port := parsed.Port()
	host := parsed.Host
	if strings.Contains(host, "@") {
		parts := strings.Split(host, "@")
		host = parts[len(parts)-1]
	}

	if scheme == "ssh" || scheme == "git" || scheme == "git+ssh" || scheme == "ssh+git" || port == "22" || port == "443" || port == "80" {
		host = hostname
	}

	cleanPath := strings.Trim(parsed.Path, "/")
	if strings.Contains(parsed.Path, "..") || strings.Contains(parsed.RawPath, "..") {
		return nil, fmt.Errorf("invalid repository url: path traversal in %q", trimmed)
	}
	pathParts := strings.Split(cleanPath, "/")
	if len(pathParts) == 0 || pathParts[0] == "" {
		return nil, fmt.Errorf("invalid repository url: missing path in %q", trimmed)
	}

	webHost := host
	if strings.Contains(hostname, ":") {
		if port != "" && port != "443" && port != "80" {
			webHost = fmt.Sprintf("[%s]:%s", hostname, port)
		} else {
			webHost = fmt.Sprintf("[%s]", hostname)
		}
	}

	if len(pathParts) == 1 {
		owner := strings.TrimSuffix(pathParts[0], ".git")
		if !isValidTargetIdentifier(owner) {
			return nil, fmt.Errorf("invalid owner %q in %q", owner, trimmed)
		}
		return &TargetInfo{
			RawURL: trimmed,
			WebURL: fmt.Sprintf("https://%s/%s", webHost, owner),
			Host:   host,
			Owner:  owner,
			Repo:   "",
			Scope:  ScopeOrg,
		}, nil
	}

	owner := pathParts[0]
	repo := strings.TrimSuffix(pathParts[1], ".git")
	if !isValidTargetIdentifier(owner) || !isValidTargetIdentifier(repo) {
		return nil, fmt.Errorf("invalid owner or repo in %q", trimmed)
	}

	return &TargetInfo{
		RawURL: trimmed,
		WebURL: fmt.Sprintf("https://%s/%s/%s", webHost, owner, repo),
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

package registration

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseTargetURL(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    *TargetInfo
		wantErr bool
	}{
		{
			name:  "SSH repo url",
			input: "git@github.com:adrijshikhar/trove.git",
			want: &TargetInfo{
				RawURL: "git@github.com:adrijshikhar/trove.git",
				WebURL: "https://github.com/adrijshikhar/trove",
				Host:   "github.com",
				Owner:  "adrijshikhar",
				Repo:   "trove",
				Scope:  ScopeRepo,
			},
		},
		{
			name:  "SSH repo url with trailing slash",
			input: "git@github.com:adrijshikhar/trove.git/",
			want: &TargetInfo{
				RawURL: "git@github.com:adrijshikhar/trove.git/",
				WebURL: "https://github.com/adrijshikhar/trove",
				Host:   "github.com",
				Owner:  "adrijshikhar",
				Repo:   "trove",
				Scope:  ScopeRepo,
			},
		},
		{
			name:  "HTTPS repo url with .git",
			input: "https://github.com/owner/my-repo.git",
			want: &TargetInfo{
				RawURL: "https://github.com/owner/my-repo.git",
				WebURL: "https://github.com/owner/my-repo",
				Host:   "github.com",
				Owner:  "owner",
				Repo:   "my-repo",
				Scope:  ScopeRepo,
			},
		},
		{
			name:  "HTTPS repo url without .git",
			input: "https://github.com/owner/my-repo",
			want: &TargetInfo{
				RawURL: "https://github.com/owner/my-repo",
				WebURL: "https://github.com/owner/my-repo",
				Host:   "github.com",
				Owner:  "owner",
				Repo:   "my-repo",
				Scope:  ScopeRepo,
			},
		},
		{
			name:  "HTTPS repo url with trailing slash",
			input: "https://github.com/owner/my-repo/",
			want: &TargetInfo{
				RawURL: "https://github.com/owner/my-repo/",
				WebURL: "https://github.com/owner/my-repo",
				Host:   "github.com",
				Owner:  "owner",
				Repo:   "my-repo",
				Scope:  ScopeRepo,
			},
		},
		{
			name:  "Organization URL",
			input: "https://github.com/my-org",
			want: &TargetInfo{
				RawURL: "https://github.com/my-org",
				WebURL: "https://github.com/my-org",
				Host:   "github.com",
				Owner:  "my-org",
				Repo:   "",
				Scope:  ScopeOrg,
			},
		},
		{
			name:  "Organization URL with trailing slash",
			input: "https://github.com/my-org/",
			want: &TargetInfo{
				RawURL: "https://github.com/my-org/",
				WebURL: "https://github.com/my-org",
				Host:   "github.com",
				Owner:  "my-org",
				Repo:   "",
				Scope:  ScopeOrg,
			},
		},
		{
			name:  "Custom port HTTPS enterprise repo",
			input: "https://ghe.company.internal:8443/team/service",
			want: &TargetInfo{
				RawURL: "https://ghe.company.internal:8443/team/service",
				WebURL: "https://ghe.company.internal:8443/team/service",
				Host:   "ghe.company.internal:8443",
				Owner:  "team",
				Repo:   "service",
				Scope:  ScopeRepo,
			},
		},
		{
			name:  "Scheme-less input repo",
			input: "github.com/adrijshikhar/trove",
			want: &TargetInfo{
				RawURL: "https://github.com/adrijshikhar/trove",
				WebURL: "https://github.com/adrijshikhar/trove",
				Host:   "github.com",
				Owner:  "adrijshikhar",
				Repo:   "trove",
				Scope:  ScopeRepo,
			},
		},
		{
			name:  "Enterprise SSH repo",
			input: "git@ghe.company.internal:team/service.git",
			want: &TargetInfo{
				RawURL: "git@ghe.company.internal:team/service.git",
				WebURL: "https://ghe.company.internal/team/service",
				Host:   "ghe.company.internal",
				Owner:  "team",
				Repo:   "service",
				Scope:  ScopeRepo,
			},
		},
		{
			name:  "SSH scheme repo url with user and port",
			input: "ssh://git@github.com:22/owner/my-repo.git",
			want: &TargetInfo{
				RawURL: "ssh://git@github.com:22/owner/my-repo.git",
				WebURL: "https://github.com/owner/my-repo",
				Host:   "github.com",
				Owner:  "owner",
				Repo:   "my-repo",
				Scope:  ScopeRepo,
			},
		},
		{
			name:  "HTTPS repo url with userinfo in host",
			input: "https://token@github.com/owner/my-repo.git",
			want: &TargetInfo{
				RawURL: "https://token@github.com/owner/my-repo.git",
				WebURL: "https://github.com/owner/my-repo",
				Host:   "github.com",
				Owner:  "owner",
				Repo:   "my-repo",
				Scope:  ScopeRepo,
			},
		},
		{
			name:    "Invalid empty input",
			input:   "",
			wantErr: true,
		},
		{
			name:    "Invalid whitespace only",
			input:   "   ",
			wantErr: true,
		},
		{
			name:    "Invalid missing host",
			input:   "https:///owner/repo",
			wantErr: true,
		},
		{
			name:    "Invalid missing path",
			input:   "https://github.com/",
			wantErr: true,
		},
		{
			name:    "Invalid url syntax",
			input:   "://bad-url",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseTargetURL(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDetectGitRemote(t *testing.T) {
	t.Run("success via get-url", func(t *testing.T) {
		mockRunner := func(ctx context.Context, dir string, args ...string) (string, error) {
			assert.Equal(t, []string{"remote", "get-url", "origin"}, args)
			return "git@github.com:adrijshikhar/trove.git\n", nil
		}

		target, err := DetectGitRemote(context.Background(), ".", mockRunner)
		require.NoError(t, err)
		assert.Equal(t, "adrijshikhar", target.Owner)
		assert.Equal(t, "trove", target.Repo)
		assert.Equal(t, ScopeRepo, target.Scope)
	})

	t.Run("fallback to git config --get", func(t *testing.T) {
		calls := 0
		mockRunner := func(ctx context.Context, dir string, args ...string) (string, error) {
			calls++
			if calls == 1 {
				assert.Equal(t, []string{"remote", "get-url", "origin"}, args)
				return "", errors.New("error: unknown subcommand get-url")
			}
			assert.Equal(t, []string{"config", "--get", "remote.origin.url"}, args)
			return "https://github.com/owner/fallback-repo.git\n", nil
		}

		target, err := DetectGitRemote(context.Background(), ".", mockRunner)
		require.NoError(t, err)
		assert.Equal(t, "owner", target.Owner)
		assert.Equal(t, "fallback-repo", target.Repo)
		assert.Equal(t, ScopeRepo, target.Scope)
		assert.Equal(t, 2, calls)
	})

	t.Run("both remote and config fail", func(t *testing.T) {
		mockRunner := func(ctx context.Context, dir string, args ...string) (string, error) {
			return "", errors.New("command failed")
		}

		target, err := DetectGitRemote(context.Background(), "/nonexistent", mockRunner)
		require.Error(t, err)
		assert.Nil(t, target)
		assert.Contains(t, err.Error(), "no git remote 'origin' found in /nonexistent")
	})

	t.Run("nil runner uses DefaultGitRunner against current repo", func(t *testing.T) {
		target, err := DetectGitRemote(context.Background(), ".", nil)
		require.NoError(t, err)
		assert.NotNil(t, target)
		assert.NotEmpty(t, target.Owner)
		assert.NotEmpty(t, target.Repo)
		assert.Equal(t, ScopeRepo, target.Scope)
	})
}

func TestParseTargetURL_HandlesIPv6AndRejectsFlagAndTraversalPayloads(t *testing.T) {
	info, err := ParseTargetURL("https://[::1]:443/org/repo")
	require.NoError(t, err)
	assert.Equal(t, "::1", info.Host)

	_, err = ParseTargetURL("git@--output=/tmp/x:owner/../../orgs/victim")
	require.Error(t, err)

	_, err = ParseTargetURL("javascript://evil.com/a/b")
	require.Error(t, err)

	_, err = ParseTargetURL("https://github.com/--flag/repo")
	require.Error(t, err)

	_, err = ParseTargetURL("https://github.com/owner/..%2f..%2fvictim")
	require.Error(t, err)
}

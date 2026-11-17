package registration

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFetchRegistrationTokenGH(t *testing.T) {
	target := &TargetInfo{
		Host:  "github.com",
		Owner: "adrijshikhar",
		Repo:  "trove",
		Scope: ScopeRepo,
	}

	t.Run("success repo scope", func(t *testing.T) {
		mockRunner := func(ctx context.Context, args ...string) (string, error) {
			assert.Contains(t, args, "/repos/adrijshikhar/trove/actions/runners/registration-token")
			assert.Contains(t, args, "api")
			assert.Contains(t, args, "--hostname")
			assert.Contains(t, args, "github.com")
			return "MOCK_TOKEN_12345\n", nil
		}
		tok, err := FetchRegistrationTokenGH(context.Background(), target, mockRunner)
		require.NoError(t, err)
		assert.Equal(t, "MOCK_TOKEN_12345", tok)
	})

	t.Run("org scope", func(t *testing.T) {
		orgTarget := &TargetInfo{
			Host:  "github.com",
			Owner: "my-org",
			Scope: ScopeOrg,
		}
		mockRunner := func(ctx context.Context, args ...string) (string, error) {
			assert.Contains(t, args, "/orgs/my-org/actions/runners/registration-token")
			return "ORG_TOKEN_67890\n", nil
		}
		tok, err := FetchRegistrationTokenGH(context.Background(), orgTarget, mockRunner)
		require.NoError(t, err)
		assert.Equal(t, "ORG_TOKEN_67890", tok)
	})

	t.Run("gh error", func(t *testing.T) {
		mockRunner := func(ctx context.Context, args ...string) (string, error) {
			return "", errors.New("exit status 1: HTTP 403 Forbidden")
		}
		_, err := FetchRegistrationTokenGH(context.Background(), target, mockRunner)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "HTTP 403")
	})

	t.Run("empty token returned", func(t *testing.T) {
		mockRunner := func(ctx context.Context, args ...string) (string, error) {
			return "  \n", nil
		}
		_, err := FetchRegistrationTokenGH(context.Background(), target, mockRunner)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "empty token received")
	})

	t.Run("nil target", func(t *testing.T) {
		_, err := FetchRegistrationTokenGH(context.Background(), nil, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "target is nil")
	})
}

func TestGetRunnerSettingsURL(t *testing.T) {
	assert.Equal(t, "", GetRunnerSettingsURL(nil))

	repoTarget := &TargetInfo{Host: "github.com", Owner: "owner", Repo: "repo", Scope: ScopeRepo}
	assert.Equal(t, "https://github.com/owner/repo/settings/actions/runners/new", GetRunnerSettingsURL(repoTarget))

	orgTarget := &TargetInfo{Host: "github.com", Owner: "my-org", Scope: ScopeOrg}
	assert.Equal(t, "https://github.com/organizations/my-org/settings/actions/runners/new", GetRunnerSettingsURL(orgTarget))
}

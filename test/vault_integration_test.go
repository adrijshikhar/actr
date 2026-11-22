package test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/adrijshikhar/actr/internal/driver/host"
	"github.com/adrijshikhar/actr/internal/engine"
	"github.com/adrijshikhar/actr/internal/protocol"
	"github.com/adrijshikhar/actr/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// safeBuffer provides a thread-safe bytes.Buffer implementation for capturing test outputs under race detector.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (n int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func (s *safeBuffer) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buf.Reset()
}

func TestEndToEnd_RunnerSecretVault(t *testing.T) {
	tmpDir := t.TempDir()
	vaultPath := filepath.Join(tmpDir, "vault.enc")
	fileV, err := vault.NewFileProvider(vaultPath, []byte("test-passphrase-42"))
	require.NoError(t, err)

	ctx := context.Background()
	secretVal := "super-sensitive-jwt-token-987"
	err = fileV.Set(ctx, "DEPLOY_KEY", secretVal)
	require.NoError(t, err)

	workDir := filepath.Join(tmpDir, "work")
	require.NoError(t, os.MkdirAll(workDir, 0o755))

	var logCapture safeBuffer
	coord := engine.NewCoordinator(host.NewDriver(), nil, workDir).
		WithColor(false).
		WithVault(fileV).
		WithOutput(&logCapture, &logCapture)

	// 1. Trusted context test (push event)
	t.Run("TrustedPush_ResolutionAndMasking", func(t *testing.T) {
		logCapture.Reset()
		jobTrusted := &protocol.JobSpecification{
			ID:        "job-trusted-1",
			JobID:     "job-trusted-1",
			JobName:   "Trusted Push Job",
			EventName: "push",
			Steps: []protocol.Step{
				{
					ID:   "s1",
					Name: "Echo Secret",
					Type: "script",
					Environment: map[string]string{
						"DEPLOY_KEY": "",
					},
					Script: "echo 'TOKEN='\"$DEPLOY_KEY\"",
				},
			},
		}

		resp, err := coord.ExecuteJob(ctx, jobTrusted, "", "")
		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.Equal(t, "success", resp.Conclusion)

		output := logCapture.String()
		assert.NotContains(t, output, secretVal, "secret must never leak in logs")
		assert.Contains(t, output, "TOKEN=***", "secret must be redacted with ***")
	})

	// 2. Fork Guard protection: Untrusted pull request blocked
	t.Run("ForkGuard_UntrustedPRBlocked", func(t *testing.T) {
		logCapture.Reset()
		jobFork := &protocol.JobSpecification{
			ID:        "job-fork-1",
			JobID:     "job-fork-1",
			JobName:   "Fork PR Job",
			EventName: "pull_request",
			Variables: map[string]string{
				"GITHUB_HEAD_REF_REPO": "external-attacker/actr",
				"GITHUB_REPOSITORY":    "adrijshikhar/actr",
			},
			Steps: []protocol.Step{
				{
					ID:   "s1",
					Name: "Exfiltrate",
					Type: "script",
					Environment: map[string]string{
						"DEPLOY_KEY": "",
					},
					Script: "echo 'TOKEN='\"$DEPLOY_KEY\"",
				},
			},
		}

		resp, err := coord.ExecuteJob(ctx, jobFork, "", "")
		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.Equal(t, "success", resp.Conclusion)

		forkOutput := logCapture.String()
		assert.NotContains(t, forkOutput, secretVal, "secret must never leak in untrusted fork logs")
		assert.Contains(t, forkOutput, "TOKEN=", "environment variable must remain empty")
		assert.Contains(t, forkOutput, "Vault secret resolution blocked for untrusted fork PR: DEPLOY_KEY")
	})

	// 3. Fork Guard validation: Internal same-repo pull request allowed
	t.Run("ForkGuard_SameRepoPRAllowed", func(t *testing.T) {
		logCapture.Reset()
		jobInternalPR := &protocol.JobSpecification{
			ID:        "job-internal-pr-1",
			JobID:     "job-internal-pr-1",
			JobName:   "Internal PR Job",
			EventName: "pull_request",
			Variables: map[string]string{
				"GITHUB_HEAD_REF_REPO": "adrijshikhar/actr",
				"GITHUB_REPOSITORY":    "adrijshikhar/actr",
			},
			Steps: []protocol.Step{
				{
					ID:   "s1",
					Name: "Echo Secret Internal PR",
					Type: "script",
					Environment: map[string]string{
						"DEPLOY_KEY": "",
					},
					Script: "echo 'TOKEN='\"$DEPLOY_KEY\"",
				},
			},
		}

		resp, err := coord.ExecuteJob(ctx, jobInternalPR, "", "")
		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.Equal(t, "success", resp.Conclusion)

		prOutput := logCapture.String()
		assert.NotContains(t, prOutput, secretVal, "secret must never leak in logs")
		assert.Contains(t, prOutput, "TOKEN=***", "secret must be redacted with ***")
	})

	// 4. Multi-provider chained resolution fallback
	t.Run("ChainedProvider_FallbackResolutionAndMasking", func(t *testing.T) {
		vaultPath1 := filepath.Join(tmpDir, "vault1.enc")
		vaultPath2 := filepath.Join(tmpDir, "vault2.enc")

		fileV1, err := vault.NewFileProvider(vaultPath1, []byte("pass1"))
		require.NoError(t, err)
		fileV2, err := vault.NewFileProvider(vaultPath2, []byte("pass2"))
		require.NoError(t, err)

		// Provider 1 does not have PROD_API_KEY; Provider 2 has it
		err = fileV1.Set(ctx, "PROVIDER1_ONLY", "v1-secret-val")
		require.NoError(t, err)

		fallbackSecret := "prod-fallback-token-xyz-456"
		err = fileV2.Set(ctx, "PROD_API_KEY", fallbackSecret)
		require.NoError(t, err)

		chainedV := vault.NewChainedProvider(fileV1, fileV2)

		logCapture.Reset()
		chainedCoord := engine.NewCoordinator(host.NewDriver(), nil, workDir).
			WithColor(false).
			WithVault(chainedV).
			WithOutput(&logCapture, &logCapture)

		jobChained := &protocol.JobSpecification{
			ID:        "job-chained-1",
			JobID:     "job-chained-1",
			JobName:   "Chained Provider Pipeline",
			EventName: "push",
			Steps: []protocol.Step{
				{
					ID:   "s1",
					Name: "Echo Chained Secret",
					Type: "script",
					Environment: map[string]string{
						"PROD_API_KEY": "",
					},
					Script: "echo 'API_KEY='\"$PROD_API_KEY\"",
				},
			},
		}

		resp, err := chainedCoord.ExecuteJob(ctx, jobChained, "", "")
		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.Equal(t, "success", resp.Conclusion)

		chainedOutput := logCapture.String()
		assert.NotContains(t, chainedOutput, fallbackSecret, "chained fallback secret must never leak in logs")
		assert.Contains(t, chainedOutput, "API_KEY=***", "chained secret must be masked with ***")
	})
}

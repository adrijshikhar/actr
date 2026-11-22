package test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/adrijshikhar/actr/internal/driver/host"
	"github.com/adrijshikhar/actr/internal/engine"
	"github.com/adrijshikhar/actr/internal/protocol"
	"github.com/adrijshikhar/actr/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAim_TinkerVaultIntegration(t *testing.T) {
	// 1. Load the OS Keychain provider default used by actr
	keychainV := vault.NewKeychainProvider("com.github.actr.secrets")

	ctx := context.Background()
	tinkerKey, err := keychainV.Get(ctx, "TINKER_API_KEY")
	require.NoError(t, err, "TINKER_API_KEY must exist in keychain vault")
	require.NotEmpty(t, tinkerKey, "TINKER_API_KEY must not be empty")

	tinkerBaseURL, err := keychainV.Get(ctx, "TINKER_API_BASE_URL")
	require.NoError(t, err, "TINKER_API_BASE_URL must exist in keychain vault")
	require.NotEmpty(t, tinkerBaseURL, "TINKER_API_BASE_URL must not be empty")

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "aim-work")
	require.NoError(t, os.MkdirAll(workDir, 0o755))

	var logCapture safeBuffer
	coord := engine.NewCoordinator(host.NewDriver(), nil, workDir).
		WithColor(false).
		WithVault(keychainV).
		WithOutput(&logCapture, &logCapture).
		WithRepoAlias("aim").
		WithRepositoryURL("https://github.com/adrijshikhar/aim")

	// Test 1: Trusted Aim Workflow Execution (Simulating .github/workflows/ci.yml step)
	t.Run("TrustedAimCI_TinkerResolutionAndMasking", func(t *testing.T) {
		logCapture.Reset()
		job := &protocol.JobSpecification{
			ID:        "job-aim-ci-1",
			JobID:     "job-aim-ci-1",
			JobName:   "Run Web Dashboard E2E Tests (Aim CI)",
			EventName: "push",
			Variables: map[string]string{
				"GITHUB_REPOSITORY": "adrijshikhar/aim",
			},
			Steps: []protocol.Step{
				{
					ID:   "step-tinker-auth",
					Name: "Authenticate and Validate Tinker API",
					Type: "script",
					Environment: map[string]string{
						"TINKER_API_KEY":      "",
						"TINKER_API_BASE_URL": "",
					},
					Script: `
						if [ -z "$TINKER_API_KEY" ]; then
							echo "ERROR: TINKER_API_KEY was not resolved from vault"
							exit 1
						fi
						if [ -z "$TINKER_API_BASE_URL" ]; then
							echo "ERROR: TINKER_API_BASE_URL was not resolved from vault"
							exit 1
						fi

						# Verify Tinker API connectivity using resolved secret
						STATUS=$(curl -s -o /dev/null -w "%{http_code}" -H "Authorization: Bearer $TINKER_API_KEY" "$TINKER_API_BASE_URL/models")
						echo "Tinker API HTTP Status: $STATUS"

						if [ "$STATUS" != "200" ]; then
							echo "ERROR: Tinker API returned non-200 status: $STATUS"
							exit 1
						fi

						# Intentionally attempt to echo the key to prove secret masking
						echo "MASK_CHECK: KEY_IS=$TINKER_API_KEY"
					`,
				},
			},
		}

		resp, err := coord.ExecuteJob(ctx, job, "", "")
		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.Equal(t, "success", resp.Conclusion)

		output := logCapture.String()
		// 1. Proves live Tinker API authentication succeeded
		assert.Contains(t, output, "Tinker API HTTP Status: 200")
		// 2. Proves raw secret value NEVER appears in runner output
		assert.NotContains(t, output, tinkerKey, "Raw TINKER_API_KEY must NEVER leak into step logs")
		// 3. Proves secret was redacted to ***
		assert.Contains(t, output, "MASK_CHECK: KEY_IS=***")
	})

	// Test 2: Fork Guard Protection on Aim PR
	t.Run("ForkGuard_UntrustedAimPRDenied", func(t *testing.T) {
		logCapture.Reset()
		forkJob := &protocol.JobSpecification{
			ID:        "job-aim-fork-1",
			JobID:     "job-aim-fork-1",
			JobName:   "Untrusted Fork PR Job",
			EventName: "pull_request",
			Variables: map[string]string{
				"GITHUB_HEAD_REF_REPO": "untrusted-fork/aim",
				"GITHUB_REPOSITORY":    "adrijshikhar/aim",
			},
			Steps: []protocol.Step{
				{
					ID:   "step-exfiltrate",
					Name: "Attempt Exfiltration",
					Type: "script",
					Environment: map[string]string{
						"TINKER_API_KEY": "",
					},
					Script: `
						echo "FORK_KEY_VAL='$TINKER_API_KEY'"
					`,
				},
			},
		}

		resp, err := coord.ExecuteJob(ctx, forkJob, "", "")
		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.Equal(t, "success", resp.Conclusion)

		output := logCapture.String()
		assert.NotContains(t, output, tinkerKey)
		assert.Contains(t, output, "FORK_KEY_VAL=''")
		assert.Contains(t, output, "Vault secret resolution blocked for untrusted fork PR: TINKER_API_KEY")
	})

	// Test 3: Aim Web Workspace Step Execution
	t.Run("AimWebWorkspace_SecretFulfillment", func(t *testing.T) {
		aimWebDir := "/Users/nemesis/Projects/my-projects/aim/web"
		if _, err := os.Stat(aimWebDir); os.IsNotExist(err) {
			t.Skip("aim/web directory not present; skipping workspace execution")
		}

		logCapture.Reset()
		job := &protocol.JobSpecification{
			ID:        "job-aim-web-1",
			JobID:     "job-aim-web-1",
			JobName:   "Aim Web E2E Step Execution",
			EventName: "push",
			Variables: map[string]string{
				"GITHUB_REPOSITORY": "adrijshikhar/aim",
			},
			Steps: []protocol.Step{
				{
					ID:               "step-node-env",
					Name:             "Verify Node process.env in aim/web",
					Type:             "script",
					WorkingDirectory: aimWebDir,
					Environment: map[string]string{
						"TINKER_API_KEY":      "",
						"TINKER_API_BASE_URL": "",
					},
					Script: `
						node -e '
							const isLive = Boolean(process.env.TINKER_API_KEY);
							const hasBase = Boolean(process.env.TINKER_API_BASE_URL);
							console.log("AIM_WEB_TINKER_CONFIGURED:", isLive && hasBase);
						'
					`,
				},
			},
		}

		resp, err := coord.ExecuteJob(ctx, job, "", "")
		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.Equal(t, "success", resp.Conclusion)

		output := logCapture.String()
		assert.Contains(t, output, "AIM_WEB_TINKER_CONFIGURED: true")
		assert.NotContains(t, output, tinkerKey)
	})
}

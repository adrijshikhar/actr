package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adrijshikhar/actr/internal/vault"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockVault struct {
	secrets map[string]string
}

var _ vault.Provider = (*mockVault)(nil)

func (m *mockVault) Name() string { return "mock" }
func (m *mockVault) Get(ctx context.Context, key string) (string, error) {
	if val, ok := m.secrets[key]; ok {
		return val, nil
	}
	return "", vault.ErrSecretNotFound
}
func (m *mockVault) Set(ctx context.Context, key, value string) error {
	m.secrets[key] = value
	return nil
}
func (m *mockVault) Delete(ctx context.Context, key string) error {
	delete(m.secrets, key)
	return nil
}
func (m *mockVault) List(ctx context.Context) ([]vault.SecretMetadata, error) {
	var list []vault.SecretMetadata
	for k := range m.secrets {
		list = append(list, vault.SecretMetadata{Key: k, Provider: "mock"})
	}
	return list, nil
}

func TestSecretsCompletion_GetAndDeleteKeys(t *testing.T) {
	origResolver := vaultProviderResolver
	t.Cleanup(func() { vaultProviderResolver = origResolver })

	mv := &mockVault{
		secrets: map[string]string{
			"GITHUB_TOKEN":   "ghp_123",
			"TINKER_API_KEY": "secret_abc",
			"AWS_SECRET":     "aws_pass",
		},
	}
	vaultProviderResolver = func(cmd *cobra.Command) (vault.Provider, error) {
		return mv, nil
	}

	tests := []struct {
		name       string
		args       []string
		wantSubstr []string
	}{
		{
			name:       "complete get all keys",
			args:       []string{"__complete", "secrets", "get", ""},
			wantSubstr: []string{"GITHUB_TOKEN", "TINKER_API_KEY", "AWS_SECRET"},
		},
		{
			name:       "complete get prefix match",
			args:       []string{"__complete", "secrets", "get", "TIN"},
			wantSubstr: []string{"TINKER_API_KEY"},
		},
		{
			name:       "complete delete all keys",
			args:       []string{"__complete", "secrets", "delete", ""},
			wantSubstr: []string{"GITHUB_TOKEN", "TINKER_API_KEY", "AWS_SECRET"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := &bytes.Buffer{}
			errOut := &bytes.Buffer{}
			cmd := newRootCmd(out, errOut)
			cmd.SetArgs(tt.args)

			if err := cmd.Execute(); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			output := out.String()
			for _, want := range tt.wantSubstr {
				if !strings.Contains(output, want) {
					t.Errorf("expected %q in completion output, got:\n%s", want, output)
				}
			}
		})
	}
}

type errorListVault struct {
	mockVault
}

func (e *errorListVault) List(ctx context.Context) ([]vault.SecretMetadata, error) {
	return nil, errors.New("simulated vault list failure")
}

func TestSecretsCompletion_FaultRecoveryAndEdgeCases(t *testing.T) {
	origResolver := vaultProviderResolver
	t.Cleanup(func() { vaultProviderResolver = origResolver })

	t.Run("extra args len != 0 returns empty slice and NoFileComp", func(t *testing.T) {
		mv := &mockVault{
			secrets: map[string]string{"KEY": "val"},
		}
		vaultProviderResolver = func(cmd *cobra.Command) (vault.Provider, error) {
			return mv, nil
		}

		// Direct function call
		cmd := newRootCmd(&bytes.Buffer{}, &bytes.Buffer{})
		matches, directive := completeSecretKeys(cmd, []string{"KEY"}, "extra")
		assert.Empty(t, matches)
		assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)

		// Invocation via cobra __complete
		out := &bytes.Buffer{}
		errOut := &bytes.Buffer{}
		cmd = newRootCmd(out, errOut)
		cmd.SetArgs([]string{"__complete", "secrets", "get", "KEY", "extra"})
		err := cmd.Execute()
		require.NoError(t, err)
		assert.NotContains(t, out.String(), "KEY")
	})

	t.Run("vaultProviderResolver error returns empty slice without panic", func(t *testing.T) {
		vaultProviderResolver = func(cmd *cobra.Command) (vault.Provider, error) {
			return nil, errors.New("failed to connect to vault")
		}

		// Direct function call
		cmd := newRootCmd(&bytes.Buffer{}, &bytes.Buffer{})
		matches, directive := completeSecretKeys(cmd, nil, "")
		assert.Empty(t, matches)
		assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)

		// Invocation via cobra __complete
		out := &bytes.Buffer{}
		errOut := &bytes.Buffer{}
		cmd = newRootCmd(out, errOut)
		cmd.SetArgs([]string{"__complete", "secrets", "get", ""})
		err := cmd.Execute()
		require.NoError(t, err)
		assert.NotContains(t, out.String(), "KEY")
	})

	t.Run("v.List error returns empty slice without panic", func(t *testing.T) {
		vaultProviderResolver = func(cmd *cobra.Command) (vault.Provider, error) {
			return &errorListVault{}, nil
		}

		// Direct function call
		cmd := newRootCmd(&bytes.Buffer{}, &bytes.Buffer{})
		matches, directive := completeSecretKeys(cmd, nil, "")
		assert.Empty(t, matches)
		assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)

		// Invocation via cobra __complete
		out := &bytes.Buffer{}
		errOut := &bytes.Buffer{}
		cmd = newRootCmd(out, errOut)
		cmd.SetArgs([]string{"__complete", "secrets", "get", ""})
		err := cmd.Execute()
		require.NoError(t, err)
		assert.NotContains(t, out.String(), "KEY")
	})
}

func TestSecretsCLI_Lifecycle(t *testing.T) {
	tempDir := t.TempDir()

	// 1. Set secret via --value flag
	var outBuf, errBuf bytes.Buffer
	code := dispatchWithContext(context.Background(), []string{"secrets", "set", "TEST_KEY", "--value", "secret123", "--config-dir", tempDir}, &outBuf, &errBuf)
	require.Equal(t, 0, code, "expected secrets set exit 0: %s", errBuf.String())

	// 2. Get secret
	outBuf.Reset()
	errBuf.Reset()
	code = dispatchWithContext(context.Background(), []string{"secrets", "get", "TEST_KEY", "--config-dir", tempDir}, &outBuf, &errBuf)
	require.Equal(t, 0, code, "expected secrets get exit 0: %s", errBuf.String())
	assert.Equal(t, "secret123", strings.TrimSpace(outBuf.String()))

	// 3. List secrets
	outBuf.Reset()
	errBuf.Reset()
	code = dispatchWithContext(context.Background(), []string{"secrets", "list", "--config-dir", tempDir}, &outBuf, &errBuf)
	require.Equal(t, 0, code, "expected secrets list exit 0: %s", errBuf.String())
	assert.Contains(t, outBuf.String(), "TEST_KEY")

	// 4. Delete secret
	outBuf.Reset()
	errBuf.Reset()
	code = dispatchWithContext(context.Background(), []string{"secrets", "delete", "TEST_KEY", "--config-dir", tempDir}, &outBuf, &errBuf)
	require.Equal(t, 0, code, "expected secrets delete exit 0: %s", errBuf.String())
}

func TestSecretsCLI_FileVault_Lifecycle(t *testing.T) {
	tempDir := t.TempDir()
	configContent := `
vault:
  enabled: true
  providers:
    - type: "encrypted-file"
      path: "` + filepath.Join(tempDir, "vault.enc") + `"
      auth:
        passphrase: "test-file-vault-pass"
`
	err := os.WriteFile(filepath.Join(tempDir, "config.yaml"), []byte(configContent), 0600)
	require.NoError(t, err)

	// Set secret
	var outBuf, errBuf bytes.Buffer
	code := dispatchWithContext(context.Background(), []string{"secrets", "set", "DB_PASSWORD", "--value", "super-secret-pass", "--config-dir", tempDir}, &outBuf, &errBuf)
	require.Equal(t, 0, code, "set DB_PASSWORD failed: %s", errBuf.String())
	assert.Contains(t, outBuf.String(), "✓ Stored DB_PASSWORD in encrypted-file")

	// Set second secret
	outBuf.Reset()
	errBuf.Reset()
	code = dispatchWithContext(context.Background(), []string{"secrets", "set", "API_KEY", "--value", "apikey987", "--config-dir", tempDir}, &outBuf, &errBuf)
	require.Equal(t, 0, code, "set API_KEY failed: %s", errBuf.String())

	// Get secret
	outBuf.Reset()
	errBuf.Reset()
	code = dispatchWithContext(context.Background(), []string{"secrets", "get", "DB_PASSWORD", "--config-dir", tempDir}, &outBuf, &errBuf)
	require.Equal(t, 0, code, "get DB_PASSWORD failed: %s", errBuf.String())
	assert.Equal(t, "super-secret-pass", strings.TrimSpace(outBuf.String()))

	// List secrets
	outBuf.Reset()
	errBuf.Reset()
	code = dispatchWithContext(context.Background(), []string{"secrets", "list", "--config-dir", tempDir}, &outBuf, &errBuf)
	require.Equal(t, 0, code, "list failed: %s", errBuf.String())
	listOut := outBuf.String()
	assert.Contains(t, listOut, "KEY")
	assert.Contains(t, listOut, "PROVIDER")
	assert.Contains(t, listOut, "UPDATED")
	assert.Contains(t, listOut, "DB_PASSWORD")
	assert.Contains(t, listOut, "API_KEY")
	assert.Contains(t, listOut, "encrypted-file")

	// Delete secret
	outBuf.Reset()
	errBuf.Reset()
	code = dispatchWithContext(context.Background(), []string{"secrets", "delete", "DB_PASSWORD", "--config-dir", tempDir}, &outBuf, &errBuf)
	require.Equal(t, 0, code, "delete failed: %s", errBuf.String())
	assert.Contains(t, outBuf.String(), "✓ Deleted DB_PASSWORD")

	// Get secret after delete should fail
	outBuf.Reset()
	errBuf.Reset()
	code = dispatchWithContext(context.Background(), []string{"secrets", "get", "DB_PASSWORD", "--config-dir", tempDir}, &outBuf, &errBuf)
	require.NotEqual(t, 0, code)
	assert.Contains(t, errBuf.String(), "secret not found in vault")
}

func TestSecretsCLI_PipeInput(t *testing.T) {
	tempDir := t.TempDir()
	configContent := `
vault:
  providers:
    - type: "encrypted-file"
`
	err := os.WriteFile(filepath.Join(tempDir, "config.yaml"), []byte(configContent), 0600)
	require.NoError(t, err)

	// Pipe with trailing newline
	in := strings.NewReader("piped-secret-val-123\n")
	var outBuf, errBuf bytes.Buffer
	code := dispatchWithContextAndIn(context.Background(), in, []string{"secrets", "set", "PIPED_KEY", "--config-dir", tempDir}, &outBuf, &errBuf)
	require.Equal(t, 0, code, "pipe set failed: %s", errBuf.String())
	assert.Contains(t, outBuf.String(), "✓ Stored PIPED_KEY")

	// Verify retrieval
	outBuf.Reset()
	errBuf.Reset()
	code = dispatchWithContext(context.Background(), []string{"secrets", "get", "PIPED_KEY", "--config-dir", tempDir}, &outBuf, &errBuf)
	require.Equal(t, 0, code)
	assert.Equal(t, "piped-secret-val-123", strings.TrimSpace(outBuf.String()))
}

func TestSecretsCLI_PipeInput_NoTrailingNewline(t *testing.T) {
	tempDir := t.TempDir()
	configContent := `
vault:
  providers:
    - type: "encrypted-file"
`
	err := os.WriteFile(filepath.Join(tempDir, "config.yaml"), []byte(configContent), 0600)
	require.NoError(t, err)

	// Pipe ending with EOF (no newline)
	in := strings.NewReader("eof-secret-val-456")
	var outBuf, errBuf bytes.Buffer
	code := dispatchWithContextAndIn(context.Background(), in, []string{"secrets", "set", "EOF_KEY", "--config-dir", tempDir}, &outBuf, &errBuf)
	require.Equal(t, 0, code, "pipe set failed: %s", errBuf.String())

	// Verify retrieval
	outBuf.Reset()
	errBuf.Reset()
	code = dispatchWithContext(context.Background(), []string{"secrets", "get", "EOF_KEY", "--config-dir", tempDir}, &outBuf, &errBuf)
	require.Equal(t, 0, code)
	assert.Equal(t, "eof-secret-val-456", strings.TrimSpace(outBuf.String()))
}

func TestSecretsCLI_InteractivePrompt(t *testing.T) {
	tempDir := t.TempDir()
	configContent := `
vault:
  providers:
    - type: "encrypted-file"
`
	err := os.WriteFile(filepath.Join(tempDir, "config.yaml"), []byte(configContent), 0600)
	require.NoError(t, err)

	origIsTerm := isTerminalReaderFunc
	origReadPass := readPasswordFunc
	defer func() {
		isTerminalReaderFunc = origIsTerm
		readPasswordFunc = origReadPass
	}()

	isTerminalReaderFunc = func(r io.Reader) bool { return true }
	readPasswordFunc = func(fd int) ([]byte, error) {
		return []byte("interactive-prompt-value"), nil
	}

	var outBuf, errBuf bytes.Buffer
	code := dispatchWithContext(context.Background(), []string{"secrets", "set", "INTERACTIVE_KEY", "--config-dir", tempDir}, &outBuf, &errBuf)
	require.Equal(t, 0, code, "interactive set failed: %s", errBuf.String())
	assert.Contains(t, outBuf.String(), "Enter secret value for INTERACTIVE_KEY:")
	assert.Contains(t, outBuf.String(), "✓ Stored INTERACTIVE_KEY")

	// Verify retrieval
	outBuf.Reset()
	errBuf.Reset()
	code = dispatchWithContext(context.Background(), []string{"secrets", "get", "INTERACTIVE_KEY", "--config-dir", tempDir}, &outBuf, &errBuf)
	require.Equal(t, 0, code)
	assert.Equal(t, "interactive-prompt-value", strings.TrimSpace(outBuf.String()))
}

func TestSecretsCLI_InteractivePrompt_ReadError(t *testing.T) {
	tempDir := t.TempDir()
	configContent := `
vault:
  providers:
    - type: "encrypted-file"
`
	err := os.WriteFile(filepath.Join(tempDir, "config.yaml"), []byte(configContent), 0600)
	require.NoError(t, err)

	origIsTerm := isTerminalReaderFunc
	origReadPass := readPasswordFunc
	defer func() {
		isTerminalReaderFunc = origIsTerm
		readPasswordFunc = origReadPass
	}()

	isTerminalReaderFunc = func(r io.Reader) bool { return true }
	readPasswordFunc = func(fd int) ([]byte, error) {
		return nil, errors.New("terminal read error")
	}

	var outBuf, errBuf bytes.Buffer
	code := dispatchWithContext(context.Background(), []string{"secrets", "set", "FAIL_KEY", "--config-dir", tempDir}, &outBuf, &errBuf)
	require.NotEqual(t, 0, code)
	assert.Contains(t, errBuf.String(), "read secret value: terminal read error")
}

func TestSecretsCLI_GetNotFound(t *testing.T) {
	tempDir := t.TempDir()
	configContent := `
vault:
  providers:
    - type: "encrypted-file"
`
	err := os.WriteFile(filepath.Join(tempDir, "config.yaml"), []byte(configContent), 0600)
	require.NoError(t, err)

	var outBuf, errBuf bytes.Buffer
	code := dispatchWithContext(context.Background(), []string{"secrets", "get", "NO_SUCH_KEY", "--config-dir", tempDir}, &outBuf, &errBuf)
	require.NotEqual(t, 0, code)
	assert.Contains(t, errBuf.String(), "secret not found in vault")
}

func TestSecretsCLI_DeleteNotFound(t *testing.T) {
	tempDir := t.TempDir()
	configContent := `
vault:
  providers:
    - type: "encrypted-file"
`
	err := os.WriteFile(filepath.Join(tempDir, "config.yaml"), []byte(configContent), 0600)
	require.NoError(t, err)

	var outBuf, errBuf bytes.Buffer
	code := dispatchWithContext(context.Background(), []string{"secrets", "delete", "NO_SUCH_KEY", "--config-dir", tempDir}, &outBuf, &errBuf)
	require.NotEqual(t, 0, code)
	assert.Contains(t, errBuf.String(), "secret not found in vault")
}

func TestSecretsCLI_ArgumentValidation(t *testing.T) {
	tempDir := t.TempDir()

	tests := []struct {
		name string
		args []string
	}{
		{"set missing key", []string{"secrets", "set", "--config-dir", tempDir}},
		{"get missing key", []string{"secrets", "get", "--config-dir", tempDir}},
		{"delete missing key", []string{"secrets", "delete", "--config-dir", tempDir}},
		{"list unexpected args", []string{"secrets", "list", "unexpected", "--config-dir", tempDir}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var outBuf, errBuf bytes.Buffer
			code := dispatchWithContext(context.Background(), tt.args, &outBuf, &errBuf)
			require.NotEqual(t, 0, code)
		})
	}
}

func TestSecretsCLI_Help(t *testing.T) {
	var outBuf, errBuf bytes.Buffer
	code := dispatchWithContext(context.Background(), []string{"secrets", "--help"}, &outBuf, &errBuf)
	require.Equal(t, 0, code)
	out := outBuf.String()
	assert.Contains(t, out, "Manage runner-local credentials in the secret vault")
	assert.Contains(t, out, "set")
	assert.Contains(t, out, "get")
	assert.Contains(t, out, "list")
	assert.Contains(t, out, "delete")
	assert.Contains(t, out, "--config-dir")
}

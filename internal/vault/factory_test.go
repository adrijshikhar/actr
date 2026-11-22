package vault

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/adrijshikhar/actr/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFactory_DefaultFallback(t *testing.T) {
	tmpDir := t.TempDir()

	// Case 1: nil config
	p, err := NewProviderFromConfig(nil, tmpDir)
	require.NoError(t, err)
	require.NotNil(t, p)

	if runtime.GOOS == "darwin" {
		assert.Equal(t, "keychain", p.Name())
	} else {
		assert.Equal(t, "encrypted-file", p.Name())
	}

	// Case 2: empty providers list in config
	pEmpty, err := NewProviderFromConfig(&config.VaultYAMLConfig{
		Enabled:   true,
		Providers: []config.VaultProviderYAMLConfig{},
	}, tmpDir)
	require.NoError(t, err)
	require.NotNil(t, pEmpty)

	if runtime.GOOS == "darwin" {
		assert.Equal(t, "keychain", pEmpty.Name())
	} else {
		assert.Equal(t, "encrypted-file", pEmpty.Name())
	}
}

func TestFactory_ConfiguredProviders_Chained(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.VaultYAMLConfig{
		Enabled:     true,
		TrustedOnly: true,
		Providers: []config.VaultProviderYAMLConfig{
			{Type: "keychain", Label: "com.github.actr.custom"},
			{Type: "encrypted-file", Path: filepath.Join(tmpDir, "vault.enc")},
		},
	}

	p, err := NewProviderFromConfig(cfg, tmpDir)
	require.NoError(t, err)
	require.NotNil(t, p)
	assert.Equal(t, "chained", p.Name())
}

func TestFactory_SingleProvider_Keychain(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.VaultYAMLConfig{
		Enabled: true,
		Providers: []config.VaultProviderYAMLConfig{
			{Type: "keychain", Label: "com.github.actr.single"},
		},
	}

	p, err := NewProviderFromConfig(cfg, tmpDir)
	require.NoError(t, err)
	require.NotNil(t, p)
	assert.Equal(t, "keychain", p.Name())
}

func TestFactory_SingleProvider_EncryptedFile(t *testing.T) {
	tmpDir := t.TempDir()
	vaultPath := filepath.Join(tmpDir, "custom-vault.enc")
	cfg := &config.VaultYAMLConfig{
		Enabled: true,
		Providers: []config.VaultProviderYAMLConfig{
			{
				Type: "encrypted-file",
				Path: vaultPath,
				Auth: map[string]string{
					"passphrase": "custom-passphrase-123",
				},
			},
		},
	}

	p, err := NewProviderFromConfig(cfg, tmpDir)
	require.NoError(t, err)
	require.NotNil(t, p)
	assert.Equal(t, "encrypted-file", p.Name())

	// Test store & retrieve
	ctx := context.Background()
	err = p.Set(ctx, "FACTORY_KEY", "factory-value-999")
	require.NoError(t, err)

	val, err := p.Get(ctx, "FACTORY_KEY")
	require.NoError(t, err)
	assert.Equal(t, "factory-value-999", val)
}

func TestFactory_SingleProvider_EncryptedFile_DefaultPath(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.VaultYAMLConfig{
		Enabled: true,
		Providers: []config.VaultProviderYAMLConfig{
			{Type: "encrypted-file"},
		},
	}

	p, err := NewProviderFromConfig(cfg, tmpDir)
	require.NoError(t, err)
	require.NotNil(t, p)
	assert.Equal(t, "encrypted-file", p.Name())

	ctx := context.Background()
	err = p.Set(ctx, "DEF_PATH_KEY", "def-val")
	require.NoError(t, err)

	// Verify file was created in tmpDir/vault.enc
	_, err = os.Stat(filepath.Join(tmpDir, "vault.enc"))
	require.NoError(t, err)
}

func TestFactory_SingleProvider_HashiCorpVault(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.VaultYAMLConfig{
		Enabled: true,
		Providers: []config.VaultProviderYAMLConfig{
			{
				Type:    "hashicorp-vault",
				Address: "http://127.0.0.1:8200",
				Path:    "actr/secrets",
				Auth: map[string]string{
					"token": "hvs.mocktoken",
				},
			},
		},
	}

	p, err := NewProviderFromConfig(cfg, tmpDir)
	require.NoError(t, err)
	require.NotNil(t, p)
	assert.Equal(t, "hashicorp-vault", p.Name())
}

func TestFactory_SingleProvider_AWSSecretsManager(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.VaultYAMLConfig{
		Enabled: true,
		Providers: []config.VaultProviderYAMLConfig{
			{
				Type:   "aws-secrets-manager",
				Region: "us-west-2",
				Prefix: "actr/prod/",
			},
		},
	}

	p, err := NewProviderFromConfig(cfg, tmpDir)
	require.NoError(t, err)
	require.NotNil(t, p)
	assert.Equal(t, "aws-secrets-manager", p.Name())
}

func TestFactory_EncryptedFile_Corrupted(t *testing.T) {
	tmpDir := t.TempDir()
	corruptPath := filepath.Join(tmpDir, "corrupted.enc")
	err := os.WriteFile(corruptPath, []byte("not-a-valid-encrypted-file"), 0600)
	require.NoError(t, err)

	cfg := &config.VaultYAMLConfig{
		Enabled: true,
		Providers: []config.VaultProviderYAMLConfig{
			{Type: "encrypted-file", Path: corruptPath},
		},
	}

	p, err := NewProviderFromConfig(cfg, tmpDir)
	assert.Error(t, err)
	assert.Nil(t, p)
}

package vault

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/adrijshikhar/actr/internal/config"
)

const defaultVaultKeyBytes = 32

// NewProviderFromConfig constructs a Provider or ChainedProvider based on VaultYAMLConfig.
// If cfg is nil or has no providers configured, it returns the platform default provider:
// - macOS: KeychainProvider (service: "com.github.actr.secrets")
// - other OS: FileVaultProvider (path: defaultDir/vault.enc) keyed by ACTR_VAULT_PASSPHRASE
//   or a randomly generated 32-byte key stored in defaultDir/vault.key.
func NewProviderFromConfig(cfg *config.VaultYAMLConfig, defaultDir string) (Provider, error) {
	if cfg == nil || len(cfg.Providers) == 0 {
		if runtime.GOOS == "darwin" {
			return NewKeychainProvider("com.github.actr.secrets"), nil
		}
		passphrase := []byte(os.Getenv("ACTR_VAULT_PASSPHRASE"))
		if len(passphrase) == 0 {
			var err error
			passphrase, err = loadOrCreateDefaultVaultKey(defaultDir)
			if err != nil {
				return nil, err
			}
		}
		vaultPath := filepath.Join(defaultDir, "vault.enc")
		fp, err := NewFileProvider(vaultPath, passphrase)
		if err != nil {
			return nil, err
		}
		return fp, nil
	}

	var providers []Provider
	for _, pcfg := range cfg.Providers {
		switch pcfg.Type {
		case "keychain":
			label := pcfg.Label
			if label == "" {
				label = "com.github.actr.secrets"
			}
			providers = append(providers, NewKeychainProvider(label))
		case "encrypted-file":
			p := pcfg.Path
			if p == "" {
				p = filepath.Join(defaultDir, "vault.enc")
			}
			passphrase, err := resolveFileVaultPassphrase(pcfg.Auth)
			if err != nil {
				return nil, err
			}
			fp, err := NewFileProvider(p, passphrase)
			if err != nil {
				return nil, err
			}
			providers = append(providers, fp)
		case "hashicorp-vault":
			token := ""
			if pcfg.Auth != nil {
				token = pcfg.Auth["token"]
			}
			providers = append(providers, NewHashiCorpVaultProvider(pcfg.Address, token, "secret", pcfg.Path))
		case "aws-secrets-manager":
			providers = append(providers, NewAWSSecretsManagerProvider(pcfg.Region, pcfg.Prefix))
		default:
			return nil, fmt.Errorf("unsupported vault provider type: %q", pcfg.Type)
		}
	}

	if len(providers) == 1 {
		return providers[0], nil
	}
	return NewChainedProvider(providers...), nil
}

func resolveFileVaultPassphrase(auth map[string]string) ([]byte, error) {
	if auth != nil {
		if pass, ok := auth["passphrase"]; ok && pass != "" {
			return []byte(pass), nil
		}
		if key, ok := auth["key"]; ok && key != "" {
			return []byte(key), nil
		}
	}
	if envPass := os.Getenv("ACTR_VAULT_PASSPHRASE"); envPass != "" {
		return []byte(envPass), nil
	}
	return nil, errors.New("encrypted-file vault requires auth.passphrase, auth.key, or ACTR_VAULT_PASSPHRASE")
}

func loadOrCreateDefaultVaultKey(defaultDir string) ([]byte, error) {
	if defaultDir == "" {
		return nil, errors.New("default vault directory is required when ACTR_VAULT_PASSPHRASE is not set")
	}
	if err := os.MkdirAll(defaultDir, 0o700); err != nil {
		return nil, fmt.Errorf("create default vault directory: %w", err)
	}

	keyPath := filepath.Join(defaultDir, "vault.key")
	existing, err := os.ReadFile(keyPath)
	if err == nil {
		if len(existing) == 0 {
			return nil, fmt.Errorf("default vault key file %q is empty", keyPath)
		}
		return existing, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read default vault key: %w", err)
	}

	key := make([]byte, defaultVaultKeyBytes)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("generate default vault key: %w", err)
	}

	f, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			existing, readErr := os.ReadFile(keyPath)
			if readErr != nil {
				return nil, fmt.Errorf("read existing default vault key: %w", readErr)
			}
			if len(existing) == 0 {
				return nil, fmt.Errorf("default vault key file %q is empty", keyPath)
			}
			return existing, nil
		}
		return nil, fmt.Errorf("create default vault key file: %w", err)
	}

	if _, err := f.Write(key); err != nil {
		_ = f.Close()
		_ = os.Remove(keyPath)
		return nil, fmt.Errorf("write default vault key: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(keyPath)
		return nil, fmt.Errorf("sync default vault key: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(keyPath)
		return nil, fmt.Errorf("close default vault key: %w", err)
	}

	return key, nil
}

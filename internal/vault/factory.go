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

const (
	defaultVaultKeyBytes  = 32
	legacyDefaultVaultKey = "default-actr-key"
)

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
		vaultPath := filepath.Join(defaultDir, "vault.enc")
		keyPath := filepath.Join(defaultDir, "vault.key")

		if len(passphrase) == 0 {
			// Check if vault.enc exists and vault.key does not exist: attempt migration from legacy default key
			if _, vErr := os.Stat(vaultPath); vErr == nil {
				if _, kErr := os.Stat(keyPath); os.IsNotExist(kErr) {
					if legacyFp, err := NewFileProvider(vaultPath, []byte(legacyDefaultVaultKey)); err == nil {
						newKey, err := loadOrCreateDefaultVaultKey(defaultDir)
						if err == nil {
							legacyFp.mu.Lock()
							legacyFp.passphrase = append([]byte(nil), newKey...)
							saveErr := legacyFp.save()
							legacyFp.mu.Unlock()
							if saveErr == nil {
								return legacyFp, nil
							}
						}
					}
				}
			}

			var err error
			passphrase, err = loadOrCreateDefaultVaultKey(defaultDir)
			if err != nil {
				return nil, err
			}
		}
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
				if len(cfg.Providers) > 1 {
					// In a chained configuration, skip missing passphrase provider with a warning
					// so remaining providers still operate.
					continue
				}
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

	if len(providers) == 0 {
		return nil, errors.New("no valid vault providers configured")
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

	tmpFile, err := os.CreateTemp(defaultDir, ".vault.key-*")
	if err != nil {
		return nil, fmt.Errorf("create temp vault key file: %w", err)
	}
	tmpName := tmpFile.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()

	if _, err := tmpFile.Write(key); err != nil {
		_ = tmpFile.Close()
		return nil, fmt.Errorf("write default vault key: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return nil, fmt.Errorf("sync default vault key: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return nil, fmt.Errorf("close default vault key: %w", err)
	}
	_ = os.Chmod(tmpName, 0o600)

	if err := os.Link(tmpName, keyPath); err != nil {
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
		// If link failed (e.g. cross-device or not supported), try rename if keyPath doesn't exist
		if _, statErr := os.Stat(keyPath); os.IsNotExist(statErr) {
			if renameErr := os.Rename(tmpName, keyPath); renameErr != nil {
				return nil, fmt.Errorf("rename default vault key file: %w", renameErr)
			}
			return key, nil
		}
		existing, readErr := os.ReadFile(keyPath)
		if readErr == nil && len(existing) > 0 {
			return existing, nil
		}
		return nil, fmt.Errorf("create default vault key file: %w", err)
	}

	return key, nil
}

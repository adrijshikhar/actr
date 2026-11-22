package vault

import (
	"fmt"
	"path/filepath"
	"runtime"

	"github.com/adrijshikhar/actr/internal/config"
)

// NewProviderFromConfig constructs a Provider or ChainedProvider based on VaultYAMLConfig.
// If cfg is nil or has no providers configured, it returns the platform default provider:
// - macOS: KeychainProvider (service: "com.github.actr.secrets")
// - other OS: FileVaultProvider (path: defaultDir/vault.enc, key: "default-actr-key")
func NewProviderFromConfig(cfg *config.VaultYAMLConfig, defaultDir string) (Provider, error) {
	if cfg == nil || len(cfg.Providers) == 0 {
		if runtime.GOOS == "darwin" {
			return NewKeychainProvider("com.github.actr.secrets"), nil
		}
		vaultPath := filepath.Join(defaultDir, "vault.enc")
		fp, err := NewFileProvider(vaultPath, []byte("default-actr-key"))
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
			passphrase := []byte("default-actr-key")
			if pcfg.Auth != nil {
				if pass, ok := pcfg.Auth["passphrase"]; ok && pass != "" {
					passphrase = []byte(pass)
				} else if key, ok := pcfg.Auth["key"]; ok && key != "" {
					passphrase = []byte(key)
				}
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

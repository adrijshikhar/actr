package vault

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

var _ Provider = (*HashiCorpVaultProvider)(nil)

// HashiCorpVaultProvider provides read-only secret resolution from HashiCorp Vault KV v2.
type HashiCorpVaultProvider struct {
	address    string
	token      string
	mountPath  string
	secretPath string
	client     *http.Client
}

// NewHashiCorpVaultProvider creates a new HashiCorp Vault KV v2 provider.
func NewHashiCorpVaultProvider(address, token, mountPath, secretPath string) Provider {
	return &HashiCorpVaultProvider{
		address:    strings.TrimRight(address, "/"),
		token:      token,
		mountPath:  strings.Trim(mountPath, "/"),
		secretPath: strings.Trim(secretPath, "/"),
		client:     &http.Client{Timeout: 10 * time.Second},
	}
}

// Name returns the provider identifier.
func (h *HashiCorpVaultProvider) Name() string {
	return "hashicorp-vault"
}

// Get fetches a secret value by key from HashiCorp Vault KV v2.
func (h *HashiCorpVaultProvider) Get(ctx context.Context, key string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	url := fmt.Sprintf("%s/v1/%s/data/%s", h.address, h.mountPath, h.secretPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-Vault-Token", h.token)

	resp, err := h.client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return "", ErrSecretNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("hashicorp vault returned HTTP %d", resp.StatusCode)
	}

	var data struct {
		Data struct {
			Data map[string]any `json:"data"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", err
	}

	if val, ok := data.Data.Data[key]; ok {
		switch v := val.(type) {
		case string:
			return v, nil
		default:
			return fmt.Sprintf("%v", v), nil
		}
	}
	return "", ErrSecretNotFound
}

// Set is not supported as HashiCorp Vault provider is read-only.
func (h *HashiCorpVaultProvider) Set(ctx context.Context, key, value string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return ErrReadOnlyVault
}

// Delete is not supported as HashiCorp Vault provider is read-only.
func (h *HashiCorpVaultProvider) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return ErrReadOnlyVault
}

// List returns secret keys available under the configured KV v2 path.
func (h *HashiCorpVaultProvider) List(ctx context.Context) ([]SecretMetadata, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	url := fmt.Sprintf("%s/v1/%s/data/%s", h.address, h.mountPath, h.secretPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Vault-Token", h.token)

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hashicorp vault returned HTTP %d", resp.StatusCode)
	}

	var data struct {
		Data struct {
			Data map[string]any `json:"data"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}

	if len(data.Data.Data) == 0 {
		return nil, nil
	}

	list := make([]SecretMetadata, 0, len(data.Data.Data))
	for k := range data.Data.Data {
		list = append(list, SecretMetadata{Key: k, Provider: h.Name()})
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].Key < list[j].Key
	})
	return list, nil
}

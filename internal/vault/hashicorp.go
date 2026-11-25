package vault

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const maxHashiCorpResponseBytes = 1 << 20

var _ Provider = (*HashiCorpVaultProvider)(nil)

// HashiCorpVaultProvider provides read-only secret resolution from HashiCorp Vault KV v2.
type HashiCorpVaultProvider struct {
	address    string
	token      string
	mountPath  string
	secretPath string
	client     *http.Client
	initErr    error
}

// NewHashiCorpVaultProvider creates a new HashiCorp Vault KV v2 provider.
func NewHashiCorpVaultProvider(address, token, mountPath, secretPath string) Provider {
	cleanAddr := strings.TrimRight(strings.TrimSpace(address), "/")
	initErr := validateVaultAddress(cleanAddr)

	return &HashiCorpVaultProvider{
		address:    cleanAddr,
		token:      token,
		mountPath:  strings.Trim(mountPath, "/"),
		secretPath: strings.Trim(secretPath, "/"),
		client: &http.Client{
			Timeout: 10 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		initErr: initErr,
	}
}

func validateVaultAddress(address string) error {
	if address == "" {
		return errors.New("hashicorp vault address must not be empty")
	}
	u, err := url.Parse(address)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("invalid hashicorp vault address %q", address)
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if strings.EqualFold(host, "localhost") {
			return nil
		}
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			return nil
		}
		return fmt.Errorf("hashicorp vault address %q must use https for non-loopback hosts", address)
	default:
		return fmt.Errorf("unsupported hashicorp vault address scheme %q", u.Scheme)
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
	if h.initErr != nil {
		return "", h.initErr
	}

	reqURL := fmt.Sprintf("%s/v1/%s/data/%s", h.address, h.mountPath, h.secretPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
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
	dec := json.NewDecoder(io.LimitReader(resp.Body, maxHashiCorpResponseBytes))
	dec.UseNumber()
	if err := dec.Decode(&data); err != nil {
		return "", err
	}

	val, ok := data.Data.Data[key]
	if !ok {
		return "", ErrSecretNotFound
	}
	if val == nil {
		return "", fmt.Errorf("hashicorp vault secret %q has null value: %w", key, ErrSecretNotFound)
	}
	switch v := val.(type) {
	case string:
		return v, nil
	case json.Number:
		return v.String(), nil
	case bool:
		return fmt.Sprintf("%t", v), nil
	default:
		return "", fmt.Errorf("unsupported hashicorp vault secret value type %T for key %q", val, key)
	}
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
	if h.initErr != nil {
		return nil, h.initErr
	}

	reqURL := fmt.Sprintf("%s/v1/%s/data/%s", h.address, h.mountPath, h.secretPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
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
	dec := json.NewDecoder(io.LimitReader(resp.Body, maxHashiCorpResponseBytes))
	dec.UseNumber()
	if err := dec.Decode(&data); err != nil {
		return nil, err
	}

	if len(data.Data.Data) == 0 {
		return nil, nil
	}

	list := make([]SecretMetadata, 0, len(data.Data.Data))
	for k, v := range data.Data.Data {
		if v == nil {
			continue
		}
		list = append(list, SecretMetadata{Key: k, Provider: h.Name()})
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].Key < list[j].Key
	})
	return list, nil
}

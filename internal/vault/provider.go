package vault

import (
	"context"
	"errors"
)

var (
	ErrSecretNotFound   = errors.New("secret not found in vault")
	ErrReadOnlyVault    = errors.New("vault provider is read-only")
	ErrUntrustedContext = errors.New("secret resolution blocked in untrusted fork PR context")
)

type SecretMetadata struct {
	Key       string `json:"key"`
	Provider  string `json:"provider"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

type Provider interface {
	Name() string
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, value string) error
	Delete(ctx context.Context, key string) error
	List(ctx context.Context) ([]SecretMetadata, error)
}

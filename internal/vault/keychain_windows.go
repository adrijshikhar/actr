//go:build windows

package vault

import (
	"context"
)

type WindowsDPAPIProvider struct {
	service string
}

func NewKeychainProvider(serviceName string) Provider {
	if serviceName == "" {
		serviceName = "com.github.actr.secrets"
	}
	return &WindowsDPAPIProvider{service: serviceName}
}

func (w *WindowsDPAPIProvider) Name() string { return "keychain" }

func (w *WindowsDPAPIProvider) Get(ctx context.Context, key string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return "", ErrSecretNotFound
}

func (w *WindowsDPAPIProvider) Set(ctx context.Context, key, value string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func (w *WindowsDPAPIProvider) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func (w *WindowsDPAPIProvider) List(ctx context.Context) ([]SecretMetadata, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, nil
}

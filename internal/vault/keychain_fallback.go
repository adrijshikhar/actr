//go:build !darwin && !windows

package vault

import "context"

type FallbackKeychainProvider struct{}

func NewKeychainProvider(serviceName string) Provider {
	return &FallbackKeychainProvider{}
}

func (f *FallbackKeychainProvider) Name() string { return "keychain" }

func (f *FallbackKeychainProvider) Get(ctx context.Context, key string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return "", ErrSecretNotFound
}

func (f *FallbackKeychainProvider) Set(ctx context.Context, key, value string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return ErrReadOnlyVault
}

func (f *FallbackKeychainProvider) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return ErrSecretNotFound
}

func (f *FallbackKeychainProvider) List(ctx context.Context) ([]SecretMetadata, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, nil
}

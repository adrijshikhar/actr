package vault

import (
	"context"
	"errors"
)

type ChainedProvider struct {
	providers []Provider
}

func NewChainedProvider(providers ...Provider) *ChainedProvider {
	return &ChainedProvider{providers: providers}
}

func (c *ChainedProvider) Name() string {
	return "chained"
}

func (c *ChainedProvider) Get(ctx context.Context, key string) (string, error) {
	for _, p := range c.providers {
		val, err := p.Get(ctx, key)
		if err == nil && val != "" {
			return val, nil
		}
	}
	return "", ErrSecretNotFound
}

func (c *ChainedProvider) Set(ctx context.Context, key, value string) error {
	for _, p := range c.providers {
		err := p.Set(ctx, key, value)
		if !errors.Is(err, ErrReadOnlyVault) {
			return err
		}
	}
	return ErrReadOnlyVault
}

func (c *ChainedProvider) Delete(ctx context.Context, key string) error {
	var deleted bool
	for _, p := range c.providers {
		if err := p.Delete(ctx, key); err == nil {
			deleted = true
		}
	}
	if !deleted {
		return ErrSecretNotFound
	}
	return nil
}

func (c *ChainedProvider) List(ctx context.Context) ([]SecretMetadata, error) {
	seen := make(map[string]bool)
	var result []SecretMetadata
	for _, p := range c.providers {
		items, err := p.List(ctx)
		if err != nil {
			continue
		}
		for _, item := range items {
			if !seen[item.Key] {
				seen[item.Key] = true
				result = append(result, item)
			}
		}
	}
	return result, nil
}

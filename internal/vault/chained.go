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
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var backendErrs []error
	for _, p := range c.providers {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		val, err := p.Get(ctx, key)
		if err == nil && val != "" {
			return val, nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return "", err
		}
		if err != nil && !errors.Is(err, ErrSecretNotFound) {
			backendErrs = append(backendErrs, err)
		}
	}
	if len(backendErrs) > 0 {
		return "", errors.Join(backendErrs...)
	}
	return "", ErrSecretNotFound
}

func (c *ChainedProvider) Set(ctx context.Context, key, value string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, p := range c.providers {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := p.Set(ctx, key, value)
		if !errors.Is(err, ErrReadOnlyVault) {
			return err
		}
	}
	return ErrReadOnlyVault
}

func (c *ChainedProvider) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var deleted bool
	var backendErrs []error
	for _, p := range c.providers {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := p.Delete(ctx, key); err == nil {
			deleted = true
		} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		} else if !errors.Is(err, ErrSecretNotFound) && !errors.Is(err, ErrReadOnlyVault) {
			backendErrs = append(backendErrs, err)
		}
	}
	if !deleted {
		if len(backendErrs) > 0 {
			return errors.Join(backendErrs...)
		}
		return ErrSecretNotFound
	}
	return nil
}

func (c *ChainedProvider) List(ctx context.Context) ([]SecretMetadata, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	var result []SecretMetadata
	var listErrs []error
	var anySucceeded bool
	for _, p := range c.providers {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		items, err := p.List(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			listErrs = append(listErrs, err)
			continue
		}
		anySucceeded = true
		for _, item := range items {
			if !seen[item.Key] {
				seen[item.Key] = true
				result = append(result, item)
			}
		}
	}
	if !anySucceeded && len(listErrs) > 0 {
		return nil, errors.Join(listErrs...)
	}
	return result, nil
}

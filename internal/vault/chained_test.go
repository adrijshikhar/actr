package vault

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockProvider struct {
	name     string
	readOnly bool
	store    map[string]string
}

func newMock(name string, readOnly bool, init map[string]string) *mockProvider {
	cp := make(map[string]string)
	for k, v := range init {
		cp[k] = v
	}
	return &mockProvider{name: name, readOnly: readOnly, store: cp}
}

func (m *mockProvider) Name() string { return m.name }
func (m *mockProvider) Get(_ context.Context, key string) (string, error) {
	if val, ok := m.store[key]; ok {
		return val, nil
	}
	return "", ErrSecretNotFound
}
func (m *mockProvider) Set(_ context.Context, key, value string) error {
	if m.readOnly {
		return ErrReadOnlyVault
	}
	m.store[key] = value
	return nil
}
func (m *mockProvider) Delete(_ context.Context, key string) error {
	if m.readOnly {
		return ErrReadOnlyVault
	}
	if _, ok := m.store[key]; !ok {
		return ErrSecretNotFound
	}
	delete(m.store, key)
	return nil
}
func (m *mockProvider) List(_ context.Context) ([]SecretMetadata, error) {
	var res []SecretMetadata
	for k := range m.store {
		res = append(res, SecretMetadata{Key: k, Provider: m.name})
	}
	return res, nil
}

type errListProvider struct {
	mockProvider
}

func (e *errListProvider) List(_ context.Context) ([]SecretMetadata, error) {
	return nil, errors.New("provider list failure")
}

func TestChainedProvider_GetFallback(t *testing.T) {
	ctx := context.Background()
	p1 := newMock("keychain", false, map[string]string{"K1": "v1-keychain"})
	p2 := newMock("vault", true, map[string]string{"K1": "v1-vault", "K2": "v2-vault"})
	p3 := newMock("aws", true, map[string]string{"K3": "v3-aws"})

	chain := NewChainedProvider(p1, p2, p3)
	assert.Equal(t, "chained", chain.Name())

	// K1 should resolve from p1 (first hit)
	val, err := chain.Get(ctx, "K1")
	require.NoError(t, err)
	assert.Equal(t, "v1-keychain", val)

	// K2 should resolve from p2
	val, err = chain.Get(ctx, "K2")
	require.NoError(t, err)
	assert.Equal(t, "v2-vault", val)

	// K3 should resolve from p3
	val, err = chain.Get(ctx, "K3")
	require.NoError(t, err)
	assert.Equal(t, "v3-aws", val)

	// Missing key
	_, err = chain.Get(ctx, "MISSING")
	assert.True(t, errors.Is(err, ErrSecretNotFound))
}

func TestChainedProvider_SetAndListDeduplication(t *testing.T) {
	ctx := context.Background()
	p1 := newMock("read-only-vault", true, map[string]string{"K1": "val1"})
	p2 := newMock("local-keychain", false, map[string]string{"K2": "val2"})

	chain := NewChainedProvider(p1, p2)

	// Set should bypass read-only p1 and write to p2
	err := chain.Set(ctx, "NEW_KEY", "new-secret")
	require.NoError(t, err)
	val, err := p2.Get(ctx, "NEW_KEY")
	require.NoError(t, err)
	assert.Equal(t, "new-secret", val)

	// List should return deduplicated metadata
	list, err := chain.List(ctx)
	require.NoError(t, err)
	assert.Len(t, list, 3) // K1, K2, NEW_KEY
}

func TestChainedProvider_Delete(t *testing.T) {
	ctx := context.Background()
	p1 := newMock("p1", false, map[string]string{"K1": "val1"})
	p2 := newMock("p2", false, map[string]string{"K2": "val2"})

	chain := NewChainedProvider(p1, p2)

	// Delete existing key
	err := chain.Delete(ctx, "K1")
	require.NoError(t, err)
	_, err = chain.Get(ctx, "K1")
	assert.True(t, errors.Is(err, ErrSecretNotFound))

	// Delete non-existent key
	err = chain.Delete(ctx, "NON_EXISTENT")
	assert.True(t, errors.Is(err, ErrSecretNotFound))
}

func TestChainedProvider_Set_AllReadOnly(t *testing.T) {
	ctx := context.Background()
	p1 := newMock("p1", true, map[string]string{"K1": "val1"})
	p2 := newMock("p2", true, map[string]string{"K2": "val2"})

	chain := NewChainedProvider(p1, p2)

	err := chain.Set(ctx, "KEY", "val")
	assert.True(t, errors.Is(err, ErrReadOnlyVault))
}

func TestChainedProvider_EmptyChain(t *testing.T) {
	ctx := context.Background()
	chain := NewChainedProvider()

	assert.Equal(t, "chained", chain.Name())

	_, err := chain.Get(ctx, "KEY")
	assert.True(t, errors.Is(err, ErrSecretNotFound))

	err = chain.Set(ctx, "KEY", "val")
	assert.True(t, errors.Is(err, ErrReadOnlyVault))

	err = chain.Delete(ctx, "KEY")
	assert.True(t, errors.Is(err, ErrSecretNotFound))

	list, err := chain.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, list)
}

func TestChainedProvider_List_SkipsFailedProviders(t *testing.T) {
	ctx := context.Background()
	p1 := &errListProvider{mockProvider: *newMock("failing", false, map[string]string{"K1": "val1"})}
	p2 := newMock("p2", false, map[string]string{"K2": "val2"})

	chain := NewChainedProvider(p1, p2)

	list, err := chain.List(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "K2", list[0].Key)
}

type errGetProvider struct {
	mockProvider
	getErr error
}

func (e *errGetProvider) Get(_ context.Context, _ string) (string, error) {
	return "", e.getErr
}

func TestChainedProvider_PropagatesBackendErrors(t *testing.T) {
	ctx := context.Background()
	backendErr := errors.New("vault HTTP 403 forbidden")
	p1 := newMock("local", false, map[string]string{})
	p2 := &errGetProvider{
		mockProvider: *newMock("remote", true, map[string]string{}),
		getErr:       backendErr,
	}

	chain := NewChainedProvider(p1, p2)
	_, err := chain.Get(ctx, "MISSING_KEY")
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrSecretNotFound), "backend error must not be masked by ErrSecretNotFound")
	assert.ErrorIs(t, err, backendErr)
}

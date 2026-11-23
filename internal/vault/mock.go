package vault

import (
	"context"
	"sync"
)

// MockProvider is an in-memory implementation of Provider for testing.
type MockProvider struct {
	mu       sync.RWMutex
	name     string
	readOnly bool
	store    map[string]string
}

// NewMockProvider creates a new in-memory MockProvider initialized with the given key-value map.
func NewMockProvider(init map[string]string) *MockProvider {
	store := make(map[string]string, len(init))
	for k, v := range init {
		store[k] = v
	}
	return &MockProvider{
		name:  "mock",
		store: store,
	}
}

// WithName sets the name of the mock provider.
func (m *MockProvider) WithName(name string) *MockProvider {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.name = name
	return m
}

// WithReadOnly sets whether the mock provider is read-only.
func (m *MockProvider) WithReadOnly(ro bool) *MockProvider {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.readOnly = ro
	return m
}

func (m *MockProvider) Name() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.name == "" {
		return "mock"
	}
	return m.name
}

func (m *MockProvider) Get(_ context.Context, key string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if val, ok := m.store[key]; ok {
		return val, nil
	}
	return "", ErrSecretNotFound
}

func (m *MockProvider) Set(_ context.Context, key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.readOnly {
		return ErrReadOnlyVault
	}
	m.store[key] = value
	return nil
}

func (m *MockProvider) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.readOnly {
		return ErrReadOnlyVault
	}
	if _, ok := m.store[key]; !ok {
		return ErrSecretNotFound
	}
	delete(m.store, key)
	return nil
}

func (m *MockProvider) List(_ context.Context) ([]SecretMetadata, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	name := m.name
	if name == "" {
		name = "mock"
	}
	res := make([]SecretMetadata, 0, len(m.store))
	for k := range m.store {
		res = append(res, SecretMetadata{Key: k, Provider: name})
	}
	return res, nil
}

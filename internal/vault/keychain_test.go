package vault

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKeychainProvider_Name(t *testing.T) {
	p := NewKeychainProvider("com.github.actr.test")
	assert.Equal(t, "keychain", p.Name())
}

func TestKeychainProvider_ContextCancellation(t *testing.T) {
	p := NewKeychainProvider("com.github.actr.test.canceled")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := p.Get(ctx, "ANY_KEY")
	assert.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled))

	err = p.Set(ctx, "ANY_KEY", "val")
	assert.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled))

	err = p.Delete(ctx, "ANY_KEY")
	assert.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled))

	_, err = p.List(ctx)
	assert.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled))
}

func TestKeychainProvider_DarwinOperations(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("skipping macOS keychain integration test on non-darwin")
	}
	// Verify /usr/bin/security exists
	if _, err := exec.LookPath("security"); err != nil {
		t.Skip("security binary not found")
	}

	ctx := context.Background()
	p := NewKeychainProvider("com.github.actr.test.unit")

	testKey := "ACTR_TEST_SECRET_KEY"
	testVal := "super-secure-token-12345"

	// Cleanup any previous run
	_ = p.Delete(ctx, testKey)

	// Set
	err := p.Set(ctx, testKey, testVal)
	require.NoError(t, err)

	// Get
	gotVal, err := p.Get(ctx, testKey)
	require.NoError(t, err)
	assert.Equal(t, testVal, gotVal)

	// List
	list, err := p.List(ctx)
	require.NoError(t, err)
	var found bool
	for _, m := range list {
		if m.Key == testKey {
			found = true
			assert.Equal(t, "keychain", m.Provider)
			break
		}
	}
	assert.True(t, found, "expected testKey to appear in List")

	// Delete
	err = p.Delete(ctx, testKey)
	require.NoError(t, err)

	// Get after delete should return ErrSecretNotFound
	_, err = p.Get(ctx, testKey)
	assert.True(t, errors.Is(err, ErrSecretNotFound))

	// Delete non-existent should return ErrSecretNotFound
	err = p.Delete(ctx, testKey)
	assert.True(t, errors.Is(err, ErrSecretNotFound))
}

//go:build windows

package vault

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWindowsDPAPIProvider_ReturnsExplicitError(t *testing.T) {
	ctx := context.Background()
	p := NewKeychainProvider("com.github.actr.test")

	_, err := p.Get(ctx, "KEY")
	require.Error(t, err)
	assert.ErrorIs(t, err, errWindowsKeychainUnsupported)

	err = p.Set(ctx, "KEY", "VAL")
	require.Error(t, err)
	assert.ErrorIs(t, err, errWindowsKeychainUnsupported)

	err = p.Delete(ctx, "KEY")
	require.Error(t, err)
	assert.ErrorIs(t, err, errWindowsKeychainUnsupported)

	_, err = p.List(ctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, errWindowsKeychainUnsupported)
}

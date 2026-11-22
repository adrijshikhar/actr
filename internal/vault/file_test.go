package vault

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileVaultProvider_Lifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	vaultPath := filepath.Join(tmpDir, "vault.enc")
	passphrase := []byte("runner-machine-passphrase-42")

	ctx := context.Background()
	provider, err := NewFileProvider(vaultPath, passphrase)
	require.NoError(t, err)
	assert.Equal(t, "encrypted-file", provider.Name())

	// Initially empty
	_, err = provider.Get(ctx, "TOKEN")
	assert.True(t, errors.Is(err, ErrSecretNotFound))

	// Set secret
	err = provider.Set(ctx, "TOKEN", "ghp_1234567890abcdef")
	require.NoError(t, err)

	// Check file permissions are 0600
	info, err := os.Stat(vaultPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	// Read secret back
	val, err := provider.Get(ctx, "TOKEN")
	require.NoError(t, err)
	assert.Equal(t, "ghp_1234567890abcdef", val)

	// List
	list, err := provider.List(ctx)
	require.NoError(t, err)
	assert.Len(t, list, 1)
	assert.Equal(t, "TOKEN", list[0].Key)
	assert.Equal(t, "encrypted-file", list[0].Provider)

	// Delete secret
	err = provider.Delete(ctx, "TOKEN")
	require.NoError(t, err)
	_, err = provider.Get(ctx, "TOKEN")
	assert.True(t, errors.Is(err, ErrSecretNotFound))

	// Delete non-existent secret returns ErrSecretNotFound
	err = provider.Delete(ctx, "TOKEN")
	assert.True(t, errors.Is(err, ErrSecretNotFound))
}

func TestFileVaultProvider_TamperDetection(t *testing.T) {
	tmpDir := t.TempDir()
	vaultPath := filepath.Join(tmpDir, "vault.enc")
	passphrase := []byte("secret")

	ctx := context.Background()
	p, err := NewFileProvider(vaultPath, passphrase)
	require.NoError(t, err)
	err = p.Set(ctx, "K", "V")
	require.NoError(t, err)

	// Tamper with ciphertext bytes
	data, err := os.ReadFile(vaultPath)
	require.NoError(t, err)
	data[len(data)-1] ^= 0xFF
	err = os.WriteFile(vaultPath, data, 0o600)
	require.NoError(t, err)

	// Reopen should fail GCM authentication
	_, err = NewFileProvider(vaultPath, passphrase)
	assert.Error(t, err, "expected GCM authentication failure on tampered file")
}

func TestFileVaultProvider_WrongPassphrase(t *testing.T) {
	tmpDir := t.TempDir()
	vaultPath := filepath.Join(tmpDir, "vault.enc")

	ctx := context.Background()
	p1, err := NewFileProvider(vaultPath, []byte("correct-passphrase"))
	require.NoError(t, err)
	err = p1.Set(ctx, "API_KEY", "super-secret-value")
	require.NoError(t, err)

	// Open with wrong passphrase
	_, err = NewFileProvider(vaultPath, []byte("wrong-passphrase"))
	assert.Error(t, err, "expected decryption failure with wrong passphrase")
}

func TestFileVaultProvider_CorruptedHeader(t *testing.T) {
	tmpDir := t.TempDir()
	vaultPath := filepath.Join(tmpDir, "vault.enc")

	// Write file with fewer bytes than salt + nonce
	err := os.WriteFile(vaultPath, []byte("too-short"), 0o600)
	require.NoError(t, err)

	_, err = NewFileProvider(vaultPath, []byte("passphrase"))
	assert.Error(t, err, "expected failure for truncated vault file")
}

func TestFileVaultProvider_Persistence(t *testing.T) {
	tmpDir := t.TempDir()
	vaultPath := filepath.Join(tmpDir, "vault.enc")
	passphrase := []byte("persistence-passphrase")

	ctx := context.Background()
	p1, err := NewFileProvider(vaultPath, passphrase)
	require.NoError(t, err)

	require.NoError(t, p1.Set(ctx, "A", "val-a"))
	require.NoError(t, p1.Set(ctx, "B", "val-b"))
	require.NoError(t, p1.Set(ctx, "C", "val-c"))

	// Create new provider instance pointing to same file
	p2, err := NewFileProvider(vaultPath, passphrase)
	require.NoError(t, err)

	valA, err := p2.Get(ctx, "A")
	require.NoError(t, err)
	assert.Equal(t, "val-a", valA)

	valB, err := p2.Get(ctx, "B")
	require.NoError(t, err)
	assert.Equal(t, "val-b", valB)

	valC, err := p2.Get(ctx, "C")
	require.NoError(t, err)
	assert.Equal(t, "val-c", valC)

	list, err := p2.List(ctx)
	require.NoError(t, err)
	assert.Len(t, list, 3)
}

func TestFileVaultProvider_DirectoryCreation(t *testing.T) {
	tmpDir := t.TempDir()
	vaultPath := filepath.Join(tmpDir, "nested", "sub", "vault.enc")
	passphrase := []byte("dir-creation-passphrase")

	ctx := context.Background()
	p, err := NewFileProvider(vaultPath, passphrase)
	require.NoError(t, err)

	err = p.Set(ctx, "KEY", "VALUE")
	require.NoError(t, err)

	// Verify directory exists
	dirInfo, err := os.Stat(filepath.Dir(vaultPath))
	require.NoError(t, err)
	assert.True(t, dirInfo.IsDir())

	// Verify secret readable
	val, err := p.Get(ctx, "KEY")
	require.NoError(t, err)
	assert.Equal(t, "VALUE", val)
}

func TestFileVaultProvider_ContextCancellation(t *testing.T) {
	tmpDir := t.TempDir()
	vaultPath := filepath.Join(tmpDir, "vault.enc")
	passphrase := []byte("ctx-passphrase")

	p, err := NewFileProvider(vaultPath, passphrase)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err = p.Get(ctx, "K")
	assert.ErrorIs(t, err, context.Canceled)

	err = p.Set(ctx, "K", "V")
	assert.ErrorIs(t, err, context.Canceled)

	err = p.Delete(ctx, "K")
	assert.ErrorIs(t, err, context.Canceled)

	_, err = p.List(ctx)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestFileVaultProvider_ConcurrentAccess(t *testing.T) {
	tmpDir := t.TempDir()
	vaultPath := filepath.Join(tmpDir, "vault.enc")
	passphrase := []byte("concurrent-passphrase")

	ctx := context.Background()
	p, err := NewFileProvider(vaultPath, passphrase)
	require.NoError(t, err)

	// Pre-populate
	for i := 0; i < 5; i++ {
		require.NoError(t, p.Set(ctx, fmt.Sprintf("init-%d", i), "val"))
	}

	var wg sync.WaitGroup
	workers := 10
	iterations := 20

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				key := fmt.Sprintf("key-%d-%d", workerID, i)
				val := fmt.Sprintf("val-%d-%d", workerID, i)

				// Set
				_ = p.Set(ctx, key, val)

				// Get
				_, _ = p.Get(ctx, key)

				// List
				_, _ = p.List(ctx)

				// Delete
				_ = p.Delete(ctx, key)
			}
		}(w)
	}

	wg.Wait()

	// Initial keys should still be readable
	for i := 0; i < 5; i++ {
		v, err := p.Get(ctx, fmt.Sprintf("init-%d", i))
		require.NoError(t, err)
		assert.Equal(t, "val", v)
	}
}

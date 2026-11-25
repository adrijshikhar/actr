package vault

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHashiCorpVaultProvider_GetAndList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "test-token", r.Header.Get("X-Vault-Token"))
		if r.URL.Path == "/v1/secret/data/ci" && r.Method == http.MethodGet {
			resp := map[string]interface{}{
				"data": map[string]interface{}{
					"data": map[string]interface{}{
						"PROD_API_KEY": "hashi-secret-42",
						"DATABASE_URL": "postgres://localhost:5432/db",
					},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	ctx := context.Background()
	provider := NewHashiCorpVaultProvider(server.URL, "test-token", "secret", "ci")
	assert.Equal(t, "hashicorp-vault", provider.Name())

	val, err := provider.Get(ctx, "PROD_API_KEY")
	require.NoError(t, err)
	assert.Equal(t, "hashi-secret-42", val)

	_, err = provider.Get(ctx, "NON_EXISTENT")
	assert.True(t, errors.Is(err, ErrSecretNotFound))

	list, err := provider.List(ctx)
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, "DATABASE_URL", list[0].Key)
	assert.Equal(t, "hashicorp-vault", list[0].Provider)
	assert.Equal(t, "PROD_API_KEY", list[1].Key)
	assert.Equal(t, "hashicorp-vault", list[1].Provider)

	assert.True(t, errors.Is(provider.Set(ctx, "FOO", "BAR"), ErrReadOnlyVault))
	assert.True(t, errors.Is(provider.Delete(ctx, "FOO"), ErrReadOnlyVault))
}

func TestHashiCorpVaultProvider_ContextCancellation(t *testing.T) {
	provider := NewHashiCorpVaultProvider("http://localhost:8200", "test-token", "secret", "ci")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := provider.Get(ctx, "KEY")
	assert.ErrorIs(t, err, context.Canceled)

	err = provider.Set(ctx, "KEY", "VAL")
	assert.ErrorIs(t, err, context.Canceled)

	err = provider.Delete(ctx, "KEY")
	assert.ErrorIs(t, err, context.Canceled)

	_, err = provider.List(ctx)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestHashiCorpVaultProvider_Errors(t *testing.T) {
	// Server returning 404
	server404 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer server404.Close()

	ctx := context.Background()
	p404 := NewHashiCorpVaultProvider(server404.URL, "test-token", "secret", "missing")
	_, err := p404.Get(ctx, "FOO")
	assert.ErrorIs(t, err, ErrSecretNotFound)
	list, err := p404.List(ctx)
	assert.NoError(t, err)
	assert.Empty(t, list)

	// Server returning 500 Internal Server Error
	server500 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server500.Close()

	p500 := NewHashiCorpVaultProvider(server500.URL, "test-token", "secret", "ci")
	_, err = p500.Get(ctx, "FOO")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "500")

	_, err = p500.List(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "500")

	// Server returning malformed JSON
	serverBadJSON := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{not-json`))
	}))
	defer serverBadJSON.Close()

	pBadJSON := NewHashiCorpVaultProvider(serverBadJSON.URL, "test-token", "secret", "ci")
	_, err = pBadJSON.Get(ctx, "FOO")
	assert.Error(t, err)
	_, err = pBadJSON.List(ctx)
	assert.Error(t, err)
}

func TestHashiCorpVaultProvider_BlocksRedirectsAndRejectsNullOrInsecure(t *testing.T) {
	ctx := context.Background()

	// Reject non-loopback HTTP and empty address
	_, err := NewHashiCorpVaultProvider("", "tok", "secret", "ci").Get(ctx, "KEY")
	require.Error(t, err)
	_, err = NewHashiCorpVaultProvider("http://vault.example.com:8200", "tok", "secret", "ci").Get(ctx, "KEY")
	require.Error(t, err)

	// Block HTTP redirects (must not follow and must not forward token)
	var targetHit bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()

	pRedirect := NewHashiCorpVaultProvider(redirector.URL, "super-secret-token", "secret", "ci")
	_, err = pRedirect.Get(ctx, "KEY")
	require.Error(t, err)
	assert.False(t, targetHit, "redirect target must never be requested")

	// Handle null JSON secret value explicitly instead of returning "<nil>"
	nullServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"data":{"NULL_KEY":null,"NUM_KEY":12345678901234567890}}}`))
	}))
	defer nullServer.Close()

	pNull := NewHashiCorpVaultProvider(nullServer.URL, "tok", "secret", "ci")
	val, err := pNull.Get(ctx, "NULL_KEY")
	require.Error(t, err)
	assert.NotEqual(t, "<nil>", val)

	numVal, err := pNull.Get(ctx, "NUM_KEY")
	require.NoError(t, err)
	assert.Equal(t, "12345678901234567890", numVal)
}

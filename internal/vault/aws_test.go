package vault

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAWSSecretsManagerProvider_Get(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "secretsmanager.us-east-1.amazonaws.com", r.Host)
		assert.Equal(t, "application/x-amz-json-1.1", r.Header.Get("Content-Type"))
		assert.Equal(t, "secretsmanager.GetSecretValue", r.Header.Get("X-Amz-Target"))

		body, _ := io.ReadAll(r.Body)
		var req map[string]string
		_ = json.Unmarshal(body, &req)

		if req["SecretId"] == "actr/ci/DATABASE_URL" {
			resp := map[string]interface{}{
				"Name":         "actr/ci/DATABASE_URL",
				"SecretString": "postgres://user:pass@localhost:5432/db",
			}
			w.Header().Set("Content-Type", "application/x-amz-json-1.1")
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		if req["SecretId"] == "actr/ci/NOT_FOUND" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"__type":  "ResourceNotFoundException",
				"Message": "Secrets Manager can't find the specified secret.",
			})
			return
		}

		if req["SecretId"] == "actr/ci/EMPTY_SECRET" {
			resp := map[string]interface{}{
				"Name":         "actr/ci/EMPTY_SECRET",
				"SecretString": "",
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	ctx := context.Background()
	provider := newAWSProviderWithEndpoint("us-east-1", "actr/ci/", server.URL)
	assert.Equal(t, "aws-secrets-manager", provider.Name())

	val, err := provider.Get(ctx, "DATABASE_URL")
	require.NoError(t, err)
	assert.Equal(t, "postgres://user:pass@localhost:5432/db", val)

	_, err = provider.Get(ctx, "NOT_FOUND")
	assert.True(t, errors.Is(err, ErrSecretNotFound))

	_, err = provider.Get(ctx, "EMPTY_SECRET")
	assert.True(t, errors.Is(err, ErrSecretNotFound))

	list, err := provider.List(ctx)
	assert.NoError(t, err)
	assert.Nil(t, list)

	assert.True(t, errors.Is(provider.Set(ctx, "FOO", "BAR"), ErrReadOnlyVault))
	assert.True(t, errors.Is(provider.Delete(ctx, "FOO"), ErrReadOnlyVault))
}

func TestAWSSecretsManagerProvider_ContextCancellation(t *testing.T) {
	provider := newAWSProviderWithEndpoint("us-east-1", "actr/ci/", "http://localhost:4566")
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

func TestAWSSecretsManagerProvider_ConstructorAndDefaults(t *testing.T) {
	p := NewAWSSecretsManagerProvider("", "my-prefix/")
	require.NotNil(t, p)
	assert.Equal(t, "aws-secrets-manager", p.Name())

	awsP, ok := p.(*AWSSecretsManagerProvider)
	require.True(t, ok)
	assert.Equal(t, "us-east-1", awsP.region)
	assert.Equal(t, "my-prefix/", awsP.prefix)
	assert.Equal(t, "", awsP.endpoint)
}

func TestAWSSecretsManagerProvider_ServerErrorAndBadJSON(t *testing.T) {
	server500 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server500.Close()

	ctx := context.Background()
	p500 := newAWSProviderWithEndpoint("us-east-1", "prefix/", server500.URL)
	_, err := p500.Get(ctx, "KEY")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "500")

	serverBad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`bad json`))
	}))
	defer serverBad.Close()

	pBad := newAWSProviderWithEndpoint("us-east-1", "prefix/", serverBad.URL)
	_, err = pBad.Get(ctx, "KEY")
	assert.Error(t, err)
}

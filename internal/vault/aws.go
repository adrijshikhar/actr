package vault

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

var _ Provider = (*AWSSecretsManagerProvider)(nil)

// AWSSecretsManagerProvider provides read-only secret resolution from AWS Secrets Manager
// using pure Go HTTP requests with zero external cloud SDK dependencies.
type AWSSecretsManagerProvider struct {
	region   string
	prefix   string
	endpoint string
	client   *http.Client
}

// NewAWSSecretsManagerProvider creates a new AWS Secrets Manager provider for the given region and secret key prefix.
func NewAWSSecretsManagerProvider(region, prefix string) Provider {
	return newAWSProviderWithEndpoint(region, prefix, "")
}

func newAWSProviderWithEndpoint(region, prefix, customEndpoint string) *AWSSecretsManagerProvider {
	if region == "" {
		region = "us-east-1"
	}
	return &AWSSecretsManagerProvider{
		region:   region,
		prefix:   prefix,
		endpoint: customEndpoint,
		client:   &http.Client{Timeout: 10 * time.Second},
	}
}

// Name returns the provider identifier.
func (a *AWSSecretsManagerProvider) Name() string {
	return "aws-secrets-manager"
}

// Get retrieves a secret by key from AWS Secrets Manager.
func (a *AWSSecretsManagerProvider) Get(ctx context.Context, key string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	secretID := a.prefix + key
	endpoint := a.endpoint
	if endpoint == "" {
		endpoint = fmt.Sprintf("https://secretsmanager.%s.amazonaws.com", a.region)
	}

	reqBody, _ := json.Marshal(map[string]string{"SecretId": secretID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}

	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "secretsmanager.GetSecretValue")
	req.Host = fmt.Sprintf("secretsmanager.%s.amazonaws.com", a.region)

	resp, err := a.client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusBadRequest {
		return "", ErrSecretNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("aws secrets manager returned HTTP %d", resp.StatusCode)
	}

	var res struct {
		SecretString string `json:"SecretString"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", err
	}
	if res.SecretString == "" {
		return "", ErrSecretNotFound
	}
	return res.SecretString, nil
}

// Set is not supported as AWS Secrets Manager provider is read-only.
func (a *AWSSecretsManagerProvider) Set(ctx context.Context, key, value string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return ErrReadOnlyVault
}

// Delete is not supported as AWS Secrets Manager provider is read-only.
func (a *AWSSecretsManagerProvider) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return ErrReadOnlyVault
}

// List is not supported for AWS Secrets Manager in this lightweight client and returns nil, nil.
func (a *AWSSecretsManagerProvider) List(ctx context.Context) ([]SecretMetadata, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, nil
}

package vault

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

const maxAWSResponseBytes = 1 << 20

var (
	_ Provider = (*AWSSecretsManagerProvider)(nil)

	awsRegionPattern = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]+$`)
)

// AWSSecretsManagerProvider provides read-only secret resolution from AWS Secrets Manager
// using pure Go HTTP requests with SigV4 signing and zero external cloud SDK dependencies.
type AWSSecretsManagerProvider struct {
	region   string
	prefix   string
	endpoint string
	client   *http.Client
	initErr  error
}

// NewAWSSecretsManagerProvider creates a new AWS Secrets Manager provider for the given region and secret key prefix.
func NewAWSSecretsManagerProvider(region, prefix string) Provider {
	return newAWSProviderWithEndpoint(region, prefix, "")
}

func newAWSProviderWithEndpoint(region, prefix, customEndpoint string) *AWSSecretsManagerProvider {
	if region == "" {
		region = "us-east-1"
	}
	var initErr error
	if !awsRegionPattern.MatchString(region) {
		initErr = fmt.Errorf("invalid AWS region %q", region)
	}
	return &AWSSecretsManagerProvider{
		region:   region,
		prefix:   prefix,
		endpoint: customEndpoint,
		client:   &http.Client{Timeout: 10 * time.Second},
		initErr:  initErr,
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
	if a.initErr != nil {
		return "", a.initErr
	}
	if !awsRegionPattern.MatchString(a.region) {
		return "", fmt.Errorf("invalid AWS region %q", a.region)
	}

	accessKey := os.Getenv("AWS_ACCESS_KEY_ID")
	secretKey := os.Getenv("AWS_SECRET_ACCESS_KEY")
	sessionToken := os.Getenv("AWS_SESSION_TOKEN")
	if (accessKey == "" || secretKey == "") && !isLoopbackURL(a.endpoint) {
		return "", errors.New("aws credentials missing: AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY are required")
	}

	secretID := a.prefix + key
	endpoint := a.endpoint
	if endpoint == "" {
		endpoint = fmt.Sprintf("https://secretsmanager.%s.amazonaws.com", a.region)
	}

	reqBody, err := json.Marshal(map[string]string{"SecretId": secretID})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}

	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "secretsmanager.GetSecretValue")
	req.Host = fmt.Sprintf("secretsmanager.%s.amazonaws.com", a.region)

	if accessKey != "" && secretKey != "" {
		signSigV4(req, reqBody, accessKey, secretKey, sessionToken, a.region, "secretsmanager", time.Now().UTC())
	}

	resp, err := a.client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return "", ErrSecretNotFound
	}
	if resp.StatusCode == http.StatusBadRequest {
		var errResp struct {
			Type    string `json:"__type"`
			Message string `json:"message"`
			CapMsg  string `json:"Message"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, maxAWSResponseBytes)).Decode(&errResp); err != nil {
			return "", fmt.Errorf("aws secrets manager returned HTTP %d", resp.StatusCode)
		}
		if errResp.Type == "ResourceNotFoundException" || strings.HasSuffix(errResp.Type, "#ResourceNotFoundException") {
			return "", ErrSecretNotFound
		}
		msg := errResp.Message
		if msg == "" {
			msg = errResp.CapMsg
		}
		if msg != "" {
			return "", fmt.Errorf("aws secrets manager error (%s): %s", errResp.Type, msg)
		}
		return "", fmt.Errorf("aws secrets manager error (%s)", errResp.Type)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("aws secrets manager returned HTTP %d", resp.StatusCode)
	}

	var res struct {
		SecretString string `json:"SecretString"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxAWSResponseBytes)).Decode(&res); err != nil {
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

// List is not supported for AWS Secrets Manager in this lightweight client.
func (a *AWSSecretsManagerProvider) List(ctx context.Context) ([]SecretMetadata, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.initErr != nil {
		return nil, a.initErr
	}
	return nil, fmt.Errorf("aws secrets manager does not support listing secrets: %w", ErrReadOnlyVault)
}

func signSigV4(req *http.Request, body []byte, accessKey, secretKey, sessionToken, region, service string, now time.Time) {
	now = now.UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	payloadSum := sha256.Sum256(body)
	payloadHash := hex.EncodeToString(payloadSum[:])

	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if sessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", sessionToken)
	}

	canonicalURI := req.URL.EscapedPath()
	if canonicalURI == "" {
		canonicalURI = "/"
	}
	canonicalQuery := canonicalQueryString(req.URL.Query())

	headerMap := make(map[string]string)
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	headerMap["host"] = strings.TrimSpace(host)
	for k, vals := range req.Header {
		lk := strings.ToLower(k)
		trimmed := make([]string, len(vals))
		for i, v := range vals {
			trimmed[i] = collapseSpaces(strings.TrimSpace(v))
		}
		headerMap[lk] = strings.Join(trimmed, ",")
	}

	headerKeys := make([]string, 0, len(headerMap))
	for k := range headerMap {
		headerKeys = append(headerKeys, k)
	}
	sort.Strings(headerKeys)

	var canonicalHeaders strings.Builder
	for _, k := range headerKeys {
		canonicalHeaders.WriteString(k)
		canonicalHeaders.WriteByte(':')
		canonicalHeaders.WriteString(headerMap[k])
		canonicalHeaders.WriteByte('\n')
	}
	signedHeaders := strings.Join(headerKeys, ";")

	canonicalRequest := strings.Join([]string{
		req.Method,
		canonicalURI,
		canonicalQuery,
		canonicalHeaders.String(),
		signedHeaders,
		payloadHash,
	}, "\n")

	crSum := sha256.Sum256([]byte(canonicalRequest))
	credentialScope := fmt.Sprintf("%s/%s/%s/aws4_request", dateStamp, region, service)
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		credentialScope,
		hex.EncodeToString(crSum[:]),
	}, "\n")

	kDate := hmacSHA256([]byte("AWS4"+secretKey), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(service))
	kSigning := hmacSHA256(kService, []byte("aws4_request"))
	signature := hex.EncodeToString(hmacSHA256(kSigning, []byte(stringToSign)))

	req.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		accessKey, credentialScope, signedHeaders, signature,
	))
}

func hmacSHA256(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(data)
	return mac.Sum(nil)
}

func collapseSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func canonicalQueryString(q url.Values) string {
	if len(q) == 0 {
		return ""
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var parts []string
	for _, k := range keys {
		vals := append([]string(nil), q[k]...)
		sort.Strings(vals)
		ek := url.QueryEscape(k)
		for _, v := range vals {
			parts = append(parts, ek+"="+url.QueryEscape(v))
		}
	}
	return strings.Join(parts, "&")
}

func isLoopbackURL(rawURL string) bool {
	if rawURL == "" {
		return false
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

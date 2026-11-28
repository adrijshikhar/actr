package protocol

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	maxDrainBytes            = 4096
	defaultConflictRetryBase = 2 * time.Second
	defaultConflictRetryCap  = 10 * time.Second
	defaultConflictMaxTries  = 9
	sleepWallGapThresholdSec = 120
)

// HTTPStatusError represents a non-2xx HTTP response returned by a backend service.
type HTTPStatusError struct {
	Action     string
	StatusCode int
	Body       string
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("%s returned status %d: %s", e.Action, e.StatusCode, e.Body)
}

// IsSessionInvalidError reports whether err indicates the broker session is missing or expired.
func IsSessionInvalidError(err error) bool {
	if err == nil {
		return false
	}
	var httpErr *HTTPStatusError
	if errors.As(err, &httpErr) {
		if httpErr.StatusCode == http.StatusNotFound || httpErr.StatusCode == http.StatusGone {
			return true
		}
	}
	msg := err.Error()
	return strings.Contains(msg, "RunnerSessionInvalid") ||
		strings.Contains(msg, "Runner session is invalid") ||
		strings.Contains(msg, "no active session")
}

// IsJobCancelledOrGoneError reports whether err from RenewJob indicates the job is permanently
// gone or cancelled on the GitHub Actions server (e.g. timed out while asleep).
func IsJobCancelledOrGoneError(err error) bool {
	if err == nil {
		return false
	}
	var httpErr *HTTPStatusError
	if errors.As(err, &httpErr) {
		switch httpErr.StatusCode {
		case http.StatusNotFound, http.StatusGone, http.StatusConflict:
			return true
		}
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "cancelled") ||
		strings.Contains(lower, "canceled") ||
		strings.Contains(lower, "expired")
}

// IsUnauthorizedError reports whether err represents an HTTP 401 Unauthorized response.
func IsUnauthorizedError(err error) bool {
	if err == nil {
		return false
	}
	var httpErr *HTTPStatusError
	return errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusUnauthorized
}

func newDefaultHTTPClient(totalTimeout time.Duration) *http.Client {
	if totalTimeout <= 0 {
		totalTimeout = 30 * time.Second
	}
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: totalTimeout,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	}
	return &http.Client{
		Timeout:       totalTimeout,
		Transport:     transport,
		CheckRedirect: checkSafeRedirect,
	}
}

func checkSafeRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	if _, err := validateRemoteURL(req.URL.String()); err != nil {
		return fmt.Errorf("unsafe redirect target: %w", err)
	}
	if len(via) > 0 {
		prev := via[len(via)-1]
		if strings.EqualFold(prev.URL.Scheme, "https") && !strings.EqualFold(req.URL.Scheme, "https") {
			return fmt.Errorf("refusing HTTPS to %s redirect", req.URL.Scheme)
		}
		if !strings.EqualFold(prev.URL.Hostname(), req.URL.Hostname()) {
			return fmt.Errorf("refusing cross-host redirect from %s to %s", prev.URL.Hostname(), req.URL.Hostname())
		}
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validateRemoteURL(raw string) (*url.URL, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, errors.New("empty URL")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("parse URL %q: %w", raw, err)
	}
	host := parsed.Hostname()
	if parsed.Host == "" || host == "" {
		return nil, fmt.Errorf("URL missing host: %q", raw)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		return parsed, nil
	case "http":
		if !isLoopbackHost(host) {
			return nil, fmt.Errorf("insecure http URL not permitted for non-loopback host %q", host)
		}
		return parsed, nil
	default:
		return nil, fmt.Errorf("unsupported URL scheme %q: must be https", parsed.Scheme)
	}
}

func drainAndClose(body io.ReadCloser) {
	if body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(body, maxDrainBytes))
	_ = body.Close()
}

// JobRequestReference holds metadata describing a dispatched job message.
type JobRequestReference struct {
	RunnerRequestID string `json:"runner_request_id"`
	RunServiceURL   string `json:"run_service_url"`
	BillingOwnerID  string `json:"billing_owner_id"`
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// UnmarshalJSON supports snake_case, camelCase, and PascalCase variants of JobRequestReference fields.
func (r *JobRequestReference) UnmarshalJSON(data []byte) error {
	var raw struct {
		RunnerRequestID1 string `json:"runner_request_id"`
		RunnerRequestID2 string `json:"runnerRequestId"`
		RunnerRequestID3 string `json:"RunnerRequestId"`
		RunnerRequestID4 string `json:"jobMessageId"`
		RunnerRequestID5 string `json:"job_message_id"`

		RunServiceURL1 string `json:"run_service_url"`
		RunServiceURL2 string `json:"runServiceUrl"`
		RunServiceURL3 string `json:"RunServiceUrl"`
		RunServiceURL4 string `json:"runServiceURL"`

		BillingOwnerID1 string `json:"billing_owner_id"`
		BillingOwnerID2 string `json:"billingOwnerId"`
		BillingOwnerID3 string `json:"BillingOwnerId"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	r.RunnerRequestID = firstNonEmpty(
		raw.RunnerRequestID1,
		raw.RunnerRequestID2,
		raw.RunnerRequestID3,
		raw.RunnerRequestID4,
		raw.RunnerRequestID5,
	)
	r.RunServiceURL = firstNonEmpty(
		raw.RunServiceURL1,
		raw.RunServiceURL2,
		raw.RunServiceURL3,
		raw.RunServiceURL4,
	)
	r.BillingOwnerID = firstNonEmpty(
		raw.BillingOwnerID1,
		raw.BillingOwnerID2,
		raw.BillingOwnerID3,
	)
	return nil
}

// BrokerMessage represents a message returned by GitHub Actions Broker.
type BrokerMessage struct {
	MessageID   int64  `json:"messageId"`
	MessageType string `json:"messageType"`
	IV          string `json:"iv,omitempty"`
	Body        string `json:"body"`
}

// UnmarshalJSON handles both numeric and string representations of messageId without losing int64 precision.
func (m *BrokerMessage) UnmarshalJSON(data []byte) error {
	type Alias BrokerMessage
	aux := struct {
		RawID json.RawMessage `json:"messageId"`
		*Alias
	}{
		Alias: (*Alias)(m),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	raw := bytes.TrimSpace(aux.RawID)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		m.MessageID = 0
		return nil
	}

	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return fmt.Errorf("invalid messageId string: %w", err)
		}
		id, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid messageId %q: %w", s, err)
		}
		m.MessageID = id
		return nil
	}

	if id, err := strconv.ParseInt(string(raw), 10, 64); err == nil {
		m.MessageID = id
		return nil
	}

	var f float64
	if err := json.Unmarshal(raw, &f); err != nil {
		return fmt.Errorf("invalid messageId %s: %w", string(raw), err)
	}
	if math.IsNaN(f) || math.IsInf(f, 0) || math.Trunc(f) != f || f < -9223372036854775808.0 || f >= 9223372036854775808.0 {
		return fmt.Errorf("invalid messageId numeric value %s", string(raw))
	}
	m.MessageID = int64(f)
	return nil
}

// BrokerClient manages sessions and long-polling message retrieval with the GitHub Actions Broker.
type BrokerClient struct {
	mu                 sync.RWMutex
	refreshMu          sync.Mutex
	sessionFileMu      sync.Mutex
	client             *http.Client
	brokerURL          string
	authURL            string
	clientID           string
	privateKey         *rsa.PrivateKey
	sessionKey         []byte
	sessionID          string
	sessionStatePath   string
	token              string
	tokenExpiresAt     time.Time
	poolID             int64
	lastPollWallUnix   atomic.Int64
	conflictRetryBase  time.Duration
	conflictMaxRetries int
}

// NewBrokerClient creates a new BrokerClient instance.
func NewBrokerClient(client *http.Client, brokerURL, authURL, clientID string, key *rsa.PrivateKey) *BrokerClient {
	if client == nil {
		client = newDefaultHTTPClient(90 * time.Second)
	}
	return &BrokerClient{
		client:             client,
		brokerURL:          strings.TrimRight(brokerURL, "/"),
		authURL:            strings.TrimRight(authURL, "/"),
		clientID:           clientID,
		privateKey:         key,
		conflictRetryBase:  defaultConflictRetryBase,
		conflictMaxRetries: defaultConflictMaxTries,
	}
}

// WithSessionStatePath configures a file path used to persist the active broker session ID
// so stale sessions left behind by abrupt daemon termination can be deleted on restart.
func (b *BrokerClient) WithSessionStatePath(path string) *BrokerClient {
	b.sessionFileMu.Lock()
	b.sessionStatePath = path
	b.sessionFileMu.Unlock()
	return b
}

func (b *BrokerClient) persistSessionID(sessionID string) {
	b.sessionFileMu.Lock()
	defer b.sessionFileMu.Unlock()
	if b.sessionStatePath == "" || sessionID == "" {
		return
	}
	if dir := filepath.Dir(b.sessionStatePath); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0700)
	}
	_ = os.WriteFile(b.sessionStatePath, []byte(sessionID), 0600)
}

func (b *BrokerClient) loadPersistedSessionID() string {
	b.sessionFileMu.Lock()
	defer b.sessionFileMu.Unlock()
	if b.sessionStatePath == "" {
		return ""
	}
	data, err := os.ReadFile(b.sessionStatePath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func (b *BrokerClient) clearPersistedSessionID() {
	b.sessionFileMu.Lock()
	defer b.sessionFileMu.Unlock()
	if b.sessionStatePath == "" {
		return
	}
	_ = os.Remove(b.sessionStatePath)
}

func (b *BrokerClient) closeIdleConnectionsOnSleepOrErr(err error, ctx context.Context) {
	if b.client != nil && err != nil && (ctx == nil || ctx.Err() == nil) {
		b.client.CloseIdleConnections()
	}
}

func (b *BrokerClient) checkSleepWallGap() {
	nowUnix := time.Now().Unix()
	prev := b.lastPollWallUnix.Swap(nowUnix)
	if b.client != nil && prev > 0 && nowUnix-prev > sleepWallGapThresholdSec {
		b.client.CloseIdleConnections()
	}
}

// SessionID returns the active broker session ID, or empty string if no session is active.
func (b *BrokerClient) SessionID() string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.sessionID
}

// SessionKey returns a defensive copy of the decrypted AES-256 session key, or nil if no session is active.
func (b *BrokerClient) SessionKey() []byte {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return append([]byte(nil), b.sessionKey...)
}

// Token returns the active OAuth access token, or empty string if none.
func (b *BrokerClient) Token() string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.token
}

// CreateSession acquires an OAuth token and establishes a new runner session with the broker.
func (b *BrokerClient) CreateSession(ctx context.Context, agentName string, agentID int64, poolID int64) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if b.brokerURL == "" {
		return errors.New("empty broker URL")
	}
	if _, err := validateRemoteURL(b.brokerURL); err != nil {
		return fmt.Errorf("invalid broker URL: %w", err)
	}
	if agentName == "" {
		return errors.New("empty agent name")
	}

	// 1. Acquire OAuth bearer token from authURL if configured
	if b.authURL != "" {
		token, expiresAt, err := acquireOAuthTokenWithExpiry(ctx, b.client, b.authURL, b.clientID, b.privateKey)
		if err != nil {
			return fmt.Errorf("acquire oauth token: %w", err)
		}
		b.mu.Lock()
		b.token = token
		b.tokenExpiresAt = expiresAt
		b.mu.Unlock()
	}

	// 2. Establish session: POST {brokerURL}/session
	reqBody := struct {
		OwnerName         string `json:"ownerName"`
		UseFipsEncryption bool   `json:"useFipsEncryption"`
		Agent             struct {
			ID            int64  `json:"id"`
			Name          string `json:"name"`
			Version       string `json:"version"`
			OSDescription string `json:"osDescription"`
		} `json:"agent"`
	}{
		OwnerName:         "RUNNER",
		UseFipsEncryption: false,
		Agent: struct {
			ID            int64  `json:"id"`
			Name          string `json:"name"`
			Version       string `json:"version"`
			OSDescription string `json:"osDescription"`
		}{
			ID:            agentID,
			Name:          agentName,
			Version:       "3.0.0",
			OSDescription: "actr",
		},
	}

	jsonBytes, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshal session request: %w", err)
	}

	reqURL := b.brokerURL + "/session"
	var statusCode int
	var respBody []byte

	doAttempt := func() (int, []byte, string, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(jsonBytes))
		if err != nil {
			return 0, nil, "", fmt.Errorf("create session request: %w", err)
		}
		usedToken := b.Token()
		if usedToken != "" {
			req.Header.Set("Authorization", "Bearer "+usedToken)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")

		resp, err := b.client.Do(req) //nolint:bodyclose // drainAndClose drains and closes resp.Body
		if err != nil {
			b.closeIdleConnectionsOnSleepOrErr(err, ctx)
			return 0, nil, usedToken, fmt.Errorf("execute session request: %w", err)
		}
		defer drainAndClose(resp.Body)

		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return resp.StatusCode, nil, usedToken, fmt.Errorf("read session response: %w", err)
		}
		return resp.StatusCode, body, usedToken, nil
	}

	b.mu.RLock()
	retryBase := b.conflictRetryBase
	maxRetries := b.conflictMaxRetries
	b.mu.RUnlock()
	if retryBase <= 0 {
		retryBase = defaultConflictRetryBase
	}
	if maxRetries <= 0 {
		maxRetries = defaultConflictMaxTries
	}

	attemptedStaleDelete := false
	refreshedAuth := false
	conflictRetries := 0

	for {
		var usedToken string
		statusCode, respBody, usedToken, err = doAttempt()
		if err != nil {
			return err
		}

		if statusCode == http.StatusUnauthorized && !refreshedAuth && b.authURL != "" && b.privateKey != nil {
			refreshedAuth = true
			if refreshErr := b.refreshIfNeeded(ctx, usedToken); refreshErr != nil {
				return fmt.Errorf("create session returned status %d (refresh token failed: %w)", statusCode, refreshErr)
			}
			continue
		}

		if statusCode == http.StatusConflict {
			if !attemptedStaleDelete {
				attemptedStaleDelete = true
				if staleID := b.loadPersistedSessionID(); staleID != "" {
					b.clearPersistedSessionID()
					if delErr := b.deleteSessionByID(ctx, staleID); delErr == nil {
						continue
					}
				}
			}

			if conflictRetries < maxRetries {
				delay := retryBase
				for i := 0; i < conflictRetries && delay < defaultConflictRetryCap; i++ {
					if delay > defaultConflictRetryCap/2 {
						delay = defaultConflictRetryCap
						break
					}
					delay *= 2
				}
				if delay > defaultConflictRetryCap {
					delay = defaultConflictRetryCap
				}
				conflictRetries++
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				case <-timer.C:
				}
				continue
			}
		}
		break
	}

	if statusCode != http.StatusOK && statusCode != http.StatusCreated {
		return &HTTPStatusError{
			Action:     "create session",
			StatusCode: statusCode,
			Body:       string(respBody),
		}
	}

	var sessionResp struct {
		SessionID     string `json:"sessionId"`
		EncryptionKey *struct {
			Value string `json:"value"`
		} `json:"encryptionKey"`
	}
	if err := json.Unmarshal(respBody, &sessionResp); err != nil {
		return fmt.Errorf("unmarshal session response: %w", err)
	}

	if sessionResp.SessionID == "" {
		return errors.New("missing sessionId in broker response")
	}

	var decryptedAESKey []byte
	if sessionResp.EncryptionKey != nil && sessionResp.EncryptionKey.Value != "" {
		encKeyBytes, err := decodeBase64(sessionResp.EncryptionKey.Value)
		if err != nil {
			return fmt.Errorf("decode encryption key base64: %w", err)
		}

		decryptedAESKey, err = DecryptAESKey(b.privateKey, encKeyBytes)
		if err != nil {
			return fmt.Errorf("decrypt session key: %w", err)
		}
	}

	b.mu.Lock()
	if decryptedAESKey != nil {
		b.sessionKey = decryptedAESKey
	}
	b.sessionID = sessionResp.SessionID
	b.poolID = poolID
	b.mu.Unlock()
	b.persistSessionID(sessionResp.SessionID)
	return nil
}

// RefreshToken acquires a fresh OAuth access token from authURL if configured.
func (b *BrokerClient) RefreshToken(ctx context.Context) error {
	return b.refreshIfNeeded(ctx, b.Token())
}

func (b *BrokerClient) refreshIfNeeded(ctx context.Context, failedToken string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if b.authURL == "" || b.privateKey == nil {
		return errors.New("authURL or privateKey not configured for token refresh")
	}

	b.refreshMu.Lock()
	defer b.refreshMu.Unlock()

	b.mu.RLock()
	cur := b.token
	b.mu.RUnlock()
	if cur != "" && cur != failedToken {
		return nil
	}

	token, expiresAt, err := acquireOAuthTokenWithExpiry(ctx, b.client, b.authURL, b.clientID, b.privateKey)
	if err != nil {
		return fmt.Errorf("refresh oauth token: %w", err)
	}

	b.mu.Lock()
	b.token = token
	b.tokenExpiresAt = expiresAt
	b.mu.Unlock()
	return nil
}

func (b *BrokerClient) maybeRefreshExpiredToken(ctx context.Context) error {
	if b.authURL == "" || b.privateKey == nil {
		return nil
	}
	b.mu.RLock()
	tok := b.token
	expiresAt := b.tokenExpiresAt
	b.mu.RUnlock()
	if !expiresAt.IsZero() && time.Now().After(expiresAt) {
		return b.refreshIfNeeded(ctx, tok)
	}
	return nil
}

func (b *BrokerClient) fetchMessage(ctx context.Context, reqURL string) (*BrokerMessage, int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, 0, "", fmt.Errorf("create message request: %w", err)
	}
	usedToken := b.Token()
	if usedToken != "" {
		req.Header.Set("Authorization", "Bearer "+usedToken)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := b.client.Do(req) //nolint:bodyclose // drainAndClose drains and closes resp.Body
	if err != nil {
		b.closeIdleConnectionsOnSleepOrErr(err, ctx)
		return nil, 0, usedToken, fmt.Errorf("execute message request: %w", err)
	}
	defer drainAndClose(resp.Body)

	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusAccepted {
		return nil, resp.StatusCode, usedToken, nil
	}

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, usedToken, fmt.Errorf("read message response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, usedToken, &HTTPStatusError{
			Action:     "get message",
			StatusCode: resp.StatusCode,
			Body:       string(bodyBytes),
		}
	}

	if len(bytes.TrimSpace(bodyBytes)) == 0 {
		return nil, resp.StatusCode, usedToken, nil
	}

	var msg BrokerMessage
	if err := json.Unmarshal(bodyBytes, &msg); err != nil {
		return nil, resp.StatusCode, usedToken, fmt.Errorf("unmarshal broker message: %w", err)
	}
	if msg.MessageID == 0 && msg.MessageType == "" && msg.Body == "" {
		return nil, resp.StatusCode, usedToken, nil
	}

	if msg.IV != "" {
		sessionKey := b.SessionKey()
		if len(sessionKey) == 0 {
			return nil, resp.StatusCode, usedToken, errors.New("cannot decrypt message: session key is empty")
		}
		ivBytes, err := decodeBase64(msg.IV)
		if err != nil {
			return nil, resp.StatusCode, usedToken, fmt.Errorf("decode iv base64: %w", err)
		}
		cipherBytes, err := decodeBase64(msg.Body)
		if err != nil {
			return nil, resp.StatusCode, usedToken, fmt.Errorf("decode message body base64: %w", err)
		}
		decrypted, err := DecryptPayload(sessionKey, ivBytes, cipherBytes)
		if err != nil {
			return nil, resp.StatusCode, usedToken, fmt.Errorf("decrypt message payload: %w", err)
		}
		msg.Body = string(decrypted)
	}

	return &msg, resp.StatusCode, usedToken, nil
}

// GetMessage polls the broker for a pending message.
// Returns (nil, nil) on HTTP 204 No Content or empty responses.
// Decrypts the body with AES-CBC when IV is present.
// Automatically refreshes the OAuth token and retries once on HTTP 401 Unauthorized.
func (b *BrokerClient) GetMessage(ctx context.Context, runnerVersion, os, arch string) (*BrokerMessage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	sessionID := b.SessionID()
	if sessionID == "" {
		return nil, errors.New("no active session")
	}
	if b.brokerURL == "" {
		return nil, errors.New("empty broker URL")
	}
	if _, err := validateRemoteURL(b.brokerURL); err != nil {
		return nil, fmt.Errorf("invalid broker URL: %w", err)
	}

	reqURL, err := url.Parse(b.brokerURL + "/message")
	if err != nil {
		return nil, fmt.Errorf("parse broker url: %w", err)
	}

	if runnerVersion == "" {
		runnerVersion = "3.0.0"
	}

	q := reqURL.Query()
	q.Set("sessionId", sessionID)
	q.Set("status", "Online")
	q.Set("runnerVersion", runnerVersion)
	q.Set("os", os)
	q.Set("architecture", arch)
	q.Set("disableUpdate", "false")
	reqURL.RawQuery = q.Encode()

	// Flush stale keep-alive sockets if the host slept between polls (monotonic timers freeze on macOS sleep)
	b.checkSleepWallGap()

	if err := b.maybeRefreshExpiredToken(ctx); err != nil {
		return nil, err
	}

	msg, statusCode, usedToken, err := b.fetchMessage(ctx, reqURL.String())
	if statusCode == http.StatusUnauthorized && b.authURL != "" && b.privateKey != nil {
		if refreshErr := b.refreshIfNeeded(ctx, usedToken); refreshErr != nil {
			return nil, fmt.Errorf("%w (refresh token failed: %w)", err, refreshErr)
		}
		msg, _, _, err = b.fetchMessage(ctx, reqURL.String())
	}
	return msg, err
}

// AcknowledgeMessage confirms receipt and processing of a message.
func (b *BrokerClient) AcknowledgeMessage(ctx context.Context, runnerRequestID string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	sessionID := b.SessionID()
	if sessionID == "" {
		return errors.New("no active session")
	}
	if b.brokerURL == "" {
		return errors.New("empty broker URL")
	}
	if _, err := validateRemoteURL(b.brokerURL); err != nil {
		return fmt.Errorf("invalid broker URL: %w", err)
	}

	reqURL, err := url.Parse(b.brokerURL + "/acknowledge")
	if err != nil {
		return fmt.Errorf("parse broker url: %w", err)
	}
	q := reqURL.Query()
	q.Set("sessionId", sessionID)
	q.Set("status", "Online")
	reqURL.RawQuery = q.Encode()

	reqPayload := struct {
		RunnerRequestID string `json:"runnerRequestId"`
	}{
		RunnerRequestID: runnerRequestID,
	}
	jsonBody, err := json.Marshal(reqPayload)
	if err != nil {
		return fmt.Errorf("marshal acknowledge payload: %w", err)
	}

	doAck := func() (int, string, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL.String(), bytes.NewReader(jsonBody))
		if err != nil {
			return 0, "", fmt.Errorf("create acknowledge request: %w", err)
		}
		usedToken := b.Token()
		if usedToken != "" {
			req.Header.Set("Authorization", "Bearer "+usedToken)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")

		resp, err := b.client.Do(req) //nolint:bodyclose // drainAndClose drains and closes resp.Body
		if err != nil {
			b.closeIdleConnectionsOnSleepOrErr(err, ctx)
			return 0, usedToken, fmt.Errorf("execute acknowledge request: %w", err)
		}
		defer drainAndClose(resp.Body)

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			return resp.StatusCode, usedToken, &HTTPStatusError{
				Action:     "acknowledge message",
				StatusCode: resp.StatusCode,
				Body:       string(body),
			}
		}
		return resp.StatusCode, usedToken, nil
	}

	if err := b.maybeRefreshExpiredToken(ctx); err != nil {
		return err
	}

	statusCode, usedToken, err := doAck()
	if statusCode == http.StatusUnauthorized && b.authURL != "" && b.privateKey != nil {
		if refreshErr := b.refreshIfNeeded(ctx, usedToken); refreshErr != nil {
			return fmt.Errorf("%w (refresh token failed: %w)", err, refreshErr)
		}
		_, _, err = doAck()
	}
	return err
}

func (b *BrokerClient) deleteSessionByID(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return nil
	}
	if b.brokerURL == "" {
		return errors.New("empty broker URL")
	}
	if _, err := validateRemoteURL(b.brokerURL); err != nil {
		return fmt.Errorf("invalid broker URL: %w", err)
	}

	reqURL, err := url.Parse(b.brokerURL + "/session")
	if err != nil {
		return fmt.Errorf("parse broker url: %w", err)
	}
	q := reqURL.Query()
	q.Set("sessionId", sessionID)
	reqURL.RawQuery = q.Encode()

	doDelete := func() (int, string, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodDelete, reqURL.String(), nil)
		if err != nil {
			return 0, "", fmt.Errorf("create delete session request: %w", err)
		}
		usedToken := b.Token()
		if usedToken != "" {
			req.Header.Set("Authorization", "Bearer "+usedToken)
		}
		req.Header.Set("Accept", "application/json")

		resp, err := b.client.Do(req) //nolint:bodyclose // drainAndClose drains and closes resp.Body
		if err != nil {
			b.closeIdleConnectionsOnSleepOrErr(err, ctx)
			return 0, usedToken, fmt.Errorf("execute delete session request: %w", err)
		}
		defer drainAndClose(resp.Body)

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			return resp.StatusCode, usedToken, &HTTPStatusError{
				Action:     "delete session",
				StatusCode: resp.StatusCode,
				Body:       string(body),
			}
		}
		return resp.StatusCode, usedToken, nil
	}

	_ = b.maybeRefreshExpiredToken(ctx)

	statusCode, usedToken, err := doDelete()
	if statusCode == http.StatusUnauthorized && b.authURL != "" && b.privateKey != nil {
		if refreshErr := b.refreshIfNeeded(ctx, usedToken); refreshErr != nil {
			return fmt.Errorf("%w (refresh token failed: %w)", err, refreshErr)
		}
		_, _, err = doDelete()
	}
	return err
}

// DeleteSession terminates the active session with the broker and cleans up local session state.
func (b *BrokerClient) DeleteSession(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	sessionID := b.SessionID()
	if sessionID == "" {
		b.clearPersistedSessionID()
		return nil
	}
	if err := b.deleteSessionByID(ctx, sessionID); err != nil {
		var httpErr *HTTPStatusError
		if errors.As(err, &httpErr) && (httpErr.StatusCode == http.StatusNotFound || httpErr.StatusCode == http.StatusGone) {
			b.clearPersistedSessionID()
		}
		return err
	}

	b.mu.Lock()
	b.sessionID = ""
	b.sessionKey = nil
	b.poolID = 0
	b.mu.Unlock()
	b.clearPersistedSessionID()
	return nil
}

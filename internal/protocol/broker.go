package protocol

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// BrokerMessage represents a message returned by GitHub Actions Broker.
type BrokerMessage struct {
	MessageID   int64  `json:"messageId"`
	MessageType string `json:"messageType"`
	IV          string `json:"iv,omitempty"`
	Body        string `json:"body"`
}

// UnmarshalJSON handles both numeric and string representations of messageId.
func (m *BrokerMessage) UnmarshalJSON(data []byte) error {
	type Alias BrokerMessage
	aux := struct {
		RawID any `json:"messageId"`
		*Alias
	}{
		Alias: (*Alias)(m),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	switch v := aux.RawID.(type) {
	case float64:
		m.MessageID = int64(v)
	case string:
		id, err := strconv.ParseInt(v, 10, 64)
		if err == nil {
			m.MessageID = id
		}
	case int64:
		m.MessageID = v
	case json.Number:
		id, err := v.Int64()
		if err == nil {
			m.MessageID = id
		}
	}
	return nil
}

// BrokerClient manages sessions and long-polling message retrieval with the GitHub Actions Broker.
type BrokerClient struct {
	client     *http.Client
	brokerURL  string
	authURL    string
	clientID   string
	privateKey *rsa.PrivateKey
	sessionKey []byte
	sessionID  string
	token      string
	poolID     int64
}

// NewBrokerClient creates a new BrokerClient instance.
func NewBrokerClient(client *http.Client, brokerURL, authURL, clientID string, key *rsa.PrivateKey) *BrokerClient {
	if client == nil {
		client = http.DefaultClient
	}
	return &BrokerClient{
		client:     client,
		brokerURL:  strings.TrimRight(brokerURL, "/"),
		authURL:    strings.TrimRight(authURL, "/"),
		clientID:   clientID,
		privateKey: key,
	}
}

// SessionID returns the active broker session ID, or empty string if no session is active.
func (b *BrokerClient) SessionID() string {
	return b.sessionID
}

// SessionKey returns the decrypted AES-256 session key, or nil if no session is active.
func (b *BrokerClient) SessionKey() []byte {
	return b.sessionKey
}

// Token returns the active OAuth access token, or empty string if none.
func (b *BrokerClient) Token() string {
	return b.token
}

// CreateSession acquires an OAuth token and establishes a new runner session with the broker.
func (b *BrokerClient) CreateSession(ctx context.Context, agentName string, agentID int64, poolID int64) error {
	if b.brokerURL == "" {
		return errors.New("empty broker URL")
	}
	if agentName == "" {
		return errors.New("empty agent name")
	}

	// 1. Acquire OAuth bearer token from authURL if configured
	if b.authURL != "" {
		token, err := AcquireOAuthToken(ctx, b.client, b.authURL, b.clientID, b.privateKey)
		if err != nil {
			return fmt.Errorf("acquire oauth token: %w", err)
		}
		b.token = token
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
	var resp *http.Response
	for attempt := 0; attempt < 20; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(jsonBytes))
		if err != nil {
			return fmt.Errorf("create session request: %w", err)
		}
		if b.token != "" {
			req.Header.Set("Authorization", "Bearer "+b.token)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")

		resp, err = b.client.Do(req)
		if err != nil {
			return fmt.Errorf("execute session request: %w", err)
		}

		if resp.StatusCode == http.StatusConflict && attempt < 19 {
			_ = resp.Body.Close()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(3 * time.Second):
			}
			continue
		}
		break
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read session response: %w", err)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("create session returned status %d: %s", resp.StatusCode, string(respBody))
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

	if sessionResp.EncryptionKey != nil && sessionResp.EncryptionKey.Value != "" {
		encKeyBytes, err := decodeBase64(sessionResp.EncryptionKey.Value)
		if err != nil {
			return fmt.Errorf("decode encryption key base64: %w", err)
		}

		decryptedAESKey, err := DecryptAESKey(b.privateKey, encKeyBytes)
		if err != nil {
			return fmt.Errorf("decrypt session key: %w", err)
		}
		b.sessionKey = decryptedAESKey
	}

	b.sessionID = sessionResp.SessionID
	b.poolID = poolID
	return nil
}

// RefreshToken acquires a fresh OAuth access token from authURL if configured.
func (b *BrokerClient) RefreshToken(ctx context.Context) error {
	if b.authURL == "" || b.privateKey == nil {
		return errors.New("authURL or privateKey not configured for token refresh")
	}
	token, err := AcquireOAuthToken(ctx, b.client, b.authURL, b.clientID, b.privateKey)
	if err != nil {
		return fmt.Errorf("refresh oauth token: %w", err)
	}
	b.token = token
	return nil
}

func (b *BrokerClient) fetchMessage(ctx context.Context, reqURL string) (*BrokerMessage, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("create message request: %w", err)
	}
	if b.token != "" {
		req.Header.Set("Authorization", "Bearer "+b.token)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("execute message request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusAccepted {
		return nil, resp.StatusCode, nil
	}

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read message response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, fmt.Errorf("get message returned status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	if len(bytes.TrimSpace(bodyBytes)) == 0 {
		return nil, resp.StatusCode, nil
	}

	var msg BrokerMessage
	if err := json.Unmarshal(bodyBytes, &msg); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("unmarshal broker message: %w", err)
	}
	if msg.MessageID == 0 && msg.MessageType == "" && msg.Body == "" {
		return nil, resp.StatusCode, nil
	}

	if msg.IV != "" {
		if len(b.sessionKey) == 0 {
			return nil, resp.StatusCode, errors.New("cannot decrypt message: session key is empty")
		}
		ivBytes, err := decodeBase64(msg.IV)
		if err != nil {
			return nil, resp.StatusCode, fmt.Errorf("decode iv base64: %w", err)
		}
		cipherBytes, err := decodeBase64(msg.Body)
		if err != nil {
			return nil, resp.StatusCode, fmt.Errorf("decode message body base64: %w", err)
		}
		decrypted, err := DecryptPayload(b.sessionKey, ivBytes, cipherBytes)
		if err != nil {
			return nil, resp.StatusCode, fmt.Errorf("decrypt message payload: %w", err)
		}
		msg.Body = string(decrypted)
	}

	return &msg, resp.StatusCode, nil
}

// GetMessage polls the broker for a pending message.
// Returns (nil, nil) on HTTP 204 No Content or empty responses.
// Decrypts the body with AES-CBC when IV is present.
// Automatically refreshes the OAuth token and retries once on HTTP 401 Unauthorized.
func (b *BrokerClient) GetMessage(ctx context.Context, runnerVersion, os, arch string) (*BrokerMessage, error) {
	if b.sessionID == "" {
		return nil, errors.New("no active session")
	}
	if b.brokerURL == "" {
		return nil, errors.New("empty broker URL")
	}

	reqURL, err := url.Parse(b.brokerURL + "/message")
	if err != nil {
		return nil, fmt.Errorf("parse broker url: %w", err)
	}

	if runnerVersion == "" {
		runnerVersion = "3.0.0"
	}

	q := reqURL.Query()
	q.Set("sessionId", b.sessionID)
	q.Set("status", "Online")
	q.Set("runnerVersion", runnerVersion)
	q.Set("os", os)
	q.Set("architecture", arch)
	q.Set("disableUpdate", "false")
	reqURL.RawQuery = q.Encode()

	msg, statusCode, err := b.fetchMessage(ctx, reqURL.String())
	if statusCode == http.StatusUnauthorized && b.authURL != "" && b.privateKey != nil {
		if refreshErr := b.RefreshToken(ctx); refreshErr == nil {
			msg, _, err = b.fetchMessage(ctx, reqURL.String())
		}
	}
	return msg, err
}

// AcknowledgeMessage confirms receipt and processing of a message.
func (b *BrokerClient) AcknowledgeMessage(ctx context.Context, runnerRequestID string) error {
	if b.sessionID == "" {
		return errors.New("no active session")
	}
	if b.brokerURL == "" {
		return errors.New("empty broker URL")
	}

	reqURL, err := url.Parse(b.brokerURL + "/acknowledge")
	if err != nil {
		return fmt.Errorf("parse broker url: %w", err)
	}
	q := reqURL.Query()
	q.Set("sessionId", b.sessionID)
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

	doAck := func() (int, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL.String(), bytes.NewReader(jsonBody))
		if err != nil {
			return 0, fmt.Errorf("create acknowledge request: %w", err)
		}
		if b.token != "" {
			req.Header.Set("Authorization", "Bearer "+b.token)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")

		resp, err := b.client.Do(req)
		if err != nil {
			return 0, fmt.Errorf("execute acknowledge request: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			return resp.StatusCode, fmt.Errorf("acknowledge message returned status %d: %s", resp.StatusCode, string(body))
		}
		return resp.StatusCode, nil
	}

	statusCode, err := doAck()
	if statusCode == http.StatusUnauthorized && b.authURL != "" && b.privateKey != nil {
		if refreshErr := b.RefreshToken(ctx); refreshErr == nil {
			_, err = doAck()
		}
	}
	return err
}

// DeleteSession terminates the active session with the broker and cleans up local session state.
func (b *BrokerClient) DeleteSession(ctx context.Context) error {
	if b.sessionID == "" {
		return nil
	}
	if b.brokerURL == "" {
		return errors.New("empty broker URL")
	}

	reqURL, err := url.Parse(b.brokerURL + "/session")
	if err != nil {
		return fmt.Errorf("parse broker url: %w", err)
	}
	q := reqURL.Query()
	q.Set("sessionId", b.sessionID)
	reqURL.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, reqURL.String(), nil)
	if err != nil {
		return fmt.Errorf("create delete session request: %w", err)
	}
	if b.token != "" {
		req.Header.Set("Authorization", "Bearer "+b.token)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("execute delete session request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("delete session returned status %d: %s", resp.StatusCode, string(body))
	}

	b.sessionID = ""
	b.sessionKey = nil
	return nil
}

package protocol

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestNewBrokerClient(t *testing.T) {
	key, err := GenerateRSAKeyPair(2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	b := NewBrokerClient(nil, "https://broker.example.com/", "https://auth.example.com/", "client-123", key)
	if b == nil {
		t.Fatal("expected non-nil BrokerClient")
	}
	if b.client == nil {
		t.Error("expected default client to be set when nil passed")
	}
	if b.brokerURL != "https://broker.example.com" {
		t.Errorf("expected trailing slash removed from brokerURL, got %q", b.brokerURL)
	}
	if b.authURL != "https://auth.example.com" {
		t.Errorf("expected trailing slash removed from authURL, got %q", b.authURL)
	}
	if b.clientID != "client-123" {
		t.Errorf("expected clientID 'client-123', got %q", b.clientID)
	}
	if b.privateKey != key {
		t.Error("expected privateKey to match")
	}
}

func setupMockOAuthServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "invalid method", http.StatusMethodNotAllowed)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "parse error", http.StatusBadRequest)
			return
		}
		if r.FormValue("grant_type") != "client_credentials" {
			http.Error(w, "invalid grant_type", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "mock-access-token-999",
			"expires_in":   3600,
			"token_type":   "bearer",
		})
	}))
}

func TestBrokerClient_CreateSession_PKCS1v15(t *testing.T) {
	key, err := GenerateRSAKeyPair(2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	authServer := setupMockOAuthServer(t)
	defer authServer.Close()

	rawAESKey := make([]byte, 32)
	if _, err := rand.Read(rawAESKey); err != nil {
		t.Fatalf("rand read: %v", err)
	}

	// Encrypt rawAESKey with PKCS#1 v1.5
	//nolint:staticcheck // testing PKCS#1 v1.5 session key decryption
	encAESKey, err := rsa.EncryptPKCS1v15(rand.Reader, &key.PublicKey, rawAESKey)
	if err != nil {
		t.Fatalf("encrypt AES key: %v", err)
	}

	sessionID := "11111111-2222-3333-4444-555555555555"
	brokerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/session" || r.Method != http.MethodPost {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}

		authHeader := r.Header.Get("Authorization")
		if authHeader != "Bearer mock-access-token-999" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			http.Error(w, "invalid content type", http.StatusBadRequest)
			return
		}

		var reqBody struct {
			OwnerName         string `json:"ownerName"`
			UseFipsEncryption bool   `json:"useFipsEncryption"`
			Agent             struct {
				ID            int64  `json:"id"`
				Name          string `json:"name"`
				Version       string `json:"version"`
				OSDescription string `json:"osDescription"`
			} `json:"agent"`
		}
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			http.Error(w, "decode error: "+err.Error(), http.StatusBadRequest)
			return
		}

		if reqBody.OwnerName != "RUNNER" {
			http.Error(w, "expected ownerName RUNNER", http.StatusBadRequest)
			return
		}
		if reqBody.Agent.ID != 42 || reqBody.Agent.Name != "agent-alpha" {
			http.Error(w, "unexpected agent id or name", http.StatusBadRequest)
			return
		}
		if reqBody.Agent.Version != "3.0.0" || reqBody.Agent.OSDescription != "actr" {
			http.Error(w, "unexpected agent version or osDescription", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"sessionId": sessionID,
			"encryptionKey": map[string]string{
				"value": base64.StdEncoding.EncodeToString(encAESKey),
			},
		})
	}))
	defer brokerServer.Close()

	b := NewBrokerClient(brokerServer.Client(), brokerServer.URL, authServer.URL, "client-42", key)
	ctx := context.Background()

	if err := b.CreateSession(ctx, "agent-alpha", 42, 1); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	if b.SessionID() != sessionID {
		t.Errorf("expected sessionID %q, got %q", sessionID, b.SessionID())
	}
	if !bytes.Equal(b.SessionKey(), rawAESKey) {
		t.Error("decrypted session key does not match original rawAESKey")
	}
}

func TestBrokerClient_CreateSession_OAEP(t *testing.T) {
	key, err := GenerateRSAKeyPair(2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	authServer := setupMockOAuthServer(t)
	defer authServer.Close()

	rawAESKey := make([]byte, 32)
	if _, err := rand.Read(rawAESKey); err != nil {
		t.Fatalf("rand read: %v", err)
	}

	// Encrypt rawAESKey with RSA-OAEP SHA-256
	encAESKey, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, &key.PublicKey, rawAESKey, nil)
	if err != nil {
		t.Fatalf("encrypt OAEP: %v", err)
	}

	sessionID := "oaep-session-12345"
	brokerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/session" || r.Method != http.MethodPost {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"sessionId": sessionID,
			"encryptionKey": map[string]string{
				"value": base64.StdEncoding.EncodeToString(encAESKey),
			},
		})
	}))
	defer brokerServer.Close()

	b := NewBrokerClient(brokerServer.Client(), brokerServer.URL, authServer.URL, "client-42", key)
	ctx := context.Background()

	if err := b.CreateSession(ctx, "agent-beta", 84, 1); err != nil {
		t.Fatalf("CreateSession with OAEP failed: %v", err)
	}

	if b.SessionID() != sessionID {
		t.Errorf("expected sessionID %q, got %q", sessionID, b.SessionID())
	}
	if !bytes.Equal(b.SessionKey(), rawAESKey) {
		t.Error("OAEP decrypted session key does not match original rawAESKey")
	}
}

func TestBrokerClient_GetMessage_Encrypted(t *testing.T) {
	key, err := GenerateRSAKeyPair(2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	rawAESKey := make([]byte, 32)
	if _, err := rand.Read(rawAESKey); err != nil {
		t.Fatalf("rand read: %v", err)
	}

	sessionID := "session-get-msg-1"
	plaintext := `{"jobId":"job-999","planId":"plan-111","runnerRequestId":"req-555"}`

	// Encrypt plaintext with AES-CBC + PKCS7 padding
	iv := make([]byte, aes.BlockSize)
	if _, err := rand.Read(iv); err != nil {
		t.Fatalf("rand read iv: %v", err)
	}
	padded := pkcs7Pad([]byte(plaintext), aes.BlockSize)
	block, err := aes.NewCipher(rawAESKey)
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	enc := cipher.NewCBCEncrypter(block, iv)
	ciphertext := make([]byte, len(padded))
	enc.CryptBlocks(ciphertext, padded)

	brokerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/message" || r.Method != http.MethodGet {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}

		q := r.URL.Query()
		if q.Get("sessionId") != sessionID {
			http.Error(w, "invalid sessionId", http.StatusBadRequest)
			return
		}
		if q.Get("status") != "Online" {
			http.Error(w, "invalid status", http.StatusBadRequest)
			return
		}
		if q.Get("runnerVersion") != "3.0.0" {
			http.Error(w, "invalid runnerVersion", http.StatusBadRequest)
			return
		}
		if q.Get("os") != "macOS" {
			http.Error(w, "invalid os", http.StatusBadRequest)
			return
		}
		if q.Get("architecture") != "ARM64" {
			http.Error(w, "invalid architecture", http.StatusBadRequest)
			return
		}
		if q.Get("disableUpdate") != "false" {
			http.Error(w, "invalid disableUpdate", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"messageId":   int64(1001),
			"messageType": "PipelineAgentJobRequest",
			"iv":          base64.StdEncoding.EncodeToString(iv),
			"body":        base64.StdEncoding.EncodeToString(ciphertext),
		})
	}))
	defer brokerServer.Close()

	b := NewBrokerClient(brokerServer.Client(), brokerServer.URL, "", "client-42", key)
	b.sessionID = sessionID
	b.sessionKey = rawAESKey

	msg, err := b.GetMessage(context.Background(), "3.0.0", "macOS", "ARM64")
	if err != nil {
		t.Fatalf("GetMessage failed: %v", err)
	}
	if msg == nil {
		t.Fatal("expected non-nil message")
	}
	if msg.MessageID != 1001 {
		t.Errorf("expected MessageID 1001, got %d", msg.MessageID)
	}
	if msg.MessageType != "PipelineAgentJobRequest" {
		t.Errorf("expected MessageType PipelineAgentJobRequest, got %q", msg.MessageType)
	}
	if msg.Body != plaintext {
		t.Errorf("expected decrypted body %q, got %q", plaintext, msg.Body)
	}
}

func TestBrokerClient_GetMessage_Unencrypted(t *testing.T) {
	key, err := GenerateRSAKeyPair(2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	sessionID := "session-unencrypted-1"
	plaintext := `{"message":"hello world"}`

	brokerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"messageId":   int64(2002),
			"messageType": "Notification",
			"body":        plaintext,
		})
	}))
	defer brokerServer.Close()

	b := NewBrokerClient(brokerServer.Client(), brokerServer.URL, "", "client-42", key)
	b.sessionID = sessionID

	msg, err := b.GetMessage(context.Background(), "3.0.0", "Linux", "X64")
	if err != nil {
		t.Fatalf("GetMessage failed: %v", err)
	}
	if msg == nil {
		t.Fatal("expected non-nil message")
	}
	if msg.MessageID != 2002 {
		t.Errorf("expected messageId 2002, got %d", msg.MessageID)
	}
	if msg.Body != plaintext {
		t.Errorf("expected body %q, got %q", plaintext, msg.Body)
	}
}

func TestBrokerClient_GetMessage_NoContent(t *testing.T) {
	key, err := GenerateRSAKeyPair(2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	brokerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer brokerServer.Close()

	b := NewBrokerClient(brokerServer.Client(), brokerServer.URL, "", "client-42", key)
	b.sessionID = "session-nocontent"

	msg, err := b.GetMessage(context.Background(), "3.0.0", "macOS", "ARM64")
	if err != nil {
		t.Fatalf("expected nil error on 204, got: %v", err)
	}
	if msg != nil {
		t.Fatalf("expected nil message on 204, got: %+v", msg)
	}
}

func TestBrokerClient_GetMessage_Accepted202(t *testing.T) {
	key, err := GenerateRSAKeyPair(2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	brokerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer brokerServer.Close()

	b := NewBrokerClient(brokerServer.Client(), brokerServer.URL, "", "client-42", key)
	b.sessionID = "session-202"

	msg, err := b.GetMessage(context.Background(), "3.0.0", "macOS", "ARM64")
	if err != nil {
		t.Fatalf("expected nil error on 202 Accepted, got: %v", err)
	}
	if msg != nil {
		t.Fatalf("expected nil message on 202 Accepted, got: %+v", msg)
	}
}

func TestBrokerClient_GetMessage_EmptyBody(t *testing.T) {
	key, err := GenerateRSAKeyPair(2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	brokerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("   "))
	}))
	defer brokerServer.Close()

	b := NewBrokerClient(brokerServer.Client(), brokerServer.URL, "", "client-42", key)
	b.sessionID = "session-empty"

	msg, err := b.GetMessage(context.Background(), "3.0.0", "macOS", "ARM64")
	if err != nil {
		t.Fatalf("expected nil error on empty body, got: %v", err)
	}
	if msg != nil {
		t.Fatalf("expected nil message on empty body, got: %+v", msg)
	}
}

func TestBrokerClient_AcknowledgeMessage_Success(t *testing.T) {
	key, err := GenerateRSAKeyPair(2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	sessionID := "session-ack-1"
	var receivedBody []byte
	var bodyMutex sync.Mutex

	brokerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/acknowledge" || r.Method != http.MethodPost {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if r.URL.Query().Get("sessionId") != sessionID {
			http.Error(w, "invalid sessionId", http.StatusBadRequest)
			return
		}
		if r.URL.Query().Get("status") != "Online" {
			http.Error(w, "invalid status", http.StatusBadRequest)
			return
		}
		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read error", http.StatusBadRequest)
			return
		}
		bodyMutex.Lock()
		receivedBody = data
		bodyMutex.Unlock()

		w.WriteHeader(http.StatusOK)
	}))
	defer brokerServer.Close()

	b := NewBrokerClient(brokerServer.Client(), brokerServer.URL, "", "client-42", key)
	b.sessionID = sessionID

	if err := b.AcknowledgeMessage(context.Background(), "req-uuid-999"); err != nil {
		t.Fatalf("AcknowledgeMessage failed: %v", err)
	}

	bodyMutex.Lock()
	defer bodyMutex.Unlock()
	var ackReq struct {
		RunnerRequestID string `json:"runnerRequestId"`
	}
	if err := json.Unmarshal(receivedBody, &ackReq); err != nil {
		t.Fatalf("unmarshal received body: %v", err)
	}
	if ackReq.RunnerRequestID != "req-uuid-999" {
		t.Errorf("expected runnerRequestId 'req-uuid-999', got %q", ackReq.RunnerRequestID)
	}
}

func TestBrokerClient_DeleteSession_Success(t *testing.T) {
	key, err := GenerateRSAKeyPair(2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	sessionID := "session-del-1"
	deleted := false

	brokerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/session" || r.Method != http.MethodDelete {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if r.URL.Query().Get("sessionId") != sessionID {
			http.Error(w, "invalid sessionId", http.StatusBadRequest)
			return
		}
		deleted = true
		w.WriteHeader(http.StatusOK)
	}))
	defer brokerServer.Close()

	b := NewBrokerClient(brokerServer.Client(), brokerServer.URL, "", "client-42", key)
	b.sessionID = sessionID
	b.sessionKey = []byte("some-session-key-32-bytes-long!")

	if err := b.DeleteSession(context.Background()); err != nil {
		t.Fatalf("DeleteSession failed: %v", err)
	}

	if !deleted {
		t.Error("expected server to receive DELETE request")
	}
	if b.SessionID() != "" {
		t.Errorf("expected sessionID to be cleared, got %q", b.SessionID())
	}
	if b.SessionKey() != nil {
		t.Error("expected sessionKey to be cleared")
	}
}

func TestBrokerClient_DeleteSession_NoSession(t *testing.T) {
	b := NewBrokerClient(nil, "http://localhost", "", "client-42", nil)
	// sessionID is empty
	if err := b.DeleteSession(context.Background()); err != nil {
		t.Fatalf("expected nil error when no session exists, got %v", err)
	}
}

func TestBrokerClient_ErrorCases(t *testing.T) {
	key, err := GenerateRSAKeyPair(2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	ctx := context.Background()

	t.Run("GetMessage without session", func(t *testing.T) {
		b := NewBrokerClient(nil, "http://localhost", "", "c", key)
		if _, err := b.GetMessage(ctx, "3.0.0", "macOS", "ARM64"); err == nil {
			t.Error("expected error calling GetMessage without session, got nil")
		}
	})

	t.Run("AcknowledgeMessage without session", func(t *testing.T) {
		b := NewBrokerClient(nil, "http://localhost", "", "c", key)
		if err := b.AcknowledgeMessage(ctx, "req-1"); err == nil {
			t.Error("expected error calling AcknowledgeMessage without session, got nil")
		}
	})

	t.Run("CreateSession auth failure", func(t *testing.T) {
		authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "auth server error", http.StatusInternalServerError)
		}))
		defer authServer.Close()

		b := NewBrokerClient(authServer.Client(), "http://localhost", authServer.URL, "c", key)
		if err := b.CreateSession(ctx, "agent", 1, 1); err == nil {
			t.Error("expected error when auth fails, got nil")
		}
	})

	t.Run("CreateSession broker server 500", func(t *testing.T) {
		authServer := setupMockOAuthServer(t)
		defer authServer.Close()

		brokerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "broker internal failure", http.StatusInternalServerError)
		}))
		defer brokerServer.Close()

		b := NewBrokerClient(brokerServer.Client(), brokerServer.URL, authServer.URL, "c", key)
		if err := b.CreateSession(ctx, "agent", 1, 1); err == nil {
			t.Error("expected error when broker returns 500, got nil")
		}
	})

	t.Run("CreateSession missing sessionId", func(t *testing.T) {
		authServer := setupMockOAuthServer(t)
		defer authServer.Close()

		brokerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"encryptionKey": map[string]string{"value": "some-val"},
			})
		}))
		defer brokerServer.Close()

		b := NewBrokerClient(brokerServer.Client(), brokerServer.URL, authServer.URL, "c", key)
		if err := b.CreateSession(ctx, "agent", 1, 1); err == nil {
			t.Error("expected error when sessionId missing, got nil")
		}
	})

	t.Run("CreateSession invalid encryptionKey base64", func(t *testing.T) {
		authServer := setupMockOAuthServer(t)
		defer authServer.Close()

		brokerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sessionId": "ses-123",
				"encryptionKey": map[string]any{
					"value": "invalid-base-64!!",
				},
			})
		}))
		defer brokerServer.Close()

		b := NewBrokerClient(brokerServer.Client(), brokerServer.URL, authServer.URL, "c", key)
		if err := b.CreateSession(ctx, "agent", 1, 1); err == nil {
			t.Error("expected error when encryptionKey.value invalid base64, got nil")
		}
	})

	t.Run("CreateSession without encryptionKey succeeds", func(t *testing.T) {
		authServer := setupMockOAuthServer(t)
		defer authServer.Close()

		brokerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sessionId": "ses-v2-123",
			})
		}))
		defer brokerServer.Close()

		b := NewBrokerClient(brokerServer.Client(), brokerServer.URL, authServer.URL, "c", key)
		if err := b.CreateSession(ctx, "agent", 1, 1); err != nil {
			t.Fatalf("expected success when encryptionKey omitted, got: %v", err)
		}
		if b.SessionID() != "ses-v2-123" {
			t.Errorf("expected session ID ses-v2-123, got: %s", b.SessionID())
		}
	})

	t.Run("GetMessage broker server 500", func(t *testing.T) {
		brokerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "broker server error", http.StatusInternalServerError)
		}))
		defer brokerServer.Close()

		b := NewBrokerClient(brokerServer.Client(), brokerServer.URL, "", "c", key)
		b.sessionID = "ses-123"

		if _, err := b.GetMessage(ctx, "3.0.0", "macOS", "ARM64"); err == nil {
			t.Error("expected error when broker returns 500 on GetMessage, got nil")
		}
	})

	t.Run("GetMessage decryption failure - missing session key", func(t *testing.T) {
		brokerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"messageId":   int64(1),
				"messageType": "Job",
				"iv":          base64.StdEncoding.EncodeToString(make([]byte, 16)),
				"body":        base64.StdEncoding.EncodeToString([]byte("ciphertext-16-b")),
			})
		}))
		defer brokerServer.Close()

		b := NewBrokerClient(brokerServer.Client(), brokerServer.URL, "", "c", key)
		b.sessionID = "ses-123"
		b.sessionKey = nil // missing session key!

		if _, err := b.GetMessage(ctx, "3.0.0", "macOS", "ARM64"); err == nil {
			t.Error("expected error when session key missing for encrypted message, got nil")
		}
	})

	t.Run("AcknowledgeMessage broker server 500", func(t *testing.T) {
		brokerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "server error", http.StatusInternalServerError)
		}))
		defer brokerServer.Close()

		b := NewBrokerClient(brokerServer.Client(), brokerServer.URL, "", "c", key)
		b.sessionID = "ses-123"

		if err := b.AcknowledgeMessage(ctx, "req-1"); err == nil {
			t.Error("expected error when acknowledge returns 500, got nil")
		}
	})

	t.Run("DeleteSession broker server 500", func(t *testing.T) {
		brokerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "server error", http.StatusInternalServerError)
		}))
		defer brokerServer.Close()

		b := NewBrokerClient(brokerServer.Client(), brokerServer.URL, "", "c", key)
		b.sessionID = "ses-123"

		if err := b.DeleteSession(ctx); err == nil {
			t.Error("expected error when delete returns 500, got nil")
		}
	})

	t.Run("CreateSession empty broker URL", func(t *testing.T) {
		b := NewBrokerClient(nil, "", "", "c", key)
		if err := b.CreateSession(ctx, "agent", 1, 1); err == nil {
			t.Error("expected error for empty broker URL, got nil")
		}
	})

	t.Run("CreateSession empty agent name", func(t *testing.T) {
		b := NewBrokerClient(nil, "http://localhost", "", "c", key)
		if err := b.CreateSession(ctx, "", 1, 1); err == nil {
			t.Error("expected error for empty agent name, got nil")
		}
	})

	t.Run("CreateSession invalid JSON response", func(t *testing.T) {
		brokerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("not-valid-json"))
		}))
		defer brokerServer.Close()

		b := NewBrokerClient(brokerServer.Client(), brokerServer.URL, "", "c", key)
		if err := b.CreateSession(ctx, "agent", 1, 1); err == nil {
			t.Error("expected error for invalid JSON response, got nil")
		}
	})

	t.Run("GetMessage invalid JSON response", func(t *testing.T) {
		brokerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("invalid-json"))
		}))
		defer brokerServer.Close()

		b := NewBrokerClient(brokerServer.Client(), brokerServer.URL, "", "c", key)
		b.sessionID = "ses-123"

		if _, err := b.GetMessage(ctx, "3.0.0", "macOS", "ARM64"); err == nil {
			t.Error("expected error for invalid JSON in GetMessage, got nil")
		}
	})

	t.Run("GetMessage invalid IV base64", func(t *testing.T) {
		brokerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"messageId":   int64(1),
				"messageType": "Job",
				"iv":          "!!!not-base64!!!",
				"body":        "some-body",
			})
		}))
		defer brokerServer.Close()

		b := NewBrokerClient(brokerServer.Client(), brokerServer.URL, "", "c", key)
		b.sessionID = "ses-123"
		b.sessionKey = make([]byte, 32)

		if _, err := b.GetMessage(ctx, "3.0.0", "macOS", "ARM64"); err == nil {
			t.Error("expected error for invalid IV base64, got nil")
		}
	})

	t.Run("GetMessage invalid body base64", func(t *testing.T) {
		brokerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"messageId":   int64(1),
				"messageType": "Job",
				"iv":          base64.StdEncoding.EncodeToString(make([]byte, 16)),
				"body":        "!!!not-base64!!!",
			})
		}))
		defer brokerServer.Close()

		b := NewBrokerClient(brokerServer.Client(), brokerServer.URL, "", "c", key)
		b.sessionID = "ses-123"
		b.sessionKey = make([]byte, 32)

		if _, err := b.GetMessage(ctx, "3.0.0", "macOS", "ARM64"); err == nil {
			t.Error("expected error for invalid body base64, got nil")
		}
	})

	t.Run("GetMessage decryption padding error", func(t *testing.T) {
		brokerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"messageId":   int64(1),
				"messageType": "Job",
				"iv":          base64.StdEncoding.EncodeToString(make([]byte, 16)),
				"body":        base64.StdEncoding.EncodeToString([]byte("1234567890123456")),
			})
		}))
		defer brokerServer.Close()

		b := NewBrokerClient(brokerServer.Client(), brokerServer.URL, "", "c", key)
		b.sessionID = "ses-123"
		b.sessionKey = make([]byte, 32)

		if _, err := b.GetMessage(ctx, "3.0.0", "macOS", "ARM64"); err == nil {
			t.Error("expected error for corrupted ciphertext padding, got nil")
		}
	})
}

func TestBrokerMessage_UnmarshalJSON_Variants(t *testing.T) {
	// String messageId
	rawJSONStr := `{"messageId":"98765","messageType":"PipelineJob","body":"hello"}`
	var msg1 BrokerMessage
	if err := json.Unmarshal([]byte(rawJSONStr), &msg1); err != nil {
		t.Fatalf("unmarshal string messageId: %v", err)
	}
	if msg1.MessageID != 98765 {
		t.Errorf("expected 98765, got %d", msg1.MessageID)
	}

	// Float messageId
	rawJSONFloat := `{"messageId":456.0,"messageType":"PipelineJob","body":"hello"}`
	var msg2 BrokerMessage
	if err := json.Unmarshal([]byte(rawJSONFloat), &msg2); err != nil {
		t.Fatalf("unmarshal float messageId: %v", err)
	}
	if msg2.MessageID != 456 {
		t.Errorf("expected 456, got %d", msg2.MessageID)
	}

	// Invalid JSON
	var msg3 BrokerMessage
	if err := json.Unmarshal([]byte(`{"messageId": invalid}`), &msg3); err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

func TestBrokerClient_GetMessage_TokenRefreshOn401(t *testing.T) {
	key, err := GenerateRSAKeyPair(2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	authCalls := 0
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authCalls++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "refreshed-access-token",
			"expires_in":   3600,
			"token_type":   "bearer",
		})
	}))
	defer authServer.Close()

	getCalls := 0
	brokerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/message" {
			getCalls++
			if r.Header.Get("Authorization") == "Bearer initial-token" {
				http.Error(w, `{"error":"token expired"}`, http.StatusUnauthorized)
				return
			}
			if r.Header.Get("Authorization") == "Bearer refreshed-access-token" {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"messageId":   int64(777),
					"messageType": "RunnerJobRequest",
					"body":        `{"runnerRequestId":"777"}`,
				})
				return
			}
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer brokerServer.Close()

	b := NewBrokerClient(brokerServer.Client(), brokerServer.URL, authServer.URL, "client-test", key)
	b.sessionID = "ses-123"
	b.token = "initial-token"

	msg, err := b.GetMessage(context.Background(), "3.0.0", "macOS", "ARM64")
	if err != nil {
		t.Fatalf("unexpected error after 401 retry: %v", err)
	}
	if msg == nil || msg.MessageID != 777 {
		t.Fatalf("expected message 777, got %+v", msg)
	}
	if authCalls != 1 {
		t.Errorf("expected 1 auth call, got %d", authCalls)
	}
	if getCalls != 2 {
		t.Errorf("expected 2 get message calls, got %d", getCalls)
	}
	if b.Token() != "refreshed-access-token" {
		t.Errorf("expected token updated to refreshed-access-token, got %s", b.Token())
	}
}

func TestBrokerClient_AcknowledgeMessage_TokenRefreshOn401(t *testing.T) {
	key, err := GenerateRSAKeyPair(2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	authCalls := 0
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authCalls++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "refreshed-ack-token",
			"expires_in":   3600,
			"token_type":   "bearer",
		})
	}))
	defer authServer.Close()

	ackCalls := 0
	brokerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/acknowledge" {
			ackCalls++
			if r.Header.Get("Authorization") == "Bearer initial-ack-token" {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			if r.Header.Get("Authorization") == "Bearer refreshed-ack-token" {
				w.WriteHeader(http.StatusOK)
				return
			}
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer brokerServer.Close()

	b := NewBrokerClient(brokerServer.Client(), brokerServer.URL, authServer.URL, "client-test", key)
	b.sessionID = "ses-123"
	b.token = "initial-ack-token"

	if err := b.AcknowledgeMessage(context.Background(), "req-456"); err != nil {
		t.Fatalf("unexpected error in acknowledge message after refresh: %v", err)
	}
	if authCalls != 1 {
		t.Errorf("expected 1 auth call, got %d", authCalls)
	}
	if ackCalls != 2 {
		t.Errorf("expected 2 acknowledge calls, got %d", ackCalls)
	}
	if b.Token() != "refreshed-ack-token" {
		t.Errorf("expected token updated to refreshed-ack-token, got %s", b.Token())
	}
}

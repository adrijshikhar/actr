package main

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
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/adrijshikhar/actr/internal/config"
	"github.com/adrijshikhar/actr/internal/driver"
	"github.com/adrijshikhar/actr/internal/history"
	"github.com/adrijshikhar/actr/internal/protocol"
	"github.com/spf13/cobra"
)

func createTestConfigDir(t *testing.T, serverURL string) (string, *rsa.PrivateKey) {
	t.Helper()
	dir := t.TempDir()
	if len(filepath.Join(dir, "daemon.sock")) >= 100 {
		shortDir, err := os.MkdirTemp("/tmp", "act-cfg-*")
		if err != nil {
			t.Fatalf("failed to create short temp dir: %v", err)
		}
		t.Cleanup(func() {
			_ = os.RemoveAll(shortDir)
		})
		dir = shortDir
	}

	cfg := &config.RunnerConfig{
		AgentID:    42,
		AgentName:  "test-daemon-runner",
		PoolID:     1,
		ServerURL:  serverURL,
		GitHubURL:  "https://github.com/adrijshikhar/actr",
		WorkFolder: filepath.Join(dir, "_work"),
	}
	if err := config.SaveRunnerConfig(dir, cfg); err != nil {
		t.Fatalf("save runner config: %v", err)
	}

	creds := &config.Credentials{
		ClientID:  "test-client-id-42",
		IDToken:   "test-id-token-abc",
		SessionID: "test-session-id-xyz",
	}
	if err := config.SaveCredentials(dir, creds); err != nil {
		t.Fatalf("save credentials: %v", err)
	}

	key, err := protocol.GenerateRSAKeyPair(2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}

	params := protocol.ExportRSAParameters(key)
	data, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal rsa params: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".credentials_rsaparams"), data, 0600); err != nil {
		t.Fatalf("write .credentials_rsaparams: %v", err)
	}

	return dir, key
}

func TestRunCommand_Flags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	ctx := context.Background()

	code := dispatchWithContext(ctx, []string{"run", "--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected code 0, got %d. stderr: %s", code, stderr.String())
	}

	out := stdout.String()
	for _, flag := range []string{"--once", "--config-dir", "--trace"} {
		if !strings.Contains(out, flag) {
			t.Errorf("expected help output to contain %q, got: %s", flag, out)
		}
	}
}

func TestRunCommand_MissingCredentials(t *testing.T) {
	emptyDir := t.TempDir()
	var stdout, stderr bytes.Buffer
	ctx := context.Background()

	code := dispatchWithContext(ctx, []string{"run", "--config-dir", emptyDir}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit code when runner is not configured, got 0")
	}

	combined := stdout.String() + "\n" + stderr.String()
	expectedErr := "runner is not configured; run 'actr register' first"
	if !strings.Contains(combined, expectedErr) {
		t.Fatalf("expected output to contain %q, got: %s", expectedErr, combined)
	}
}

func TestRunCommand_GracefulShutdown(t *testing.T) {
	var sessionCreated, sessionDeleted atomic.Bool

	rawAESKey := make([]byte, 32)
	if _, err := rand.Read(rawAESKey); err != nil {
		t.Fatalf("rand read: %v", err)
	}

	var encKeyStr string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/session" && r.Method == http.MethodPost:
			sessionCreated.Store(true)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sessionId": "mock-session-123",
				"encryptionKey": map[string]string{
					"value": encKeyStr,
				},
			})
		case r.URL.Path == "/session" && r.Method == http.MethodDelete:
			sessionDeleted.Store(true)
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/message":
			// Block until request context is canceled
			<-r.Context().Done()
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	configDir, key := createTestConfigDir(t, server.URL)
	encAESKey, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, &key.PublicKey, rawAESKey, nil)
	if err != nil {
		t.Fatalf("encrypt AES key: %v", err)
	}
	encKeyStr = base64.StdEncoding.EncodeToString(encAESKey)

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel after 200ms to test graceful shutdown
	time.AfterFunc(200*time.Millisecond, cancel)

	var stdout, stderr bytes.Buffer
	code := dispatchWithContext(ctx, []string{"run", "--config-dir", configDir}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("expected exit code 0 on context cancellation, got %d. stderr: %s", code, stderr.String())
	}

	combinedOut := stdout.String() + "\n" + stderr.String()
	if !strings.Contains(combinedOut, "Listening for Jobs") {
		t.Errorf("expected 'Listening for Jobs' in output, got: %s", combinedOut)
	}

	if !sessionCreated.Load() {
		t.Errorf("expected broker session to be created")
	}
	if !sessionDeleted.Load() {
		t.Errorf("expected broker session to be deleted on shutdown")
	}
}

func TestRunCommand_Once_HttptestServer(t *testing.T) {
	var (
		sessionCreated      atomic.Bool
		sessionDeleted      atomic.Bool
		msgAcknowledged     atomic.Bool
		jobAcquired         atomic.Bool
		jobCompleted        atomic.Bool
		completedConclusion string
		mu                  sync.Mutex
		encKeyStr           string
	)

	rawAESKey := make([]byte, 32)
	if _, err := rand.Read(rawAESKey); err != nil {
		t.Fatalf("rand read: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/session" && r.Method == http.MethodPost:
			sessionCreated.Store(true)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sessionId": "session-once-test",
				"encryptionKey": map[string]string{
					"value": encKeyStr,
				},
			})

		case r.URL.Path == "/session" && r.Method == http.MethodDelete:
			sessionDeleted.Store(true)
			w.WriteHeader(http.StatusOK)

		case r.URL.Path == "/message" && r.Method == http.MethodGet:
			// If already acquired, return no content
			if jobAcquired.Load() {
				w.WriteHeader(http.StatusNoContent)
				return
			}

			// Plaintext payload representing RunnerJobRequest reference
			refPayload := map[string]string{
				"runner_request_id": "req-job-001",
				"run_service_url":   "", // empty indicates use default ServerURL
				"billing_owner_id":  "owner-abc",
			}
			refBytes, _ := json.Marshal(refPayload)

			// Encrypt refBytes with AES-CBC
			iv := make([]byte, aes.BlockSize)
			_, _ = rand.Read(iv)
			padded := pkcs7Pad(refBytes, aes.BlockSize)
			block, _ := aes.NewCipher(rawAESKey)
			enc := cipher.NewCBCEncrypter(block, iv)
			ciphertext := make([]byte, len(padded))
			enc.CryptBlocks(ciphertext, padded)

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"messageId":   int64(5001),
				"messageType": "RunnerJobRequest",
				"iv":          base64.StdEncoding.EncodeToString(iv),
				"body":        base64.StdEncoding.EncodeToString(ciphertext),
			})

		case r.URL.Path == "/acknowledge" && r.Method == http.MethodPost:
			msgAcknowledged.Store(true)
			w.WriteHeader(http.StatusOK)

		case r.URL.Path == "/acquirejob" && r.Method == http.MethodPost:
			jobAcquired.Store(true)
			jobSpec := protocol.JobSpecification{
				JobID:   "job-test-once",
				JobName: "test-echo-step",
				PlanID:  "plan-test-once",
				Steps: []protocol.JobStep{
					{
						ID:     "step-1",
						Name:   "Echo Hello",
						Type:   "script",
						Script: "echo 'hello from daemon test'",
					},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(jobSpec)

		case r.URL.Path == "/completejob" && r.Method == http.MethodPost:
			jobCompleted.Store(true)
			var completeReq protocol.CompleteJobRequest
			_ = json.NewDecoder(r.Body).Decode(&completeReq)
			mu.Lock()
			completedConclusion = completeReq.Conclusion
			mu.Unlock()
			w.WriteHeader(http.StatusOK)

		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	configDir, key := createTestConfigDir(t, server.URL)

	// Encrypt rawAESKey with OAEP using key.PublicKey
	encAESKey, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, &key.PublicKey, rawAESKey, nil)
	if err != nil {
		t.Fatalf("encrypt AES key: %v", err)
	}
	encKeyStr = base64.StdEncoding.EncodeToString(encAESKey)

	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	code := dispatchWithContext(ctx, []string{"run", "--config-dir", configDir, "--once", "--trace"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("expected exit code 0 for --once execution, got %d. stderr: %s", code, stderr.String())
	}

	if !sessionCreated.Load() {
		t.Errorf("expected session to be created")
	}
	if !msgAcknowledged.Load() {
		t.Errorf("expected message to be acknowledged")
	}
	if !jobAcquired.Load() {
		t.Errorf("expected job to be acquired")
	}
	if !jobCompleted.Load() {
		t.Errorf("expected job to be completed")
	}
	mu.Lock()
	conc := completedConclusion
	mu.Unlock()
	if conc != "succeeded" && conc != "success" {
		t.Errorf("expected conclusion 'succeeded' or 'success', got %q", conc)
	}
	if !sessionDeleted.Load() {
		t.Errorf("expected session to be deleted after --once completes")
	}
}

func TestRunCommand_Once_CompleteJobFailure(t *testing.T) {
	rawAESKey := make([]byte, 32)
	if _, err := rand.Read(rawAESKey); err != nil {
		t.Fatalf("rand read: %v", err)
	}

	var encKeyStr string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/session" && r.Method == http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sessionId": "session-complete-fail",
				"encryptionKey": map[string]string{
					"value": encKeyStr,
				},
			})
		case r.URL.Path == "/session" && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/message" && r.Method == http.MethodGet:
			refPayload := map[string]string{
				"runner_request_id": "req-job-fail",
				"run_service_url":   "",
				"billing_owner_id":  "owner-fail",
			}
			refBytes, _ := json.Marshal(refPayload)

			iv := make([]byte, aes.BlockSize)
			_, _ = rand.Read(iv)
			padded := pkcs7Pad(refBytes, aes.BlockSize)
			block, _ := aes.NewCipher(rawAESKey)
			enc := cipher.NewCBCEncrypter(block, iv)
			ciphertext := make([]byte, len(padded))
			enc.CryptBlocks(ciphertext, padded)

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"messageId":   int64(7001),
				"messageType": "RunnerJobRequest",
				"iv":          base64.StdEncoding.EncodeToString(iv),
				"body":        base64.StdEncoding.EncodeToString(ciphertext),
			})
		case r.URL.Path == "/acknowledge" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/acquirejob" && r.Method == http.MethodPost:
			jobSpec := protocol.JobSpecification{
				JobID:   "job-test-fail-complete",
				JobName: "test-echo-step",
				PlanID:  "plan-test-fail-complete",
				Steps: []protocol.JobStep{
					{
						ID:     "step-1",
						Name:   "Echo Step",
						Type:   "script",
						Script: "echo 'hello'",
					},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(jobSpec)
		case r.URL.Path == "/completejob" && r.Method == http.MethodPost:
			// Simulate RunService failing to record completion
			http.Error(w, "internal server error: failed to persist job completion", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	configDir, key := createTestConfigDir(t, server.URL)
	encAESKey, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, &key.PublicKey, rawAESKey, nil)
	if err != nil {
		t.Fatalf("encrypt AES key: %v", err)
	}
	encKeyStr = base64.StdEncoding.EncodeToString(encAESKey)

	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	code := dispatchWithContext(ctx, []string{"run", "--config-dir", configDir, "--once"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit code when completejob fails in --once mode, got 0")
	}

	combined := stdout.String() + "\n" + stderr.String()
	if !strings.Contains(combined, "complete job:") {
		t.Errorf("expected error message to contain 'complete job:', got: %s", combined)
	}
}

func TestRunCommand_TraceFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel context

	// Calling run with invalid directory and --trace should still initialize trace and return the missing config error
	code := dispatchWithContext(ctx, []string{"run", "--trace", "--config-dir", t.TempDir()}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit code, got 0")
	}
	combined := stdout.String() + "\n" + stderr.String()
	if !strings.Contains(combined, "runner is not configured; run 'actr register' first") {
		t.Fatalf("expected missing configuration error, got: %s", combined)
	}
}

func TestRunCommand_RecreatesSessionOnRunnerSessionInvalid(t *testing.T) {
	rawAESKey := make([]byte, 32)
	_, _ = rand.Read(rawAESKey)
	var encKeyStr string

	var sessionCount atomic.Int32
	var getMessageCount atomic.Int32
	var jobCompleted atomic.Bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/session" && r.Method == http.MethodPost:
			count := sessionCount.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sessionId": fmt.Sprintf("session-%d", count),
				"encryptionKey": map[string]string{
					"value": encKeyStr,
				},
			})

		case r.URL.Path == "/session" && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusOK)

		case r.URL.Path == "/message" && r.Method == http.MethodGet:
			callNum := getMessageCount.Add(1)
			if callNum == 1 {
				// First call simulates an expired/invalid session from GitHub Broker
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"source":"actions-broker-listener","errorMessage":"Runner session is invalid","errorKind":"RunnerSessionInvalid","statusCode":400}`))
				return
			}

			// Second call returns a job
			refPayload := map[string]string{
				"runner_request_id": "req-job-recreate",
				"run_service_url":   "",
				"billing_owner_id":  "owner-abc",
			}
			refBytes, _ := json.Marshal(refPayload)
			iv := make([]byte, aes.BlockSize)
			_, _ = rand.Read(iv)
			padded := pkcs7Pad(refBytes, aes.BlockSize)
			block, _ := aes.NewCipher(rawAESKey)
			enc := cipher.NewCBCEncrypter(block, iv)
			ciphertext := make([]byte, len(padded))
			enc.CryptBlocks(ciphertext, padded)

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"messageId":   int64(9001),
				"messageType": "RunnerJobRequest",
				"iv":          base64.StdEncoding.EncodeToString(iv),
				"body":        base64.StdEncoding.EncodeToString(ciphertext),
			})

		case r.URL.Path == "/acknowledge" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusOK)

		case r.URL.Path == "/acquirejob" && r.Method == http.MethodPost:
			jobSpec := protocol.JobSpecification{
				JobID:   "job-test-recreate",
				JobName: "recreate-test",
				PlanID:  "plan-test-recreate",
				Steps: []protocol.JobStep{
					{
						ID:     "step-1",
						Name:   "Echo Step",
						Type:   "script",
						Script: "echo 'recreated session successfully'",
					},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(jobSpec)

		case r.URL.Path == "/completejob" && r.Method == http.MethodPost:
			jobCompleted.Store(true)
			w.WriteHeader(http.StatusOK)

		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	configDir, key := createTestConfigDir(t, server.URL)
	encAESKey, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, &key.PublicKey, rawAESKey, nil)
	if err != nil {
		t.Fatalf("encrypt AES key: %v", err)
	}
	encKeyStr = base64.StdEncoding.EncodeToString(encAESKey)

	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	code := dispatchWithContext(ctx, []string{"run", "--config-dir", configDir, "--once"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected 0 exit code, got %d. stderr: %s", code, stderr.String())
	}

	if sessionCount.Load() < 2 {
		t.Errorf("expected at least 2 session creations (initial + recreation on invalid session), got %d", sessionCount.Load())
	}
	if !jobCompleted.Load() {
		t.Errorf("expected job to complete after session recreation")
	}
}

func pkcs7Pad(b []byte, blockSize int) []byte {
	padding := blockSize - (len(b) % blockSize)
	padtext := bytes.Repeat([]byte{byte(padding)}, padding)
	return append(b, padtext...)
}

func TestRunCommand_DefaultConfigDirFlag(t *testing.T) {
	cmd := newRunCmd()
	f := cmd.Flags().Lookup("config-dir")
	if f == nil {
		t.Fatal("expected --config-dir flag")
	}
	expectedDefault := config.DefaultConfigDir()
	if f.DefValue != expectedDefault {
		t.Fatalf("expected default config-dir %q, got %q", expectedDefault, f.DefValue)
	}
}

func TestRunCommand_ResolveConfigDir(t *testing.T) {
	defaultDir := config.DefaultConfigDir()

	// 1. Explicit non-default directory passed
	customDir := "/custom/test/dir"
	if got := resolveConfigDir(customDir, true); got != customDir {
		t.Fatalf("expected %q, got %q", customDir, got)
	}

	// 2. Explicit defaultDir passed with explicitlySet=true
	if got := resolveConfigDir(defaultDir, true); got != defaultDir {
		t.Fatalf("expected %q, got %q", defaultDir, got)
	}

	// 3. Empty string passed without local .runner
	if got := resolveConfigDir("", false); got != defaultDir {
		t.Fatalf("expected %q, got %q", defaultDir, got)
	}
}

func TestRunUnconfigured_NonInteractive(t *testing.T) {
	origIsTerminal := isTerminalFunc
	t.Cleanup(func() {
		isTerminalFunc = origIsTerminal
	})
	isTerminalFunc = func(fd uintptr) bool { return false }

	emptyDir := t.TempDir()
	var stdout, stderr bytes.Buffer
	ctx := context.Background()

	code := dispatchWithContext(ctx, []string{"run", "--config-dir", emptyDir}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit code when runner is not configured")
	}

	combined := stdout.String() + "\n" + stderr.String()
	expectedErr := "runner is not configured; run 'actr register' first"
	if !strings.Contains(combined, expectedErr) {
		t.Fatalf("expected output to contain %q, got: %s", expectedErr, combined)
	}
}

func TestRunUnconfigured_InteractivePromptDecline(t *testing.T) {
	origIsTerminal := isTerminalFunc
	t.Cleanup(func() {
		isTerminalFunc = origIsTerminal
	})
	isTerminalFunc = func(fd uintptr) bool { return true }

	emptyDir := t.TempDir()
	var stdout, stderr bytes.Buffer
	ctx := context.Background()
	stdin := strings.NewReader("n\n")

	code := dispatchWithContextAndIn(ctx, stdin, []string{"run", "--config-dir", emptyDir}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit code when registration is declined")
	}

	out := stdout.String()
	expectedPrompt := "Runner is not configured yet. Would you like to register now? [Y/n]"
	if !strings.Contains(out, expectedPrompt) {
		t.Fatalf("expected prompt %q in stdout, got: %s", expectedPrompt, out)
	}

	combined := out + "\n" + stderr.String()
	expectedErr := "runner is not configured; run 'actr register' first"
	if !strings.Contains(combined, expectedErr) {
		t.Fatalf("expected error %q, got: %s", expectedErr, combined)
	}
}

func TestRunUnconfigured_InteractivePromptAccept(t *testing.T) {
	origIsTerminal := isTerminalFunc
	origRunRegister := runRegisterCmdFunc
	t.Cleanup(func() {
		isTerminalFunc = origIsTerminal
		runRegisterCmdFunc = origRunRegister
	})
	isTerminalFunc = func(fd uintptr) bool { return true }

	registerCalled := false
	runRegisterCmdFunc = func(cmd *cobra.Command) error {
		registerCalled = true
		return fmt.Errorf("simulated registration failure")
	}

	emptyDir := t.TempDir()
	var stdout, stderr bytes.Buffer
	ctx := context.Background()
	stdin := strings.NewReader("y\n")

	code := dispatchWithContextAndIn(ctx, stdin, []string{"run", "--config-dir", emptyDir}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit code on simulated registration failure")
	}

	if !registerCalled {
		t.Fatalf("expected runRegisterCmdFunc to be called")
	}

	out := stdout.String()
	expectedPrompt := "Runner is not configured yet. Would you like to register now? [Y/n]"
	if !strings.Contains(out, expectedPrompt) {
		t.Fatalf("expected prompt %q in stdout, got: %s", expectedPrompt, out)
	}

	combined := out + "\n" + stderr.String()
	if !strings.Contains(combined, "simulated registration failure") {
		t.Fatalf("expected simulated registration failure in output, got: %s", combined)
	}
}

type recordingCoordinator struct {
	mu             sync.Mutex
	executedJob    *protocol.JobSpecification
	receivedLimits *config.LimitsConfig
	receivedDocker driver.Driver
	executeCalls   int
}

func (r *recordingCoordinator) ExecuteJob(ctx context.Context, job *protocol.JobSpecification, runServiceURL, token string) (*protocol.CompleteJobRequest, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.executedJob = job
	r.executeCalls++
	return &protocol.CompleteJobRequest{
		PlanID:     job.PlanID,
		JobID:      job.JobID,
		Conclusion: "success",
	}, nil
}

func (r *recordingCoordinator) SetLimits(l *config.LimitsConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.receivedLimits = l
}

func (r *recordingCoordinator) SetDockerDriver(d driver.Driver) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.receivedDocker = d
}

func TestRunCommand_HelpIncludesLimitAndDriverFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	ctx := context.Background()

	code := dispatchWithContext(ctx, []string{"run", "--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d. stderr: %s", code, stderr.String())
	}

	out := stdout.String()
	expectedFlags := []string{
		"--limit-cpu",
		"--limit-memory",
		"--limit-pids",
		"--driver",
	}
	for _, flag := range expectedFlags {
		if !strings.Contains(out, flag) {
			t.Errorf("expected help output to contain %q, got:\n%s", flag, out)
		}
	}
}

func TestRunCommand_LimitFlags_InvalidInputs(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		expectedErr string
	}{
		{
			name:        "invalid cpu string",
			args:        []string{"run", "--limit-cpu", "abc"},
			expectedErr: "invalid --limit-cpu",
		},
		{
			name:        "negative cpu",
			args:        []string{"run", "--limit-cpu", "-1"},
			expectedErr: "invalid --limit-cpu",
		},
		{
			name:        "invalid memory string",
			args:        []string{"run", "--limit-memory", "badmem"},
			expectedErr: "invalid --limit-memory",
		},
		{
			name:        "negative memory",
			args:        []string{"run", "--limit-memory", "-100MB"},
			expectedErr: "invalid --limit-memory",
		},
		{
			name:        "negative pids",
			args:        []string{"run", "--limit-pids", "-1"},
			expectedErr: "limit-pids",
		},
		{
			name:        "invalid driver",
			args:        []string{"run", "--driver", "containerd"},
			expectedErr: "invalid driver",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			ctx := context.Background()

			code := dispatchWithContext(ctx, tt.args, &stdout, &stderr)
			if code == 0 {
				t.Fatalf("expected non-zero exit code for invalid input %v, got 0", tt.args)
			}
			combined := stdout.String() + "\n" + stderr.String()
			if !strings.Contains(combined, tt.expectedErr) {
				t.Errorf("expected error %q, got: %s", tt.expectedErr, combined)
			}
		})
	}
}

func setupMockRunEnvironment(t *testing.T) (string, *httptest.Server, *rsa.PrivateKey, []byte) {
	t.Helper()
	rawAESKey := make([]byte, 32)
	if _, err := rand.Read(rawAESKey); err != nil {
		t.Fatalf("rand read: %v", err)
	}

	var encKeyStr string
	var msgCounter atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/session" && r.Method == http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sessionId": "mock-session-test",
				"encryptionKey": map[string]string{
					"value": encKeyStr,
				},
			})
		case r.URL.Path == "/session" && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/message" && r.Method == http.MethodGet:
			id := msgCounter.Add(1)
			refPayload := map[string]string{
				"runner_request_id": fmt.Sprintf("req-job-%d", id),
				"run_service_url":   "",
				"billing_owner_id":  "owner-test",
			}
			refBytes, _ := json.Marshal(refPayload)
			iv := make([]byte, aes.BlockSize)
			_, _ = rand.Read(iv)
			padded := pkcs7Pad(refBytes, aes.BlockSize)
			block, _ := aes.NewCipher(rawAESKey)
			enc := cipher.NewCBCEncrypter(block, iv)
			ciphertext := make([]byte, len(padded))
			enc.CryptBlocks(ciphertext, padded)

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"messageId":   int64(1000 + id),
				"messageType": "RunnerJobRequest",
				"iv":          base64.StdEncoding.EncodeToString(iv),
				"body":        base64.StdEncoding.EncodeToString(ciphertext),
			})
		case r.URL.Path == "/acknowledge" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/acquirejob" && r.Method == http.MethodPost:
			jobSpec := protocol.JobSpecification{
				JobID:   "job-test-limits",
				JobName: "test-limits",
				PlanID:  "plan-test-limits",
				Steps: []protocol.JobStep{
					{ID: "step-1", Name: "Echo", Type: "script", Script: "echo 'hello'"},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(jobSpec)
		case r.URL.Path == "/completejob" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))

	configDir, key := createTestConfigDir(t, server.URL)
	encAESKey, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, &key.PublicKey, rawAESKey, nil)
	if err != nil {
		t.Fatalf("encrypt AES key: %v", err)
	}
	encKeyStr = base64.StdEncoding.EncodeToString(encAESKey)

	return configDir, server, key, rawAESKey
}

func TestRunCommand_LimitFlags_ParsingAndCoordinatorWiring(t *testing.T) {
	configDir, server, _, _ := setupMockRunEnvironment(t)
	defer server.Close()

	recCoord := &recordingCoordinator{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = WithCoordinator(ctx, recCoord)

	var stdout, stderr bytes.Buffer
	args := []string{
		"run",
		"--config-dir", configDir,
		"--once",
		"--limit-cpu", "1.5",
		"--limit-memory", "4GiB",
		"--limit-pids", "512",
		"--driver", "host",
	}

	code := dispatchWithContext(ctx, args, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d. stderr: %s", code, stderr.String())
	}

	if recCoord.receivedLimits == nil {
		t.Fatal("expected coordinator to receive limits, got nil")
	}
	if recCoord.receivedLimits.CPU != 1.5 {
		t.Errorf("expected CPU 1.5, got %v", recCoord.receivedLimits.CPU)
	}
	expectedMem := int64(4 * 1024 * 1024 * 1024)
	if recCoord.receivedLimits.Memory != expectedMem {
		t.Errorf("expected Memory %d, got %d", expectedMem, recCoord.receivedLimits.Memory)
	}
	if recCoord.receivedLimits.Pids != 512 {
		t.Errorf("expected Pids 512, got %d", recCoord.receivedLimits.Pids)
	}
}

func TestRunCommand_LimitFlags_MillicoresAndMBParsing(t *testing.T) {
	configDir, server, _, _ := setupMockRunEnvironment(t)
	defer server.Close()

	recCoord := &recordingCoordinator{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = WithCoordinator(ctx, recCoord)

	var stdout, stderr bytes.Buffer
	args := []string{
		"run",
		"--config-dir", configDir,
		"--once",
		"--limit-cpu", "500m",
		"--limit-memory", "512MB",
		"--limit-pids", "128",
	}

	code := dispatchWithContext(ctx, args, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d. stderr: %s", code, stderr.String())
	}

	if recCoord.receivedLimits == nil {
		t.Fatal("expected coordinator to receive limits, got nil")
	}
	if recCoord.receivedLimits.CPU != 0.5 {
		t.Errorf("expected CPU 0.5 for 500m, got %v", recCoord.receivedLimits.CPU)
	}
	expectedMem := int64(512 * 1000 * 1000) // 512 MB decimal
	if recCoord.receivedLimits.Memory != expectedMem {
		t.Errorf("expected Memory %d, got %d", expectedMem, recCoord.receivedLimits.Memory)
	}
	if recCoord.receivedLimits.Pids != 128 {
		t.Errorf("expected Pids 128, got %d", recCoord.receivedLimits.Pids)
	}
}

func TestRunCommand_LimitFlags_Precedence_CLIOverConfig(t *testing.T) {
	configDir, server, _, _ := setupMockRunEnvironment(t)
	defer server.Close()

	// Write config.yaml into configDir
	configYAML := `
limits:
  cpu: "1.0"
  memory: "1GiB"
  pids: 100
driver:
  default: "host"
`
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(configYAML), 0644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	// 1. Without flags, should use config.yaml
	{
		recCoord := &recordingCoordinator{}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		ctx = WithCoordinator(ctx, recCoord)
		var stdout, stderr bytes.Buffer
		code := dispatchWithContext(ctx, []string{"run", "--config-dir", configDir, "--once"}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("expected exit code 0, got %d. stderr: %s", code, stderr.String())
		}
		if recCoord.receivedLimits == nil {
			t.Fatal("expected coordinator to receive limits from config.yaml")
		}
		if recCoord.receivedLimits.CPU != 1.0 {
			t.Errorf("expected CPU 1.0 from config.yaml, got %v", recCoord.receivedLimits.CPU)
		}
		if recCoord.receivedLimits.Memory != 1024*1024*1024 {
			t.Errorf("expected Memory 1GiB from config.yaml, got %v", recCoord.receivedLimits.Memory)
		}
		if recCoord.receivedLimits.Pids != 100 {
			t.Errorf("expected Pids 100 from config.yaml, got %v", recCoord.receivedLimits.Pids)
		}
	}

	// 2. With CLI flags, CLI flags should take precedence over config.yaml
	{
		recCoord := &recordingCoordinator{}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		ctx = WithCoordinator(ctx, recCoord)
		var stdout, stderr bytes.Buffer
		args := []string{
			"run",
			"--config-dir", configDir,
			"--once",
			"--limit-cpu", "2.5",
			"--limit-memory", "2GiB",
			"--limit-pids", "200",
		}
		code := dispatchWithContext(ctx, args, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("expected exit code 0, got %d. stderr: %s", code, stderr.String())
		}
		if recCoord.receivedLimits == nil {
			t.Fatal("expected coordinator to receive limits from CLI flags")
		}
		if recCoord.receivedLimits.CPU != 2.5 {
			t.Errorf("expected CLI CPU 2.5 to override config, got %v", recCoord.receivedLimits.CPU)
		}
		if recCoord.receivedLimits.Memory != 2*1024*1024*1024 {
			t.Errorf("expected CLI Memory 2GiB to override config, got %v", recCoord.receivedLimits.Memory)
		}
		if recCoord.receivedLimits.Pids != 200 {
			t.Errorf("expected CLI Pids 200 to override config, got %v", recCoord.receivedLimits.Pids)
		}
	}

	// 3. Env var takes precedence over config.yaml, and CLI flag takes precedence over env var
	{
		t.Setenv("ACTR_LIMIT_CPU", "3.0")
		recCoord := &recordingCoordinator{}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		ctx = WithCoordinator(ctx, recCoord)
		var stdout, stderr bytes.Buffer
		code := dispatchWithContext(ctx, []string{"run", "--config-dir", configDir, "--once"}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("expected exit code 0, got %d. stderr: %s", code, stderr.String())
		}
		if recCoord.receivedLimits.CPU != 3.0 {
			t.Errorf("expected ACTR_LIMIT_CPU 3.0 to override config, got %v", recCoord.receivedLimits.CPU)
		}

		// When flag is also provided, flag overrides env var
		recCoordFlag := &recordingCoordinator{}
		ctxFlag, cancelFlag := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelFlag()
		ctxFlag = WithCoordinator(ctxFlag, recCoordFlag)
		var stdoutFlag, stderrFlag bytes.Buffer
		codeFlag := dispatchWithContext(ctxFlag, []string{"run", "--config-dir", configDir, "--once", "--limit-cpu", "4.0"}, &stdoutFlag, &stderrFlag)
		if codeFlag != 0 {
			t.Fatalf("expected exit code 0, got %d. stderr: %s", codeFlag, stderrFlag.String())
		}
		if recCoordFlag.receivedLimits.CPU != 4.0 {
			t.Errorf("expected CLI flag 4.0 to override ACTR_LIMIT_CPU, got %v", recCoordFlag.receivedLimits.CPU)
		}
	}
}

func TestRunCommand_DockerDriver_UnreachableError(t *testing.T) {
	configDir, server, _, _ := setupMockRunEnvironment(t)
	defer server.Close()

	// Write config.yaml with custom non-existent socket
	configYAML := `
driver:
  default: "docker"
  docker:
    socket: "/tmp/nonexistent-actr-socket.sock"
`
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(configYAML), 0644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := dispatchWithContext(context.Background(), []string{"run", "--config-dir", configDir, "--driver", "docker", "--once"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit code when --driver docker is requested but unreachable")
	}

	combined := stdout.String() + "\n" + stderr.String()
	if !strings.Contains(combined, "Docker daemon is unreachable") && !strings.Contains(combined, "docker daemon is unreachable") && !strings.Contains(combined, "unreachable") {
		t.Errorf("expected unreachable error message, got: %s", combined)
	}
}

func TestRunCommand_DockerDriverWiring_MockClient(t *testing.T) {
	configDir, server, _, _ := setupMockRunEnvironment(t)
	defer server.Close()

	recCoord := &recordingCoordinator{}
	stubCli := &stubDockerClient{}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = WithCoordinator(ctx, recCoord)
	ctx = WithDockerClient(ctx, stubCli)

	var stdout, stderr bytes.Buffer
	code := dispatchWithContext(ctx, []string{"run", "--config-dir", configDir, "--once"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d. stderr: %s", code, stderr.String())
	}

	if recCoord.receivedDocker == nil {
		t.Fatal("expected coordinator to receive Docker driver from mock client, got nil")
	}
	if recCoord.receivedDocker.Name() != "docker" {
		t.Errorf("expected driver name 'docker', got %s", recCoord.receivedDocker.Name())
	}
}

func TestRunCmd_MultiRepoMode(t *testing.T) {
	tmpDir := t.TempDir()
	cfgYAML := `version: "1"
global:
  max_concurrent_jobs: 2
repos:
  aim:
    url: https://github.com/adrijshikhar/aim
`
	_ = os.WriteFile(filepath.Join(tmpDir, "config.yaml"), []byte(cfgYAML), 0644)

	// Create mock credentials for the repo
	repoDir := filepath.Join(tmpDir, "repos", "aim")
	_ = os.MkdirAll(repoDir, 0700)
	_ = config.SaveRunnerConfig(repoDir, &config.RunnerConfig{
		AgentID:    101,
		AgentName:  "aim-runner",
		PoolID:     1,
		ServerURL:  "https://github.com/adrijshikhar/aim",
		GitHubURL:  "https://github.com/adrijshikhar/aim",
		WorkFolder: filepath.Join(repoDir, "_work"),
	})
	_ = config.SaveCredentials(repoDir, &config.Credentials{
		ClientID:  "client-aim",
		IDToken:   "id-token-aim",
		SessionID: "session-aim",
	})
	rsaKey, _ := protocol.GenerateRSAKeyPair(2048)
	rsaBytes, _ := json.Marshal(protocol.ExportRSAParameters(rsaKey))
	_ = os.WriteFile(filepath.Join(repoDir, ".credentials_rsaparams"), rsaBytes, 0600)

	cmd := newRunCmd()
	cmd.SetArgs([]string{"--config-dir", tmpDir})

	// Verify detection logic identifies multi-repo mode
	isMulti := config.IsMultiRepoConfig(filepath.Join(tmpDir, "config.yaml"))
	if !isMulti {
		t.Fatal("expected IsMultiRepoConfig to return true")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	cmd.SetContext(ctx)

	err := cmd.Execute()
	if err != nil && strings.Contains(err.Error(), "run 'actr register' first") {
		t.Fatalf("expected multi-repo dispatch, but got single-repo missing .runner error: %v", err)
	}
}

func TestPurgeLegacyJSONRuns(t *testing.T) {
	// 1. Non-existent path returns nil
	nonExistent := filepath.Join(t.TempDir(), "nonexistent")
	if err := purgeLegacyJSONRuns(nonExistent); err != nil {
		t.Fatalf("expected nil for non-existent path, got: %v", err)
	}

	// 2. Empty directory is not deleted
	emptyDir := t.TempDir()
	if err := purgeLegacyJSONRuns(emptyDir); err != nil {
		t.Fatalf("expected nil for empty dir, got: %v", err)
	}
	if _, err := os.Stat(emptyDir); os.IsNotExist(err) {
		t.Fatalf("expected empty directory to not be deleted")
	}

	// 3. Unrelated directory without legacy markers is not deleted
	unrelatedDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(unrelatedDir, "somefile.txt"), []byte("data"), 0644)
	if err := purgeLegacyJSONRuns(unrelatedDir); err != nil {
		t.Fatalf("expected nil for unrelated dir, got: %v", err)
	}
	if _, err := os.Stat(unrelatedDir); os.IsNotExist(err) {
		t.Fatalf("expected unrelated directory to not be deleted")
	}

	// 4. Directory containing index.json is purged
	legacyIndexDir := filepath.Join(t.TempDir(), "runs")
	_ = os.MkdirAll(legacyIndexDir, 0755)
	_ = os.WriteFile(filepath.Join(legacyIndexDir, "index.json"), []byte(`{"runs":[]}`), 0644)
	if err := purgeLegacyJSONRuns(legacyIndexDir); err != nil {
		t.Fatalf("expected nil when purging legacy dir with index.json, got: %v", err)
	}
	if _, err := os.Stat(legacyIndexDir); !os.IsNotExist(err) {
		t.Fatalf("expected legacy index dir to be removed")
	}

	// 5. Directory containing run subfolder with meta.json is purged
	legacyRunsDir := filepath.Join(t.TempDir(), "runs")
	_ = os.MkdirAll(filepath.Join(legacyRunsDir, "flaky-integration-test"), 0755)
	_ = os.WriteFile(filepath.Join(legacyRunsDir, "flaky-integration-test", "meta.json"), []byte(`{"id":"flaky-integration-test"}`), 0644)
	if err := purgeLegacyJSONRuns(legacyRunsDir); err != nil {
		t.Fatalf("expected nil when purging legacy dir with meta.json, got: %v", err)
	}
	if _, err := os.Stat(legacyRunsDir); !os.IsNotExist(err) {
		t.Fatalf("expected legacy runs dir to be removed")
	}
}

func TestRunCommand_WiresSQLiteStoreAndPurgesLegacyRuns(t *testing.T) {
	configDir, server, _, _ := setupMockRunEnvironment(t)
	defer server.Close()

	// Seed legacy mock runs dir
	legacyRunsDir := filepath.Join(configDir, "runs")
	_ = os.MkdirAll(filepath.Join(legacyRunsDir, "flaky-integration-test"), 0755)
	_ = os.WriteFile(filepath.Join(legacyRunsDir, "index.json"), []byte(`{"runs":[]}`), 0644)
	_ = os.WriteFile(filepath.Join(legacyRunsDir, "flaky-integration-test", "meta.json"), []byte(`{"id":"flaky-integration-test"}`), 0644)

	recCoord := &recordingCoordinator{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = WithCoordinator(ctx, recCoord)

	var stdout, stderr bytes.Buffer
	code := dispatchWithContext(ctx, []string{"run", "--config-dir", configDir, "--once"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d. stderr: %s", code, stderr.String())
	}

	// 1. Verify legacy runs directory was purged
	if _, err := os.Stat(legacyRunsDir); !os.IsNotExist(err) {
		t.Fatalf("expected legacy runs dir %s to be purged, but it still exists", legacyRunsDir)
	}

	// 2. Verify history.db was initialized
	dbPath := filepath.Join(configDir, "history.db")
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("expected history.db to exist at %s, got err: %v", dbPath, err)
	}

	// 3. Verify SQLiteStore can open and query the database
	store, err := history.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("failed to open history.db: %v", err)
	}
	defer func() { _ = store.Close() }()

	_, err = store.ListRuns(10)
	if err != nil {
		t.Fatalf("failed to query runs from history.db: %v", err)
	}
}

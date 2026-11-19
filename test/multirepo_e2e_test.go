package test

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
	"github.com/adrijshikhar/actr/internal/monitor"
	"github.com/adrijshikhar/actr/internal/protocol"
	"github.com/adrijshikhar/actr/internal/supervisor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func pkcs7Pad(data []byte, blockSize int) []byte {
	padding := blockSize - (len(data) % blockSize)
	padtext := bytes.Repeat([]byte{byte(padding)}, padding)
	return append(data, padtext...)
}

type multiRepoMockServer struct {
	server       *httptest.Server
	aesKey       []byte
	encKeyHeader string
	msgCounters  map[string]*atomic.Int64
	activeJobs   atomic.Int32
	peakJobs     atomic.Int32
	completed    atomic.Int32
}

func newMultiRepoMockServer(t *testing.T, pubKey *rsa.PublicKey) *multiRepoMockServer {
	t.Helper()
	rawAESKey := make([]byte, 32)
	_, _ = rand.Read(rawAESKey)

	encAESKey, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, pubKey, rawAESKey, nil)
	require.NoError(t, err)

	ms := &multiRepoMockServer{
		aesKey:       rawAESKey,
		encKeyHeader: base64.StdEncoding.EncodeToString(encAESKey),
		msgCounters: map[string]*atomic.Int64{
			"aim":          {},
			"cxstatusline": {},
		},
	}

	ms.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		trim := strings.Trim(r.URL.Path, "/")
		parts := strings.Split(trim, "/")
		var repo, endpoint string
		if len(parts) >= 2 {
			repo = parts[0]
			endpoint = parts[len(parts)-1]
		} else if len(parts) == 1 {
			endpoint = parts[0]
		}

		switch endpoint {
		case "session":
			switch r.Method {
			case http.MethodPost:
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"sessionId": "mock-multi-session-" + repo,
					"encryptionKey": map[string]string{
						"value": ms.encKeyHeader,
					},
				})
			case http.MethodDelete:
				w.WriteHeader(http.StatusOK)
			default:
				http.Error(w, "bad method", http.StatusMethodNotAllowed)
			}
		case "message":
			w.Header().Set("Content-Type", "application/json")
			counter := ms.msgCounters[repo]
			if counter == nil {
				counter = &atomic.Int64{}
				ms.msgCounters[repo] = counter
			}

			msgNum := counter.Add(1)
			if msgNum > 1 {
				w.WriteHeader(http.StatusNoContent)
				return
			}

			jobID := fmt.Sprintf("job-%s-%d", repo, msgNum)
			refPayload := map[string]string{
				"runner_request_id": jobID,
				"run_service_url":   "",
				"billing_owner_id":  "owner-" + repo,
			}
			refBytes, _ := json.Marshal(refPayload)

			iv := make([]byte, aes.BlockSize)
			_, _ = rand.Read(iv)
			padded := pkcs7Pad(refBytes, aes.BlockSize)
			block, _ := aes.NewCipher(ms.aesKey)
			enc := cipher.NewCBCEncrypter(block, iv)
			ciphertext := make([]byte, len(padded))
			enc.CryptBlocks(ciphertext, padded)

			_ = json.NewEncoder(w).Encode(map[string]any{
				"messageId":   int64(2000 + msgNum),
				"messageType": "RunnerJobRequest",
				"iv":          base64.StdEncoding.EncodeToString(iv),
				"body":        base64.StdEncoding.EncodeToString(ciphertext),
			})
		case "acknowledge":
			w.WriteHeader(http.StatusOK)
		case "acquirejob":
			cur := ms.activeJobs.Add(1)
			for {
				peak := ms.peakJobs.Load()
				if cur <= peak || ms.peakJobs.CompareAndSwap(peak, cur) {
					break
				}
			}

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(protocol.JobSpecification{
				JobID:   "job-e2e-" + repo,
				JobName: "E2E Workflow " + repo,
				PlanID:  "plan-e2e-" + repo,
				Steps: []protocol.JobStep{
					{
						ID:     "step-1",
						Name:   "Echo Step",
						Type:   "script",
						Script: "echo 'hello from multi-repo e2e'",
					},
				},
			})
		case "completejob":
			ms.activeJobs.Add(-1)
			ms.completed.Add(1)
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	return ms
}

func setupRepoCredentials(t *testing.T, baseDir, alias, serverURL string, key *rsa.PrivateKey) string {
	t.Helper()
	repoDir := filepath.Join(baseDir, "repos", alias)
	require.NoError(t, os.MkdirAll(repoDir, 0700))

	runnerCfg := &config.RunnerConfig{
		AgentID:    int64(100 + len(alias)),
		AgentName:  alias + "-runner",
		PoolID:     1,
		ServerURL:  serverURL + "/" + alias,
		GitHubURL:  "https://github.com/adrijshikhar/" + alias,
		WorkFolder: filepath.Join(repoDir, "_work"),
	}
	require.NoError(t, config.SaveRunnerConfig(repoDir, runnerCfg))

	creds := &config.Credentials{
		ClientID:  "client-" + alias,
		IDToken:   "id-token-" + alias,
		SessionID: "session-" + alias,
	}
	require.NoError(t, config.SaveCredentials(repoDir, creds))

	rsaParams := protocol.ExportRSAParameters(key)
	rsaBytes, err := json.Marshal(rsaParams)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, ".credentials_rsaparams"), rsaBytes, 0600))

	return repoDir
}

// TestMultiRepo_EndToEnd_ConcurrentDispatch verifies that two distinct repositories
// ('aim' and 'cxstatusline') dispatch and execute jobs concurrently under the single
// supervisor daemon without token contention or deadlock.
func TestMultiRepo_EndToEnd_ConcurrentDispatch(t *testing.T) {
	key, err := protocol.GenerateRSAKeyPair(2048)
	require.NoError(t, err)

	mockServer := newMultiRepoMockServer(t, &key.PublicKey)
	defer mockServer.server.Close()

	tmpDir := t.TempDir()
	sockPath := testSocketPath(t)

	aimDir := setupRepoCredentials(t, tmpDir, "aim", mockServer.server.URL, key)
	cxDir := setupRepoCredentials(t, tmpDir, "cxstatusline", mockServer.server.URL, key)

	supCfg := &config.SupervisorConfig{
		Version: "1",
		Global: config.GlobalConfig{
			MaxConcurrentJobs: 2,
		},
		Repos: map[string]config.RepoConfig{
			"aim": {
				URL:               "https://github.com/adrijshikhar/aim",
				ConfigDir:         aimDir,
				MaxConcurrentJobs: 1,
				Limits: config.LimitsYAMLConfig{
					CPU:    "4.0",
					Memory: "8GB",
				},
			},
			"cxstatusline": {
				URL:               "https://github.com/adrijshikhar/cxstatusline",
				ConfigDir:         cxDir,
				MaxConcurrentJobs: 1,
				Limits: config.LimitsYAMLConfig{
					CPU:    "2.0",
					Memory: "3GB",
				},
			},
		},
	}

	sup, err := supervisor.NewSupervisor(supCfg, sockPath)
	require.NoError(t, err)

	// Custom mock coordinators to simulate measurable execution time for overlap verification
	var executingJobs atomic.Int32
	var peakExecuting atomic.Int32
	var jobCompletionWg sync.WaitGroup
	jobCompletionWg.Add(2)

	for alias, worker := range sup.Workers() {
		repoName := alias
		worker.SetCoordinator(&testMockCoordinator{
			executeFn: func(ctx context.Context, job *protocol.JobSpecification, runServiceURL, token string) (*protocol.CompleteJobRequest, error) {
				cur := executingJobs.Add(1)
				for {
					peak := peakExecuting.Load()
					if cur <= peak || peakExecuting.CompareAndSwap(peak, cur) {
						break
					}
				}

				// Simulate job execution work
				time.Sleep(100 * time.Millisecond)

				executingJobs.Add(-1)
				jobCompletionWg.Done()
				return &protocol.CompleteJobRequest{Conclusion: "success"}, nil
			},
			repoName: repoName,
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- sup.Start(ctx)
	}()

	// Wait for both jobs to execute and complete
	jobCompletionWg.Wait()

	// Verify peak concurrent execution reached 2
	assert.Equal(t, int32(2), peakExecuting.Load(), "both repositories should execute concurrently")

	// Verify IPC snapshot status
	client := monitor.NewClient(sockPath)
	snap, err := client.GetSnapshot(context.Background())
	require.NoError(t, err)
	require.NotNil(t, snap.SupervisorStatus)
	assert.Equal(t, 2, snap.SupervisorStatus.GlobalMaxJobs)

	slots := make(map[string]*monitor.RepoSlotInfo)
	for _, s := range snap.SupervisorStatus.RepoSlots {
		slots[s.Alias] = s
	}
	assert.Contains(t, slots, "aim")
	assert.Contains(t, slots, "cxstatusline")

	// Trigger graceful cancellation
	cancel()
	select {
	case err := <-errCh:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("supervisor did not shut down in time")
	}
}

// TestMultiRepo_EndToEnd_GlobalCapacityGovernance verifies that when global
// capacity is restricted to 1, multiple repositories queue up and peak concurrency
// strictly remains 1.
func TestMultiRepo_EndToEnd_GlobalCapacityGovernance(t *testing.T) {
	key, err := protocol.GenerateRSAKeyPair(2048)
	require.NoError(t, err)

	mockServer := newMultiRepoMockServer(t, &key.PublicKey)
	defer mockServer.server.Close()

	tmpDir := t.TempDir()
	sockPath := testSocketPath(t)

	aimDir := setupRepoCredentials(t, tmpDir, "aim", mockServer.server.URL, key)
	cxDir := setupRepoCredentials(t, tmpDir, "cxstatusline", mockServer.server.URL, key)

	// Global concurrency is 1, so only 1 job can run at a time across all repos
	supCfg := &config.SupervisorConfig{
		Version: "1",
		Global: config.GlobalConfig{
			MaxConcurrentJobs: 1,
		},
		Repos: map[string]config.RepoConfig{
			"aim": {
				URL:               "https://github.com/adrijshikhar/aim",
				ConfigDir:         aimDir,
				MaxConcurrentJobs: 1,
			},
			"cxstatusline": {
				URL:               "https://github.com/adrijshikhar/cxstatusline",
				ConfigDir:         cxDir,
				MaxConcurrentJobs: 1,
			},
		},
	}

	sup, err := supervisor.NewSupervisor(supCfg, sockPath)
	require.NoError(t, err)

	var executingJobs atomic.Int32
	var peakExecuting atomic.Int32
	var jobCompletionWg sync.WaitGroup
	jobCompletionWg.Add(2)

	for _, worker := range sup.Workers() {
		worker.SetCoordinator(&testMockCoordinator{
			executeFn: func(ctx context.Context, job *protocol.JobSpecification, runServiceURL, token string) (*protocol.CompleteJobRequest, error) {
				cur := executingJobs.Add(1)
				for {
					peak := peakExecuting.Load()
					if cur <= peak || peakExecuting.CompareAndSwap(peak, cur) {
						break
					}
				}

				time.Sleep(100 * time.Millisecond)

				executingJobs.Add(-1)
				jobCompletionWg.Done()
				return &protocol.CompleteJobRequest{Conclusion: "success"}, nil
			},
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- sup.Start(ctx)
	}()

	jobCompletionWg.Wait()

	// Under max_concurrent_jobs: 1, peak concurrency must strictly be 1
	assert.Equal(t, int32(1), peakExecuting.Load(), "global capacity limit of 1 must be respected")

	cancel()
	select {
	case err := <-errCh:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("supervisor did not shut down in time")
	}
}

// TestMultiRepo_EndToEnd_IPCReload verifies that dynamic reload via IPC POST /reload
// updates global capacity and worker configurations seamlessly.
func TestMultiRepo_EndToEnd_IPCReload(t *testing.T) {
	key, err := protocol.GenerateRSAKeyPair(2048)
	require.NoError(t, err)

	mockServer := newMultiRepoMockServer(t, &key.PublicKey)
	defer mockServer.server.Close()

	tmpDir := t.TempDir()
	sockPath := testSocketPath(t)

	aimDir := setupRepoCredentials(t, tmpDir, "aim", mockServer.server.URL, key)
	cfgPath := filepath.Join(tmpDir, "config.yaml")

	supCfg := &config.SupervisorConfig{
		Version: "1",
		Global: config.GlobalConfig{
			MaxConcurrentJobs: 2,
		},
		Repos: map[string]config.RepoConfig{
			"aim": {
				URL:               "https://github.com/adrijshikhar/aim",
				ConfigDir:         aimDir,
				MaxConcurrentJobs: 1,
			},
		},
	}
	require.NoError(t, config.SaveSupervisorConfig(cfgPath, supCfg))

	sup, err := supervisor.NewSupervisor(supCfg, sockPath)
	require.NoError(t, err)

	// Hook reload function to reload from file
	sup.MonitorServer().WithReloadFunc(func() error {
		newCfg, err := config.LoadSupervisorConfig(cfgPath)
		if err != nil {
			return err
		}
		return sup.Reload(newCfg)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- sup.Start(ctx)
	}()

	// Wait for server to start listening
	time.Sleep(100 * time.Millisecond)

	client := monitor.NewClient(sockPath)
	snap, err := client.GetSnapshot(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, snap.SupervisorStatus.GlobalMaxJobs)
	assert.Len(t, snap.SupervisorStatus.RepoSlots, 1)

	// Now add cxstatusline to config.yaml and increase MaxConcurrentJobs to 4
	cxDir := setupRepoCredentials(t, tmpDir, "cxstatusline", mockServer.server.URL, key)
	supCfg.Global.MaxConcurrentJobs = 4
	supCfg.Repos["cxstatusline"] = config.RepoConfig{
		URL:               "https://github.com/adrijshikhar/cxstatusline",
		ConfigDir:         cxDir,
		MaxConcurrentJobs: 2,
	}
	require.NoError(t, config.SaveSupervisorConfig(cfgPath, supCfg))

	// Send POST /reload over unix socket
	require.NoError(t, client.Reload(ctx))

	// Verify updated snapshot reflects changes immediately
	snapUpdated, err := client.GetSnapshot(ctx)
	require.NoError(t, err)
	assert.Equal(t, 4, snapUpdated.SupervisorStatus.GlobalMaxJobs)

	slotsUpdated := make(map[string]*monitor.RepoSlotInfo)
	for _, s := range snapUpdated.SupervisorStatus.RepoSlots {
		slotsUpdated[s.Alias] = s
	}
	assert.Contains(t, slotsUpdated, "aim")
	assert.Contains(t, slotsUpdated, "cxstatusline")

	cancel()
	select {
	case err := <-errCh:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("supervisor did not shut down in time")
	}
}

type testMockCoordinator struct {
	executeFn func(ctx context.Context, job *protocol.JobSpecification, runServiceURL, token string) (*protocol.CompleteJobRequest, error)
	repoName  string
}

func (m *testMockCoordinator) ExecuteJob(ctx context.Context, job *protocol.JobSpecification, runServiceURL, token string) (*protocol.CompleteJobRequest, error) {
	if m.executeFn != nil {
		return m.executeFn(ctx, job, runServiceURL, token)
	}
	return &protocol.CompleteJobRequest{Conclusion: "success"}, nil
}

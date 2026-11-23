package main

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/adrijshikhar/actr/internal/config"
	"github.com/adrijshikhar/actr/internal/history"
	"github.com/adrijshikhar/actr/internal/monitor"
	"github.com/adrijshikhar/actr/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWebCmd_Flags(t *testing.T) {
	cmd := newWebCmd()

	// Verify flag defaults
	portFlag := cmd.Flag("port")
	require.NotNil(t, portFlag, "port flag should exist")
	assert.Equal(t, "18490", portFlag.DefValue)
	assert.Equal(t, "p", portFlag.Shorthand)

	noOpenFlag := cmd.Flag("no-open")
	require.NotNil(t, noOpenFlag, "no-open flag should exist")
	assert.Equal(t, "false", noOpenFlag.DefValue)

	fgFlag := cmd.Flag("foreground")
	require.NotNil(t, fgFlag, "foreground flag should exist")
	assert.Equal(t, "false", fgFlag.DefValue)
	assert.Equal(t, "f", fgFlag.Shorthand)

	devFlag := cmd.Flag("dev")
	require.NotNil(t, devFlag, "dev flag should exist")
	assert.Equal(t, "false", devFlag.DefValue)

	cfgDirFlag := cmd.Flag("config-dir")
	require.NotNil(t, cfgDirFlag, "config-dir flag should exist")
	assert.Equal(t, config.DefaultConfigDir(), cfgDirFlag.DefValue)

	sockFlag := cmd.Flag("sock")
	require.NotNil(t, sockFlag, "sock flag should exist")
	assert.Equal(t, "", sockFlag.DefValue)

	// Check subcommands
	subCmds := make(map[string]bool)
	for _, c := range cmd.Commands() {
		subCmds[c.Name()] = true
	}
	assert.True(t, subCmds["start"], "expected start subcommand")
	assert.True(t, subCmds["status"], "expected status subcommand")
	assert.True(t, subCmds["stop"], "expected stop subcommand")
}

func TestWebCmd_Help(t *testing.T) {
	buf := new(bytes.Buffer)
	errBuf := new(bytes.Buffer)
	root := newRootCmd(buf, errBuf)
	root.SetOut(buf)
	root.SetErr(errBuf)
	root.SetArgs([]string{"web", "--help"})

	err := root.Execute()
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "web dashboard")
	assert.Contains(t, out, "--port")
	assert.Contains(t, out, "--dev")
	assert.Contains(t, out, "--no-open")
	assert.Contains(t, out, "--foreground")
	assert.Contains(t, out, "--config-dir")
	assert.Contains(t, out, "--sock")
	assert.Contains(t, out, "18490")
}

func TestWebCmd_DefaultPortEnv(t *testing.T) {
	// Without env var
	t.Setenv("ACTR_WEB_PORT", "")
	assert.Equal(t, defaultWebPort, resolveDefaultPort())

	// With valid env var
	t.Setenv("ACTR_WEB_PORT", "19999")
	assert.Equal(t, 19999, resolveDefaultPort())

	// With invalid env var
	t.Setenv("ACTR_WEB_PORT", "not-a-number")
	assert.Equal(t, defaultWebPort, resolveDefaultPort())

	// With out-of-range env var
	t.Setenv("ACTR_WEB_PORT", "999999")
	assert.Equal(t, defaultWebPort, resolveDefaultPort())
}

func TestWebCmd_PIDLifecycle(t *testing.T) {
	tmpDir := t.TempDir()

	// 1. Read non-existent PID file
	info, err := readWebPID(tmpDir)
	assert.Nil(t, info)
	assert.True(t, os.IsNotExist(err))

	// 2. Write valid PID file
	err = writeWebPID(tmpDir, 4242, 18490)
	require.NoError(t, err)

	pidPath := filepath.Join(tmpDir, "web.pid")
	assert.FileExists(t, pidPath)

	// 3. Read back PID file
	info, err = readWebPID(tmpDir)
	require.NoError(t, err)
	require.NotNil(t, info)
	assert.Equal(t, 4242, info.PID)
	assert.Equal(t, 18490, info.Port)
	assert.False(t, info.StartedAt.IsZero())

	// 4. Remove own web PID when PID does not match current process
	err = removeOwnWebPID(tmpDir)
	require.NoError(t, err)
	assert.FileExists(t, pidPath, "PID should not be removed when PID does not match os.Getpid()")

	// 5. Remove own web PID when PID matches current process
	err = writeWebPID(tmpDir, os.Getpid(), 18490)
	require.NoError(t, err)
	err = removeOwnWebPID(tmpDir)
	require.NoError(t, err)
	assert.NoFileExists(t, pidPath, "PID should be removed when matching os.Getpid()")

	// 6. Direct removeWebPID
	err = writeWebPID(tmpDir, 1234, 18490)
	require.NoError(t, err)
	err = removeWebPID(tmpDir)
	require.NoError(t, err)
	assert.NoFileExists(t, pidPath)

	// 7. Removing already removed PID should not return error
	err = removeWebPID(tmpDir)
	assert.NoError(t, err)

	// 8. Corrupted PID file
	err = os.WriteFile(pidPath, []byte("invalid-json"), 0600)
	require.NoError(t, err)
	info, err = readWebPID(tmpDir)
	assert.Error(t, err)
	assert.Nil(t, info)
}

func TestWebCmd_HealthCheck(t *testing.T) {
	// Server responding with 200 OK
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/status" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"version":"v0.2.0"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	_, portStr, err := net.SplitHostPort(ts.Listener.Addr().String())
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)

	assert.True(t, checkServerHealth(port))

	// Server responding with 500 error
	tsErr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer tsErr.Close()

	_, portStrErr, err := net.SplitHostPort(tsErr.Listener.Addr().String())
	require.NoError(t, err)
	portErr, err := strconv.Atoi(portStrErr)
	require.NoError(t, err)

	assert.False(t, checkServerHealth(portErr))

	// Port with nothing listening
	assert.False(t, checkServerHealth(1))
}

func TestWebCmd_StatusAndStop(t *testing.T) {
	tmpDir := t.TempDir()

	// 1. Status when stopped
	buf := new(bytes.Buffer)
	cmdStatus := newWebStatusCmd()
	cmdStatus.SetOut(buf)
	cmdStatus.SetArgs([]string{"--config-dir", tmpDir})

	err := cmdStatus.Execute()
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "actr web dashboard is stopped")

	// 2. Stop when stopped
	buf.Reset()
	cmdStop := newWebStopCmd()
	cmdStop.SetOut(buf)
	cmdStop.SetArgs([]string{"--config-dir", tmpDir})

	err = cmdStop.Execute()
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "not running")

	// 3. Stale PID file (dead process PID)
	err = writeWebPID(tmpDir, 9999999, 18490)
	require.NoError(t, err)

	buf.Reset()
	cmdStatus = newWebStatusCmd()
	cmdStatus.SetOut(buf)
	cmdStatus.SetArgs([]string{"--config-dir", tmpDir})
	err = cmdStatus.Execute()
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "actr web dashboard is stopped")
	// Stale PID file should be removed
	assert.NoFileExists(t, filepath.Join(tmpDir, "web.pid"))

	// 4. Fake dead PID file and stop
	err = writeWebPID(tmpDir, 9999999, 18490)
	require.NoError(t, err)

	buf.Reset()
	cmdStop = newWebStopCmd()
	cmdStop.SetOut(buf)
	cmdStop.SetArgs([]string{"--config-dir", tmpDir})
	err = cmdStop.Execute()
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "not running")
	assert.NoFileExists(t, filepath.Join(tmpDir, "web.pid"))
}

func TestWebCmd_ExecutionForeground(t *testing.T) {
	tmpDir := t.TempDir()

	cmd := newWebCmd()

	// Mock browser open to avoid opening browser during test
	origBrowser := openBrowserFunc
	browserOpened := false
	openBrowserFunc = func(url string) error {
		browserOpened = true
		return nil
	}
	defer func() {
		openBrowserFunc = origBrowser
	}()

	ctx, cancel := context.WithCancel(context.Background())
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetArgs([]string{"--foreground", "--port", "0", "--no-open", "--config-dir", tmpDir})

	done := make(chan error, 1)
	go func() {
		done <- cmd.ExecuteContext(ctx)
	}()

	// Wait for PID file to be written
	pidPath := filepath.Join(tmpDir, "web.pid")
	var pidErr error
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("webCmd exited prematurely: %v (output: %s)", err, buf.String())
		default:
		}
		if _, pidErr = os.Stat(pidPath); pidErr == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.NoError(t, pidErr, "PID file should be written in foreground mode")

	info, err := readWebPID(tmpDir)
	require.NoError(t, err)
	require.NotNil(t, info)
	assert.Equal(t, os.Getpid(), info.PID)
	assert.Greater(t, info.Port, 0)

	// Verify server health endpoint is responding
	assert.True(t, checkServerHealth(info.Port))

	// Stop the foreground server
	cancel()

	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("webCmd did not shut down within timeout")
	}

	assert.False(t, browserOpened, "browser should not have been opened with --no-open")

	// PID file should be removed on shutdown
	assert.NoFileExists(t, pidPath)
}

func TestWebCmd_StartSubcommand(t *testing.T) {
	tmpDir := t.TempDir()

	cmd := newWebStartCmd()

	ctx, cancel := context.WithCancel(context.Background())
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetArgs([]string{"--foreground", "--port", "0", "--no-open", "--config-dir", tmpDir})

	done := make(chan error, 1)
	go func() {
		done <- cmd.ExecuteContext(ctx)
	}()

	pidPath := filepath.Join(tmpDir, "web.pid")
	var pidErr error
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("webStartCmd exited prematurely: %v", err)
		default:
		}
		if _, pidErr = os.Stat(pidPath); pidErr == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.NoError(t, pidErr)

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("webStartCmd did not shut down within timeout")
	}
}

func TestWebCmd_Foreground_AlreadyRunning(t *testing.T) {
	tmpDir := t.TempDir()

	// Write PID of current process (alive)
	err := writeWebPID(tmpDir, os.Getpid(), 18490)
	require.NoError(t, err)

	cmd := newWebCmd()
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetArgs([]string{"--foreground", "--port", "0", "--config-dir", tmpDir})

	err = cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already running")
}

func TestWebCmd_Background_AlreadyRunning(t *testing.T) {
	tmpDir := t.TempDir()

	// Mock server running at a test port
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"version":"v0.2.0"}`))
	}))
	defer ts.Close()

	_, portStr, err := net.SplitHostPort(ts.Listener.Addr().String())
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)

	// Write PID of current process (alive) pointing to this test server
	err = writeWebPID(tmpDir, os.Getpid(), port)
	require.NoError(t, err)

	cmd := newWebCmd()
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetArgs([]string{"--config-dir", tmpDir, "--no-open"})

	err = cmd.Execute()
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "already running")
}

func TestWebCmd_Adapters(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "history.db")
	store, err := history.NewSQLiteStore(dbPath)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	// Record a run in sqlite store
	runMeta := &monitor.RunMetadata{
		ID:        "run-web-adapter-1",
		Name:      "Adapter Run",
		Workflow:  "test.yml",
		Repo:      "test/repo",
		RepoAlias: "test",
		Status:    monitor.StatusSuccess,
		StartedAt: time.Now().Add(-10 * time.Minute),
		EndedAt:   time.Now(),
		Steps: []*monitor.StepMetadata{
			{ID: "step-1", Name: "Step 1", Status: monitor.StatusSuccess},
		},
	}
	err = store.RecordRun(runMeta)
	require.NoError(t, err)
	err = store.AppendStepLogs("run-web-adapter-1", "step-1", []string{"log line 1", "log line 2"})
	require.NoError(t, err)

	adapter := &historyRunStoreAdapter{store: store}
	ctx := context.Background()

	// 1. GetRuns
	runs, total, err := adapter.GetRuns(ctx, 10, 0, "", "", "")
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, runs, 1)
	assert.Equal(t, "run-web-adapter-1", runs[0].ID)

	// Filter by repo
	runs, total, err = adapter.GetRuns(ctx, 10, 0, "other/repo", "", "")
	require.NoError(t, err)
	assert.Equal(t, 0, total)
	assert.Empty(t, runs)

	// Filter by status
	_, total, err = adapter.GetRuns(ctx, 10, 0, "", "failure", "")
	require.NoError(t, err)
	assert.Equal(t, 0, total)

	// Filter by query
	runs, total, err = adapter.GetRuns(ctx, 10, 0, "", "", "Adapter")
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, runs, 1)

	// 2. GetRun
	rec, err := adapter.GetRun(ctx, "run-web-adapter-1")
	require.NoError(t, err)
	assert.Equal(t, "run-web-adapter-1", rec.ID)

	// 3. GetRunSteps
	steps, err := adapter.GetRunSteps(ctx, "run-web-adapter-1")
	require.NoError(t, err)
	require.Len(t, steps, 1)
	assert.Equal(t, "step-1", steps[0].ID)

	// 4. GetStepLogs
	logs, err := adapter.GetStepLogs(ctx, "step-1")
	require.NoError(t, err)
	assert.Equal(t, []string{"log line 1", "log line 2"}, logs)

	// Non-existent step logs
	nonLogs, err := adapter.GetStepLogs(ctx, "non-existent")
	require.NoError(t, err)
	assert.Empty(t, nonLogs)

	// 5. GetRunMetrics
	metrics, err := adapter.GetRunMetrics(ctx, "run-web-adapter-1")
	require.NoError(t, err)
	assert.Empty(t, metrics)

	// 6. Service daemon controller
	ctrl := &serviceDaemonController{}
	status, err := ctrl.Status()
	// Service status may be not installed or stopped in test env
	assert.NoError(t, err)
	assert.NotEmpty(t, status)

	// 7. Vault Secret Manager Adapter with mock provider
	mockVault := vault.NewMockProvider(nil)
	vAdapter := &vaultSecretManagerAdapter{provider: mockVault}
	err = vAdapter.SetSecret(ctx, "TEST_KEY", "secret-val", "")
	require.NoError(t, err)
	list, err := vAdapter.ListSecrets(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "TEST_KEY", list[0].Key)
	err = vAdapter.DeleteSecret(ctx, "TEST_KEY")
	require.NoError(t, err)

	// 8. Monitor Event Subscriber Adapter
	mClient := monitor.NewClient(filepath.Join(tmpDir, "nonexistent.sock"))
	subAdapter := &monitorEventSubscriberAdapter{client: mClient}
	evCh, cancelSub, err := subAdapter.SubscribeEvents(ctx)
	require.NoError(t, err)
	require.NotNil(t, evCh)
	require.NotNil(t, cancelSub)
	cancelSub()
}

func TestWebCmd_ResolveVaultForDir(t *testing.T) {
	tmpDir := t.TempDir()

	// 1. Empty dir returns default provider
	p, err := resolveVaultForDir(tmpDir)
	require.NoError(t, err)
	assert.NotNil(t, p)

	// 2. Dir with config.yaml specifying encrypted-file provider
	cfgContent := `
version: "1"
vault:
  providers:
    - type: encrypted-file
      path: secrets.enc
`
	err = os.WriteFile(filepath.Join(tmpDir, "config.yaml"), []byte(cfgContent), 0600)
	require.NoError(t, err)

	p, err = resolveVaultForDir(tmpDir)
	require.NoError(t, err)
	assert.NotNil(t, p)
}

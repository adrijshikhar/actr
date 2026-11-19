package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adrijshikhar/actr/internal/config"
	"github.com/adrijshikhar/actr/internal/protocol"
	"github.com/adrijshikhar/actr/internal/registration"
)

func newMockRegistrationServer(t *testing.T, expectedToken *string) (*httptest.Server, *string, *string) {
	t.Helper()
	var capturedAuth1 string
	var capturedAuth2 string
	var serverPrefix string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if r.URL.Path == "/actions/runner-registration" {
			capturedAuth1 = r.Header.Get("Authorization")
			if expectedToken != nil {
				expectedHeader := "RemoteAuth " + *expectedToken
				if capturedAuth1 != expectedHeader {
					http.Error(w, fmt.Sprintf("expected auth header %q, got %q", expectedHeader, capturedAuth1), http.StatusUnauthorized)
					return
				}
			}
			resp := protocol.RunnerAuthResponse{
				Token:       "tenant-oauth-token",
				TokenSchema: "OAuthAccessToken",
				URL:         serverPrefix + "/tenant/",
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		if strings.HasPrefix(r.URL.Path, "/tenant/_apis/distributedtask/pools/1/agents") {
			capturedAuth2 = r.Header.Get("Authorization")
			var req protocol.TaskAgentRegisterRequest
			if err := json.Unmarshal(body, &req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			resp := map[string]any{
				"id":   int64(4242),
				"name": req.Name,
				"authorization": map[string]any{
					"authorization_url": "https://auth.actions.githubusercontent.com/token",
					"client_id":         "client-uuid-4242",
				},
				"properties": map[string]any{
					"ServerUrl": map[string]any{
						"$type":  "System.String",
						"$value": "https://pipelines.actions.githubusercontent.com/",
					},
					"RequireFipsCryptography": map[string]any{
						"$type":  "System.Boolean",
						"$value": true,
					},
					"ServerUrlV2": map[string]any{
						"$type":  "System.String",
						"$value": "https://broker.actions.githubusercontent.com/",
					},
					"UseV2Flow": map[string]any{
						"$type":  "System.Boolean",
						"$value": true,
					},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		http.NotFound(w, r)
	}))
	serverPrefix = ts.URL
	return ts, &capturedAuth1, &capturedAuth2
}

func TestRegisterFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	ctx := context.Background()

	code := dispatchWithContext(ctx, []string{"register", "--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected code 0, got %d", code)
	}
	out := stdout.String()
	for _, flag := range []string{"--url", "--token", "--name", "--config-dir", "--work-folder", "--force"} {
		if !strings.Contains(out, flag) {
			t.Fatalf("missing expected flag %s in register help: %s", flag, out)
		}
	}

	cmd := newRegisterCmd()
	f := cmd.Flag("config-dir")
	if f == nil {
		t.Fatalf("missing flag config-dir")
	}
	expectedDefault := config.DefaultConfigDir()
	if f.DefValue != expectedDefault {
		t.Fatalf("expected default config-dir %q, got %q", expectedDefault, f.DefValue)
	}
}

func TestRegister_MissingRequiredFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	ctx := context.Background()

	// Missing both
	code := dispatchWithContext(ctx, []string{"register"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit code when --url and --token are missing")
	}

	// Missing token
	stdout.Reset()
	stderr.Reset()
	code = dispatchWithContext(ctx, []string{"register", "--url", "https://github.com/my-org/my-repo"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit code when --token is missing")
	}

	// Missing url
	stdout.Reset()
	stderr.Reset()
	code = dispatchWithContext(ctx, []string{"register", "--token", "secret-token"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit code when --url is missing")
	}
}

func TestRegister_Success(t *testing.T) {
	ts, capturedAuth1, capturedAuth2 := newMockRegistrationServer(t, nil)
	defer ts.Close()

	tmpDir := t.TempDir()
	var stdout, stderr bytes.Buffer
	ctx := WithHTTPClient(context.Background(), ts.Client())
	repoURL := ts.URL + "/my-org/my-repo"

	code := dispatchWithContext(ctx, []string{
		"register",
		"--url", repoURL,
		"--token", "secret-token",
		"--name", "custom-runner",
		"--config-dir", tmpDir,
		"--work-folder", "my-work",
	}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("expected code 0, got %d. stderr: %s", code, stderr.String())
	}

	if *capturedAuth1 != "RemoteAuth secret-token" {
		t.Errorf("unexpected authorization header on step 1: %s", *capturedAuth1)
	}
	if *capturedAuth2 != "Bearer tenant-oauth-token" {
		t.Errorf("unexpected authorization header on step 2: %s", *capturedAuth2)
	}

	expectedPrefix := "Successfully registered runner 'custom-runner' (ID 4242) for " + repoURL
	if !strings.Contains(stdout.String(), expectedPrefix) {
		t.Fatalf("expected stdout containing %q, got: %q", expectedPrefix, stdout.String())
	}
	expectedSaved := "Configuration saved to " + tmpDir
	if !strings.Contains(stdout.String(), expectedSaved) {
		t.Fatalf("expected stdout containing %q, got: %q", expectedSaved, stdout.String())
	}

	// Verify .runner file
	cfg, err := config.LoadRunnerConfig(tmpDir)
	if err != nil {
		t.Fatalf("failed to load saved runner config: %v", err)
	}
	if cfg.AgentID != 4242 || cfg.AgentName != "custom-runner" || cfg.WorkFolder != "my-work" || !cfg.UseV2Flow {
		t.Fatalf("unexpected runner config: %+v", cfg)
	}
	runnerStat, err := os.Stat(filepath.Join(tmpDir, ".runner"))
	if err != nil {
		t.Fatalf("stat .runner failed: %v", err)
	}
	if runnerStat.Mode().Perm() != 0600 {
		t.Errorf("expected .runner mode 0600, got %o", runnerStat.Mode().Perm())
	}

	// Verify .credentials file
	creds, err := config.LoadCredentials(tmpDir)
	if err != nil {
		t.Fatalf("failed to load saved credentials: %v", err)
	}
	if creds.ClientID != "client-uuid-4242" || creds.AuthorizationURL != "https://auth.actions.githubusercontent.com/token" || creds.Scheme != "OAuth" {
		t.Fatalf("unexpected credentials: %+v", creds)
	}
	credStat, err := os.Stat(filepath.Join(tmpDir, ".credentials"))
	if err != nil {
		t.Fatalf("stat .credentials failed: %v", err)
	}
	if credStat.Mode().Perm() != 0600 {
		t.Errorf("expected .credentials mode 0600, got %o", credStat.Mode().Perm())
	}

	// Verify .credentials_rsaparams file
	rsaPath := filepath.Join(tmpDir, ".credentials_rsaparams")
	rsaStat, err := os.Stat(rsaPath)
	if err != nil {
		t.Fatalf("stat .credentials_rsaparams failed: %v", err)
	}
	if rsaStat.Mode().Perm() != 0600 {
		t.Errorf("expected .credentials_rsaparams mode 0600, got %o", rsaStat.Mode().Perm())
	}
	rsaBytes, err := os.ReadFile(rsaPath)
	if err != nil {
		t.Fatalf("read .credentials_rsaparams failed: %v", err)
	}
	var rsaParams protocol.RSAParameters
	if err := json.Unmarshal(rsaBytes, &rsaParams); err != nil {
		t.Fatalf("unmarshal .credentials_rsaparams failed: %v", err)
	}
	key, err := protocol.ParseRSAPrivateKey(&rsaParams)
	if err != nil {
		t.Fatalf("failed to parse rsa private key: %v", err)
	}
	if key == nil || key.N == nil || key.D == nil {
		t.Fatalf("parsed invalid key from saved rsa parameters")
	}
}

func TestRegister_ServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message": "Registration token has expired"}`))
	}))
	defer ts.Close()

	tmpDir := t.TempDir()
	var stdout, stderr bytes.Buffer
	ctx := WithHTTPClient(context.Background(), ts.Client())

	code := dispatchWithContext(ctx, []string{
		"register",
		"--url", ts.URL + "/my-org/my-repo",
		"--token", "expired-token",
		"--config-dir", tmpDir,
	}, &stdout, &stderr)

	if code == 0 {
		t.Fatalf("expected non-zero exit code on server error")
	}
}

func clearEnv(t *testing.T) {
	t.Helper()
	for _, env := range []string{"RUNNER_URL", "RUNNER_TOKEN", "GITHUB_TOKEN", "GH_TOKEN", "ACT_RUNNER_CONFIG_DIR"} {
		t.Setenv(env, "")
	}
}

func TestRegister_Headless_BypassesInteractive(t *testing.T) {
	origIsTerminal := isTerminalFunc
	origInstall := serviceInstallFunc
	origStart := serviceStartFunc
	defer func() {
		isTerminalFunc = origIsTerminal
		serviceInstallFunc = origInstall
		serviceStartFunc = origStart
	}()

	isTerminalFunc = func(fd uintptr) bool { return true }
	serviceCalled := false
	serviceInstallFunc = func(binPath string, configDirs ...string) error {
		serviceCalled = true
		return nil
	}
	serviceStartFunc = func() error {
		serviceCalled = true
		return nil
	}

	ts, _, _ := newMockRegistrationServer(t, nil)
	defer ts.Close()

	tmpDir := t.TempDir()
	// Pre-create .runner to verify overwrite with --force in headless mode
	if err := os.WriteFile(filepath.Join(tmpDir, ".runner"), []byte("{}"), 0600); err != nil {
		t.Fatalf("write existing .runner: %v", err)
	}

	var stdout, stderr bytes.Buffer
	ctx := WithHTTPClient(context.Background(), ts.Client())

	// Without --force, it should fail
	errCode := dispatchWithContext(ctx, []string{
		"register",
		"--url", ts.URL + "/my-org/my-repo",
		"--token", "headless-token",
		"--config-dir", tmpDir,
	}, &stdout, &stderr)
	if errCode == 0 {
		t.Fatalf("expected error when registering without --force in headless mode over existing config")
	}

	stdout.Reset()
	stderr.Reset()
	code := dispatchWithContext(ctx, []string{
		"register",
		"--url", ts.URL + "/my-org/my-repo",
		"--token", "headless-token",
		"--force",
		"--config-dir", tmpDir,
	}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("expected code 0, got %d. stderr: %s", code, stderr.String())
	}
	if serviceCalled {
		t.Errorf("expected service install/start NOT to be called in headless mode")
	}
}

func TestRegister_Headless_EnvironmentVariables(t *testing.T) {
	origIsTerminal := isTerminalFunc
	t.Cleanup(func() {
		isTerminalFunc = origIsTerminal
	})
	isTerminalFunc = func(fd uintptr) bool { return false }

	ts, capturedAuth1, _ := newMockRegistrationServer(t, nil)
	defer ts.Close()

	tmpDir := t.TempDir()
	var stdout, stderr bytes.Buffer
	ctx := WithHTTPClient(context.Background(), ts.Client())
	repoURL := ts.URL + "/my-org/my-repo"

	t.Setenv("RUNNER_URL", repoURL)
	t.Setenv("RUNNER_TOKEN", "env-secret-token")

	code := dispatchWithContext(ctx, []string{
		"register",
		"--config-dir", tmpDir,
	}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("expected code 0, got %d. stderr: %s", code, stderr.String())
	}

	if *capturedAuth1 != "RemoteAuth env-secret-token" {
		t.Errorf("unexpected authorization header on step 1: %s", *capturedAuth1)
	}
}

func TestRegister_Interactive_AutoDetectURL_GHToken_StartService(t *testing.T) {
	clearEnv(t)
	origIsTerminal := isTerminalFunc
	origDetect := detectRemoteFunc
	origFetchGH := fetchGHTokenFunc
	origInstall := serviceInstallFunc
	origStart := serviceStartFunc
	defer func() {
		isTerminalFunc = origIsTerminal
		detectRemoteFunc = origDetect
		fetchGHTokenFunc = origFetchGH
		serviceInstallFunc = origInstall
		serviceStartFunc = origStart
	}()

	expectedToken := "dummy-sample-reg-val"
	ts, _, _ := newMockRegistrationServer(t, &expectedToken)
	defer ts.Close()

	isTerminalFunc = func(fd uintptr) bool { return true }
	detectRemoteFunc = func(ctx context.Context, dir string) (*registration.TargetInfo, error) {
		return &registration.TargetInfo{
			RawURL: ts.URL + "/detected-org/detected-repo",
			WebURL: ts.URL + "/detected-org/detected-repo",
			Host:   "github.com",
			Owner:  "detected-org",
			Repo:   "detected-repo",
			Scope:  registration.ScopeRepo,
		}, nil
	}
	fetchGHTokenFunc = func(ctx context.Context, target *registration.TargetInfo) (string, error) {
		if target.Owner != "detected-org" || target.Repo != "detected-repo" {
			return "", fmt.Errorf("unexpected target: %+v", target)
		}
		return expectedToken, nil
	}

	installed := false
	started := false
	var installedConfigDir string
	serviceInstallFunc = func(binPath string, configDirs ...string) error {
		installed = true
		if len(configDirs) > 0 {
			installedConfigDir = configDirs[0]
		}
		return nil
	}
	serviceStartFunc = func() error {
		started = true
		return nil
	}

	tmpDir := t.TempDir()
	var stdout, stderr bytes.Buffer
	ctx := WithHTTPClient(context.Background(), ts.Client())

	// Input: Enter on URL prompt (accept default), Enter on start service prompt (accept [Y/n] default yes)
	stdin := bytes.NewBufferString("\n\n")

	code := dispatchWithContextAndIn(ctx, stdin, []string{
		"register",
		"--config-dir", tmpDir,
	}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("expected code 0, got %d. stderr: %s", code, stderr.String())
	}

	out := stdout.String()
	if !strings.Contains(out, "Detected repository: "+ts.URL+"/detected-org/detected-repo") {
		t.Errorf("expected stdout to mention detected repository, got: %s", out)
	}
	if !strings.Contains(out, "✓ Successfully retrieved runner registration token via gh CLI") {
		t.Errorf("expected stdout to mention gh CLI token retrieval, got: %s", out)
	}
	if !strings.Contains(out, "✓ Runner service started! Run 'actr monitor' to inspect live jobs.") {
		t.Errorf("expected stdout to mention service started, got: %s", out)
	}
	if !installed || !started {
		t.Errorf("expected service to be installed and started: installed=%v, started=%v", installed, started)
	}
	if installedConfigDir != tmpDir {
		t.Errorf("expected installed config dir %s, got %s", tmpDir, installedConfigDir)
	}

	// Verify .runner exists
	if _, err := os.Stat(filepath.Join(tmpDir, ".runner")); err != nil {
		t.Errorf("expected .runner file to exist: %v", err)
	}
}

func TestRegister_Interactive_ManualURL_EnvToken_NoStartService(t *testing.T) {
	clearEnv(t)
	origIsTerminal := isTerminalFunc
	origDetect := detectRemoteFunc
	origFetchGH := fetchGHTokenFunc
	origInstall := serviceInstallFunc
	origStart := serviceStartFunc
	defer func() {
		isTerminalFunc = origIsTerminal
		detectRemoteFunc = origDetect
		fetchGHTokenFunc = origFetchGH
		serviceInstallFunc = origInstall
		serviceStartFunc = origStart
	}()

	expectedToken := "env-runner-token"
	t.Setenv("RUNNER_TOKEN", expectedToken)

	ts, _, _ := newMockRegistrationServer(t, &expectedToken)
	defer ts.Close()

	isTerminalFunc = func(fd uintptr) bool { return true }
	// Remote detection fails -> prompts for URL
	detectRemoteFunc = func(ctx context.Context, dir string) (*registration.TargetInfo, error) {
		return nil, errors.New("not a git repository")
	}
	fetchGHTokenFunc = func(ctx context.Context, target *registration.TargetInfo) (string, error) {
		t.Fatalf("fetchGHTokenFunc should not be called when token is present in env")
		return "", nil
	}

	serviceCalled := false
	serviceInstallFunc = func(binPath string, configDirs ...string) error {
		serviceCalled = true
		return nil
	}
	serviceStartFunc = func() error {
		serviceCalled = true
		return nil
	}

	tmpDir := t.TempDir()
	var stdout, stderr bytes.Buffer
	ctx := WithHTTPClient(context.Background(), ts.Client())

	// Input: manually enter URL, decline start service prompt ("n")
	stdin := bytes.NewBufferString(ts.URL + "/manual-org/manual-repo\nn\n")

	code := dispatchWithContextAndIn(ctx, stdin, []string{
		"register",
		"--config-dir", tmpDir,
	}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("expected code 0, got %d. stderr: %s", code, stderr.String())
	}
	if serviceCalled {
		t.Errorf("expected service NOT to be called when declined")
	}

	if _, err := os.Stat(filepath.Join(tmpDir, ".runner")); err != nil {
		t.Errorf("expected .runner file to exist: %v", err)
	}
}

func TestRegister_Interactive_GHFails_BrowserFallback(t *testing.T) {
	clearEnv(t)
	origIsTerminal := isTerminalFunc
	origDetect := detectRemoteFunc
	origFetchGH := fetchGHTokenFunc
	origBrowser := openBrowserFunc
	origInstall := serviceInstallFunc
	origStart := serviceStartFunc
	defer func() {
		isTerminalFunc = origIsTerminal
		detectRemoteFunc = origDetect
		fetchGHTokenFunc = origFetchGH
		openBrowserFunc = origBrowser
		serviceInstallFunc = origInstall
		serviceStartFunc = origStart
	}()

	expectedToken := "browser-manual-token"
	ts, _, _ := newMockRegistrationServer(t, &expectedToken)
	defer ts.Close()

	isTerminalFunc = func(fd uintptr) bool { return true }
	detectRemoteFunc = func(ctx context.Context, dir string) (*registration.TargetInfo, error) {
		return &registration.TargetInfo{
			RawURL: ts.URL + "/browser-org/browser-repo",
			WebURL: ts.URL + "/browser-org/browser-repo",
			Host:   "github.com",
			Owner:  "browser-org",
			Repo:   "browser-repo",
			Scope:  registration.ScopeRepo,
		}, nil
	}
	fetchGHTokenFunc = func(ctx context.Context, target *registration.TargetInfo) (string, error) {
		return "", errors.New("gh: command not found")
	}

	var openedURL string
	openBrowserFunc = func(url string) error {
		openedURL = url
		return nil
	}

	serviceInstallFunc = func(binPath string, configDirs ...string) error { return nil }
	serviceStartFunc = func() error { return nil }

	tmpDir := t.TempDir()
	var stdout, stderr bytes.Buffer
	ctx := WithHTTPClient(context.Background(), ts.Client())

	// Input: accept detected URL (\n), paste token (expectedToken), decline service (n)
	stdin := bytes.NewBufferString("\n" + expectedToken + "\nn\n")

	code := dispatchWithContextAndIn(ctx, stdin, []string{
		"register",
		"--config-dir", tmpDir,
	}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("expected code 0, got %d. stderr: %s", code, stderr.String())
	}

	out := stdout.String()
	if !strings.Contains(out, "Opening browser to GitHub runner settings... Paste registration token below:") {
		t.Errorf("expected stdout to mention opening browser, got: %s", out)
	}
	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("parse ts.URL: %v", err)
	}
	expectedSettingsURL := fmt.Sprintf("https://%s/browser-org/browser-repo/settings/actions/runners/new", u.Host)
	if openedURL != expectedSettingsURL {
		t.Errorf("expected opened URL %q, got %q", expectedSettingsURL, openedURL)
	}
}

func TestRegister_Interactive_BrowserFails_PrintsURL(t *testing.T) {
	clearEnv(t)
	origIsTerminal := isTerminalFunc
	origDetect := detectRemoteFunc
	origFetchGH := fetchGHTokenFunc
	origBrowser := openBrowserFunc
	defer func() {
		isTerminalFunc = origIsTerminal
		detectRemoteFunc = origDetect
		fetchGHTokenFunc = origFetchGH
		openBrowserFunc = origBrowser
	}()

	expectedToken := "headless-manual-token"
	ts, _, _ := newMockRegistrationServer(t, &expectedToken)
	defer ts.Close()

	isTerminalFunc = func(fd uintptr) bool { return true }
	detectRemoteFunc = func(ctx context.Context, dir string) (*registration.TargetInfo, error) {
		return &registration.TargetInfo{
			RawURL: ts.URL + "/headless-org/headless-repo",
			WebURL: ts.URL + "/headless-org/headless-repo",
			Host:   "github.com",
			Owner:  "headless-org",
			Repo:   "headless-repo",
			Scope:  registration.ScopeRepo,
		}, nil
	}
	fetchGHTokenFunc = func(ctx context.Context, target *registration.TargetInfo) (string, error) {
		return "", errors.New("gh api error")
	}
	openBrowserFunc = func(url string) error {
		return registration.ErrHeadlessEnvironment
	}

	tmpDir := t.TempDir()
	var stdout, stderr bytes.Buffer
	ctx := WithHTTPClient(context.Background(), ts.Client())

	// Input: accept URL (\n), enter token (expectedToken), decline service (n)
	stdin := bytes.NewBufferString("\n" + expectedToken + "\nn\n")

	code := dispatchWithContextAndIn(ctx, stdin, []string{
		"register",
		"--config-dir", tmpDir,
	}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("expected code 0, got %d. stderr: %s", code, stderr.String())
	}

	out := stdout.String()
	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("parse ts.URL: %v", err)
	}
	expectedPrint := fmt.Sprintf("Please open in your browser: https://%s/headless-org/headless-repo/settings/actions/runners/new", u.Host)
	if !strings.Contains(out, expectedPrint) {
		t.Errorf("expected stdout to contain %q, got: %s", expectedPrint, out)
	}
}

func TestRegister_ExistingRunner_PromptNo_Aborts(t *testing.T) {
	origIsTerminal := isTerminalFunc
	defer func() { isTerminalFunc = origIsTerminal }()

	isTerminalFunc = func(fd uintptr) bool { return true }

	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, ".runner"), []byte(`{"agentId": 123}`), 0600); err != nil {
		t.Fatalf("failed to write .runner: %v", err)
	}

	var stdout, stderr bytes.Buffer
	ctx := context.Background()

	// Input: decline overwrite ("n")
	stdin := bytes.NewBufferString("n\n")

	code := dispatchWithContextAndIn(ctx, stdin, []string{
		"register",
		"--config-dir", tmpDir,
	}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("expected code 0 on clean abort, got %d. stderr: %s", code, stderr.String())
	}

	out := stdout.String()
	if !strings.Contains(out, "Runner is already registered in "+tmpDir+". Overwrite?") {
		t.Errorf("expected stdout to prompt overwrite, got: %s", out)
	}

	// Verify original .runner unchanged
	data, _ := os.ReadFile(filepath.Join(tmpDir, ".runner"))
	if !strings.Contains(string(data), `"agentId": 123`) {
		t.Errorf("expected original .runner not to be overwritten")
	}
}

func TestRegister_ExistingRunner_PromptYes_Overwrites(t *testing.T) {
	origIsTerminal := isTerminalFunc
	origDetect := detectRemoteFunc
	origFetchGH := fetchGHTokenFunc
	defer func() {
		isTerminalFunc = origIsTerminal
		detectRemoteFunc = origDetect
		fetchGHTokenFunc = origFetchGH
	}()

	isTerminalFunc = func(fd uintptr) bool { return true }

	expectedToken := "overwrite-token"
	ts, _, _ := newMockRegistrationServer(t, &expectedToken)
	defer ts.Close()

	detectRemoteFunc = func(ctx context.Context, dir string) (*registration.TargetInfo, error) {
		return &registration.TargetInfo{
			RawURL: ts.URL + "/org/repo",
			WebURL: ts.URL + "/org/repo",
			Host:   "github.com",
			Owner:  "org",
			Repo:   "repo",
			Scope:  registration.ScopeRepo,
		}, nil
	}
	fetchGHTokenFunc = func(ctx context.Context, target *registration.TargetInfo) (string, error) {
		return expectedToken, nil
	}

	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, ".runner"), []byte(`{"agentId": 999}`), 0600); err != nil {
		t.Fatalf("failed to write .runner: %v", err)
	}

	var stdout, stderr bytes.Buffer
	ctx := WithHTTPClient(context.Background(), ts.Client())

	// Input: "y" to overwrite, Enter on URL, "n" on service start
	stdin := bytes.NewBufferString("y\n\nn\n")

	code := dispatchWithContextAndIn(ctx, stdin, []string{
		"register",
		"--config-dir", tmpDir,
	}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("expected code 0, got %d. stderr: %s", code, stderr.String())
	}

	// Verify .runner overwritten with new ID 4242
	cfg, err := config.LoadRunnerConfig(tmpDir)
	if err != nil {
		t.Fatalf("load runner config: %v", err)
	}
	if cfg.AgentID != 4242 {
		t.Errorf("expected AgentID 4242, got %d", cfg.AgentID)
	}
}

func TestRegister_ExistingRunner_ForceFlag_SkipsPrompt(t *testing.T) {
	origIsTerminal := isTerminalFunc
	origDetect := detectRemoteFunc
	origFetchGH := fetchGHTokenFunc
	defer func() {
		isTerminalFunc = origIsTerminal
		detectRemoteFunc = origDetect
		fetchGHTokenFunc = origFetchGH
	}()

	isTerminalFunc = func(fd uintptr) bool { return true }

	expectedToken := "force-token"
	ts, _, _ := newMockRegistrationServer(t, &expectedToken)
	defer ts.Close()

	detectRemoteFunc = func(ctx context.Context, dir string) (*registration.TargetInfo, error) {
		return &registration.TargetInfo{
			RawURL: ts.URL + "/org/repo",
			WebURL: ts.URL + "/org/repo",
			Host:   "github.com",
			Owner:  "org",
			Repo:   "repo",
			Scope:  registration.ScopeRepo,
		}, nil
	}
	fetchGHTokenFunc = func(ctx context.Context, target *registration.TargetInfo) (string, error) {
		return expectedToken, nil
	}

	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, ".runner"), []byte(`{"agentId": 999}`), 0600); err != nil {
		t.Fatalf("failed to write .runner: %v", err)
	}

	var stdout, stderr bytes.Buffer
	ctx := WithHTTPClient(context.Background(), ts.Client())

	// Input: only URL Enter (\n) and service start decline ("n\n"). No overwrite answer provided!
	// If it prompted for overwrite, reading '\n' would decline overwrite (default is no).
	stdin := bytes.NewBufferString("\nn\n")

	code := dispatchWithContextAndIn(ctx, stdin, []string{
		"register",
		"--force",
		"--config-dir", tmpDir,
	}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("expected code 0, got %d. stderr: %s", code, stderr.String())
	}

	out := stdout.String()
	if strings.Contains(out, "Overwrite?") {
		t.Errorf("expected overwrite prompt to be skipped with --force, got: %s", out)
	}

	// Verify .runner overwritten
	cfg, err := config.LoadRunnerConfig(tmpDir)
	if err != nil {
		t.Fatalf("load runner config: %v", err)
	}
	if cfg.AgentID != 4242 {
		t.Errorf("expected AgentID 4242, got %d", cfg.AgentID)
	}
}

func TestRegisterCmd_MultiRepoAlias(t *testing.T) {
	expectedToken := "test-token-alias"
	ts, _, _ := newMockRegistrationServer(t, &expectedToken)
	defer ts.Close()

	tmpDir := t.TempDir()
	var stdout, stderr bytes.Buffer
	ctx := WithHTTPClient(context.Background(), ts.Client())

	code := dispatchWithContext(ctx, []string{
		"register",
		"--url", ts.URL + "/adrijshikhar/cxstatusline",
		"--token", expectedToken,
		"--repo-alias", "cxstatusline",
		"--limit-cpu", "2.0",
		"--limit-memory", "3GB",
		"--config-dir", tmpDir,
	}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("expected code 0, got %d. stderr: %s", code, stderr.String())
	}

	// Verify credentials exist in ~/.actr/repos/cxstatusline/
	repoDir := filepath.Join(tmpDir, "repos", "cxstatusline")
	cfg, err := config.LoadRunnerConfig(repoDir)
	if err != nil {
		t.Fatalf("failed to load runner config in repoDir: %v", err)
	}
	if cfg.AgentID != 4242 {
		t.Errorf("expected agent ID 4242, got %d", cfg.AgentID)
	}

	// Verify ~/.actr/config.yaml created
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	supCfg, err := config.LoadSupervisorConfig(cfgPath)
	if err != nil {
		t.Fatalf("failed to load supervisor config: %v", err)
	}
	if supCfg.Version != "1" {
		t.Errorf("expected version '1', got %q", supCfg.Version)
	}
	repo, ok := supCfg.Repos["cxstatusline"]
	if !ok {
		t.Fatalf("expected cxstatusline in supervisor repos, got: %v", supCfg.Repos)
	}
	if repo.Limits.Memory != "3GB" {
		t.Errorf("expected memory limit 3GB, got %v", repo.Limits.Memory)
	}
}

func TestRegisterCmd_TokenStdin(t *testing.T) {
	expectedToken := "stdin-secret-token"
	ts, _, _ := newMockRegistrationServer(t, &expectedToken)
	defer ts.Close()

	tmpDir := t.TempDir()
	var stdout, stderr bytes.Buffer
	ctx := WithHTTPClient(context.Background(), ts.Client())

	stdin := bytes.NewBufferString(expectedToken + "\n")
	code := dispatchWithContextAndIn(ctx, stdin, []string{
		"register",
		"--url", ts.URL + "/org/repo",
		"--token-stdin",
		"--config-dir", tmpDir,
	}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("expected code 0, got %d. stderr: %s", code, stderr.String())
	}

	cfg, err := config.LoadRunnerConfig(tmpDir)
	if err != nil {
		t.Fatalf("failed to load runner config: %v", err)
	}
	if cfg.AgentID != 4242 {
		t.Errorf("expected agent ID 4242, got %d", cfg.AgentID)
	}
}

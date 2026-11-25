package protocol

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRegisterRunner_Repo_Success(t *testing.T) {
	var (
		step1Called  bool
		step1Auth    string
		step1Body    RunnerAuthRequest
		step2Called  bool
		step2Auth    string
		step2Body    TaskAgentRegisterRequest
		serverPrefix string
	)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if r.URL.Path == "/actions/runner-registration" {
			step1Called = true
			step1Auth = r.Header.Get("Authorization")
			if err := json.Unmarshal(body, &step1Body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			resp := RunnerAuthResponse{
				Token:       "mock-tenant-bearer-token",
				TokenSchema: "OAuthAccessToken",
				URL:         serverPrefix + "/tenant/",
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		if strings.HasPrefix(r.URL.Path, "/tenant/_apis/distributedtask/pools/1/agents") {
			step2Called = true
			step2Auth = r.Header.Get("Authorization")
			if err := json.Unmarshal(body, &step2Body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			resp := map[string]any{
				"id":   int64(9876),
				"name": "mock-repo-runner",
				"authorization": map[string]any{
					"authorizationUrl": "https://auth.actions.githubusercontent.com/token",
					"clientId":         "client-uuid-1111",
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
	defer ts.Close()
	serverPrefix = ts.URL

	ctx := context.Background()
	repoURL := ts.URL + "/adrijshikhar/actr"
	token := "mock-registration-token"
	runnerName := "mock-repo-runner"
	workFolder := "_work"

	cfg, creds, rsaParams, err := RegisterRunner(ctx, ts.Client(), repoURL, token, runnerName, workFolder)
	if err != nil {
		t.Fatalf("RegisterRunner failed: %v", err)
	}

	// Verify Step 1
	if !step1Called {
		t.Error("expected step 1 (runner-registration) to be called")
	}
	if step1Auth != "RemoteAuth "+token {
		t.Errorf("expected step 1 auth RemoteAuth %s, got %s", token, step1Auth)
	}
	if step1Body.URL != repoURL || step1Body.RunnerEvent != "register" {
		t.Errorf("unexpected step 1 body: %+v", step1Body)
	}

	// Verify Step 2
	if !step2Called {
		t.Error("expected step 2 (agent registration) to be called")
	}
	if step2Auth != "Bearer mock-tenant-bearer-token" {
		t.Errorf("expected step 2 auth Bearer token, got %s", step2Auth)
	}
	if step2Body.Name != runnerName {
		t.Errorf("expected step 2 body name %s, got %s", runnerName, step2Body.Name)
	}
	if step2Body.Version != "3.0.0" {
		t.Errorf("expected version 3.0.0, got %s", step2Body.Version)
	}
	if step2Body.Authorization.PublicKey == nil || step2Body.Authorization.PublicKey.Exponent == "" || step2Body.Authorization.PublicKey.Modulus == "" {
		t.Errorf("step 2 body missing valid public key parameters")
	}
	if len(step2Body.Labels) < 3 {
		t.Errorf("expected at least 3 labels, got %d", len(step2Body.Labels))
	}

	// Verify RunnerConfig
	if cfg == nil {
		t.Fatal("expected non-nil RunnerConfig")
	}
	if cfg.AgentID != 9876 {
		t.Errorf("expected AgentID 9876, got %d", cfg.AgentID)
	}
	if cfg.AgentName != runnerName {
		t.Errorf("expected AgentName %s, got %s", runnerName, cfg.AgentName)
	}
	if cfg.PoolID != 1 {
		t.Errorf("expected PoolID 1, got %d", cfg.PoolID)
	}
	if cfg.PoolName != "Default" {
		t.Errorf("expected PoolName Default, got %s", cfg.PoolName)
	}
	if cfg.ServerURL != "https://pipelines.actions.githubusercontent.com/" {
		t.Errorf("expected ServerURL https://pipelines.actions.githubusercontent.com/, got %s", cfg.ServerURL)
	}
	if cfg.GitHubURL != repoURL {
		t.Errorf("expected GitHubURL %s, got %s", repoURL, cfg.GitHubURL)
	}
	if cfg.WorkFolder != workFolder {
		t.Errorf("expected WorkFolder %s, got %s", workFolder, cfg.WorkFolder)
	}
	if !cfg.UseV2Flow {
		t.Errorf("expected UseV2Flow true")
	}
	if cfg.ServerURLV2 != "https://broker.actions.githubusercontent.com/" {
		t.Errorf("expected ServerURLV2 https://broker.actions.githubusercontent.com/, got %s", cfg.ServerURLV2)
	}

	// Verify Credentials
	if creds == nil {
		t.Fatal("expected non-nil Credentials")
	}
	if creds.Scheme != "OAuth" {
		t.Errorf("expected Scheme OAuth, got %s", creds.Scheme)
	}
	if creds.ClientID != "client-uuid-1111" {
		t.Errorf("expected ClientID client-uuid-1111, got %s", creds.ClientID)
	}
	if creds.AuthorizationURL != "https://auth.actions.githubusercontent.com/token" {
		t.Errorf("expected AuthorizationURL https://auth.actions.githubusercontent.com/token, got %s", creds.AuthorizationURL)
	}
	if !creds.RequireFipsCryptography {
		t.Errorf("expected RequireFipsCryptography true")
	}
	if creds.Data == nil {
		t.Fatal("expected non-nil creds.Data")
	}
	if creds.Data.ClientID != "client-uuid-1111" {
		t.Errorf("expected Data.ClientID client-uuid-1111, got %s", creds.Data.ClientID)
	}
	if creds.Data.AuthorizationURL != "https://auth.actions.githubusercontent.com/token" {
		t.Errorf("expected Data.AuthorizationURL https://auth.actions.githubusercontent.com/token, got %s", creds.Data.AuthorizationURL)
	}
	if !creds.Data.RequireFipsCryptography {
		t.Errorf("expected Data.RequireFipsCryptography true")
	}

	// Verify RSAParameters can be parsed back into private key
	if rsaParams == nil {
		t.Fatal("expected non-nil RSAParameters")
	}
	key, err := ParseRSAPrivateKey(rsaParams)
	if err != nil {
		t.Fatalf("failed to parse returned RSAParameters: %v", err)
	}
	if key == nil || key.N == nil || key.D == nil {
		t.Fatal("parsed private key is missing essential components")
	}
}

func TestRegisterRunner_CustomLabels(t *testing.T) {
	var step2Body TaskAgentRegisterRequest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path == "/actions/runner-registration" {
			resp := RunnerAuthResponse{
				Token:       "mock-token",
				TokenSchema: "OAuthAccessToken",
				URL:         "http://" + r.Host + "/tenant/",
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/tenant/_apis/distributedtask/pools/1/agents") {
			_ = json.Unmarshal(body, &step2Body)
			resp := map[string]any{
				"id":   int64(1234),
				"name": "labeled-runner",
				"authorization": map[string]any{
					"authorizationUrl": "https://auth.actions.githubusercontent.com/token",
					"clientId":         "client-1",
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
	}))
	defer ts.Close()

	_, _, _, err := RegisterRunner(context.Background(), ts.Client(), ts.URL+"/org/repo", "tok", "labeled-runner", "_work", "m5-pro", "fast-build")
	if err != nil {
		t.Fatalf("RegisterRunner failed: %v", err)
	}

	foundM5 := false
	foundFast := false
	for _, l := range step2Body.Labels {
		if l.Name == "m5-pro" && l.Type == "user" {
			foundM5 = true
		}
		if l.Name == "fast-build" && l.Type == "user" {
			foundFast = true
		}
	}
	if !foundM5 || !foundFast {
		t.Errorf("custom labels missing in step 2 body: %+v", step2Body.Labels)
	}
}

func TestRegisterRunner_Org_Success(t *testing.T) {
	var step1Called, step2Called bool
	var serverPrefix string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/actions/runner-registration" {
			step1Called = true
			resp := RunnerAuthResponse{
				Token:       "org-tenant-token",
				TokenSchema: "OAuthAccessToken",
				URL:         serverPrefix + "/org-tenant/",
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/org-tenant/_apis/distributedtask/pools/1/agents") {
			step2Called = true
			resp := map[string]any{
				"id":   int64(5432),
				"name": "org-runner",
				"authorization": map[string]any{
					"authorization_url": "https://auth.actions.githubusercontent.com/token",
					"client_id":         "client-org-5432",
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()
	serverPrefix = ts.URL

	ctx := context.Background()
	orgURL := ts.URL + "/my-awesome-org"
	cfg, creds, _, err := RegisterRunner(ctx, ts.Client(), orgURL, "org-token", "org-runner", "")
	if err != nil {
		t.Fatalf("RegisterRunner org failed: %v", err)
	}

	if !step1Called || !step2Called {
		t.Errorf("expected both steps called, got step1=%v, step2=%v", step1Called, step2Called)
	}
	if cfg.AgentID != 5432 {
		t.Errorf("expected agent ID 5432, got %d", cfg.AgentID)
	}
	if cfg.WorkFolder != "_work" {
		t.Errorf("expected default work folder '_work', got %s", cfg.WorkFolder)
	}
	if creds.ClientID != "client-org-5432" {
		t.Errorf("expected client ID client-org-5432, got %s", creds.ClientID)
	}
}

func TestRegisterRunner_InvalidInputs(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name       string
		repoURL    string
		token      string
		runnerName string
		wantErr    string
	}{
		{"empty repo URL", "", "tok", "runner", "repository or organization URL"},
		{"empty token", "https://github.com/owner/repo", "", "runner", "registration token"},
		{"too many path segments", "https://github.com/a/b/c/d", "tok", "runner", "invalid repository or organization URL"},
		{"empty path", "https://github.com/", "tok", "runner", "invalid repository or organization URL"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, err := RegisterRunner(ctx, nil, tt.repoURL, tt.token, tt.runnerName, "")
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}

func TestRegisterRunner_Step1Error(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message": "Bad registration token"}`))
	}))
	defer ts.Close()

	ctx := context.Background()
	_, _, _, err := RegisterRunner(ctx, ts.Client(), ts.URL+"/owner/repo", "bad-token", "runner", "")
	if err == nil {
		t.Fatalf("expected error for 401 unauthorized, got nil")
	}
	if !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "Bad registration token") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestRegisterRunner_Step2Error(t *testing.T) {
	var serverPrefix string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/actions/runner-registration" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(RunnerAuthResponse{
				Token: "tenant-token",
				URL:   serverPrefix + "/tenant/",
			})
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message": "internal agent error"}`))
	}))
	defer ts.Close()
	serverPrefix = ts.URL

	ctx := context.Background()
	_, _, _, err := RegisterRunner(ctx, ts.Client(), ts.URL+"/owner/repo", "token", "runner", "")
	if err == nil {
		t.Fatalf("expected error for step 2 failure, got nil")
	}
	if !strings.Contains(err.Error(), "500") || !strings.Contains(err.Error(), "internal agent error") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestRegisterRunner_Step1_InvalidJSONResponse(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{not valid json`))
	}))
	defer ts.Close()

	ctx := context.Background()
	_, _, _, err := RegisterRunner(ctx, ts.Client(), ts.URL+"/owner/repo", "token", "runner", "")
	if err == nil {
		t.Fatalf("expected error for invalid JSON response, got nil")
	}
	if !strings.Contains(err.Error(), "unmarshal") {
		t.Fatalf("expected unmarshal error, got: %v", err)
	}
}

func TestRegisterRunner_Step2_InvalidJSONResponse(t *testing.T) {
	var serverPrefix string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/actions/runner-registration" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(RunnerAuthResponse{
				Token: "tenant-token",
				URL:   serverPrefix + "/tenant/",
			})
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{bad json in agent response`))
	}))
	defer ts.Close()
	serverPrefix = ts.URL

	ctx := context.Background()
	_, _, _, err := RegisterRunner(ctx, ts.Client(), ts.URL+"/owner/repo", "token", "runner", "")
	if err == nil {
		t.Fatalf("expected error for step 2 invalid json, got nil")
	}
	if !strings.Contains(err.Error(), "unmarshal") {
		t.Fatalf("expected unmarshal error, got: %v", err)
	}
}

func TestRegisterRunner_ContextCancelled(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, _, _, err := RegisterRunner(ctx, ts.Client(), ts.URL+"/owner/repo", "token", "runner", "")
	if err == nil {
		t.Fatalf("expected error for cancelled context, got nil")
	}
}

func TestRegisterRunner_MissingRequiredResponseFields(t *testing.T) {
	tests := []struct {
		name       string
		respBody   map[string]any
		errPattern string
	}{
		{
			name: "missing id",
			respBody: map[string]any{
				"id":   int64(0),
				"name": "runner",
				"authorization": map[string]any{
					"client_id":         "client-1",
					"authorization_url": "https://auth.example.com",
				},
			},
			errPattern: "missing required fields: id=0",
		},
		{
			name: "missing client_id",
			respBody: map[string]any{
				"id":   int64(123),
				"name": "runner",
				"authorization": map[string]any{
					"authorization_url": "https://auth.example.com",
				},
			},
			errPattern: `clientId=""`,
		},
		{
			name: "missing authorization_url",
			respBody: map[string]any{
				"id":   int64(123),
				"name": "runner",
				"authorization": map[string]any{
					"client_id": "client-1",
				},
			},
			errPattern: `authURL=""`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var serverPrefix string
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/actions/runner-registration" {
					_ = json.NewEncoder(w).Encode(RunnerAuthResponse{
						Token: "tenant-token",
						URL:   serverPrefix + "/tenant/",
					})
					return
				}
				_ = json.NewEncoder(w).Encode(tt.respBody)
			}))
			defer ts.Close()
			serverPrefix = ts.URL

			ctx := context.Background()
			_, _, _, err := RegisterRunner(ctx, ts.Client(), ts.URL+"/owner/repo", "token", "runner", "")
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.errPattern) {
				t.Fatalf("expected error containing %q, got: %v", tt.errPattern, err)
			}
		})
	}
}

func TestResolveRunnerRegistrationURL_CaseInsensitiveAndValidation(t *testing.T) {
	u, err := resolveRunnerRegistrationURL("https://GitHub.com/owner/repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if u != "https://api.github.com/actions/runner-registration" {
		t.Fatalf("expected api.github.com URL, got %q", u)
	}

	if _, err := resolveRunnerRegistrationURL("http://example.com/owner/repo"); err == nil {
		t.Fatal("expected error for insecure non-loopback http URL")
	}
}

package protocol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/adrijshikhar/actr/internal/config"
)

// RunnerLabel represents a runner label in the GitHub Actions registration payload.
type RunnerLabel struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// RunnerAuthRequest is the payload sent to actions/runner-registration.
type RunnerAuthRequest struct {
	URL         string `json:"url"`
	RunnerEvent string `json:"runner_event"`
}

// RunnerAuthResponse is the response returned by actions/runner-registration.
type RunnerAuthResponse struct {
	Token       string `json:"token"`
	TokenSchema string `json:"token_schema"`
	URL         string `json:"url"`
}

// TaskAgentPublicKey contains RSA public key parameters for runner authentication.
type TaskAgentPublicKey struct {
	Exponent string `json:"exponent"`
	Modulus  string `json:"modulus"`
}

// TaskAgentAuthorization contains the public key during agent registration.
type TaskAgentAuthorization struct {
	PublicKey *TaskAgentPublicKey `json:"publicKey,omitempty"`
}

// TaskAgentRegisterRequest is the payload sent to distributedtask pools agents endpoint.
type TaskAgentRegisterRequest struct {
	Name              string                 `json:"name"`
	Version           string                 `json:"version"`
	OSDescription     string                 `json:"osDescription"`
	MaxParallelism    int                    `json:"maxParallelism"`
	ProvisioningState string                 `json:"provisioningState"`
	Authorization     TaskAgentAuthorization `json:"authorization"`
	Labels            []RunnerLabel          `json:"labels"`
}

// TaskAgentResponseAuthorization contains OAuth authorization details returned by the agent registration endpoint.
type TaskAgentResponseAuthorization struct {
	AuthorizationURL string `json:"authorizationUrl"`
	ClientID         string `json:"clientId"`
}

// UnmarshalJSON supports both camelCase and snake_case for authorization fields.
func (a *TaskAgentResponseAuthorization) UnmarshalJSON(data []byte) error {
	var raw struct {
		AuthURL1  string `json:"authorization_url"`
		AuthURL2  string `json:"authorizationUrl"`
		AuthURL3  string `json:"AuthorizationUrl"`
		ClientID1 string `json:"client_id"`
		ClientID2 string `json:"clientId"`
		ClientID3 string `json:"ClientId"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	a.AuthorizationURL = raw.AuthURL1
	if a.AuthorizationURL == "" {
		a.AuthorizationURL = raw.AuthURL2
	}
	if a.AuthorizationURL == "" {
		a.AuthorizationURL = raw.AuthURL3
	}

	a.ClientID = raw.ClientID1
	if a.ClientID == "" {
		a.ClientID = raw.ClientID2
	}
	if a.ClientID == "" {
		a.ClientID = raw.ClientID3
	}
	return nil
}

// TaskAgentRegisterResponse represents the agent data returned by distributedtask API.
type TaskAgentRegisterResponse struct {
	ID            int64                          `json:"id"`
	Name          string                         `json:"name"`
	Authorization TaskAgentResponseAuthorization `json:"authorization"`
	Properties    map[string]json.RawMessage     `json:"properties"`
}

// RunnerOS returns the normalized OS string for runner system labels.
func RunnerOS() string {
	switch runtime.GOOS {
	case "darwin":
		return "macOS"
	case "linux":
		return "Linux"
	case "windows":
		return "Windows"
	default:
		return runtime.GOOS
	}
}

// RunnerArch returns the normalized architecture string for runner system labels.
func RunnerArch() string {
	switch runtime.GOARCH {
	case "arm64":
		return "ARM64"
	case "amd64":
		return "X64"
	case "386":
		return "X86"
	case "arm":
		return "ARM"
	default:
		return strings.ToUpper(runtime.GOARCH)
	}
}

// resolveRunnerRegistrationURL computes the actions/runner-registration API URL from repoURL.
func resolveRunnerRegistrationURL(repoURL string) (string, error) {
	cleanURL := strings.TrimSpace(repoURL)
	if cleanURL == "" {
		return "", errors.New("empty repository or organization URL")
	}

	if !strings.Contains(cleanURL, "://") {
		cleanURL = "https://" + cleanURL
	}

	parsed, err := validateRemoteURL(cleanURL)
	if err != nil {
		return "", err
	}

	trimmedPath := strings.Trim(strings.TrimSuffix(parsed.Path, ".git"), "/")
	if trimmedPath == "" {
		return "", fmt.Errorf("invalid repository or organization URL: %s", repoURL)
	}

	parts := strings.Split(trimmedPath, "/")
	if len(parts) > 2 {
		return "", fmt.Errorf("invalid repository or organization URL: %s", repoURL)
	}

	host := parsed.Hostname()
	if strings.EqualFold(host, "github.com") || strings.EqualFold(host, "www.github.com") {
		return "https://api.github.com/actions/runner-registration", nil
	}

	return fmt.Sprintf("%s://%s/actions/runner-registration", parsed.Scheme, parsed.Host), nil
}

func extractPropertyString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var obj struct {
		Value string `json:"$value"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil && obj.Value != "" {
		return obj.Value
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return ""
}

func extractPropertyBool(raw json.RawMessage, defaultVal bool) bool {
	if len(raw) == 0 {
		return defaultVal
	}
	var obj struct {
		Value json.RawMessage `json:"$value"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil && len(obj.Value) > 0 {
		var b bool
		if err := json.Unmarshal(obj.Value, &b); err == nil {
			return b
		}
		var s string
		if err := json.Unmarshal(obj.Value, &s); err == nil {
			return strings.EqualFold(s, "true")
		}
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return b
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.EqualFold(s, "true")
	}
	return defaultVal
}

// RegisterRunner performs GitHub Actions 2-step self-hosted runner registration:
// 1. Authenticate with RemoteAuth against actions/runner-registration to receive a tenant token and URL.
// 2. Register the agent with Bearer auth against _apis/distributedtask/pools/1/agents.
func RegisterRunner(
	ctx context.Context,
	client *http.Client,
	repoURL, token, runnerName, workFolder string,
	customLabels ...string,
) (*config.RunnerConfig, *config.Credentials, *RSAParameters, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if client == nil {
		client = newDefaultHTTPClient(30 * time.Second)
	}
	if strings.TrimSpace(repoURL) == "" {
		return nil, nil, nil, errors.New("empty repository or organization URL")
	}
	if strings.TrimSpace(token) == "" {
		return nil, nil, nil, errors.New("empty registration token")
	}
	if runnerName == "" {
		runnerName = "actr"
	}
	if workFolder == "" {
		workFolder = "_work"
	}

	registrationURL, err := resolveRunnerRegistrationURL(repoURL)
	if err != nil {
		return nil, nil, nil, err
	}

	// Step 1: runner-registration handshake
	authReqBody, err := json.Marshal(RunnerAuthRequest{
		URL:         repoURL,
		RunnerEvent: "register",
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("marshal runner auth request: %w", err)
	}

	httpReq1, err := http.NewRequestWithContext(ctx, http.MethodPost, registrationURL, bytes.NewReader(authReqBody))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("create runner auth request: %w", err)
	}
	httpReq1.Header.Set("Authorization", "RemoteAuth "+token)
	httpReq1.Header.Set("Content-Type", "application/json")
	httpReq1.Header.Set("Accept", "application/json")
	httpReq1.Header.Set("User-Agent", "actr")

	resp1, err := client.Do(httpReq1) //nolint:bodyclose // drainAndClose drains and closes resp1.Body
	if err != nil {
		return nil, nil, nil, fmt.Errorf("execute runner auth request: %w", err)
	}
	defer drainAndClose(resp1.Body)

	body1, err := io.ReadAll(io.LimitReader(resp1.Body, 1<<20))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read runner auth response: %w", err)
	}

	if resp1.StatusCode < 200 || resp1.StatusCode >= 300 {
		return nil, nil, nil, fmt.Errorf("runner auth failed with status %d: %s", resp1.StatusCode, strings.TrimSpace(string(body1)))
	}

	var authResp RunnerAuthResponse
	if err := json.Unmarshal(body1, &authResp); err != nil {
		return nil, nil, nil, fmt.Errorf("unmarshal runner auth response: %w", err)
	}

	if authResp.Token == "" || authResp.URL == "" {
		return nil, nil, nil, errors.New("runner auth response missing required token or url")
	}

	// Generate RSA key pair for runner identity
	privKey, err := GenerateRSAKeyPair(2048)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("generate RSA key: %w", err)
	}

	rsaParams := ExportRSAParameters(privKey)
	if rsaParams == nil || rsaParams.Modulus == "" || rsaParams.Exponent == "" {
		return nil, nil, nil, errors.New("failed to export RSA parameters")
	}

	// Step 2: Register agent with distributedtask pool
	tenantParsed, err := validateRemoteURL(authResp.URL)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("invalid tenant URL: %w", err)
	}
	tenantURL := strings.TrimRight(tenantParsed.String(), "/")
	agentsURL := fmt.Sprintf("%s/_apis/distributedtask/pools/1/agents?api-version=6.0-preview.2", tenantURL)

	agentReq := TaskAgentRegisterRequest{
		Name:              runnerName,
		Version:           "3.0.0",
		OSDescription:     fmt.Sprintf("actr %s/%s", runtime.GOOS, runtime.GOARCH),
		MaxParallelism:    1,
		ProvisioningState: "Provisioned",
		Authorization: TaskAgentAuthorization{
			PublicKey: &TaskAgentPublicKey{
				Exponent: rsaParams.Exponent,
				Modulus:  rsaParams.Modulus,
			},
		},
		Labels: []RunnerLabel{
			{Name: "self-hosted", Type: "system"},
			{Name: RunnerOS(), Type: "system"},
			{Name: RunnerArch(), Type: "system"},
		},
	}

	for _, l := range customLabels {
		if trimmed := strings.TrimSpace(l); trimmed != "" {
			agentReq.Labels = append(agentReq.Labels, RunnerLabel{Name: trimmed, Type: "user"})
		}
	}

	agentReqBody, err := json.Marshal(agentReq)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("marshal agent registration request: %w", err)
	}

	httpReq2, err := http.NewRequestWithContext(ctx, http.MethodPost, agentsURL, bytes.NewReader(agentReqBody))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("create agent registration request: %w", err)
	}
	httpReq2.Header.Set("Authorization", "Bearer "+authResp.Token)
	httpReq2.Header.Set("Content-Type", "application/json; api-version=6.0-preview.2")
	httpReq2.Header.Set("Accept", "application/json; api-version=6.0-preview.2")
	httpReq2.Header.Set("User-Agent", "actr")

	resp2, err := client.Do(httpReq2) //nolint:bodyclose // drainAndClose drains and closes resp2.Body
	if err != nil {
		return nil, nil, nil, fmt.Errorf("execute agent registration request: %w", err)
	}
	defer drainAndClose(resp2.Body)

	body2, err := io.ReadAll(io.LimitReader(resp2.Body, 1<<20))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read agent registration response: %w", err)
	}

	if resp2.StatusCode < 200 || resp2.StatusCode >= 300 {
		return nil, nil, nil, fmt.Errorf("agent registration failed with status %d: %s", resp2.StatusCode, strings.TrimSpace(string(body2)))
	}

	var agentResp TaskAgentRegisterResponse
	if err := json.Unmarshal(body2, &agentResp); err != nil {
		return nil, nil, nil, fmt.Errorf("unmarshal agent registration response: %w", err)
	}

	if agentResp.ID == 0 || agentResp.Authorization.ClientID == "" || agentResp.Authorization.AuthorizationURL == "" {
		return nil, nil, nil, fmt.Errorf("agent registration response missing required fields: id=%d, clientId=%q, authURL=%q",
			agentResp.ID, agentResp.Authorization.ClientID, agentResp.Authorization.AuthorizationURL)
	}

	serverURL := extractPropertyString(agentResp.Properties["ServerUrl"])
	if serverURL == "" {
		serverURL = tenantURL + "/"
	}
	serverURLV2 := extractPropertyString(agentResp.Properties["ServerUrlV2"])
	if serverURLV2 == "" {
		serverURLV2 = "https://broker.actions.githubusercontent.com/"
	}
	useV2Flow := extractPropertyBool(agentResp.Properties["UseV2Flow"], true)
	requireFips := extractPropertyBool(agentResp.Properties["RequireFipsCryptography"], true)

	name := agentResp.Name
	if name == "" {
		name = runnerName
	}

	cfg := &config.RunnerConfig{
		AgentID:     agentResp.ID,
		AgentName:   name,
		PoolID:      1,
		PoolName:    "Default",
		ServerURL:   serverURL,
		GitHubURL:   repoURL,
		WorkFolder:  workFolder,
		UseV2Flow:   useV2Flow,
		ServerURLV2: serverURLV2,
	}

	creds := &config.Credentials{
		Scheme:                  "OAuth",
		ClientID:                agentResp.Authorization.ClientID,
		AuthorizationURL:        agentResp.Authorization.AuthorizationURL,
		RequireFipsCryptography: requireFips,
		Data: &config.CredentialsData{
			ClientID:                agentResp.Authorization.ClientID,
			AuthorizationURL:        agentResp.Authorization.AuthorizationURL,
			RequireFipsCryptography: requireFips,
		},
	}

	return cfg, creds, rsaParams, nil
}

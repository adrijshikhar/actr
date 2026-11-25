package protocol_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/adrijshikhar/actr/internal/protocol"
)

func TestNewRunServiceClient(t *testing.T) {
	clientDefault := protocol.NewRunServiceClient(nil)
	if clientDefault == nil {
		t.Fatal("expected non-nil RunServiceClient from nil http.Client")
	}

	customHTTP := &http.Client{Timeout: 5 * time.Second}
	clientCustom := protocol.NewRunServiceClient(customHTTP)
	if clientCustom == nil {
		t.Fatal("expected non-nil RunServiceClient from custom http.Client")
	}
}

func TestRunServiceClient_AcquireJob_DirectFormat(t *testing.T) {
	expectedReqID := "req-12345"
	expectedBillingID := "billing-67890"
	expectedToken := "test-auth-token"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path != "/acquirejob" {
			t.Errorf("expected /acquirejob, got %s", r.URL.Path)
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer "+expectedToken {
			t.Errorf("expected Authorization 'Bearer %s', got %q", expectedToken, auth)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("expected application/json Content-Type, got %q", ct)
		}

		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read body: %v", err)
		}

		var reqBody map[string]string
		if err := json.Unmarshal(bodyBytes, &reqBody); err != nil {
			t.Fatalf("failed to unmarshal request body: %v", err)
		}

		if reqBody["jobMessageId"] != expectedReqID {
			t.Errorf("expected jobMessageId %q, got %q", expectedReqID, reqBody["jobMessageId"])
		}
		if reqBody["billingOwnerId"] != expectedBillingID {
			t.Errorf("expected billingOwnerId %q, got %q", expectedBillingID, reqBody["billingOwnerId"])
		}

		resp := protocol.JobSpecification{
			JobID:   "job-101",
			JobName: "unit-tests",
			PlanID:  "plan-202",
			Steps: []protocol.JobStep{
				{
					ID:               "step-1",
					Name:             "checkout",
					Type:             "uses",
					Script:           "",
					WorkingDirectory: "/workspace",
					Environment:      map[string]string{"BRANCH": "main"},
				},
				{
					ID:               "step-2",
					Name:             "test",
					Type:             "run",
					Script:           "go test ./...",
					WorkingDirectory: "/workspace",
					Environment:      map[string]string{"CGO_ENABLED": "0"},
				},
			},
			Variables: map[string]string{
				"CI": "true",
			},
			ContextData: map[string]any{
				"github": map[string]any{
					"actor": "octocat",
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := protocol.NewRunServiceClient(server.Client())
	ctx := context.Background()

	jobSpec, err := client.AcquireJob(ctx, server.URL, expectedReqID, expectedBillingID, expectedToken)
	if err != nil {
		t.Fatalf("AcquireJob failed: %v", err)
	}

	if jobSpec == nil {
		t.Fatal("expected non-nil JobSpecification")
	}
	if jobSpec.JobID != "job-101" {
		t.Errorf("expected JobID 'job-101', got %q", jobSpec.JobID)
	}
	if jobSpec.JobName != "unit-tests" {
		t.Errorf("expected JobName 'unit-tests', got %q", jobSpec.JobName)
	}
	if jobSpec.PlanID != "plan-202" {
		t.Errorf("expected PlanID 'plan-202', got %q", jobSpec.PlanID)
	}
	if len(jobSpec.Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(jobSpec.Steps))
	}
	if jobSpec.Steps[0].Name != "checkout" || jobSpec.Steps[0].Type != "uses" {
		t.Errorf("step 0 mismatch: %+v", jobSpec.Steps[0])
	}
	if jobSpec.Steps[1].Script != "go test ./..." || jobSpec.Steps[1].Type != "run" {
		t.Errorf("step 1 mismatch: %+v", jobSpec.Steps[1])
	}
	if jobSpec.Variables["CI"] != "true" {
		t.Errorf("expected CI variable 'true', got %q", jobSpec.Variables["CI"])
	}
	if jobSpec.ContextData == nil || jobSpec.ContextData["github"] == nil {
		t.Error("expected ContextData.github to be present")
	}
}

func TestRunServiceClient_AcquireJob_AgentJobRequestMessageFormat(t *testing.T) {
	rawAgentMessage := `{
		"jobId": "job-uuid-5555",
		"jobDisplayName": "Build & Package",
		"plan": {
			"planId": "plan-uuid-7777",
			"planType": "build"
		},
		"steps": [
			{
				"id": "step-checkout",
				"name": "Checkout source",
				"reference": {
					"id": "checkout-action",
					"name": "actions/checkout@v4",
					"version": "v4",
					"type": "uses"
				},
				"inputs": {
					"repository": "adrijshikhar/actr"
				}
			},
			{
				"id": "step-run-build",
				"name": "Compile code",
				"reference": {
					"id": "script-runner",
					"type": "script"
				},
				"inputs": {
					"script": "go build ./...",
					"workingDirectory": "/home/runner/work"
				},
				"environment": {
					"GOARCH": "arm64"
				}
			}
		],
		"variables": {
			"system.github.token": {
				"value": "ghs_token_secret"
			},
			"RUNNER_TRACK": "v2"
		},
		"contextData": {
			"runner": {
				"name": "m5-pro"
			}
		}
	}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(rawAgentMessage))
	}))
	defer server.Close()

	client := protocol.NewRunServiceClient(server.Client())
	jobSpec, err := client.AcquireJob(context.Background(), server.URL+"/", "req-1", "owner-1", "token")
	if err != nil {
		t.Fatalf("AcquireJob failed: %v", err)
	}

	if jobSpec.JobID != "job-uuid-5555" {
		t.Errorf("expected JobID 'job-uuid-5555', got %q", jobSpec.JobID)
	}
	if jobSpec.JobName != "Build & Package" {
		t.Errorf("expected JobName 'Build & Package', got %q", jobSpec.JobName)
	}
	if jobSpec.PlanID != "plan-uuid-7777" {
		t.Errorf("expected PlanID 'plan-uuid-7777', got %q", jobSpec.PlanID)
	}
	if len(jobSpec.Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(jobSpec.Steps))
	}

	// Verify step 1 from reference
	s0 := jobSpec.Steps[0]
	if s0.ID != "step-checkout" || s0.Name != "Checkout source" || s0.Type != "uses" {
		t.Errorf("unexpected step 0: %+v", s0)
	}

	// Verify step 2 from inputs and reference
	s1 := jobSpec.Steps[1]
	if s1.ID != "step-run-build" || s1.Name != "Compile code" || s1.Type != "script" {
		t.Errorf("unexpected step 1: %+v", s1)
	}
	if s1.Script != "go build ./..." {
		t.Errorf("expected script 'go build ./...', got %q", s1.Script)
	}
	if s1.WorkingDirectory != "/home/runner/work" {
		t.Errorf("expected workingDirectory '/home/runner/work', got %q", s1.WorkingDirectory)
	}
	if s1.Environment["GOARCH"] != "arm64" {
		t.Errorf("expected GOARCH=arm64, got %q", s1.Environment["GOARCH"])
	}

	// Verify nested variable extraction
	if jobSpec.Variables["system.github.token"] != "ghs_token_secret" {
		t.Errorf("expected system.github.token='ghs_token_secret', got %q", jobSpec.Variables["system.github.token"])
	}
	if jobSpec.Variables["RUNNER_TRACK"] != "v2" {
		t.Errorf("expected RUNNER_TRACK='v2', got %q", jobSpec.Variables["RUNNER_TRACK"])
	}
}

func TestRunServiceClient_AcquireJob_Errors(t *testing.T) {
	client := protocol.NewRunServiceClient(nil)

	// Empty run service URL
	_, err := client.AcquireJob(context.Background(), "", "req-1", "owner-1", "token")
	if err == nil {
		t.Error("expected error on empty run service URL")
	}

	// Empty runnerRequestID
	_, err = client.AcquireJob(context.Background(), "http://localhost:8080", "", "owner-1", "token")
	if err == nil {
		t.Error("expected error on empty runnerRequestID")
	}

	// Server error 500
	server500 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "database lock error", http.StatusInternalServerError)
	}))
	defer server500.Close()

	_, err = client.AcquireJob(context.Background(), server500.URL, "req-1", "owner-1", "token")
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("expected 500 status error, got %v", err)
	}

	// Server invalid JSON
	serverBadJSON := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{invalid-json"))
	}))
	defer serverBadJSON.Close()

	_, err = client.AcquireJob(context.Background(), serverBadJSON.URL, "req-1", "owner-1", "token")
	if err == nil {
		t.Error("expected error on invalid JSON")
	}

	// Context cancelled
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = client.AcquireJob(ctx, server500.URL, "req-1", "owner-1", "token")
	if err == nil {
		t.Error("expected error on cancelled context")
	}
}

func TestRunServiceClient_RenewJob_Success(t *testing.T) {
	expectedPlanID := "plan-abc"
	expectedJobID := "job-xyz"
	expectedToken := "token-renew"
	expectedLockedTime := time.Date(2026, 11, 13, 20, 30, 0, 0, time.UTC)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
			http.Error(w, "bad method", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path != "/renewjob" {
			t.Errorf("expected /renewjob, got %s", r.URL.Path)
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer "+expectedToken {
			t.Errorf("expected Authorization 'Bearer %s', got %q", expectedToken, auth)
		}

		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode body: %v", err)
		}
		if body["planId"] != expectedPlanID || body["jobId"] != expectedJobID {
			t.Errorf("unexpected body: %+v", body)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"lockedUntil": expectedLockedTime.Format(time.RFC3339),
		})
	}))
	defer server.Close()

	client := protocol.NewRunServiceClient(server.Client())
	lockedUntil, err := client.RenewJob(context.Background(), server.URL, expectedPlanID, expectedJobID, expectedToken)
	if err != nil {
		t.Fatalf("RenewJob failed: %v", err)
	}

	if !lockedUntil.Equal(expectedLockedTime) {
		t.Errorf("expected lockedUntil %v, got %v", expectedLockedTime, lockedUntil)
	}
}

func TestRunServiceClient_RenewJob_UnixTimestamp(t *testing.T) {
	expectedLockedTime := time.Date(2026, 11, 13, 20, 30, 0, 0, time.UTC)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"lockedUntil": expectedLockedTime.Unix(),
		})
	}))
	defer server.Close()

	client := protocol.NewRunServiceClient(server.Client())
	lockedUntil, err := client.RenewJob(context.Background(), server.URL, "p1", "j1", "tok")
	if err != nil {
		t.Fatalf("RenewJob failed: %v", err)
	}

	if !lockedUntil.Equal(expectedLockedTime) {
		t.Errorf("expected lockedUntil %v, got %v", expectedLockedTime, lockedUntil)
	}
}

func TestRunServiceClient_RenewJob_Errors(t *testing.T) {
	client := protocol.NewRunServiceClient(nil)

	// Empty run service URL
	_, err := client.RenewJob(context.Background(), "", "p1", "j1", "tok")
	if err == nil {
		t.Error("expected error on empty URL")
	}

	// Empty planID
	_, err = client.RenewJob(context.Background(), "http://localhost", "", "j1", "tok")
	if err == nil {
		t.Error("expected error on empty planID")
	}

	// Empty jobID
	_, err = client.RenewJob(context.Background(), "http://localhost", "p1", "", "tok")
	if err == nil {
		t.Error("expected error on empty jobID")
	}

	// Server error 500
	server500 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "renew failed", http.StatusInternalServerError)
	}))
	defer server500.Close()

	_, err = client.RenewJob(context.Background(), server500.URL, "p1", "j1", "tok")
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("expected 500 error, got %v", err)
	}

	// Invalid lockedUntil format
	serverBadTime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"lockedUntil": "not-a-timestamp",
		})
	}))
	defer serverBadTime.Close()

	_, err = client.RenewJob(context.Background(), serverBadTime.URL, "p1", "j1", "tok")
	if err == nil {
		t.Error("expected error on invalid lockedUntil format")
	}
}

func TestRunServiceClient_CompleteJob_Success(t *testing.T) {
	expectedToken := "token-complete"
	req := &protocol.CompleteJobRequest{
		PlanID:     "plan-123",
		JobID:      "job-456",
		Conclusion: protocol.TaskResultSucceeded,
		Outputs: map[string]string{
			"release_tag": "v1.0.0",
		},
		StepResults: []protocol.StepResult{
			{
				ID:         "step-1",
				Conclusion: protocol.TaskResultSucceeded,
				ExitCode:   0,
			},
			{
				ID:         "step-2",
				Conclusion: protocol.TaskResultSkipped,
				ExitCode:   0,
			},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
			http.Error(w, "bad method", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path != "/completejob" {
			t.Errorf("expected /completejob, got %s", r.URL.Path)
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer "+expectedToken {
			t.Errorf("expected Authorization 'Bearer %s', got %q", expectedToken, auth)
		}

		var received protocol.CompleteJobRequest
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatalf("failed to decode body: %v", err)
		}

		if received.PlanID != req.PlanID || received.JobID != req.JobID || received.Conclusion != req.Conclusion {
			t.Errorf("mismatch in received request: %+v", received)
		}
		if received.Outputs["release_tag"] != "v1.0.0" {
			t.Errorf("expected output release_tag=v1.0.0, got %q", received.Outputs["release_tag"])
		}
		if len(received.StepResults) != 2 {
			t.Fatalf("expected 2 step results, got %d", len(received.StepResults))
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := protocol.NewRunServiceClient(server.Client())
	err := client.CompleteJob(context.Background(), server.URL, expectedToken, req)
	if err != nil {
		t.Fatalf("CompleteJob failed: %v", err)
	}
}

func TestRunServiceClient_CompleteJob_Errors(t *testing.T) {
	client := protocol.NewRunServiceClient(nil)

	// Empty URL
	err := client.CompleteJob(context.Background(), "", "tok", &protocol.CompleteJobRequest{
		PlanID:     "p1",
		JobID:      "j1",
		Conclusion: "success",
	})
	if err == nil {
		t.Error("expected error on empty URL")
	}

	// Nil request
	err = client.CompleteJob(context.Background(), "http://localhost", "tok", nil)
	if err == nil {
		t.Error("expected error on nil request")
	}

	// Empty planID
	err = client.CompleteJob(context.Background(), "http://localhost", "tok", &protocol.CompleteJobRequest{
		JobID:      "j1",
		Conclusion: "success",
	})
	if err == nil {
		t.Error("expected error on empty planID")
	}

	// Empty jobID
	err = client.CompleteJob(context.Background(), "http://localhost", "tok", &protocol.CompleteJobRequest{
		PlanID:     "p1",
		Conclusion: "success",
	})
	if err == nil {
		t.Error("expected error on empty jobID")
	}

	// Empty conclusion
	err = client.CompleteJob(context.Background(), "http://localhost", "tok", &protocol.CompleteJobRequest{
		PlanID: "p1",
		JobID:  "j1",
	})
	if err == nil {
		t.Error("expected error on empty conclusion")
	}

	// Server error 400
	server400 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "invalid job state", http.StatusBadRequest)
	}))
	defer server400.Close()

	err = client.CompleteJob(context.Background(), server400.URL, "tok", &protocol.CompleteJobRequest{
		PlanID:     "p1",
		JobID:      "j1",
		Conclusion: "failure",
	})
	if err == nil || !strings.Contains(err.Error(), "400") {
		t.Errorf("expected 400 error, got %v", err)
	}

	// Context cancelled
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = client.CompleteJob(ctx, server400.URL, "tok", &protocol.CompleteJobRequest{
		PlanID:     "p1",
		JobID:      "j1",
		Conclusion: "success",
	})
	if err == nil {
		t.Error("expected error on cancelled context")
	}
}

func TestRunServiceClient_RenewJob_AdditionalVariants(t *testing.T) {
	expectedLockedTime := time.Date(2026, 11, 13, 20, 30, 0, 0, time.UTC)

	// Test LockedUntil in milliseconds
	serverMillis := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"lockedUntil": expectedLockedTime.UnixMilli(),
		})
	}))
	defer serverMillis.Close()

	client := protocol.NewRunServiceClient(serverMillis.Client())
	lockedUntil, err := client.RenewJob(context.Background(), serverMillis.URL, "p1", "j1", "tok")
	if err != nil {
		t.Fatalf("RenewJob with milliseconds failed: %v", err)
	}
	if !lockedUntil.Equal(expectedLockedTime) {
		t.Errorf("expected %v, got %v", expectedLockedTime, lockedUntil)
	}

	// Test LockedUntilAlt (PascalCase)
	serverAlt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"LockedUntil": expectedLockedTime.Format(time.RFC3339Nano),
		})
	}))
	defer serverAlt.Close()

	lockedUntil, err = client.RenewJob(context.Background(), serverAlt.URL, "p1", "j1", "tok")
	if err != nil {
		t.Fatalf("RenewJob with LockedUntilAlt failed: %v", err)
	}
	if !lockedUntil.Equal(expectedLockedTime) {
		t.Errorf("expected %v, got %v", expectedLockedTime, lockedUntil)
	}

	// Test missing lockedUntil field
	serverMissing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ok",
		})
	}))
	defer serverMissing.Close()

	_, err = client.RenewJob(context.Background(), serverMissing.URL, "p1", "j1", "tok")
	if err == nil {
		t.Error("expected error when lockedUntil is missing")
	}

	// Test RFC1123 format
	serverRFC1123 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"lockedUntil": expectedLockedTime.Format(time.RFC1123),
		})
	}))
	defer serverRFC1123.Close()

	lockedUntil, err = client.RenewJob(context.Background(), serverRFC1123.URL, "p1", "j1", "tok")
	if err != nil {
		t.Fatalf("RenewJob with RFC1123 failed: %v", err)
	}
	if !lockedUntil.Equal(expectedLockedTime) {
		t.Errorf("expected %v, got %v", expectedLockedTime, lockedUntil)
	}

	// Test json.Number
	serverJSONNumber := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"lockedUntil": %d}`, expectedLockedTime.Unix())
	}))
	defer serverJSONNumber.Close()

	lockedUntil, err = client.RenewJob(context.Background(), serverJSONNumber.URL, "p1", "j1", "tok")
	if err != nil {
		t.Fatalf("RenewJob with json.Number failed: %v", err)
	}
	if !lockedUntil.Equal(expectedLockedTime) {
		t.Errorf("expected %v, got %v", expectedLockedTime, lockedUntil)
	}

	// Test unsupported type
	serverBadType := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"lockedUntil": []string{"invalid", "slice"},
		})
	}))
	defer serverBadType.Close()

	_, err = client.RenewJob(context.Background(), serverBadType.URL, "p1", "j1", "tok")
	if err == nil {
		t.Error("expected error on unsupported type")
	}

	// Context cancelled
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = client.RenewJob(ctx, serverAlt.URL, "p1", "j1", "tok")
	if err == nil {
		t.Error("expected error on cancelled context")
	}
}

func TestRunServiceClient_AgentJobRequestMessage_Variants(t *testing.T) {
	// Tests fallback fields: job_id, name, plan.id, working-directory, run in inputs, int/bool vars
	rawMsg := `{
		"job_id": "job-alt-999",
		"name": "Alt Job Name",
		"plan": {
			"id": "plan-alt-888"
		},
		"steps": [
			{
				"id": "step-1",
				"displayName": "Run script step",
				"inputs": {
					"run": "echo hello",
					"working-directory": "/tmp/work"
				}
			},
			{
				"id": "step-2",
				"reference": {
					"name": "actions/cache@v3"
				}
			}
		],
		"variables": {
			"int_var": 123,
			"bool_var": true,
			"nil_var": null,
			"nested_val": {
				"Value": "capital_value"
			}
		}
	}`

	var spec protocol.JobSpecification
	if err := json.Unmarshal([]byte(rawMsg), &spec); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if spec.JobID != "job-alt-999" {
		t.Errorf("expected job_id fallback 'job-alt-999', got %q", spec.JobID)
	}
	if spec.JobName != "Alt Job Name" {
		t.Errorf("expected name fallback 'Alt Job Name', got %q", spec.JobName)
	}
	if spec.PlanID != "plan-alt-888" {
		t.Errorf("expected plan.id fallback 'plan-alt-888', got %q", spec.PlanID)
	}
	if len(spec.Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(spec.Steps))
	}
	if spec.Steps[0].Name != "Run script step" || spec.Steps[0].Script != "echo hello" || spec.Steps[0].WorkingDirectory != "/tmp/work" || spec.Steps[0].Type != "script" {
		t.Errorf("unexpected step 0: %+v", spec.Steps[0])
	}
	if spec.Steps[1].Type != "uses" {
		t.Errorf("expected step 1 type 'uses', got %q", spec.Steps[1].Type)
	}
	if spec.Variables["int_var"] != "123" {
		t.Errorf("expected int_var=123, got %q", spec.Variables["int_var"])
	}
	if spec.Variables["bool_var"] != "true" {
		t.Errorf("expected bool_var=true, got %q", spec.Variables["bool_var"])
	}
	if spec.Variables["nested_val"] != "capital_value" {
		t.Errorf("expected nested_val=capital_value, got %q", spec.Variables["nested_val"])
	}
	if _, exists := spec.Variables["nil_var"]; exists {
		t.Error("expected nil_var to be omitted")
	}
}

func TestRunServiceClient_AcquireJob_PipelineSyntaxAST(t *testing.T) {
	rawMsg := `{
		"jobId": "job-ast-1234",
		"jobDisplayName": {
			"lit": "Real Pipeline Build"
		},
		"steps": [
			{
				"id": "step-ast-1",
				"contextName": "__run",
				"name": "__run",
				"displayName": {
					"type": 0,
					"file": 1,
					"line": 529,
					"col": 15,
					"lit": "Clone repository"
				},
				"inputs": {
					"type": 2,
					"map": [
						{
							"key": "script",
							"value": {
								"type": 0,
								"lit": "git clone --depth 1 https://github.com/adrijshikhar/actr.git ."
							}
						},
						{
							"key": {
								"type": 0,
								"lit": "workingDirectory"
							},
							"value": {
								"type": 0,
								"lit": "/tmp/custom"
							}
						}
					]
				},
				"environment": {
					"type": 2,
					"map": [
						{
							"key": {
								"type": 0,
								"lit": "GH_TOKEN"
							},
							"value": {
								"type": 0,
								"lit": "secret_token_val"
							}
						}
					]
				}
			}
		]
	}`

	var spec protocol.JobSpecification
	if err := json.Unmarshal([]byte(rawMsg), &spec); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if spec.JobName != "Real Pipeline Build" {
		t.Errorf("expected JobName 'Real Pipeline Build', got %q", spec.JobName)
	}
	if len(spec.Steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(spec.Steps))
	}
	step := spec.Steps[0]
	if step.Name != "Clone repository" {
		t.Errorf("expected step.Name 'Clone repository', got %q", step.Name)
	}
	if step.Script != "git clone --depth 1 https://github.com/adrijshikhar/actr.git ." {
		t.Errorf("expected script 'git clone ...', got %q", step.Script)
	}
	if step.WorkingDirectory != "/tmp/custom" {
		t.Errorf("expected workingDirectory '/tmp/custom', got %q", step.WorkingDirectory)
	}
	if step.Environment["GH_TOKEN"] != "secret_token_val" {
		t.Errorf("expected GH_TOKEN 'secret_token_val', got %q", step.Environment["GH_TOKEN"])
	}
}

func TestRunServiceClient_AcquireJob_RealGitHubWirePayload(t *testing.T) {
	rawMsg := `{
		"jobId": "5138c7df-015e-554e-bca0-f560528655ee",
		"jobName": "__default",
		"jobDisplayName": "Live Self-Hosted Daemon Test",
		"steps": [
			{
				"type": "action",
				"reference": {
					"type": "script"
				},
				"id": "7b1b99fb-a8d4-4652-94ec-8134aefa7c18",
				"contextName": "__run",
				"name": "__run",
				"displayNameToken": {
					"type": 0,
					"file": 1,
					"line": 58,
					"col": 15,
					"lit": "Clone repository"
				},
				"condition": "success()",
				"inputs": {
					"type": 2,
					"map": [
						{
							"Key": {
								"type": 0,
								"lit": "script"
							},
							"Value": {
								"type": 0,
								"file": 1,
								"line": 59,
								"col": 14,
								"lit": "git clone --depth 1 https://github.com/adrijshikhar/actr.git .\n"
							}
						}
					]
				}
			}
		]
	}`

	var spec protocol.JobSpecification
	if err := json.Unmarshal([]byte(rawMsg), &spec); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if spec.JobName != "Live Self-Hosted Daemon Test" {
		t.Errorf("expected spec.JobName to prioritize human-readable jobDisplayName over '__default', got %q", spec.JobName)
	}

	if len(spec.Steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(spec.Steps))
	}
	step := spec.Steps[0]
	if step.Name != "Clone repository" {
		t.Errorf("expected step.Name 'Clone repository', got %q", step.Name)
	}
	if strings.TrimSpace(step.Script) != "git clone --depth 1 https://github.com/adrijshikhar/actr.git ." {
		t.Errorf("expected script to be parsed from Key/Value AST map, got %q", step.Script)
	}
}

func TestJobSpecification_UnmarshalJSON_ContainerSpec(t *testing.T) {
	raw := `{
		"jobId": "job-container-test",
		"jobName": "test-in-docker",
		"resources": {
			"containers": [
				{
					"alias": "container",
					"image": "node:20-bookworm",
					"options": "--cpus 2 --memory 4g",
					"environment": {
						"NODE_ENV": "production"
					},
					"volumes": ["/host/path:/container/path"],
					"ports": ["8080:80"]
				}
			]
		},
		"steps": [
			{"id": "step-1", "name": "Test Node", "script": "node --version"}
		]
	}`

	var spec protocol.JobSpecification
	err := json.Unmarshal([]byte(raw), &spec)
	require.NoError(t, err)
	assert.Equal(t, "job-container-test", spec.JobID)
	require.NotNil(t, spec.Container)
	assert.Equal(t, "node:20-bookworm", spec.Container.Image)
	assert.Equal(t, "--cpus 2 --memory 4g", spec.Container.Options)
	assert.Equal(t, "production", spec.Container.Environment["NODE_ENV"])
	assert.Equal(t, []string{"/host/path:/container/path"}, spec.Container.Volumes)
	assert.Equal(t, []string{"8080:80"}, spec.Container.Ports)
}

func TestJobSpecification_UnmarshalJSON_DirectContainer(t *testing.T) {
	raw := `{
		"jobId": "job-direct-container",
		"container": {
			"image": "golang:1.24-alpine",
			"options": "--network host",
			"env": {
				"CGO_ENABLED": "0"
			}
		}
	}`

	var spec protocol.JobSpecification
	err := json.Unmarshal([]byte(raw), &spec)
	require.NoError(t, err)
	require.NotNil(t, spec.Container)
	assert.Equal(t, "golang:1.24-alpine", spec.Container.Image)
	assert.Equal(t, "--network host", spec.Container.Options)
	assert.Equal(t, "0", spec.Container.Environment["CGO_ENABLED"])
}

func TestJobSpecification_UnmarshalJSON_ContextDataContainer(t *testing.T) {
	raw := `{
		"jobId": "job-context-container",
		"contextData": {
			"job": {
				"container": {
					"image": "python:3.12-slim"
				}
			}
		}
	}`

	var spec protocol.JobSpecification
	err := json.Unmarshal([]byte(raw), &spec)
	require.NoError(t, err)
	require.NotNil(t, spec.Container)
	assert.Equal(t, "python:3.12-slim", spec.Container.Image)
}

func TestJobSpecification_UnmarshalJSON_ContainerAST(t *testing.T) {
	raw := `{
		"jobId": "job-ast-container",
		"resources": {
			"containers": [
				{
					"alias": "container",
					"image": {
						"type": 0,
						"lit": "rust:1.80"
					},
					"options": {
						"type": 0,
						"lit": "--rm"
					},
					"environment": {
						"type": 2,
						"map": [
							{
								"Key": {"type": 0, "lit": "RUST_BACKTRACE"},
								"Value": {"type": 0, "lit": "1"}
							}
						]
					}
				}
			]
		}
	}`

	var spec protocol.JobSpecification
	err := json.Unmarshal([]byte(raw), &spec)
	require.NoError(t, err)
	require.NotNil(t, spec.Container)
	assert.Equal(t, "rust:1.80", spec.Container.Image)
	assert.Equal(t, "--rm", spec.Container.Options)
	assert.Equal(t, "1", spec.Container.Environment["RUST_BACKTRACE"])
}

func TestJobSpecification_UnmarshalJSON_NoContainer(t *testing.T) {
	raw := `{
		"jobId": "job-no-container",
		"jobName": "host-job"
	}`

	var spec protocol.JobSpecification
	err := json.Unmarshal([]byte(raw), &spec)
	require.NoError(t, err)
	assert.Nil(t, spec.Container)
}

func TestJobSpecification_UnmarshalJSON_MultipleContainersSelectAlias(t *testing.T) {
	raw := `{
		"jobId": "job-multi-container",
		"resources": {
			"containers": [
				{
					"alias": "redis",
					"image": "redis:alpine"
				},
				{
					"alias": "container",
					"image": "node:20"
				}
			]
		}
	}`

	var spec protocol.JobSpecification
	err := json.Unmarshal([]byte(raw), &spec)
	require.NoError(t, err)
	require.NotNil(t, spec.Container)
	assert.Equal(t, "node:20", spec.Container.Image)
}

func TestParseAcquiredJob_StepReferencePreservation(t *testing.T) {
	rawJSON := `{
		"jobId": "job-market-1",
		"jobName": "test-marketplace",
		"steps": [
			{
				"id": "step-setup-go",
				"name": "Set up Go",
				"type": "uses",
				"reference": {
					"id": "ref-1",
					"type": "repository",
					"name": "actions/setup-go",
					"version": "v5",
					"ref": "cdcb360c5f153b3f9ff8182b8342530d0337c3b3",
					"path": "action.yml"
				},
				"inputs": {
					"go-version": "1.24",
					"cache": "true"
				}
			},
			{
				"id": "step-version-only",
				"name": "Checkout",
				"reference": {
					"name": "actions/checkout",
					"version": "v4"
				}
			},
			{
				"id": "step-ref-only",
				"name": "Cache",
				"reference": {
					"name": "actions/cache",
					"ref": "v3"
				}
			}
		]
	}`

	job, err := protocol.ParseAcquiredJob([]byte(rawJSON))
	require.NoError(t, err)
	require.NotNil(t, job)
	require.Len(t, job.Steps, 3)

	// Step 0: full reference
	s0 := job.Steps[0]
	require.NotNil(t, s0.Reference)
	assert.Equal(t, "ref-1", s0.Reference.ID)
	assert.Equal(t, "actions/setup-go", s0.Reference.Name)
	assert.Equal(t, "v5", s0.Reference.Version)
	assert.Equal(t, "cdcb360c5f153b3f9ff8182b8342530d0337c3b3", s0.Reference.Ref)
	assert.Equal(t, "repository", s0.Reference.Type)
	assert.Equal(t, "action.yml", s0.Reference.Path)
	assert.Equal(t, "1.24", s0.Inputs["go-version"])
	assert.Equal(t, "uses", s0.Type)

	// Step 1: version only -> ref should default to version
	s1 := job.Steps[1]
	require.NotNil(t, s1.Reference)
	assert.Equal(t, "actions/checkout", s1.Reference.Name)
	assert.Equal(t, "v4", s1.Reference.Version)
	assert.Equal(t, "v4", s1.Reference.Ref)
	assert.Equal(t, "uses", s1.Type)

	// Step 2: ref only -> version should default to ref
	s2 := job.Steps[2]
	require.NotNil(t, s2.Reference)
	assert.Equal(t, "actions/cache", s2.Reference.Name)
	assert.Equal(t, "v3", s2.Reference.Version)
	assert.Equal(t, "v3", s2.Reference.Ref)
	assert.Equal(t, "uses", s2.Type)
}

func TestParseAcquiredJob_EnvironmentVariables(t *testing.T) {
	rawJSON := `{
		"jobId": "job-env-test",
		"environmentVariables": [
			{"Key": "EXPECTED_ARCH", "Value": "arm64"},
			{"Key": "PLATFORM", "Value": "darwin-arm64"},
			{"k": "V8_TARGET", "v": "aarch64-apple-darwin"}
		],
		"steps": [
			{
				"id": "step-1",
				"name": "Check Env",
				"condition": "success()",
				"type": "run",
				"script": "echo hello"
			}
		]
	}`

	job, err := protocol.ParseAcquiredJob([]byte(rawJSON))
	require.NoError(t, err)
	require.NotNil(t, job)
	require.NotNil(t, job.Environment)
	assert.Equal(t, "arm64", job.Environment["EXPECTED_ARCH"])
	assert.Equal(t, "darwin-arm64", job.Environment["PLATFORM"])
	assert.Equal(t, "aarch64-apple-darwin", job.Environment["V8_TARGET"])
}

func TestParseAcquiredJob_Endpoints(t *testing.T) {
	rawJSON := `{
		"jobId": "job-endpoints-test",
		"resources": {
			"endpoints": [
				{
					"name": "SystemVssConnection",
					"type": "SystemVssConnection",
					"url": "https://pipelines.actions.githubusercontent.com/test",
					"authorization": {
						"parameters": {
							"AccessToken": "test-runtime-token"
						}
					},
					"data": {
						"ResultsServiceUrl": "https://results-receiver.actions.githubusercontent.com/",
						"CacheServerUrl": "https://artifactcache.actions.githubusercontent.com/"
					}
				}
			]
		},
		"steps": []
	}`

	job, err := protocol.ParseAcquiredJob([]byte(rawJSON))
	require.NoError(t, err)
	require.NotNil(t, job)
	require.Len(t, job.Endpoints, 1)

	ep := job.Endpoints[0]
	assert.Equal(t, "SystemVssConnection", ep.Name)
	assert.Equal(t, "https://pipelines.actions.githubusercontent.com/test", ep.URL)
	assert.Equal(t, "test-runtime-token", job.JobToken)
	assert.Equal(t, "https://results-receiver.actions.githubusercontent.com/", ep.Data["ResultsServiceUrl"])
	assert.Equal(t, "https://artifactcache.actions.githubusercontent.com/", ep.Data["CacheServerUrl"])
}

func TestParseAcquiredJob_StepConditions(t *testing.T) {
	rawJSON := `{
		"jobId": "job-conditions-test",
		"steps": [
			{
				"id": "s1",
				"name": "Always run",
				"condition": "always()",
				"type": "run",
				"script": "echo always"
			},
			{
				"id": "s2",
				"name": "Docker step",
				"condition": {"expression": "runner.os == 'macOS' && startsWith(matrix.platform, 'linux-')"},
				"type": "run",
				"script": "docker run"
			},
			{
				"id": "s3",
				"name": "Default step",
				"type": "run",
				"script": "echo default"
			}
		]
	}`

	job, err := protocol.ParseAcquiredJob([]byte(rawJSON))
	require.NoError(t, err)
	require.NotNil(t, job)
	require.Len(t, job.Steps, 3)

	assert.Equal(t, "always()", job.Steps[0].Condition)
	assert.Equal(t, "runner.os == 'macOS' && startsWith(matrix.platform, 'linux-')", job.Steps[1].Condition)
	assert.Equal(t, "success()", job.Steps[2].Condition)
}

func TestParseAcquiredJob_NullEnvironmentEntry(t *testing.T) {
	rawJSON := `{
		"jobId": "job-null-env-test",
		"environmentVariables": [
			{"key": "FOO", "value": null},
			{"key": "BAR", "value": "baz"}
		],
		"steps": []
	}`

	job, err := protocol.ParseAcquiredJob([]byte(rawJSON))
	require.NoError(t, err)
	require.NotNil(t, job)
	assert.Equal(t, "", job.Environment["FOO"])
	assert.Equal(t, "baz", job.Environment["BAR"])
	_, hasKey := job.Environment["key"]
	_, hasVal := job.Environment["value"]
	assert.False(t, hasKey, "environment should not contain bogus 'key'")
	assert.False(t, hasVal, "environment should not contain bogus 'value'")
}

func TestParseAcquiredJob_EmptyContainerDoesNotSetImage(t *testing.T) {
	rawJSON := `{
		"jobId": "job-empty-container-test",
		"container": {},
		"steps": []
	}`

	job, err := protocol.ParseAcquiredJob([]byte(rawJSON))
	require.NoError(t, err)
	require.NotNil(t, job)
	if job.Container != nil {
		assert.Empty(t, job.Container.Image, "container image should not be '{}'")
	}
}

func TestJobSpecification_JSONRoundTrip(t *testing.T) {
	orig := protocol.JobSpecification{
		JobID:   "job-roundtrip",
		JobName: "Test Job",
		PlanID:  "plan-1",
		Environment: map[string]string{
			"VAR1": "val1",
		},
		Endpoints: []protocol.ServiceEndpoint{
			{
				Name: "TestEndpoint",
				Type: "OAuth",
				URL:  "https://example.com",
				Authorization: &protocol.EndpointAuthorization{
					Scheme:     "Bearer",
					Parameters: map[string]string{"token": "secret"},
				},
			},
		},
	}

	data, err := json.Marshal(orig)
	require.NoError(t, err)

	var restored protocol.JobSpecification
	err = json.Unmarshal(data, &restored)
	require.NoError(t, err)

	assert.Equal(t, orig.JobID, restored.JobID)
	assert.Equal(t, orig.Environment["VAR1"], restored.Environment["VAR1"])
	require.Len(t, restored.Endpoints, 1)
	assert.Equal(t, orig.Endpoints[0].Name, restored.Endpoints[0].Name)
	assert.Equal(t, orig.Endpoints[0].Authorization.Parameters["token"], restored.Endpoints[0].Authorization.Parameters["token"])
}

func TestParseAcquiredJob_TopLevelEndpoints_ASTData(t *testing.T) {
	wireJSON := `{
		"jobId": "job-top-ep",
		"endpoints": [
			{
				"name": "TopEP",
				"type": "Custom",
				"url": "https://custom.endpoint.com",
				"data": {
					"key1": {"lit": "literal-val"},
					"key2": "plain-val"
				}
			}
		]
	}`

	var job protocol.JobSpecification
	err := json.Unmarshal([]byte(wireJSON), &job)
	require.NoError(t, err)
	require.Len(t, job.Endpoints, 1)
	assert.Equal(t, "TopEP", job.Endpoints[0].Name)
	assert.Equal(t, "literal-val", job.Endpoints[0].Data["key1"])
	assert.Equal(t, "plain-val", job.Endpoints[0].Data["key2"])
}

func TestParseAcquiredJob_BooleanConditions(t *testing.T) {
	wireJSON := `{
		"jobId": "job-bool-cond",
		"steps": [
			{
				"id": "step-false",
				"condition": false
			},
			{
				"id": "step-true",
				"if": true
			}
		]
	}`

	var job protocol.JobSpecification
	err := json.Unmarshal([]byte(wireJSON), &job)
	require.NoError(t, err)
	require.Len(t, job.Steps, 2)
	assert.Equal(t, "false", job.Steps[0].Condition)
	assert.Equal(t, "true", job.Steps[1].Condition)
}

func TestParseAcquiredJob_EndpointsDeduplication(t *testing.T) {
	wireJSON := `{
		"jobId": "job-dedup-ep",
		"endpoints": [
			{
				"name": "SharedEP",
				"type": "Custom",
				"url": "https://custom.endpoint.com"
			}
		],
		"resources": {
			"endpoints": [
				{
					"name": "SharedEP",
					"type": "Custom",
					"url": "https://custom.endpoint.com"
				},
				{
					"name": "OtherEP",
					"type": "Custom",
					"url": "https://other.endpoint.com"
				}
			]
		}
	}`

	var job protocol.JobSpecification
	err := json.Unmarshal([]byte(wireJSON), &job)
	require.NoError(t, err)
	require.Len(t, job.Endpoints, 2)
	assert.Equal(t, "SharedEP", job.Endpoints[0].Name)
	assert.Equal(t, "OtherEP", job.Endpoints[1].Name)
}

func TestCompleteJobRequest_UnmarshalJSON_SkipsNilOutputs(t *testing.T) {
	raw := `{"planId":"p1","jobId":"j1","conclusion":"succeeded","outputs":{"ok":"val","ignored":null}}`
	var req protocol.CompleteJobRequest
	require.NoError(t, json.Unmarshal([]byte(raw), &req))
	assert.Equal(t, "val", req.Outputs["ok"])
	_, exists := req.Outputs["ignored"]
	assert.False(t, exists, "nil output value should be skipped")
}

func TestRunServiceClient_RejectsInsecureRemoteURL(t *testing.T) {
	client := protocol.NewRunServiceClient(nil)
	_, err := client.AcquireJob(context.Background(), "http://run.example.com", "req-1", "bill-1", "tok")
	require.Error(t, err)
	_, err = client.RenewJob(context.Background(), "http://run.example.com", "p1", "j1", "tok")
	require.Error(t, err)
	err = client.CompleteJob(context.Background(), "http://run.example.com", "tok", &protocol.CompleteJobRequest{
		PlanID:     "p1",
		JobID:      "j1",
		Conclusion: "succeeded",
	})
	require.Error(t, err)
}

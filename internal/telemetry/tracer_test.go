package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTracer_NoOpWhenUnset(t *testing.T) {
	ctx := context.Background()
	mgr, err := NewManager(ctx, "actr-test")
	if err != nil {
		t.Fatalf("unexpected error creating tracer manager: %v", err)
	}
	defer func() { _ = mgr.Shutdown(ctx) }()

	if mgr.Enabled() {
		t.Fatal("expected Enabled() to be false when OTEL_EXPORTER_OTLP_ENDPOINT is unset")
	}

	jobCtx, jobSpan := mgr.StartJobSpan(ctx, "test-job", "CI", "repo", "owner")
	defer jobSpan.End()

	traceID := jobSpan.SpanContext().TraceID().String()
	if traceID == "" || traceID == "00000000000000000000000000000000" {
		t.Fatalf("expected valid non-zero TraceID, got %q", traceID)
	}

	_, stepSpan := mgr.StartStepSpan(jobCtx, "step-1", "Compile")
	defer stepSpan.End()

	spanID := stepSpan.SpanContext().SpanID().String()
	if spanID == "" || spanID == "0000000000000000" {
		t.Fatalf("expected valid non-zero SpanID, got %q", spanID)
	}
}

func TestTracer_EnabledWithHTTPEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", srv.URL)
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "http/protobuf")

	ctx := context.Background()
	mgr, err := NewManager(ctx, "actr-test-enabled")
	if err != nil {
		t.Fatalf("unexpected error creating enabled tracer manager: %v", err)
	}
	defer func() { _ = mgr.Shutdown(ctx) }()

	if !mgr.Enabled() {
		t.Fatal("expected Enabled() to be true when OTEL_EXPORTER_OTLP_ENDPOINT is set")
	}

	jobCtx, jobSpan := mgr.StartJobSpan(ctx, "job-42", "build", "org/repo", "user")
	defer jobSpan.End()

	if !jobSpan.SpanContext().IsValid() {
		t.Fatal("expected valid span context")
	}

	_, stepSpan := mgr.StartStepSpan(jobCtx, "step-42", "test-step")
	defer stepSpan.End()

	if !stepSpan.SpanContext().IsValid() {
		t.Fatal("expected valid step span context")
	}
}

func TestTracer_ShutdownNilCleanFn(t *testing.T) {
	mgr := &Manager{}
	if err := mgr.Shutdown(context.Background()); err != nil {
		t.Fatalf("expected nil error on Shutdown with nil cleanFn, got %v", err)
	}

	var nilMgr *Manager
	if nilMgr.Enabled() {
		t.Fatal("expected nil Manager.Enabled() to return false")
	}
	if err := nilMgr.Shutdown(context.Background()); err != nil {
		t.Fatalf("expected nil error on nil Manager.Shutdown(), got %v", err)
	}
	ctx, span := nilMgr.StartJobSpan(context.Background(), "job", "wf", "repo", "owner")
	if span == nil || ctx == nil {
		t.Fatal("expected valid non-nil ctx and span from nil Manager.StartJobSpan")
	}
	_, stepSpan := nilMgr.StartStepSpan(ctx, "step", "name")
	if stepSpan == nil {
		t.Fatal("expected valid non-nil stepSpan from nil Manager.StartStepSpan")
	}
}

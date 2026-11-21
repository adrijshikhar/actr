package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/adrijshikhar/actr/internal/monitor"
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

func TestTracer_InjectStepTraceContext(t *testing.T) {
	ctx := context.Background()
	mgr, err := NewManager(ctx, "actr-test")
	if err != nil {
		t.Fatalf("unexpected error creating tracer manager: %v", err)
	}
	defer func() { _ = mgr.Shutdown(ctx) }()

	jobCtx, jobSpan := mgr.StartJobSpan(ctx, "job-100", "CI", "repo", "owner")
	defer jobSpan.End()

	stepCtx, stepSpan := mgr.StartStepSpan(jobCtx, "step-100", "Compile")
	defer stepSpan.End()

	env := make(map[string]string)
	mgr.InjectStepTraceContext(stepCtx, env)

	traceparent, ok := env["TRACEPARENT"]
	if !ok || traceparent == "" {
		t.Fatalf("expected TRACEPARENT to be injected into env, got: %v", env)
	}

	sc := stepSpan.SpanContext()
	expectedTraceparent := "00-" + sc.TraceID().String() + "-" + sc.SpanID().String() + "-" + sc.TraceFlags().String()
	if traceparent != expectedTraceparent {
		t.Fatalf("expected TRACEPARENT %q, got %q", expectedTraceparent, traceparent)
	}
}

func TestTracer_MetricsRecording_ZeroConfigAndNil(t *testing.T) {
	ctx := context.Background()
	mgr, err := NewManager(ctx, "actr-test")
	if err != nil {
		t.Fatalf("unexpected error creating tracer manager: %v", err)
	}
	defer func() { _ = mgr.Shutdown(ctx) }()

	// Calling on zero-config initialized manager should not panic
	mgr.RecordJobCompletion(ctx, "job-1", "build", "repo", "owner", "success", 1.25)
	mgr.RecordStepCompletion(ctx, "step-1", "run", "success", 0, 0.45)

	// Calling on nil manager should not panic
	var nilMgr *Manager
	nilMgr.RecordJobCompletion(ctx, "job-1", "build", "repo", "owner", "success", 1.25)
	nilMgr.RecordStepCompletion(ctx, "step-1", "run", "success", 0, 0.45)
}

func TestTracer_RecordTelemetrySample(t *testing.T) {
	ctx := context.Background()
	mgr, err := NewManager(ctx, "actr-test")
	if err != nil {
		t.Fatalf("unexpected error creating tracer manager: %v", err)
	}
	defer func() { _ = mgr.Shutdown(ctx) }()

	sample := &monitor.TelemetrySample{
		StepCPU:        55.5,
		StepRSSBytes:   256 * 1024 * 1024,
		NetRxRate:      1024 * 1024,
		NetTxRate:      512 * 1024,
		DiskWriteRate:  4 * 1024 * 1024,
		HostCPU:        12.5,
		HostMemUsed:    8 * 1024 * 1024 * 1024,
		HostMemTotal:   16 * 1024 * 1024 * 1024,
		DiskUsedBytes:  50 * 1024 * 1024 * 1024,
		DiskTotalBytes: 250 * 1024 * 1024 * 1024,
		ActiveStepName: "Compile binaries",
	}

	// Should not panic on zero-config manager
	mgr.RecordTelemetrySample(ctx, "job-1", "my-repo", sample)

	// Should not panic on nil manager or nil sample
	var nilMgr *Manager
	nilMgr.RecordTelemetrySample(ctx, "job-1", "my-repo", sample)
	mgr.RecordTelemetrySample(ctx, "job-1", "my-repo", nil)
}

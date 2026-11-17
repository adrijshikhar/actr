package monitor

import (
	"math"
	"os"
	"sync"
	"testing"
	"time"
)

func TestTelemetrySampler_SampleHostAndProcess(t *testing.T) {
	sampler := NewTelemetrySampler()
	rawPID := os.Getpid()
	if rawPID < 0 || rawPID > math.MaxInt32 {
		t.Fatalf("PID out of int32 range: %d", rawPID)
	}
	pid := int32(rawPID)
	sampler.SetTargetPID(pid, pid)

	sample := sampler.Sample()
	if sample == nil {
		t.Fatal("expected non-nil sample")
	}
	if sample.PID != pid {
		t.Fatalf("expected PID %d, got %d", pid, sample.PID)
	}
	if sample.HostMemTotal == 0 {
		t.Fatal("expected non-zero HostMemTotal")
	}
	if sample.HostMemUsed == 0 {
		t.Fatal("expected non-zero HostMemUsed")
	}
}

func TestTelemetrySampler_NoTargetPID(t *testing.T) {
	sampler := NewTelemetrySampler()

	sample := sampler.Sample()
	if sample == nil {
		t.Fatal("expected non-nil sample")
	}
	if sample.PID != 0 {
		t.Fatalf("expected PID 0, got %d", sample.PID)
	}
	if sample.StepCPU != 0 {
		t.Fatalf("expected StepCPU 0, got %f", sample.StepCPU)
	}
	if sample.StepRSSBytes != 0 {
		t.Fatalf("expected StepRSSBytes 0, got %d", sample.StepRSSBytes)
	}
	if sample.HostMemTotal == 0 {
		t.Fatal("expected non-zero HostMemTotal")
	}
}

func TestTelemetrySampler_InvalidPID(t *testing.T) {
	sampler := NewTelemetrySampler()
	sampler.SetTargetPID(999999999, 999999999)

	sample := sampler.Sample()
	if sample == nil {
		t.Fatal("expected non-nil sample")
	}
	if sample.PID != 999999999 {
		t.Fatalf("expected PID 999999999, got %d", sample.PID)
	}
	if sample.StepCPU != 0 {
		t.Fatalf("expected StepCPU 0, got %f", sample.StepCPU)
	}
	if sample.StepRSSBytes != 0 {
		t.Fatalf("expected StepRSSBytes 0, got %d", sample.StepRSSBytes)
	}
}

func TestTelemetrySampler_ConcurrentAccess(t *testing.T) {
	sampler := NewTelemetrySampler()
	rawPID := os.Getpid()
	if rawPID < 0 || rawPID > math.MaxInt32 {
		t.Fatalf("PID out of int32 range: %d", rawPID)
	}
	pid := int32(rawPID)

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(2)
		go func(p int32) {
			defer wg.Done()
			sampler.SetTargetPID(p, p)
		}(pid)

		go func() {
			defer wg.Done()
			s := sampler.Sample()
			if s == nil {
				t.Errorf("expected non-nil sample")
			}
		}()
	}
	wg.Wait()
}

func TestTelemetrySampler_RunnerMetricsFields(t *testing.T) {
	sampler := NewTelemetrySampler()
	sampler.SetWorkspaceDir(t.TempDir())
	sampler.SetTraceContext("4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7", "Compile binaries", time.Now().Add(-5*time.Second))

	sample := sampler.Sample()
	if sample == nil {
		t.Fatal("expected non-nil sample")
	}

	// Verify trace and step context
	if sample.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("expected trace ID '4bf92f3577b34da6a3ce929d0e0e4736', got %q", sample.TraceID)
	}
	if sample.SpanID != "00f067aa0ba902b7" {
		t.Fatalf("expected span ID '00f067aa0ba902b7', got %q", sample.SpanID)
	}
	if sample.ActiveStepName != "Compile binaries" {
		t.Fatalf("expected active step name 'Compile binaries', got %q", sample.ActiveStepName)
	}
	if sample.StepElapsedSec < 4 {
		t.Fatalf("expected step elapsed >= 4s, got %d", sample.StepElapsedSec)
	}

	// Verify workspace storage metrics
	if sample.DiskTotalBytes == 0 {
		t.Fatal("expected non-zero DiskTotalBytes")
	}
	if sample.DiskFreeBytes == 0 {
		t.Fatal("expected non-zero DiskFreeBytes")
	}
	if sample.DiskUsedRatio < 0 || sample.DiskUsedRatio > 1.0 {
		t.Fatalf("expected DiskUsedRatio between 0 and 1, got %f", sample.DiskUsedRatio)
	}
}

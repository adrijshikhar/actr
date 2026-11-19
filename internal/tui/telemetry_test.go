package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/adrijshikhar/actr/internal/config"
	"github.com/adrijshikhar/actr/internal/monitor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTelemetryWidget_RenderGauges(t *testing.T) {
	tw := NewTelemetryWidget()
	sample := &monitor.TelemetrySample{
		Timestamp:    time.Now(),
		PID:          12345,
		PGID:         12345,
		StepCPU:      42.5,
		StepRSSBytes: 256 * 1024 * 1024, // 256MB
		HostCPU:      15.0,
		HostMemUsed:  8 * 1024 * 1024 * 1024,
		HostMemTotal: 16 * 1024 * 1024 * 1024,
	}

	tw.Update(sample)
	view := tw.View(60)

	if !strings.Contains(view, "CPU :") || !strings.Contains(view, "42.5%") {
		t.Fatalf("expected view to contain CPU percentage, got:\n%s", view)
	}
	if !strings.Contains(view, "RAM :") || !strings.Contains(view, "256 MB") {
		t.Fatalf("expected view to contain RAM usage, got:\n%s", view)
	}
	if !strings.Contains(view, "PID: 12345") {
		t.Fatalf("expected view to contain header metadata, got:\n%s", view)
	}
	if !strings.Contains(view, "DISK:") {
		t.Fatalf("expected view to contain DISK line, got:\n%s", view)
	}
}

func TestTelemetryWidget_AwaitingTelemetry(t *testing.T) {
	t.Run("nil sample in widget", func(t *testing.T) {
		tw := NewTelemetryWidget()
		view := tw.View(60)
		if !strings.Contains(view, "Awaiting step execution telemetry...") {
			t.Fatalf("expected awaiting message, got:\n%s", view)
		}
	})

	t.Run("nil widget pointer", func(t *testing.T) {
		var tw *TelemetryWidget
		view := tw.View(60)
		if !strings.Contains(view, "Awaiting step execution telemetry...") {
			t.Fatalf("expected awaiting message for nil widget, got:\n%s", view)
		}
		// Also verify Update does not panic
		tw.Update(&monitor.TelemetrySample{})
	})
}

func TestTelemetryWidget_RatioBoundsAndClamp(t *testing.T) {
	tw := NewTelemetryWidget()

	// High CPU (> 100%), negative CPU (< 0%), zero HostMemTotal
	sample := &monitor.TelemetrySample{
		PID:          999,
		PGID:         999,
		StepCPU:      185.5,
		StepRSSBytes: 512 * 1024 * 1024,
		HostCPU:      88.0,
		HostMemUsed:  0,
		HostMemTotal: 0, // Should not divide by zero
	}

	tw.Update(sample)
	view := tw.View(60)

	if !strings.Contains(view, "185.5%") {
		t.Fatalf("expected view to contain 185.5%%, got:\n%s", view)
	}
	if !strings.Contains(view, "512 MB") {
		t.Fatalf("expected view to contain 512 MB, got:\n%s", view)
	}

	// Negative CPU
	sample.StepCPU = -5.0
	tw.Update(sample)
	viewNeg := tw.View(60)
	if !strings.Contains(viewNeg, "-5.0%") {
		t.Fatalf("expected view to contain -5.0%%, got:\n%s", viewNeg)
	}
}

func TestTelemetryWidget_Widths(t *testing.T) {
	tw := NewTelemetryWidget()
	sample := &monitor.TelemetrySample{
		PID:          100,
		PGID:         100,
		StepCPU:      10.0,
		StepRSSBytes: 1024 * 1024,
		HostCPU:      5.0,
		HostMemTotal: 1024 * 1024 * 1024,
	}
	tw.Update(sample)

	widths := []int{-10, 0, 5, 20, 25, 40, 80, 160}
	for _, w := range widths {
		view := tw.View(w)
		if view == "" {
			t.Fatalf("expected non-empty view for width %d", w)
		}
	}
}

func TestTelemetryWidget_FormatBytes(t *testing.T) {
	tests := []struct {
		input uint64
		want  string
	}{
		{input: 0, want: "0 B"},
		{input: 512, want: "512 B"},
		{input: 1023, want: "1023 B"},
		{input: 1024, want: "1 KB"},
		{input: 1024*1024 - 1, want: "1 MB"}, // 1023.999 KB rounds to 1 MB rather than 1024.0 KB
		{input: 1536, want: "1.5 KB"},
		{input: 10 * 1024, want: "10 KB"},
		{input: 1024 * 1024, want: "1 MB"},
		{input: 256 * 1024 * 1024, want: "256 MB"},
		{input: uint64(1.5 * 1024 * 1024 * 1024), want: "1.5 GB"},
		{input: 16 * 1024 * 1024 * 1024, want: "16 GB"},
		{input: 1024 * 1024 * 1024 * 1024, want: "1 TB"},
	}

	for _, tc := range tests {
		got := formatBytes(tc.input)
		if got != tc.want {
			t.Errorf("formatBytes(%d) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestTelemetryWidget_ViewCompact(t *testing.T) {
	t.Run("nil sample compact", func(t *testing.T) {
		tw := NewTelemetryWidget()
		view := tw.ViewCompact(50)
		if !strings.Contains(view, "Awaiting step execution telemetry...") {
			t.Fatalf("expected awaiting message in compact view, got:\n%s", view)
		}
		if !strings.Contains(view, "IDLE") {
			t.Fatalf("expected IDLE pill in compact view, got:\n%s", view)
		}
	})

	t.Run("active sample compact", func(t *testing.T) {
		tw := NewTelemetryWidget()
		tw.Update(&monitor.TelemetrySample{
			PID:          5555,
			PGID:         5555,
			StepCPU:      30.0,
			StepRSSBytes: 128 * 1024 * 1024,
			HostCPU:      10.0,
			HostMemTotal: 1024 * 1024 * 1024,
		})
		view := tw.ViewCompact(50)
		if !strings.Contains(view, "ACTIVE") {
			t.Fatalf("expected ACTIVE pill in compact view, got:\n%s", view)
		}
		if !strings.Contains(view, "PID: 5555") {
			t.Fatalf("expected PID in compact view, got:\n%s", view)
		}
	})
}

func TestTelemetryWidget_StrictMonospaceAlignment(t *testing.T) {
	tw := NewTelemetryWidget()
	tw.Update(&monitor.RunnerMetrics{
		PID:            12434,
		PGID:           12434,
		StepCPU:        48.2,
		StepRSSBytes:   195 * 1024 * 1024,
		DiskUsedBytes:  18 * 1024 * 1024 * 1024,
		DiskTotalBytes: 250 * 1024 * 1024 * 1024,
		DiskFreeBytes:  142 * 1024 * 1024 * 1024,
		DiskUsedRatio:  0.14,
		DiskWriteRate:  18 * 1024 * 1024,
		NetRxRate:      14 * 1024 * 1024,
		ActiveStepName: "Compile binaries",
		StepElapsedSec: 42,
		TraceID:        "4bf92f3577b34da6a3ce929d0e0e4736",
	})

	view := tw.View(90)

	// Ensure progress bar tracks start on the EXACT same column
	lines := strings.Split(view, "\n")
	var cpuLine, ramLine, diskLine string
	for _, l := range lines {
		if strings.Contains(l, "CPU :") {
			cpuLine = l
		}
		if strings.Contains(l, "RAM :") {
			ramLine = l
		}
		if strings.Contains(l, "DISK:") {
			diskLine = l
		}
	}

	if cpuLine == "" || ramLine == "" || diskLine == "" {
		t.Fatalf("expected view to have CPU, RAM, and DISK lines, got:\n%s", view)
	}

	// Bar opening bracket '[' must appear at identical column indices
	cpuIdx := strings.Index(cpuLine, "[")
	ramIdx := strings.Index(ramLine, "[")
	diskIdx := strings.Index(diskLine, "[")

	if cpuIdx != ramIdx || ramIdx != diskIdx {
		t.Fatalf("misaligned progress bars! cpu '[' at %d, ram '[' at %d, disk '[' at %d", cpuIdx, ramIdx, diskIdx)
	}

	// Check Trace ID present
	if !strings.Contains(view, "4bf92f35") {
		t.Fatalf("expected trace ID in view, got:\n%s", view)
	}
}

func TestTelemetryWidget_IOLineAndProcessCount(t *testing.T) {
	t.Run("with io rates and process count", func(t *testing.T) {
		tw := NewTelemetryWidget()
		tw.Update(&monitor.RunnerMetrics{
			PID:            4200,
			ProcessCount:   5,
			StepCPU:        50.0,
			StepRSSBytes:   500 * 1024 * 1024,
			DiskWriteRate:  20 * 1024 * 1024,
			NetRxRate:      10 * 1024 * 1024,
			NetTxRate:      2 * 1024 * 1024,
			ActiveStepName: "Build",
			StepElapsedSec: 65,
			TraceID:        "abcd1234ef0123456789",
		})
		view := tw.View(80)
		if !strings.Contains(view, "PID: 4200 (5 procs)") {
			t.Fatalf("expected process count in view, got:\n%s", view)
		}
		if !strings.Contains(view, "TIME: 01:05") {
			t.Fatalf("expected formatted elapsed time in view, got:\n%s", view)
		}
		if !strings.Contains(view, "TRACE: abcd1234...") {
			t.Fatalf("expected truncated trace ID in view, got:\n%s", view)
		}
		if !strings.Contains(view, "I/O :         NET: 10 MB/s ↓  2 MB/s ↑  │  DISK WR: 20 MB/s") {
			t.Fatalf("expected aligned I/O line in view, got:\n%s", view)
		}
	})

	t.Run("zero io rates omits io line", func(t *testing.T) {
		tw := NewTelemetryWidget()
		tw.Update(&monitor.RunnerMetrics{
			PID:            100,
			StepCPU:        10.0,
			StepRSSBytes:   100 * 1024 * 1024,
			DiskWriteRate:  0,
			NetRxRate:      0,
			NetTxRate:      0,
			ActiveStepName: "Test",
		})
		view := tw.View(80)
		if strings.Contains(view, "I/O :") {
			t.Fatalf("expected no I/O line when rates are 0, got:\n%s", view)
		}
		if !strings.Contains(view, "TRACE: Standby") {
			t.Fatalf("expected standby trace when trace ID is empty, got:\n%s", view)
		}
	})
}

func TestTelemetryWidget_WithLimits_CPU(t *testing.T) {
	tw := NewTelemetryWidget()
	limits := &config.LimitsConfig{
		CPU: 2.0, // 2 cores = 200%
	}
	tw.SetLimits(limits)

	sample := &monitor.TelemetrySample{
		PID:          1000,
		StepCPU:      50.0, // 50% / 200% = 25% of 2.0 CPUs
		StepRSSBytes: 256 * 1024 * 1024,
		HostCPU:      20.0,
		HostMemTotal: 16 * 1024 * 1024 * 1024,
	}
	tw.Update(sample)

	view := tw.View(80)
	if !strings.Contains(view, "CPU :  50.0%") {
		t.Fatalf("expected CPU prefix to show raw 50.0%%, got:\n%s", view)
	}
	if !strings.Contains(view, "25% of 2.0 CPUs") {
		t.Fatalf("expected CPU suffix to show 25%% of 2.0 CPUs, got:\n%s", view)
	}
}

func TestTelemetryWidget_WithLimits_Memory(t *testing.T) {
	tw := NewTelemetryWidget()
	limits := &config.LimitsConfig{
		Memory: 4 * 1024 * 1024 * 1024, // 4.0 GB
	}
	tw.SetLimits(limits)

	sample := &monitor.TelemetrySample{
		PID:          1000,
		StepCPU:      10.0,
		StepRSSBytes: 1288490189, // ~1.2 GB -> 30% of 4 GB
		HostCPU:      20.0,
		HostMemTotal: 32 * 1024 * 1024 * 1024,
	}
	tw.Update(sample)

	view := tw.View(80)
	if !strings.Contains(view, "RAM :  1.2 GB") {
		t.Fatalf("expected RAM prefix to show 1.2 GB, got:\n%s", view)
	}
	if !strings.Contains(view, "30% of 4 GB") {
		t.Fatalf("expected RAM suffix to show 30%% of 4 GB, got:\n%s", view)
	}
}

func TestTelemetryWidget_WithLimits_BothAndClamp(t *testing.T) {
	tw := NewTelemetryWidget()
	limits := &config.LimitsConfig{
		CPU:    1.0,                // 100%
		Memory: 1024 * 1024 * 1024, // 1 GB
	}
	tw.SetLimits(limits)

	// Over-limit sample: 250% CPU and 2 GB RAM
	sample := &monitor.TelemetrySample{
		PID:          2000,
		StepCPU:      250.0,
		StepRSSBytes: 2 * 1024 * 1024 * 1024,
		HostCPU:      80.0,
		HostMemTotal: 16 * 1024 * 1024 * 1024,
	}
	tw.Update(sample)

	view := tw.View(80)
	if !strings.Contains(view, "CPU : 250.0%") {
		t.Fatalf("expected raw CPU prefix, got:\n%s", view)
	}
	if !strings.Contains(view, "RAM :    2 GB") {
		t.Fatalf("expected raw RAM prefix, got:\n%s", view)
	}
	// Gauge renders properly without crash
	if !strings.Contains(view, "250% of 1.0 CPUs") {
		t.Fatalf("expected CPU limit suffix, got:\n%s", view)
	}
	if !strings.Contains(view, "200% of 1 GB") {
		t.Fatalf("expected RAM limit suffix, got:\n%s", view)
	}
}

func TestTelemetryWidget_WithLimits_MonospaceAlignment(t *testing.T) {
	tw := NewTelemetryWidget()
	limits := &config.LimitsConfig{
		CPU:    2.0,
		Memory: 4 * 1024 * 1024 * 1024,
	}
	tw.SetLimits(limits)
	tw.Update(&monitor.RunnerMetrics{
		PID:            1234,
		StepCPU:        45.0,
		StepRSSBytes:   512 * 1024 * 1024,
		DiskUsedBytes:  10 * 1024 * 1024 * 1024,
		DiskTotalBytes: 100 * 1024 * 1024 * 1024,
		DiskFreeBytes:  90 * 1024 * 1024 * 1024,
	})

	view := tw.View(80)
	lines := strings.Split(view, "\n")
	var cpuLine, ramLine, diskLine string
	for _, l := range lines {
		if strings.Contains(l, "CPU :") {
			cpuLine = l
		}
		if strings.Contains(l, "RAM :") {
			ramLine = l
		}
		if strings.Contains(l, "DISK:") {
			diskLine = l
		}
	}

	require.NotEmpty(t, cpuLine)
	require.NotEmpty(t, ramLine)
	require.NotEmpty(t, diskLine)

	cpuIdx := strings.Index(cpuLine, "[")
	ramIdx := strings.Index(ramLine, "[")
	diskIdx := strings.Index(diskLine, "[")

	assert.Equal(t, cpuIdx, ramIdx, "CPU and RAM bar brackets should align")
	assert.Equal(t, ramIdx, diskIdx, "RAM and DISK bar brackets should align")
}

func TestTelemetryWidget_WithLimits_ViewCompact(t *testing.T) {
	tw := NewTelemetryWidget()
	limits := &config.LimitsConfig{
		CPU:    2.0,
		Memory: 2 * 1024 * 1024 * 1024,
	}
	tw.SetLimits(limits)
	tw.Update(&monitor.TelemetrySample{
		PID:          3333,
		StepCPU:      50.0,
		StepRSSBytes: 512 * 1024 * 1024,
		HostCPU:      15.0,
		HostMemTotal: 16 * 1024 * 1024 * 1024,
	})

	view := tw.ViewCompact(60)
	if !strings.Contains(view, "CPU:") || !strings.Contains(view, "RAM:") {
		t.Fatalf("expected compact view to contain CPU and RAM, got:\n%s", view)
	}
}

func TestTelemetryWidget_HistoricalMode_Rendering(t *testing.T) {
	tw := NewTelemetryWidget()
	summary := &HistoricalRunSummary{
		JobName:        "build-and-test",
		Status:         monitor.StatusSuccess,
		DurationSec:    225, // 03:45
		Driver:         "docker",
		TraceID:        "4bf92f35e89a",
		PeakCPUPercent: 182.4,
		PeakRSSBytes:   uint64(1.5 * 1024 * 1024 * 1024), // 1.5 GB
		TotalNetRx:     140 * 1024 * 1024,                // 140 MB
		TotalNetTx:     28 * 1024 * 1024,                 // 28 MB
		TotalDiskWrite: 84 * 1024 * 1024,                 // 84 MB
		Limits: &config.LimitsConfig{
			CPU:    2.0,
			Memory: 4 * 1024 * 1024 * 1024,
		},
	}
	tw.SetHistoricalRun(summary)
	require.True(t, tw.IsHistorical())

	view := tw.View(80)

	// Header and pill
	assert.Contains(t, view, "∿ RUN VITALS (PEAK SUMMARY)")
	assert.Contains(t, view, "● COMPLETED (SUCCESS)")

	// Metadata
	assert.Contains(t, view, "RUN: build-and-test")
	assert.Contains(t, view, "DURATION: 03:45")
	assert.Contains(t, view, "DRIVER: docker")
	assert.Contains(t, view, "TRACE: 4bf92f35")

	// Peak metrics
	assert.Contains(t, view, "PEAK CPU:")
	assert.Contains(t, view, "182.4%")
	assert.Contains(t, view, "PEAK RAM:")
	assert.Contains(t, view, "1.5 GB")

	// Total IO
	assert.Contains(t, view, "TOTAL IO:")
	assert.Contains(t, view, "NET: 140 MB ↓  28 MB ↑")
	assert.Contains(t, view, "DISK WR: 84 MB")

	// Status
	assert.Contains(t, view, "STATUS  :")
	assert.Contains(t, view, "Finished successfully (Exit 0)")

	// Strict bracket alignment '[' between PEAK CPU and PEAK RAM
	lines := strings.Split(view, "\n")
	var cpuLine, ramLine string
	for _, l := range lines {
		if strings.Contains(l, "PEAK CPU:") {
			cpuLine = l
		}
		if strings.Contains(l, "PEAK RAM:") {
			ramLine = l
		}
	}
	require.NotEmpty(t, cpuLine, "view must contain PEAK CPU line")
	require.NotEmpty(t, ramLine, "view must contain PEAK RAM line")

	cpuIdx := strings.Index(cpuLine, "[")
	ramIdx := strings.Index(ramLine, "[")
	require.NotEqual(t, -1, cpuIdx)
	require.NotEqual(t, -1, ramIdx)
	assert.Equal(t, cpuIdx, ramIdx, "Peak CPU and Peak RAM bar brackets should align")
}

func TestTelemetryWidget_HistoricalMode_LimitsScaling(t *testing.T) {
	tw := NewTelemetryWidget()
	summary := &HistoricalRunSummary{
		JobName:        "scaling-test",
		Status:         monitor.StatusSuccess,
		DurationSec:    120,
		Driver:         "docker",
		PeakCPUPercent: 182.4,
		PeakRSSBytes:   uint64(1.5 * 1024 * 1024 * 1024),
		Limits: &config.LimitsConfig{
			CPU:    2.0,
			Memory: 4 * 1024 * 1024 * 1024,
		},
	}
	tw.SetHistoricalRun(summary)

	view := tw.View(80)
	// 182.4 / 200 = 91% of 2.0 CPUs
	assert.Contains(t, view, "91% of 2.0 CPUs")
	// 1.5 GB / 4.0 GB = 38% of 4 GB
	assert.Contains(t, view, "38% of 4 GB")

	// Test scaling via widget Limits when summary.Limits is nil
	summaryNoLimits := &HistoricalRunSummary{
		JobName:        "fallback-limits",
		Status:         monitor.StatusSuccess,
		PeakCPUPercent: 100.0,
		PeakRSSBytes:   2 * 1024 * 1024 * 1024,
	}
	tw.SetLimits(&config.LimitsConfig{
		CPU:    4.0,
		Memory: 8 * 1024 * 1024 * 1024,
	})
	tw.SetHistoricalRun(summaryNoLimits)

	view2 := tw.View(80)
	// 100.0 / 400 = 25% of 4.0 CPUs
	assert.Contains(t, view2, "25% of 4.0 CPUs")
	// 2 GB / 8 GB = 25% of 8 GB
	assert.Contains(t, view2, "25% of 8 GB")
}

func TestTelemetryWidget_ClearHistoricalMode(t *testing.T) {
	tw := NewTelemetryWidget()
	tw.Update(&monitor.TelemetrySample{
		PID:          9999,
		StepCPU:      33.3,
		StepRSSBytes: 128 * 1024 * 1024,
		HostCPU:      10.0,
	})

	// Before historical: live mode
	assert.False(t, tw.IsHistorical())
	viewLive1 := tw.View(80)
	assert.Contains(t, viewLive1, "∿ SYSTEM VITALS & RADAR")
	assert.Contains(t, viewLive1, "PID: 9999")

	// Switch to historical
	tw.SetHistoricalRun(&HistoricalRunSummary{
		JobName:        "hist-job",
		Status:         monitor.StatusSuccess,
		PeakCPUPercent: 75.0,
		PeakRSSBytes:   512 * 1024 * 1024,
	})
	assert.True(t, tw.IsHistorical())
	viewHist := tw.View(80)
	assert.Contains(t, viewHist, "∿ RUN VITALS (PEAK SUMMARY)")
	assert.NotContains(t, viewHist, "PID: 9999")

	// Clear historical: restores live vitals
	tw.ClearHistoricalRun()
	assert.False(t, tw.IsHistorical())
	viewLive2 := tw.View(80)
	assert.Contains(t, viewLive2, "∿ SYSTEM VITALS & RADAR")
	assert.Contains(t, viewLive2, "PID: 9999")
	assert.Contains(t, viewLive2, "33.3%")
}

func TestTelemetryWidget_HistoricalMode_ViewCompact(t *testing.T) {
	tw := NewTelemetryWidget()
	summary := &HistoricalRunSummary{
		JobName:        "compact-hist",
		Status:         monitor.StatusSuccess,
		DurationSec:    90,
		PeakCPUPercent: 50.0,
		PeakRSSBytes:   256 * 1024 * 1024,
	}
	tw.SetHistoricalRun(summary)

	viewCompact := tw.ViewCompact(80)
	assert.Contains(t, viewCompact, "∿ VITALS")
	assert.Contains(t, viewCompact, "PEAK CPU:")
	assert.Contains(t, viewCompact, "PEAK RAM:")
	assert.Contains(t, viewCompact, "compact-hist")
}

func TestTelemetryWidget_SupervisorSlots(t *testing.T) {
	tw := NewTelemetryWidget()
	status := &monitor.SupervisorStatus{
		GlobalMaxJobs: 2,
		GlobalActive:  1,
		RepoSlots: []*monitor.RepoSlotInfo{
			{Alias: "aim", MaxJobs: 1, ActiveJobs: 1, Status: "busy"},
			{Alias: "cxstatusline", MaxJobs: 1, ActiveJobs: 0, Status: "idle"},
		},
	}
	tw.SetSupervisorStatus(status)

	// 1. Idle state rendering
	idleView := tw.View(80)
	assert.Contains(t, idleView, "ACTIVE: aim (1/1 slots), cxstatusline (0/1 slots)")
	idleCompact := tw.ViewCompact(80)
	assert.Contains(t, idleCompact, "aim (1/1 slots)")

	// 2. Active state rendering
	tw.Update(&monitor.RunnerMetrics{
		PID:          1234,
		StepCPU:      40.0,
		StepRSSBytes: 200 * 1024 * 1024,
	})
	activeView := tw.View(80)
	assert.Contains(t, activeView, "ACTIVE: aim (1/1 slots), cxstatusline (0/1 slots)")
}

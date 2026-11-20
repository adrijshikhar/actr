package tui

import (
	"fmt"
	"math"
	"strings"

	"github.com/adrijshikhar/actr/internal/config"
	"github.com/adrijshikhar/actr/internal/monitor"
	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/lipgloss"
)

// RunnerMetrics is an alias for monitor.RunnerMetrics for internal TUI convenience.
type RunnerMetrics = monitor.RunnerMetrics

var (
	radarTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#38BDF8"))
	idlePillStyle   = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#10B981")).
			Background(lipgloss.Color("#064E3B")).
			Padding(0, 1)
	activePillStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#38BDF8")).
			Background(lipgloss.Color("#1E3A8A")).
			Padding(0, 1)
	offlinePillStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#F59E0B")).
				Background(lipgloss.Color("#451A03")).
				Padding(0, 1)
	completedSuccessPillStyle = lipgloss.NewStyle().
					Bold(true).
					Foreground(lipgloss.Color("#10B981")).
					Background(lipgloss.Color("#064E3B")).
					Padding(0, 1)
	completedFailedPillStyle = lipgloss.NewStyle().
					Bold(true).
					Foreground(lipgloss.Color("#EF4444")).
					Background(lipgloss.Color("#450A0A")).
					Padding(0, 1)
	statKeyStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#64748B"))
	statValStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#E2E8F0"))
)

// TelemetryWidget renders real-time process and host resource telemetry gauges,
// including CPU percentage, RSS memory consumption, and disk metrics.
type TelemetryWidget struct {
	cpuProg          progress.Model
	memProg          progress.Model
	diskProg         progress.Model
	sample           *monitor.TelemetrySample
	limits           *config.LimitsConfig
	historical       *HistoricalRunSummary
	offline          bool
	supervisorStatus *monitor.SupervisorStatus
}

// HistoricalRunSummary captures execution metadata and aggregate peak vitals for a historical run.
type HistoricalRunSummary struct {
	JobName        string
	Status         monitor.StepStatus
	DurationSec    int64
	Driver         string
	TraceID        string
	PeakCPUPercent float64
	PeakRSSBytes   uint64
	TotalNetRx     uint64
	TotalNetTx     uint64
	TotalDiskWrite uint64
	Limits         *config.LimitsConfig
}

// NewTelemetryWidget constructs a TelemetryWidget with gradient progress bars.
func NewTelemetryWidget() *TelemetryWidget {
	cpuP := progress.New(
		progress.WithGradient("#8B5CF6", "#38BDF8"),
		progress.WithoutPercentage(),
	)
	memP := progress.New(
		progress.WithGradient("#06B6D4", "#10B981"),
		progress.WithoutPercentage(),
	)
	diskP := progress.New(
		progress.WithGradient("#F59E0B", "#EF4444"),
		progress.WithoutPercentage(),
	)
	return &TelemetryWidget{
		cpuProg:  cpuP,
		memProg:  memP,
		diskProg: diskP,
	}
}

// SetLimits configures active resource limits for scaling gauge progress bars.
func (tw *TelemetryWidget) SetLimits(limits *config.LimitsConfig) {
	if tw == nil {
		return
	}
	tw.limits = limits
}

// Limits returns the active LimitsConfig, or nil if none is configured.
func (tw *TelemetryWidget) Limits() *config.LimitsConfig {
	if tw == nil {
		return nil
	}
	return tw.limits
}

// Update updates the active telemetry sample.
func (tw *TelemetryWidget) Update(sample *monitor.TelemetrySample) {
	if tw == nil {
		return
	}
	tw.sample = sample
}

// Sample returns the current telemetry sample, or nil if none has been received.
func (tw *TelemetryWidget) Sample() *monitor.TelemetrySample {
	if tw == nil {
		return nil
	}
	return tw.sample
}

// SetHistoricalRun sets the active historical run summary for Peak Vitals mode.
func (tw *TelemetryWidget) SetHistoricalRun(summary *HistoricalRunSummary) {
	if tw == nil {
		return
	}
	tw.historical = summary
}

// SetOffline updates the offline status of the telemetry widget.
func (tw *TelemetryWidget) SetOffline(offline bool) {
	if tw == nil {
		return
	}
	tw.offline = offline
}

// IsOffline returns whether the telemetry widget is in offline history mode.
func (tw *TelemetryWidget) IsOffline() bool {
	if tw == nil {
		return false
	}
	return tw.offline
}

// ClearHistoricalRun clears the active historical run, restoring live vitals mode.
func (tw *TelemetryWidget) ClearHistoricalRun() {
	if tw == nil {
		return
	}
	tw.historical = nil
}

// IsHistorical returns true if the widget is currently displaying a historical run.
func (tw *TelemetryWidget) IsHistorical() bool {
	if tw == nil {
		return false
	}
	return tw.historical != nil
}

// formatBytes formats byte values into human-readable strings (e.g. "256 MB", "1.5 GB").
func formatBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit && exp < 5; n /= unit {
		div *= unit
		exp++
	}
	val := float64(b) / float64(div)
	if val >= 1023.95 && exp < 5 {
		val /= float64(unit)
		exp++
	}
	rounded := math.Round(val*10) / 10
	if rounded == math.Floor(rounded) {
		return fmt.Sprintf("%d %cB", uint64(rounded), "KMGTPE"[exp])
	}
	return fmt.Sprintf("%.1f %cB", val, "KMGTPE"[exp])
}

// SetSupervisorStatus sets the multi-repo supervisor status for rendering slot summary.
func (tw *TelemetryWidget) SetSupervisorStatus(status *monitor.SupervisorStatus) {
	if tw == nil {
		return
	}
	tw.supervisorStatus = status
}

func (tw *TelemetryWidget) formatSlotSummary() string {
	if tw == nil || tw.supervisorStatus == nil || len(tw.supervisorStatus.RepoSlots) == 0 {
		return ""
	}
	var slotParts []string
	for _, slot := range tw.supervisorStatus.RepoSlots {
		if slot != nil {
			slotParts = append(slotParts, fmt.Sprintf("%s (%d/%d slots)", slot.Alias, slot.ActiveJobs, slot.MaxJobs))
		}
	}
	return statKeyStyle.Render("ACTIVE: ") + statValStyle.Render(strings.Join(slotParts, ", "))
}

// View renders the telemetry widget into a monospace tabular HUD:
// Line 1: ∿ RUN VITALS & RADAR with status pill (● ACTIVE or ● IDLE)
// Line 2: Monospace metadata with STEP, PID, TIME, and TRACE
// Line 3: CPU percentage with purple-to-cyan gradient progress bar
// Line 4: RAM consumption with cyan-to-emerald gradient progress bar
// Line 5: DISK consumption with amber-to-red gradient progress bar
// Line 6 (optional): Network & Disk I/O rates
// If displaying a historical run, renders Peak Vitals Mode instead.
func (tw *TelemetryWidget) View(width int) string {
	if tw != nil && tw.IsHistorical() {
		return tw.viewHistorical(width)
	}

	if tw == nil || tw.sample == nil {
		header := radarTitleStyle.Render("∿ RUN VITALS & RADAR")
		var pill string
		var stateLine string
		if tw != nil && tw.offline {
			pill = offlinePillStyle.Render("◉ OFFLINE (HISTORY)")
			stateLine = statKeyStyle.Render("DAEMON: ") + statValStyle.Render("◉ OFFLINE (HISTORY)") + " " + dimStyle.Render("• Daemon offline, polling for connection...")
		} else {
			pill = idlePillStyle.Render("● IDLE")
			stateLine = statKeyStyle.Render("DAEMON: ") + statValStyle.Render("● CONNECTED (LIVE)") + " " + dimStyle.Render("• Ready for workflow dispatches")
		}

		gap := width - lipgloss.Width(header) - lipgloss.Width(pill)
		var topLine string
		if gap > 1 {
			topLine = header + strings.Repeat(" ", gap) + pill
		} else {
			topLine = header + " " + pill
		}

		metaLine := dimStyle.Render("PID: - | PGID: - | Host CPU: 0.0%")
		statusLine := statKeyStyle.Render("STATUS: ") + statValStyle.Render("Awaiting step execution telemetry...")

		idleLines := []string{topLine, metaLine}
		if slotSummary := tw.formatSlotSummary(); slotSummary != "" {
			idleLines = append(idleLines, slotSummary)
		}
		idleLines = append(idleLines, statusLine, stateLine)

		return lipgloss.JoinVertical(lipgloss.Left, idleLines...)
	}

	header := radarTitleStyle.Render("∿ RUN VITALS & RADAR")
	pill := activePillStyle.Render("● ACTIVE")

	gap := width - lipgloss.Width(header) - lipgloss.Width(pill)
	var topLine string
	if gap > 1 {
		topLine = header + strings.Repeat(" ", gap) + pill
	} else {
		topLine = header + " " + pill
	}

	stepName := tw.sample.ActiveStepName
	if stepName == "" {
		stepName = "Active"
	}

	pidStr := fmt.Sprintf("PID: %d", tw.sample.PID)
	if tw.sample.ProcessCount > 0 {
		pidStr = fmt.Sprintf("PID: %d (%d procs)", tw.sample.PID, tw.sample.ProcessCount)
	}

	mins := tw.sample.StepElapsedSec / 60
	secs := tw.sample.StepElapsedSec % 60
	timeStr := fmt.Sprintf("TIME: %02d:%02d", mins, secs)

	traceStr := "TRACE: Standby"
	if tw.sample.TraceID != "" {
		tid := tw.sample.TraceID
		if len(tid) > 8 {
			tid = tid[:8] + "..."
		}
		traceStr = fmt.Sprintf("TRACE: %s", tid)
	}

	metaLine := dimStyle.Render(fmt.Sprintf("STEP: %s  │  %s  │  %s  │  %s", stepName, pidStr, timeStr, traceStr))

	barWidth := width - 42
	if barWidth < 10 {
		barWidth = 10
	}
	tw.cpuProg.Width = barWidth
	tw.memProg.Width = barWidth
	tw.diskProg.Width = barWidth

	var cpuRatio float64
	var cpuSuffix string
	if tw.limits != nil && tw.limits.CPU > 0 {
		cpuRatio = tw.sample.StepCPU / (tw.limits.CPU * 100.0)
		pct := cpuRatio * 100.0
		if pct < 0 {
			pct = 0
		}
		cpuSuffix = fmt.Sprintf("  %3.0f%% of %.1f CPUs", pct, tw.limits.CPU)
	} else {
		cpuRatio = tw.sample.StepCPU / 100.0
		cpuSuffix = fmt.Sprintf("  %3.0f%%", tw.sample.StepCPU)
	}
	if cpuRatio > 1.0 {
		cpuRatio = 1.0
	}
	if cpuRatio < 0 {
		cpuRatio = 0
	}

	cpuPrefix := fmt.Sprintf("CPU : %5.1f%%  ", tw.sample.StepCPU)
	if len(cpuPrefix) != 14 {
		if len(cpuPrefix) > 14 && strings.HasSuffix(cpuPrefix, " ") {
			cpuPrefix = strings.TrimSuffix(cpuPrefix, " ")
		}
		if len(cpuPrefix) < 14 {
			cpuPrefix = fmt.Sprintf("%-14s", cpuPrefix)
		}
	}
	cpuLine := fmt.Sprintf("%s[%s]%s", cpuPrefix, tw.cpuProg.ViewAs(cpuRatio), cpuSuffix)

	var memRatio float64
	var ramSuffix string
	if tw.limits != nil && tw.limits.Memory > 0 {
		if tw.sample.StepRSSBytes > 0 {
			memRatio = float64(tw.sample.StepRSSBytes) / float64(tw.limits.Memory)
		}
		pct := memRatio * 100.0
		if pct < 0 {
			pct = 0
		}
		ramSuffix = fmt.Sprintf("  %3.0f%% of %s", pct, formatBytes(uint64(tw.limits.Memory)))
	} else {
		if tw.sample.HostMemTotal > 0 {
			if tw.sample.StepRSSBytes > 0 {
				memRatio = float64(tw.sample.StepRSSBytes) / float64(tw.sample.HostMemTotal)
			} else if tw.sample.HostMemUsed > 0 {
				memRatio = float64(tw.sample.HostMemUsed) / float64(tw.sample.HostMemTotal)
			}
		}
		ramSuffix = fmt.Sprintf("  %3.0f%%", memRatio*100)
	}
	if memRatio > 1.0 {
		memRatio = 1.0
	}
	if memRatio < 0 {
		memRatio = 0
	}

	ramPrefix := fmt.Sprintf("RAM : %7s ", formatBytes(tw.sample.StepRSSBytes))
	if len(ramPrefix) != 14 {
		if len(ramPrefix) > 14 && strings.HasSuffix(ramPrefix, " ") {
			ramPrefix = strings.TrimSuffix(ramPrefix, " ")
		}
		if len(ramPrefix) < 14 {
			ramPrefix = fmt.Sprintf("%-14s", ramPrefix)
		}
	}
	memLine := fmt.Sprintf("%s[%s]%s", ramPrefix, tw.memProg.ViewAs(memRatio), ramSuffix)

	diskRatio := tw.sample.DiskUsedRatio
	if diskRatio == 0 && tw.sample.DiskTotalBytes > 0 {
		diskRatio = float64(tw.sample.DiskUsedBytes) / float64(tw.sample.DiskTotalBytes)
	}
	if diskRatio > 1.0 {
		diskRatio = 1.0
	}
	if diskRatio < 0 {
		diskRatio = 0
	}

	diskPrefix := fmt.Sprintf("DISK: %7s ", formatBytes(tw.sample.DiskUsedBytes))
	if len(diskPrefix) != 14 {
		if len(diskPrefix) > 14 && strings.HasSuffix(diskPrefix, " ") {
			diskPrefix = strings.TrimSuffix(diskPrefix, " ")
		}
		if len(diskPrefix) < 14 {
			diskPrefix = fmt.Sprintf("%-14s", diskPrefix)
		}
	}

	diskPct := tw.sample.DiskUsedRatio * 100
	if diskPct == 0 && diskRatio > 0 {
		diskPct = diskRatio * 100
	}
	freeBytes := tw.sample.DiskFreeBytes
	if freeBytes == 0 && tw.sample.DiskTotalBytes > tw.sample.DiskUsedBytes {
		freeBytes = tw.sample.DiskTotalBytes - tw.sample.DiskUsedBytes
	}
	diskSuffix := fmt.Sprintf("  %3.0f%% (%s free)", diskPct, formatBytes(freeBytes))
	diskLine := fmt.Sprintf("%s[%s]%s", diskPrefix, tw.diskProg.ViewAs(diskRatio), diskSuffix)

	lines := []string{
		topLine,
		metaLine,
	}
	if slotSummary := tw.formatSlotSummary(); slotSummary != "" {
		lines = append(lines, slotSummary)
	}
	lines = append(lines, cpuLine, memLine, diskLine)

	if tw.sample.NetRxRate > 0 || tw.sample.NetTxRate > 0 || tw.sample.DiskWriteRate > 0 {
		var ioParts []string
		if tw.sample.NetRxRate > 0 || tw.sample.NetTxRate > 0 {
			if tw.sample.NetTxRate > 0 && tw.sample.NetRxRate > 0 {
				ioParts = append(ioParts, fmt.Sprintf("NET: %s/s ↓  %s/s ↑", formatBytes(tw.sample.NetRxRate), formatBytes(tw.sample.NetTxRate)))
			} else if tw.sample.NetTxRate > 0 {
				ioParts = append(ioParts, fmt.Sprintf("NET: %s/s ↑", formatBytes(tw.sample.NetTxRate)))
			} else {
				ioParts = append(ioParts, fmt.Sprintf("NET: %s/s ↓", formatBytes(tw.sample.NetRxRate)))
			}
		}
		if tw.sample.DiskWriteRate > 0 {
			ioParts = append(ioParts, fmt.Sprintf("DISK WR: %s/s", formatBytes(tw.sample.DiskWriteRate)))
		}
		ioPrefix := fmt.Sprintf("%-14s", "I/O : ")
		lines = append(lines, dimStyle.Render(fmt.Sprintf("%s%s", ioPrefix, strings.Join(ioParts, "  │  "))))
	}

	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

// ViewCompact renders a dense 2-line HUD for compact/low-height terminal windows (< 18 rows).
func (tw *TelemetryWidget) ViewCompact(width int) string {
	if tw != nil && tw.IsHistorical() {
		return tw.viewHistoricalCompact(width)
	}

	if tw == nil || tw.sample == nil {
		header := radarTitleStyle.Render("∿ VITALS")
		var pill string
		var subLine string
		if tw != nil && tw.offline {
			pill = offlinePillStyle.Render("◉ OFFLINE (HISTORY)")
			subLine = dimStyle.Render("Daemon offline • Polling for live connection...")
		} else {
			pill = idlePillStyle.Render("● IDLE")
			subLine = dimStyle.Render("Awaiting step execution telemetry...")
		}
		topLine := header + " " + pill
		if slotSummary := tw.formatSlotSummary(); slotSummary != "" {
			return lipgloss.JoinVertical(lipgloss.Left, topLine, slotSummary, subLine)
		}
		return lipgloss.JoinVertical(lipgloss.Left, topLine, subLine)
	}

	header := radarTitleStyle.Render("∿ VITALS")
	pill := activePillStyle.Render("● ACTIVE")
	meta := dimStyle.Render(fmt.Sprintf("PID: %d | Host: %.1f%%", tw.sample.PID, tw.sample.HostCPU))
	topLine := fmt.Sprintf("%s %s  %s", header, pill, meta)

	barWidth := (width - 32) / 2
	if barWidth < 6 {
		barWidth = 6
	}
	tw.cpuProg.Width = barWidth
	tw.memProg.Width = barWidth

	cpuRatio := tw.sample.StepCPU / 100.0
	if tw.limits != nil && tw.limits.CPU > 0 {
		cpuRatio = tw.sample.StepCPU / (tw.limits.CPU * 100.0)
	}
	if cpuRatio > 1.0 {
		cpuRatio = 1.0
	}
	if cpuRatio < 0 {
		cpuRatio = 0
	}

	var memRatio float64
	if tw.limits != nil && tw.limits.Memory > 0 {
		if tw.sample.StepRSSBytes > 0 {
			memRatio = float64(tw.sample.StepRSSBytes) / float64(tw.limits.Memory)
		}
	} else if tw.sample.HostMemTotal > 0 {
		if tw.sample.StepRSSBytes > 0 {
			memRatio = float64(tw.sample.StepRSSBytes) / float64(tw.sample.HostMemTotal)
		} else if tw.sample.HostMemUsed > 0 {
			memRatio = float64(tw.sample.HostMemUsed) / float64(tw.sample.HostMemTotal)
		}
	}
	if memRatio > 1.0 {
		memRatio = 1.0
	}
	if memRatio < 0 {
		memRatio = 0
	}

	line2 := fmt.Sprintf("CPU: %4.0f%% [%s]  RAM: %5s [%s]", tw.sample.StepCPU, tw.cpuProg.ViewAs(cpuRatio), formatBytes(tw.sample.StepRSSBytes), tw.memProg.ViewAs(memRatio))
	return lipgloss.JoinVertical(lipgloss.Left, topLine, line2)
}

func (tw *TelemetryWidget) viewHistorical(width int) string {
	summary := tw.historical
	header := radarTitleStyle.Render("∿ RUN VITALS (PEAK SUMMARY)")

	var pill string
	switch summary.Status {
	case monitor.StatusSuccess:
		pill = completedSuccessPillStyle.Render("● COMPLETED (SUCCESS)")
	case monitor.StatusFailed:
		pill = completedFailedPillStyle.Render("● COMPLETED (FAILED)")
	case monitor.StatusRunning:
		pill = activePillStyle.Render("● RUNNING")
	default:
		if summary.Status != "" {
			pill = idlePillStyle.Render(fmt.Sprintf("● %s", strings.ToUpper(string(summary.Status))))
		} else {
			pill = idlePillStyle.Render("● COMPLETED")
		}
	}

	gap := width - lipgloss.Width(header) - lipgloss.Width(pill)
	var topLine string
	if gap > 1 {
		topLine = header + strings.Repeat(" ", gap) + pill
	} else {
		topLine = header + " " + pill
	}

	runName := summary.JobName
	if runName == "" {
		runName = "Unknown"
	}

	mins := summary.DurationSec / 60
	secs := summary.DurationSec % 60
	durStr := fmt.Sprintf("DURATION: %02d:%02d", mins, secs)

	driver := summary.Driver
	if driver == "" {
		driver = "docker"
	}
	driverStr := fmt.Sprintf("DRIVER: %s", driver)

	traceStr := "TRACE: -"
	if summary.TraceID != "" {
		tid := summary.TraceID
		if len(tid) > 8 {
			tid = tid[:8] + "..."
		}
		traceStr = fmt.Sprintf("TRACE: %s", tid)
	}

	metaLine := dimStyle.Render(fmt.Sprintf("RUN: %s  │  %s  │  %s  │  %s", runName, durStr, driverStr, traceStr))

	limits := summary.Limits
	if limits == nil {
		limits = tw.limits
	}

	barWidth := width - 40
	if barWidth < 10 {
		barWidth = 10
	}
	tw.cpuProg.Width = barWidth
	tw.memProg.Width = barWidth

	// CPU
	var cpuRatio float64
	var cpuSuffix string
	if limits != nil && limits.CPU > 0 {
		cpuRatio = summary.PeakCPUPercent / (limits.CPU * 100.0)
		pct := cpuRatio * 100.0
		if pct < 0 {
			pct = 0
		}
		cpuSuffix = fmt.Sprintf("  %3.0f%% of %.1f CPUs", pct, limits.CPU)
	} else {
		cpuRatio = summary.PeakCPUPercent / 100.0
		cpuSuffix = fmt.Sprintf("  %3.0f%%", summary.PeakCPUPercent)
	}
	if cpuRatio > 1.0 {
		cpuRatio = 1.0
	}
	if cpuRatio < 0 {
		cpuRatio = 0
	}

	cpuPrefix := fmt.Sprintf("PEAK CPU: %6.1f%%    ", summary.PeakCPUPercent)
	if len(cpuPrefix) != 21 {
		for len(cpuPrefix) > 21 && strings.HasSuffix(cpuPrefix, " ") {
			cpuPrefix = strings.TrimSuffix(cpuPrefix, " ")
		}
		if len(cpuPrefix) > 21 {
			cpuPrefix = cpuPrefix[:21]
		}
		if len(cpuPrefix) < 21 {
			cpuPrefix = fmt.Sprintf("%-21s", cpuPrefix)
		}
	}
	cpuLine := fmt.Sprintf("%s[%s]%s", cpuPrefix, tw.cpuProg.ViewAs(cpuRatio), cpuSuffix)

	// RAM
	var memRatio float64
	var ramSuffix string
	ramValStr := formatBytes(summary.PeakRSSBytes)
	if limits != nil && limits.Memory > 0 {
		if summary.PeakRSSBytes > 0 {
			memRatio = float64(summary.PeakRSSBytes) / float64(limits.Memory)
		}
		pct := memRatio * 100.0
		if pct < 0 {
			pct = 0
		}
		ramSuffix = fmt.Sprintf("  %3.0f%% of %s", pct, formatBytes(uint64(limits.Memory)))
	} else {
		if tw.sample != nil && tw.sample.HostMemTotal > 0 {
			if summary.PeakRSSBytes > 0 {
				memRatio = float64(summary.PeakRSSBytes) / float64(tw.sample.HostMemTotal)
			}
		}
		ramSuffix = fmt.Sprintf("  %3.0f%%", memRatio*100)
	}
	if memRatio > 1.0 {
		memRatio = 1.0
	}
	if memRatio < 0 {
		memRatio = 0
	}

	ramPrefix := fmt.Sprintf("PEAK RAM: %9s  ", ramValStr)
	if len(ramPrefix) != 21 {
		for len(ramPrefix) > 21 && strings.HasSuffix(ramPrefix, " ") {
			ramPrefix = strings.TrimSuffix(ramPrefix, " ")
		}
		if len(ramPrefix) > 21 {
			ramPrefix = ramPrefix[:21]
		}
		if len(ramPrefix) < 21 {
			ramPrefix = fmt.Sprintf("%-21s", ramPrefix)
		}
	}
	memLine := fmt.Sprintf("%s[%s]%s", ramPrefix, tw.memProg.ViewAs(memRatio), ramSuffix)

	// Total IO
	var ioParts []string
	if summary.TotalNetRx > 0 || summary.TotalNetTx > 0 {
		ioParts = append(ioParts, fmt.Sprintf("NET: %s ↓  %s ↑", formatBytes(summary.TotalNetRx), formatBytes(summary.TotalNetTx)))
	} else {
		ioParts = append(ioParts, "NET: 0 B ↓  0 B ↑")
	}
	if summary.TotalDiskWrite > 0 {
		ioParts = append(ioParts, fmt.Sprintf("DISK WR: %s", formatBytes(summary.TotalDiskWrite)))
	} else {
		ioParts = append(ioParts, "DISK WR: 0 B")
	}
	ioPrefix := fmt.Sprintf("%-10s", "TOTAL IO: ")
	ioLine := dimStyle.Render(fmt.Sprintf("%s%s", ioPrefix, strings.Join(ioParts, "  │  ")))

	// Status line
	var statusDesc string
	switch summary.Status {
	case monitor.StatusSuccess:
		statusDesc = "Finished successfully (Exit 0)"
	case monitor.StatusFailed:
		statusDesc = "Execution failed"
	case monitor.StatusInterrupted:
		statusDesc = "Run interrupted"
	case monitor.StatusSkipped:
		statusDesc = "Run skipped"
	case monitor.StatusRunning:
		statusDesc = "Running"
	default:
		if summary.Status != "" {
			statusDesc = string(summary.Status)
		} else {
			statusDesc = "Finished"
		}
	}
	statusLine := statKeyStyle.Render("STATUS  : ") + statValStyle.Render(statusDesc)

	lines := []string{
		topLine,
		metaLine,
		cpuLine,
		memLine,
		ioLine,
		statusLine,
	}

	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

func (tw *TelemetryWidget) viewHistoricalCompact(width int) string {
	summary := tw.historical
	header := radarTitleStyle.Render("∿ VITALS")

	var pill string
	switch summary.Status {
	case monitor.StatusSuccess:
		pill = completedSuccessPillStyle.Render("● COMPLETED")
	case monitor.StatusFailed:
		pill = completedFailedPillStyle.Render("● FAILED")
	default:
		pill = idlePillStyle.Render("● IDLE")
	}

	mins := summary.DurationSec / 60
	secs := summary.DurationSec % 60
	timeStr := fmt.Sprintf("%02d:%02d", mins, secs)

	runName := summary.JobName
	if runName == "" {
		runName = "hist"
	}
	meta := dimStyle.Render(fmt.Sprintf("RUN: %s | %s", runName, timeStr))
	topLine := fmt.Sprintf("%s %s  %s", header, pill, meta)

	barWidth := (width - 40) / 2
	if barWidth < 6 {
		barWidth = 6
	}
	tw.cpuProg.Width = barWidth
	tw.memProg.Width = barWidth

	limits := summary.Limits
	if limits == nil {
		limits = tw.limits
	}

	var cpuRatio float64
	if limits != nil && limits.CPU > 0 {
		cpuRatio = summary.PeakCPUPercent / (limits.CPU * 100.0)
	} else {
		cpuRatio = summary.PeakCPUPercent / 100.0
	}
	if cpuRatio > 1.0 {
		cpuRatio = 1.0
	}
	if cpuRatio < 0 {
		cpuRatio = 0
	}

	var memRatio float64
	if limits != nil && limits.Memory > 0 {
		if summary.PeakRSSBytes > 0 {
			memRatio = float64(summary.PeakRSSBytes) / float64(limits.Memory)
		}
	} else if tw.sample != nil && tw.sample.HostMemTotal > 0 {
		if summary.PeakRSSBytes > 0 {
			memRatio = float64(summary.PeakRSSBytes) / float64(tw.sample.HostMemTotal)
		}
	}
	if memRatio > 1.0 {
		memRatio = 1.0
	}
	if memRatio < 0 {
		memRatio = 0
	}

	line2 := fmt.Sprintf("PEAK CPU: %4.0f%% [%s]  PEAK RAM: %5s [%s]",
		summary.PeakCPUPercent, tw.cpuProg.ViewAs(cpuRatio),
		formatBytes(summary.PeakRSSBytes), tw.memProg.ViewAs(memRatio))

	return lipgloss.JoinVertical(lipgloss.Left, topLine, line2)
}

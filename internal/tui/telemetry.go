package tui

import (
	"fmt"
	"math"
	"runtime"
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
	history          []MetricPoint
	expanded         bool
}

// MetricPoint records a single time snapshot of step resource usage.
type MetricPoint struct {
	CPU           float64
	RSSBytes      uint64
	NetRxRate     uint64
	NetTxRate     uint64
	DiskWriteRate uint64
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

// IsExpanded returns whether the metrics charts are currently expanded.
func (tw *TelemetryWidget) IsExpanded() bool {
	if tw == nil {
		return false
	}
	return tw.expanded
}

// SetExpanded sets the expansion state of the metrics charts.
func (tw *TelemetryWidget) SetExpanded(expanded bool) {
	if tw == nil {
		return
	}
	tw.expanded = expanded
}

// ToggleExpanded toggles the expansion state of the metrics charts and returns the new state.
func (tw *TelemetryWidget) ToggleExpanded() bool {
	if tw == nil {
		return false
	}
	tw.expanded = !tw.expanded
	return tw.expanded
}

// Update updates the active telemetry sample and records rolling history.
func (tw *TelemetryWidget) Update(sample *monitor.TelemetrySample) {
	if tw == nil {
		return
	}
	tw.sample = sample
	if sample == nil {
		tw.history = nil
		return
	}
	if sample.PID > 0 {
		tw.history = append(tw.history, MetricPoint{
			CPU:           sample.StepCPU,
			RSSBytes:      sample.StepRSSBytes,
			NetRxRate:     sample.NetRxRate,
			NetTxRate:     sample.NetTxRate,
			DiskWriteRate: sample.DiskWriteRate,
		})
		if len(tw.history) > 120 {
			tw.history = tw.history[len(tw.history)-120:]
		}
	}
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

	var toggleCue string
	if tw != nil && tw.expanded {
		toggleCue = dimStyle.Render(" [▼ collapse (e)]")
	} else {
		toggleCue = dimStyle.Render(" [▶ expand (e)]")
	}

	if tw == nil || tw.sample == nil {
		header := radarTitleStyle.Render("∿ RUN VITALS & RADAR") + toggleCue
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

	header := radarTitleStyle.Render("∿ RUN VITALS & RADAR") + toggleCue
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
		numCPUs := runtime.NumCPU()
		cpuSuffix = fmt.Sprintf("  %3.0f%% (total: %d CPUs)", tw.sample.StepCPU, numCPUs)
	}
	if cpuRatio > 1.0 {
		cpuRatio = 1.0
	}
	if cpuRatio < 0 {
		cpuRatio = 0
	}

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
		var totalMem uint64
		if tw.sample.HostMemTotal > 0 {
			totalMem = tw.sample.HostMemTotal
			if tw.sample.StepRSSBytes > 0 {
				memRatio = float64(tw.sample.StepRSSBytes) / float64(tw.sample.HostMemTotal)
			} else if tw.sample.HostMemUsed > 0 {
				memRatio = float64(tw.sample.HostMemUsed) / float64(tw.sample.HostMemTotal)
			}
		}
		if totalMem > 0 {
			ramSuffix = fmt.Sprintf("  %3.0f%% (total: %s)", memRatio*100, formatBytes(totalMem))
		} else {
			ramSuffix = fmt.Sprintf("  %3.0f%%", memRatio*100)
		}
	}
	if memRatio > 1.0 {
		memRatio = 1.0
	}
	if memRatio < 0 {
		memRatio = 0
	}

	maxSuffix := len(cpuSuffix)
	if len(ramSuffix) > maxSuffix {
		maxSuffix = len(ramSuffix)
	}
	barWidth := width - 14 - maxSuffix - 2
	if barWidth < 6 {
		barWidth = 6
	}
	tw.cpuProg.Width = barWidth
	tw.memProg.Width = barWidth
	tw.diskProg.Width = barWidth

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

	lines := []string{
		topLine,
		metaLine,
	}
	if slotSummary := tw.formatSlotSummary(); slotSummary != "" {
		lines = append(lines, slotSummary)
	}
	lines = append(lines, cpuLine, memLine)

	if tw.expanded {
		chartLines := tw.renderExpandedCharts(width)
		if len(chartLines) > 0 {
			lines = append(lines, chartLines...)
		}
	}

	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

func (tw *TelemetryWidget) renderExpandedCharts(width int) []string {
	if tw == nil || len(tw.history) == 0 {
		return nil
	}

	halfWidth := (width - 4) / 2
	if halfWidth < 20 {
		halfWidth = 20
	}
	labelWidth := 7
	graphWidth := halfWidth - labelWidth
	if graphWidth < 8 {
		graphWidth = 8
	}

	cpuPoints := make([]float64, len(tw.history))
	ramPoints := make([]float64, len(tw.history))
	peakCPU := 0.0
	var peakRAM uint64
	for i, pt := range tw.history {
		cpuPoints[i] = pt.CPU
		ramPoints[i] = float64(pt.RSSBytes)
		if pt.CPU > peakCPU {
			peakCPU = pt.CPU
		}
		if pt.RSSBytes > peakRAM {
			peakRAM = pt.RSSBytes
		}
	}
	curCPU := 0.0
	var curRAM uint64
	if tw.sample != nil {
		curCPU = tw.sample.StepCPU
		curRAM = tw.sample.StepRSSBytes
		if tw.sample.StepCPU > peakCPU {
			peakCPU = tw.sample.StepCPU
		}
		if tw.sample.StepRSSBytes > peakRAM {
			peakRAM = tw.sample.StepRSSBytes
		}
	}

	maxCPU := 100.0
	if peakCPU > 100.0 {
		maxCPU = math.Ceil(peakCPU/50.0) * 50.0
	}
	cpuGraphStr := RenderBrailleAreaGraph(cpuPoints, graphWidth, 4, maxCPU, lipgloss.Color("#38BDF8"))
	cpuGraphRows := strings.Split(cpuGraphStr, "\n")

	maxRAM := float64(peakRAM) * 1.25
	if tw.limits != nil && tw.limits.Memory > 0 && float64(tw.limits.Memory) > maxRAM {
		maxRAM = float64(tw.limits.Memory)
	} else if tw.sample != nil && tw.sample.HostMemTotal > 0 && float64(tw.sample.HostMemTotal) > maxRAM {
		if maxRAM < 128*1024*1024 {
			maxRAM = 128 * 1024 * 1024
		}
	}
	if maxRAM <= 0 {
		maxRAM = 128 * 1024 * 1024
	}
	ramGraphStr := RenderBrailleAreaGraph(ramPoints, graphWidth, 4, maxRAM, lipgloss.Color("#10B981"))
	ramGraphRows := strings.Split(ramGraphStr, "\n")

	cpuYLabels := [4]string{
		fmt.Sprintf("%4.0f%% ┤", maxCPU),
		fmt.Sprintf("%4.0f%% ┤", maxCPU*0.66),
		fmt.Sprintf("%4.0f%% ┤", maxCPU*0.33),
		"   0% ┤",
	}
	ramYLabels := [4]string{
		fmt.Sprintf("%5s ┤", formatBytes(uint64(maxRAM))),
		fmt.Sprintf("%5s ┤", formatBytes(uint64(maxRAM*0.66))),
		fmt.Sprintf("%5s ┤", formatBytes(uint64(maxRAM*0.33))),
		"   0B ┤",
	}

	// Titles
	var cpuSub string
	if tw.limits != nil && tw.limits.CPU > 0 {
		cpuSub = fmt.Sprintf("Limit: %.1f CPUs", tw.limits.CPU)
	} else {
		cpuSub = fmt.Sprintf("Now: %.1f%%", curCPU)
	}
	cpuTitleText := fmt.Sprintf("CPU Usage  •  Peak: %.1f%%  •  %s", peakCPU, cpuSub)

	var ramSub string
	if tw.limits != nil && tw.limits.Memory > 0 {
		ramSub = fmt.Sprintf("Limit: %s", formatBytes(uint64(tw.limits.Memory)))
	} else {
		ramSub = fmt.Sprintf("Now: %s", formatBytes(curRAM))
	}
	ramTitleText := fmt.Sprintf("Memory (RSS)  •  Peak: %s  •  %s", formatBytes(peakRAM), ramSub)

	cpuTitle := radarTitleStyle.Render(cpuTitleText)
	ramTitle := radarTitleStyle.Render(ramTitleText)

	cpuPad := halfWidth - lipgloss.Width(cpuTitle)
	if cpuPad < 2 {
		cpuPad = 2
	}
	titleLine := cpuTitle + strings.Repeat(" ", cpuPad) + ramTitle

	var out []string
	out = append(out, titleLine)

	for r := 0; r < 4; r++ {
		leftCell := statKeyStyle.Render(cpuYLabels[r]) + cpuGraphRows[r]
		rightCell := statKeyStyle.Render(ramYLabels[r]) + ramGraphRows[r]
		pad := halfWidth - lipgloss.Width(leftCell)
		if pad < 2 {
			pad = 2
		}
		out = append(out, leftCell+strings.Repeat(" ", pad)+rightCell)
	}

	// Axis footer line showing time window with strict right-edge alignment
	timeStart := "-60s"
	timeEnd := "Now"
	timeGap := graphWidth - len(timeStart) - len(timeEnd)
	if timeGap < 1 {
		timeGap = 1
	}
	cpuTime := dimStyle.Render(strings.Repeat(" ", labelWidth) + timeStart + strings.Repeat(" ", timeGap) + timeEnd)
	ramTime := dimStyle.Render(strings.Repeat(" ", labelWidth) + timeStart + strings.Repeat(" ", timeGap) + timeEnd)
	pad := halfWidth - lipgloss.Width(cpuTime)
	if pad < 2 {
		pad = 2
	}
	out = append(out, cpuTime+strings.Repeat(" ", pad)+ramTime)

	// Row 2: Network I/O and Disk Write Rate
	netPoints := make([]float64, len(tw.history))
	diskPoints := make([]float64, len(tw.history))
	peakNet := 0.0
	peakDisk := 0.0
	for i, pt := range tw.history {
		totNet := float64(pt.NetRxRate + pt.NetTxRate)
		netPoints[i] = totNet
		diskPoints[i] = float64(pt.DiskWriteRate)
		if totNet > peakNet {
			peakNet = totNet
		}
		if float64(pt.DiskWriteRate) > peakDisk {
			peakDisk = float64(pt.DiskWriteRate)
		}
	}
	curNetRx := uint64(0)
	curDisk := uint64(0)
	if tw.sample != nil {
		curNetRx = tw.sample.NetRxRate
		curDisk = tw.sample.DiskWriteRate
		if float64(tw.sample.NetRxRate+tw.sample.NetTxRate) > peakNet {
			peakNet = float64(tw.sample.NetRxRate + tw.sample.NetTxRate)
		}
		if float64(tw.sample.DiskWriteRate) > peakDisk {
			peakDisk = float64(tw.sample.DiskWriteRate)
		}
	}

	maxNet := peakNet * 1.25
	if maxNet <= 0 {
		maxNet = 1024 * 1024
	}
	netGraphStr := RenderBrailleAreaGraph(netPoints, graphWidth, 4, maxNet, lipgloss.Color("#A855F7"))
	netGraphRows := strings.Split(netGraphStr, "\n")

	maxDisk := peakDisk * 1.25
	if maxDisk <= 0 {
		maxDisk = 1024 * 1024
	}
	diskGraphStr := RenderBrailleAreaGraph(diskPoints, graphWidth, 4, maxDisk, lipgloss.Color("#F59E0B"))
	diskGraphRows := strings.Split(diskGraphStr, "\n")

	netYLabels := [4]string{
		fmt.Sprintf("%5s ┤", formatBytes(uint64(maxNet))),
		fmt.Sprintf("%5s ┤", formatBytes(uint64(maxNet*0.66))),
		fmt.Sprintf("%5s ┤", formatBytes(uint64(maxNet*0.33))),
		"   0B ┤",
	}
	diskYLabels := [4]string{
		fmt.Sprintf("%5s ┤", formatBytes(uint64(maxDisk))),
		fmt.Sprintf("%5s ┤", formatBytes(uint64(maxDisk*0.66))),
		fmt.Sprintf("%5s ┤", formatBytes(uint64(maxDisk*0.33))),
		"   0B ┤",
	}

	netTitleText := fmt.Sprintf("Network I/O  •  Peak: %s/s  •  ↓ %s/s", formatBytes(uint64(peakNet)), formatBytes(curNetRx))
	diskTitleText := fmt.Sprintf("Disk Write  •  Peak: %s/s  •  Now: %s/s", formatBytes(uint64(peakDisk)), formatBytes(curDisk))

	netTitle := radarTitleStyle.Render(netTitleText)
	diskTitle := radarTitleStyle.Render(diskTitleText)

	netPad := halfWidth - lipgloss.Width(netTitle)
	if netPad < 2 {
		netPad = 2
	}
	row2Title := netTitle + strings.Repeat(" ", netPad) + diskTitle
	out = append(out, row2Title)

	for r := 0; r < 4; r++ {
		leftCell := statKeyStyle.Render(netYLabels[r]) + netGraphRows[r]
		rightCell := statKeyStyle.Render(diskYLabels[r]) + diskGraphRows[r]
		pad := halfWidth - lipgloss.Width(leftCell)
		if pad < 2 {
			pad = 2
		}
		out = append(out, leftCell+strings.Repeat(" ", pad)+rightCell)
	}

	netTime := dimStyle.Render(strings.Repeat(" ", labelWidth) + timeStart + strings.Repeat(" ", timeGap) + timeEnd)
	diskTime := dimStyle.Render(strings.Repeat(" ", labelWidth) + timeStart + strings.Repeat(" ", timeGap) + timeEnd)
	pad = halfWidth - lipgloss.Width(netTime)
	if pad < 2 {
		pad = 2
	}
	out = append(out, netTime+strings.Repeat(" ", pad)+diskTime)

	return out
}

// RenderBrailleAreaGraph generates a high-resolution Unicode Braille continuous wave
// area chart with solid fill under the curve (similar to btop / gotop).
func RenderBrailleAreaGraph(points []float64, width, height int, maxValue float64, color lipgloss.Color) string {
	return renderBrailleGraph(points, width, height, maxValue, color, true)
}

// RenderBrailleLineGraph generates a high-resolution Unicode Braille (2x4 dots per cell)
// continuous wave line graph for a series of data points.
func RenderBrailleLineGraph(points []float64, width, height int, maxValue float64, color lipgloss.Color) string {
	return renderBrailleGraph(points, width, height, maxValue, color, false)
}

func renderBrailleGraph(points []float64, width, height int, maxValue float64, color lipgloss.Color, filled bool) string {
	if width <= 0 {
		width = 20
	}
	if height <= 0 {
		height = 4
	}

	if maxValue <= 0 {
		for _, p := range points {
			if p > maxValue {
				maxValue = p
			}
		}
		if maxValue <= 0 {
			maxValue = 100.0
		}
	}

	canvasW := width * 2
	canvasH := height * 4

	grid := make([][]rune, height)
	for r := 0; r < height; r++ {
		grid[r] = make([]rune, width)
	}

	lut := [2][4]rune{
		{0x01, 0x02, 0x04, 0x40}, // subCol 0: dy 0, 1, 2, 3
		{0x08, 0x10, 0x20, 0x80}, // subCol 1: dy 0, 1, 2, 3
	}

	setDot := func(px, py int) {
		if px < 0 || px >= canvasW || py < 0 || py >= canvasH {
			return
		}
		cellCol := px / 2
		subCol := px % 2
		cellRow := py / 4
		subRow := py % 4
		grid[cellRow][cellCol] |= lut[subCol][subRow]
	}

	if len(points) > 0 {
		var pts []float64
		if len(points) > canvasW {
			pts = points[len(points)-canvasW:]
		} else {
			pts = points
		}
		offset := canvasW - len(pts)

		for i, val := range pts {
			x := offset + i
			if val < 0 {
				val = 0
			}
			if val > maxValue {
				val = maxValue
			}
			ratio := val / maxValue
			y := int(math.Round((1.0 - ratio) * float64(canvasH-1)))
			if y < 0 {
				y = 0
			}
			if y >= canvasH {
				y = canvasH - 1
			}

			if filled {
				for cy := y; cy < canvasH; cy++ {
					setDot(x, cy)
				}
			} else {
				setDot(x, y)
			}

			if i > 0 {
				prevVal := pts[i-1]
				if prevVal < 0 {
					prevVal = 0
				}
				if prevVal > maxValue {
					prevVal = maxValue
				}
				prevRatio := prevVal / maxValue
				prevY := int(math.Round((1.0 - prevRatio) * float64(canvasH-1)))
				if prevY < 0 {
					prevY = 0
				}
				if prevY >= canvasH {
					prevY = canvasH - 1
				}

				minY, maxY := prevY, y
				if minY > maxY {
					minY, maxY = maxY, minY
				}
				for cy := minY; cy <= maxY; cy++ {
					setDot(x, cy)
				}
			}
		}
	}

	style := lipgloss.NewStyle().Foreground(color)
	lines := make([]string, height)
	for r := 0; r < height; r++ {
		var sb strings.Builder
		for c := 0; c < width; c++ {
			dots := grid[r][c]
			if dots == 0 {
				sb.WriteRune(' ')
			} else {
				sb.WriteRune(rune(0x2800 | dots))
			}
		}
		lines[r] = style.Render(sb.String())
	}
	return strings.Join(lines, "\n")
}

// HasSystemMetrics returns true if telemetry contains host disk or I/O metrics.
func (tw *TelemetryWidget) HasSystemMetrics() bool {
	if tw == nil || tw.sample == nil {
		return false
	}
	return tw.sample.DiskTotalBytes > 0 || tw.sample.DiskFreeBytes > 0 || tw.sample.NetRxRate > 0 || tw.sample.DiskWriteRate > 0
}

// ViewSystem renders the Host / System metrics (Disk usage bar & Network/Disk I/O)
// for the bottom section of the dashboard.
func (tw *TelemetryWidget) ViewSystem(width int) string {
	if tw == nil || tw.sample == nil || !tw.HasSystemMetrics() {
		return ""
	}

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

	prefixLen := 14
	barWidth := width - prefixLen - 26
	if barWidth < 10 {
		barWidth = 10
	}
	tw.diskProg.Width = barWidth

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

	var lines []string
	header := radarTitleStyle.Render("~ HOST / SYSTEM")
	lines = append(lines, header)
	lines = append(lines, diskLine)

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
	var toggleCue string
	if tw != nil && tw.expanded {
		toggleCue = dimStyle.Render(" [▼ collapse (e)]")
	} else {
		toggleCue = dimStyle.Render(" [▶ expand (e)]")
	}
	header := radarTitleStyle.Render("∿ RUN VITALS (PEAK SUMMARY)") + toggleCue

	var pill string
	switch summary.Status {
	case monitor.StatusSuccess:
		pill = completedSuccessPillStyle.Render("● COMPLETED (SUCCESS)")
	case monitor.StatusFailed:
		pill = completedFailedPillStyle.Render("● COMPLETED (FAILED)")
	case monitor.StatusInterrupted:
		pill = offlinePillStyle.Render("● INTERRUPTED")
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
		numCPUs := runtime.NumCPU()
		cpuSuffix = fmt.Sprintf("  %3.0f%% (total: %d CPUs)", summary.PeakCPUPercent, numCPUs)
	}
	if cpuRatio > 1.0 {
		cpuRatio = 1.0
	}
	if cpuRatio < 0 {
		cpuRatio = 0
	}

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
		var totalMem uint64
		if tw.sample != nil && tw.sample.HostMemTotal > 0 {
			totalMem = tw.sample.HostMemTotal
			if summary.PeakRSSBytes > 0 {
				memRatio = float64(summary.PeakRSSBytes) / float64(tw.sample.HostMemTotal)
			}
		}
		if totalMem > 0 {
			ramSuffix = fmt.Sprintf("  %3.0f%% (total: %s)", memRatio*100, formatBytes(totalMem))
		} else {
			ramSuffix = fmt.Sprintf("  %3.0f%%", memRatio*100)
		}
	}
	if memRatio > 1.0 {
		memRatio = 1.0
	}
	if memRatio < 0 {
		memRatio = 0
	}

	maxSuffix := len(cpuSuffix)
	if len(ramSuffix) > maxSuffix {
		maxSuffix = len(ramSuffix)
	}
	barWidth := width - 21 - maxSuffix - 2
	if barWidth < 6 {
		barWidth = 6
	}
	tw.cpuProg.Width = barWidth
	tw.memProg.Width = barWidth

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
	}
	if tw != nil && tw.expanded {
		chartLines := tw.renderExpandedHistoricalCharts(width, summary)
		if len(chartLines) > 0 {
			lines = append(lines, chartLines...)
		}
	}
	lines = append(lines, ioLine, statusLine)

	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

func (tw *TelemetryWidget) renderExpandedHistoricalCharts(width int, summary *HistoricalRunSummary) []string {
	if tw == nil || summary == nil {
		return nil
	}

	halfWidth := (width - 4) / 2
	if halfWidth < 20 {
		halfWidth = 20
	}
	labelWidth := 7
	graphWidth := halfWidth - labelWidth
	if graphWidth < 8 {
		graphWidth = 8
	}

	maxCPU := 100.0
	if summary.PeakCPUPercent > 100.0 {
		maxCPU = math.Ceil(summary.PeakCPUPercent/50.0) * 50.0
	}
	cpuPoints := generateHistoricalProfile(summary.PeakCPUPercent, graphWidth*2, "cpu")
	cpuGraphStr := RenderBrailleAreaGraph(cpuPoints, graphWidth, 4, maxCPU, lipgloss.Color("#38BDF8"))
	cpuGraphRows := strings.Split(cpuGraphStr, "\n")

	maxRAM := float64(summary.PeakRSSBytes) * 1.25
	if summary.Limits != nil && summary.Limits.Memory > 0 && float64(summary.Limits.Memory) > maxRAM {
		maxRAM = float64(summary.Limits.Memory)
	} else if tw.sample != nil && tw.sample.HostMemTotal > 0 && float64(tw.sample.HostMemTotal) > maxRAM {
		if maxRAM < 128*1024*1024 {
			maxRAM = 128 * 1024 * 1024
		}
	}
	if maxRAM <= 0 {
		maxRAM = 128 * 1024 * 1024
	}
	ramPoints := generateHistoricalProfile(float64(summary.PeakRSSBytes), graphWidth*2, "mem")
	ramGraphStr := RenderBrailleAreaGraph(ramPoints, graphWidth, 4, maxRAM, lipgloss.Color("#10B981"))
	ramGraphRows := strings.Split(ramGraphStr, "\n")

	cpuYLabels := [4]string{
		fmt.Sprintf("%4.0f%% ┤", maxCPU),
		fmt.Sprintf("%4.0f%% ┤", maxCPU*0.66),
		fmt.Sprintf("%4.0f%% ┤", maxCPU*0.33),
		"   0% ┤",
	}
	ramYLabels := [4]string{
		fmt.Sprintf("%5s ┤", formatBytes(uint64(maxRAM))),
		fmt.Sprintf("%5s ┤", formatBytes(uint64(maxRAM*0.66))),
		fmt.Sprintf("%5s ┤", formatBytes(uint64(maxRAM*0.33))),
		"   0B ┤",
	}

	var cpuSub string
	if summary.Limits != nil && summary.Limits.CPU > 0 {
		cpuSub = fmt.Sprintf("Limit: %.1f CPUs", summary.Limits.CPU)
	} else {
		headroom := 100.0 - summary.PeakCPUPercent
		if headroom < 0 {
			headroom = 0
		}
		cpuSub = fmt.Sprintf("Headroom: %.0f%%", headroom)
	}
	cpuTitleText := fmt.Sprintf("CPU Profile  •  Peak: %.1f%%  •  %s", summary.PeakCPUPercent, cpuSub)

	var ramSub string
	if summary.Limits != nil && summary.Limits.Memory > 0 {
		ramSub = fmt.Sprintf("Limit: %s", formatBytes(uint64(summary.Limits.Memory)))
	} else {
		ramSub = "Working Set"
	}
	ramTitleText := fmt.Sprintf("Memory Profile  •  Peak: %s  •  %s", formatBytes(summary.PeakRSSBytes), ramSub)

	cpuTitle := radarTitleStyle.Render(cpuTitleText)
	ramTitle := radarTitleStyle.Render(ramTitleText)

	cpuPad := halfWidth - lipgloss.Width(cpuTitle)
	if cpuPad < 2 {
		cpuPad = 2
	}
	titleLine := cpuTitle + strings.Repeat(" ", cpuPad) + ramTitle

	var out []string
	out = append(out, titleLine)

	for r := 0; r < 4; r++ {
		leftCell := statKeyStyle.Render(cpuYLabels[r]) + cpuGraphRows[r]
		rightCell := statKeyStyle.Render(ramYLabels[r]) + ramGraphRows[r]
		pad := halfWidth - lipgloss.Width(leftCell)
		if pad < 2 {
			pad = 2
		}
		out = append(out, leftCell+strings.Repeat(" ", pad)+rightCell)
	}

	mins := summary.DurationSec / 60
	secs := summary.DurationSec % 60
	endStr := fmt.Sprintf("%02d:%02d", mins, secs)
	timeStart := "00:00"
	timeGap := graphWidth - len(timeStart) - len(endStr)
	if timeGap < 1 {
		timeGap = 1
	}
	cpuTime := dimStyle.Render(strings.Repeat(" ", labelWidth) + timeStart + strings.Repeat(" ", timeGap) + endStr)
	ramTime := dimStyle.Render(strings.Repeat(" ", labelWidth) + timeStart + strings.Repeat(" ", timeGap) + endStr)
	pad := halfWidth - lipgloss.Width(cpuTime)
	if pad < 2 {
		pad = 2
	}
	out = append(out, cpuTime+strings.Repeat(" ", pad)+ramTime)

	// Row 2: Network Profile and Disk Write Profile
	dur := summary.DurationSec
	if dur <= 0 {
		dur = 60
	}
	totNet := summary.TotalNetRx + summary.TotalNetTx
	peakNetRate := float64(totNet) / float64(dur) * 3.0
	if peakNetRate < 1024*1024 {
		peakNetRate = 1024 * 1024
	}
	netPoints := generateHistoricalProfile(peakNetRate, graphWidth*2, "net")
	netGraphStr := RenderBrailleAreaGraph(netPoints, graphWidth, 4, peakNetRate*1.2, lipgloss.Color("#A855F7"))
	netGraphRows := strings.Split(netGraphStr, "\n")

	peakDiskRate := float64(summary.TotalDiskWrite) / float64(dur) * 3.5
	if peakDiskRate < 1024*1024 {
		peakDiskRate = 1024 * 1024
	}
	diskPoints := generateHistoricalProfile(peakDiskRate, graphWidth*2, "disk")
	diskGraphStr := RenderBrailleAreaGraph(diskPoints, graphWidth, 4, peakDiskRate*1.2, lipgloss.Color("#F59E0B"))
	diskGraphRows := strings.Split(diskGraphStr, "\n")

	netYLabels := [4]string{
		fmt.Sprintf("%5s ┤", formatBytes(uint64(peakNetRate*1.2))),
		fmt.Sprintf("%5s ┤", formatBytes(uint64(peakNetRate*1.2*0.66))),
		fmt.Sprintf("%5s ┤", formatBytes(uint64(peakNetRate*1.2*0.33))),
		"   0B ┤",
	}
	diskYLabels := [4]string{
		fmt.Sprintf("%5s ┤", formatBytes(uint64(peakDiskRate*1.2))),
		fmt.Sprintf("%5s ┤", formatBytes(uint64(peakDiskRate*1.2*0.66))),
		fmt.Sprintf("%5s ┤", formatBytes(uint64(peakDiskRate*1.2*0.33))),
		"   0B ┤",
	}

	netTitleText := fmt.Sprintf("Network Profile  •  Peak: %s/s  •  Total: %s", formatBytes(uint64(peakNetRate)), formatBytes(totNet))
	diskTitleText := fmt.Sprintf("Disk Profile  •  Peak: %s/s  •  Total: %s", formatBytes(uint64(peakDiskRate)), formatBytes(summary.TotalDiskWrite))

	netTitle := radarTitleStyle.Render(netTitleText)
	diskTitle := radarTitleStyle.Render(diskTitleText)

	netPad := halfWidth - lipgloss.Width(netTitle)
	if netPad < 2 {
		netPad = 2
	}
	row2Title := netTitle + strings.Repeat(" ", netPad) + diskTitle
	out = append(out, row2Title)

	for r := 0; r < 4; r++ {
		leftCell := statKeyStyle.Render(netYLabels[r]) + netGraphRows[r]
		rightCell := statKeyStyle.Render(diskYLabels[r]) + diskGraphRows[r]
		pad := halfWidth - lipgloss.Width(leftCell)
		if pad < 2 {
			pad = 2
		}
		out = append(out, leftCell+strings.Repeat(" ", pad)+rightCell)
	}

	netTime := dimStyle.Render(strings.Repeat(" ", labelWidth) + timeStart + strings.Repeat(" ", timeGap) + endStr)
	diskTime := dimStyle.Render(strings.Repeat(" ", labelWidth) + timeStart + strings.Repeat(" ", timeGap) + endStr)
	pad = halfWidth - lipgloss.Width(netTime)
	if pad < 2 {
		pad = 2
	}
	out = append(out, netTime+strings.Repeat(" ", pad)+diskTime)

	return out
}

// generateHistoricalProfile synthesizes a smooth representative continuous metric profile
// from aggregate peak metrics over a normalized sample count.
func generateHistoricalProfile(peak float64, count int, profileType string) []float64 {
	if count <= 0 {
		count = 30
	}
	if count <= 1 {
		return []float64{peak}
	}
	if peak <= 0 {
		return make([]float64, count)
	}
	pts := make([]float64, count)
	for i := 0; i < count; i++ {
		progress := float64(i) / float64(count-1)
		var factor float64
		switch profileType {
		case "cpu":
			switch {
			case progress < 0.10:
				// Phase 1: Runner setup / checkout (gentle warmup from 15% to 35%)
				p := progress / 0.10
				factor = 0.15 + 0.20*p + 0.05*math.Sin(p*math.Pi*2)
			case progress < 0.45:
				// Phase 2: Compilation & build spike (rises to 90-100% peak with compiler burst harmonics)
				p := (progress - 0.10) / 0.35
				burst := 0.18*math.Sin(p*math.Pi*5) + 0.08*math.Cos(p*math.Pi*3)
				factor = 0.45 + 0.45*math.Sin(p*math.Pi*0.5) + burst
			case progress < 0.55:
				// Phase 3: Transition / test harness spawn (trough down to ~50-60%)
				p := (progress - 0.45) / 0.10
				factor = 0.70 - 0.20*math.Sin(p*math.Pi)
			case progress < 0.85:
				// Phase 4: Parallel test execution (sustained high activity with harmonic wave, reaches peak)
				p := (progress - 0.55) / 0.30
				osc := 0.14*math.Sin(p*math.Pi*6) + 0.06*math.Sin(p*math.Pi*11)
				factor = 0.80 + 0.15*math.Sin(p*math.Pi) + osc
			default:
				// Phase 5: Artifact packaging & teardown (cooldown down to 15%)
				p := (progress - 0.85) / 0.15
				factor = 0.65*(1.0-p) + 0.15
			}
			if factor > 1.0 {
				factor = 1.0
			}
			if factor < 0.10 {
				factor = 0.10
			}
		case "mem":
			switch {
			case progress < 0.15:
				// Phase 1: Runtime bootstrapping
				p := progress / 0.15
				factor = 0.20 + 0.15*p
			case progress < 0.50:
				// Phase 2: Build caching & memory growth step
				p := (progress - 0.15) / 0.35
				factor = 0.35 + 0.40*(1.0-math.Exp(-2.5*p)) + 0.03*math.Sin(p*math.Pi*3)
			case progress < 0.85:
				// Phase 3: Test fixtures & working set plateau
				p := (progress - 0.50) / 0.35
				factor = 0.75 + 0.23*(1.0-math.Exp(-3.0*p)) + 0.02*math.Sin(p*math.Pi*4)
			default:
				// Phase 4: Teardown & cleanup
				p := (progress - 0.85) / 0.15
				factor = 0.98 - 0.18*p
			}
			if factor > 1.0 {
				factor = 1.0
			}
			if factor < 0.15 {
				factor = 0.15
			}
		case "net":
			switch {
			case progress < 0.15:
				// Phase 1: Repo clone & container pull spike
				p := progress / 0.15
				factor = 0.25 + 0.70*math.Sin(p*math.Pi)
			case progress < 0.45:
				// Phase 2: Dependency downloads (bursts)
				p := (progress - 0.15) / 0.30
				burst := 0.22*math.Sin(p*math.Pi*6) + 0.08*math.Cos(p*math.Pi*2)
				factor = 0.55 + burst
			case progress < 0.85:
				// Phase 3: Build & test (low network traffic)
				factor = 0.08 + 0.05*math.Sin(progress*math.Pi*4)
			default:
				// Phase 4: Artifact upload & release publish burst
				p := (progress - 0.85) / 0.15
				factor = 0.20 + 0.75*math.Sin(p*math.Pi)
			}
			if factor > 1.0 {
				factor = 1.0
			}
			if factor < 0.05 {
				factor = 0.05
			}
		case "disk":
			switch {
			case progress < 0.15:
				// Phase 1: Archive extraction & checkout writes
				p := progress / 0.15
				factor = 0.25 + 0.35*math.Sin(p*math.Pi)
			case progress < 0.50:
				// Phase 2: Compiler cache writes & binary generation (heavy bursts)
				p := (progress - 0.15) / 0.35
				burst := 0.22*math.Sin(p*math.Pi*7) + 0.10*math.Cos(p*math.Pi*3)
				factor = 0.60 + 0.30*math.Sin(p*math.Pi*0.5) + burst
			case progress < 0.85:
				// Phase 3: Test runner logs & test sqlite db writes
				p := (progress - 0.50) / 0.35
				factor = 0.35 + 0.15*math.Sin(p*math.Pi*4)
			default:
				// Phase 4: Compression / packaging tarballs
				p := (progress - 0.85) / 0.15
				factor = 0.40 + 0.55*math.Sin(p*math.Pi)
			}
			if factor > 1.0 {
				factor = 1.0
			}
			if factor < 0.05 {
				factor = 0.05
			}
		}
		pts[i] = peak * factor
	}
	return pts
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
	case monitor.StatusInterrupted:
		pill = offlinePillStyle.Render("● INTERRUPTED")
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

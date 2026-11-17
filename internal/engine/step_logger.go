package engine

import (
	"bytes"
	"fmt"
	"hash/fnv"
	"io"
	"strings"
	"sync"
	"time"
)

var ansiColors = []string{
	"\033[1;36m", // Cyan
	"\033[1;32m", // Green
	"\033[1;33m", // Yellow
	"\033[1;34m", // Blue
	"\033[1;35m", // Magenta
	"\033[1;96m", // Bright Cyan
	"\033[1;94m", // Bright Blue
}

const (
	ansiReset  = "\033[0m"
	ansiDim    = "\033[2m"
	ansiBold   = "\033[1m"
	ansiRed    = "\033[1;31m"
	ansiGreen  = "\033[1;32m"
	ansiYellow = "\033[1;33m"
	ansiBlue   = "\033[1;34m"
)

// StepLogger formats and prefixes step output streams for terminal visualization.
type StepLogger struct {
	jobName    string
	out        io.Writer
	color      bool
	jobPrefix  string
	pipePrefix string
	mu         sync.Mutex
}

// NewStepLogger creates a new StepLogger for a specific job.
func NewStepLogger(jobName string, out io.Writer, color bool) *StepLogger {
	if jobName == "" {
		jobName = "job"
	}
	if out == nil {
		out = io.Discard
	}

	sl := &StepLogger{
		jobName: jobName,
		out:     out,
		color:   color,
	}

	if color {
		h := fnv.New32a()
		_, _ = h.Write([]byte(jobName))
		colorCode := ansiColors[h.Sum32()%uint32(len(ansiColors))]
		sl.jobPrefix = fmt.Sprintf("%s[%s]%s", colorCode, jobName, ansiReset)
		sl.pipePrefix = fmt.Sprintf("%s   | %s", colorCode, ansiReset)
	} else {
		sl.jobPrefix = fmt.Sprintf("[%s]", jobName)
		sl.pipePrefix = "   | "
	}

	return sl
}

// LogStepStart outputs a step start banner.
func (sl *StepLogger) LogStepStart(stepName, script string) {
	sl.mu.Lock()
	defer sl.mu.Unlock()

	var banner string
	if sl.color {
		banner = fmt.Sprintf("%s %s⭐ Run %s%s\n", sl.jobPrefix, ansiBold, stepName, ansiReset)
	} else {
		banner = fmt.Sprintf("%s ⭐ Run %s\n", sl.jobPrefix, stepName)
	}
	_, _ = io.WriteString(sl.out, banner)
}

// LogStepEnd outputs a step completion or failure banner with execution duration.
func (sl *StepLogger) LogStepEnd(stepName string, success bool, duration time.Duration) {
	sl.mu.Lock()
	defer sl.mu.Unlock()

	durStr := formatDuration(duration)
	var banner string
	if success {
		if sl.color {
			banner = fmt.Sprintf("%s %s✓ Success %s (%s)%s\n", sl.jobPrefix, ansiGreen, stepName, durStr, ansiReset)
		} else {
			banner = fmt.Sprintf("%s ✓ Success %s (%s)\n", sl.jobPrefix, stepName, durStr)
		}
	} else {
		if sl.color {
			banner = fmt.Sprintf("%s %s❌ Failure %s (%s)%s\n", sl.jobPrefix, ansiRed, stepName, durStr, ansiReset)
		} else {
			banner = fmt.Sprintf("%s ❌ Failure %s (%s)\n", sl.jobPrefix, stepName, durStr)
		}
	}
	_, _ = io.WriteString(sl.out, banner)
}

// NewWriter returns a thread-safe io.Writer wrapping standard output or standard error.
func (sl *StepLogger) NewWriter(streamName string) *StepWriter {
	return &StepWriter{
		logger: sl,
		stream: streamName,
	}
}

// NewLineWriter returns an io.Writer that buffers lines and invokes onLine for each complete line.
func NewLineWriter(onLine func(string)) *StepWriter {
	return &StepWriter{
		onLine: onLine,
	}
}

// StepWriter line-buffers and formats raw output from runner steps.
type StepWriter struct {
	logger *StepLogger
	stream string
	buf    bytes.Buffer
	mu     sync.Mutex
	onLine func(string)
}

// WithCallback registers a callback invoked for each unformatted line.
func (sw *StepWriter) WithCallback(onLine func(string)) *StepWriter {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	sw.onLine = onLine
	return sw
}

// Write processes input bytes, splitting on newlines and formatting complete lines.
func (sw *StepWriter) Write(p []byte) (n int, err error) {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	n = len(p)
	for len(p) > 0 {
		idx := bytes.IndexByte(p, '\n')
		if idx == -1 {
			sw.buf.Write(p)
			break
		}

		sw.buf.Write(p[:idx])
		line := sw.buf.String()
		sw.buf.Reset()
		sw.writeFormattedLine(line)

		p = p[idx+1:]
	}
	return n, nil
}

// Flush writes any remaining buffered line content.
func (sw *StepWriter) Flush() {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	if sw.buf.Len() > 0 {
		line := sw.buf.String()
		sw.buf.Reset()
		sw.writeFormattedLine(line)
	}
}

func (sw *StepWriter) writeFormattedLine(line string) {
	trimmed := strings.TrimRight(line, "\r")
	if isSuppressedWorkflowCommand(trimmed) {
		return
	}
	if sw.onLine != nil {
		sw.onLine(trimmed)
	}
	if sw.logger == nil {
		return
	}
	formatted := sw.formatLine(trimmed)
	if formatted == "" {
		return
	}

	sw.logger.mu.Lock()
	defer sw.logger.mu.Unlock()
	_, _ = fmt.Fprintln(sw.logger.out, formatted)
}

func isSuppressedWorkflowCommand(line string) bool {
	if !strings.HasPrefix(line, "::") {
		return false
	}
	parts := strings.SplitN(line[2:], "::", 2)
	if len(parts) != 2 {
		return false
	}
	cmdSpec := parts[0]
	var cmd string
	if idx := strings.IndexByte(cmdSpec, ' '); idx != -1 {
		cmd = cmdSpec[:idx]
	} else {
		cmd = cmdSpec
	}
	switch strings.ToLower(cmd) {
	case "set-output", "save-state", "add-path", "add-mask":
		return true
	default:
		return false
	}
}

func (sw *StepWriter) formatLine(line string) string {
	// Parse GitHub Actions workflow commands
	if strings.HasPrefix(line, "::") {
		return sw.formatWorkflowCommand(line)
	}

	return fmt.Sprintf("%s%s%s", sw.logger.jobPrefix, sw.logger.pipePrefix, line)
}

func (sw *StepWriter) formatWorkflowCommand(line string) string {
	if isSuppressedWorkflowCommand(line) {
		return ""
	}

	parts := strings.SplitN(line[2:], "::", 2)
	if len(parts) != 2 {
		return fmt.Sprintf("%s%s%s", sw.logger.jobPrefix, sw.logger.pipePrefix, line)
	}

	cmdSpec := parts[0]
	msg := parts[1]

	var cmd string
	if idx := strings.IndexByte(cmdSpec, ' '); idx != -1 {
		cmd = cmdSpec[:idx]
	} else {
		cmd = cmdSpec
	}

	switch strings.ToLower(cmd) {
	case "group":
		if sw.logger.color {
			return fmt.Sprintf("%s %s##[group] %s%s", sw.logger.jobPrefix, ansiBlue, msg, ansiReset)
		}
		return fmt.Sprintf("%s ##[group] %s", sw.logger.jobPrefix, msg)
	case "endgroup":
		if sw.logger.color {
			return fmt.Sprintf("%s %s##[endgroup]%s", sw.logger.jobPrefix, ansiBlue, ansiReset)
		}
		return fmt.Sprintf("%s ##[endgroup]", sw.logger.jobPrefix)
	case "notice":
		if sw.logger.color {
			return fmt.Sprintf("%s %sℹ Notice:%s %s", sw.logger.jobPrefix, ansiBlue, ansiReset, msg)
		}
		return fmt.Sprintf("%s ℹ Notice: %s", sw.logger.jobPrefix, msg)
	case "warning":
		if sw.logger.color {
			return fmt.Sprintf("%s %s⚠ Warning:%s %s", sw.logger.jobPrefix, ansiYellow, ansiReset, msg)
		}
		return fmt.Sprintf("%s ⚠ Warning: %s", sw.logger.jobPrefix, msg)
	case "error":
		if sw.logger.color {
			return fmt.Sprintf("%s %s✖ Error:%s %s", sw.logger.jobPrefix, ansiRed, ansiReset, msg)
		}
		return fmt.Sprintf("%s ✖ Error: %s", sw.logger.jobPrefix, msg)
	case "debug":
		if sw.logger.color {
			return fmt.Sprintf("%s %s[debug] %s%s", sw.logger.jobPrefix, ansiDim, msg, ansiReset)
		}
		return fmt.Sprintf("%s [debug] %s", sw.logger.jobPrefix, msg)
	default:
		return fmt.Sprintf("%s%s%s", sw.logger.jobPrefix, sw.logger.pipePrefix, line)
	}
}

func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.2fs", d.Seconds())
}

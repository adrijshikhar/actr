package engine

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestStepLogger_PrefixAndColor(t *testing.T) {
	var buf bytes.Buffer
	sl := NewStepLogger("build-job", &buf, false) // false = no ANSI colors for predictable testing

	sl.LogStepStart("Setup Go Environment", "echo 'setting up'")
	writer := sl.NewWriter("stdout")
	_, err := writer.Write([]byte("downloading go 1.27\nextracting archive\n"))
	if err != nil {
		t.Fatalf("writer.Write error: %v", err)
	}
	sl.LogStepEnd("Setup Go Environment", true, 1250*time.Millisecond)

	output := buf.String()

	if !strings.Contains(output, "[build-job] ⭐ Run Setup Go Environment") {
		t.Errorf("expected step start header, got:\n%s", output)
	}
	if !strings.Contains(output, "[build-job]   | downloading go 1.27") {
		t.Errorf("expected prefixed stdout line, got:\n%s", output)
	}
	if !strings.Contains(output, "[build-job]   | extracting archive") {
		t.Errorf("expected prefixed stdout line, got:\n%s", output)
	}
	if !strings.Contains(output, "[build-job] ✓ Success Setup Go Environment (1.25s)") {
		t.Errorf("expected step success header with duration, got:\n%s", output)
	}
}

func TestStepLogger_Failure(t *testing.T) {
	var buf bytes.Buffer
	sl := NewStepLogger("test-job", &buf, false)

	sl.LogStepStart("Run Unit Tests", "go test ./...")
	writer := sl.NewWriter("stderr")
	_, err := writer.Write([]byte("--- FAIL: TestFoo (0.01s)\n"))
	if err != nil {
		t.Fatalf("write error: %v", err)
	}
	sl.LogStepEnd("Run Unit Tests", false, 500*time.Millisecond)

	output := buf.String()
	if !strings.Contains(output, "[test-job] ❌ Failure Run Unit Tests (500ms)") {
		t.Errorf("expected failure header with duration, got:\n%s", output)
	}
}

func TestStepLogger_PartialLineBuffering(t *testing.T) {
	var buf bytes.Buffer
	sl := NewStepLogger("job-1", &buf, false)
	writer := sl.NewWriter("stdout")

	// Write without newline
	_, _ = writer.Write([]byte("progress: 50%"))
	if buf.Len() != 0 {
		t.Errorf("expected nothing written yet due to line buffering, got %q", buf.String())
	}

	// Complete the line
	_, _ = writer.Write([]byte(" done!\n"))
	expected := "[job-1]   | progress: 50% done!\n"
	if buf.String() != expected {
		t.Errorf("expected %q, got %q", expected, buf.String())
	}

	// Test Flush of remaining partial line
	_, _ = writer.Write([]byte("remaining unbuffered"))
	writer.Flush()
	if !strings.Contains(buf.String(), "[job-1]   | remaining unbuffered\n") {
		t.Errorf("expected flushed partial line, got %q", buf.String())
	}
}

func TestStepLogger_WorkflowCommands(t *testing.T) {
	var buf bytes.Buffer
	sl := NewStepLogger("job-cmd", &buf, false)
	writer := sl.NewWriter("stdout")

	lines := []string{
		"::group::Build Details",
		"compiling package foo",
		"::endgroup::",
		"::notice file=main.go,line=1::found entry point",
		"::warning::disk usage is high",
		"::error::out of memory",
		"::set-output name=result::ok",
	}

	for _, line := range lines {
		_, _ = writer.Write([]byte(line + "\n"))
	}
	writer.Flush()

	out := buf.String()

	if !strings.Contains(out, "[job-cmd] ##[group] Build Details") {
		t.Errorf("expected formatted group command, got:\n%s", out)
	}
	if !strings.Contains(out, "[job-cmd] ##[endgroup]") {
		t.Errorf("expected formatted endgroup command, got:\n%s", out)
	}
	if !strings.Contains(out, "[job-cmd] ℹ Notice: found entry point") {
		t.Errorf("expected formatted notice, got:\n%s", out)
	}
	if !strings.Contains(out, "[job-cmd] ⚠ Warning: disk usage is high") {
		t.Errorf("expected formatted warning, got:\n%s", out)
	}
	if !strings.Contains(out, "[job-cmd] ✖ Error: out of memory") {
		t.Errorf("expected formatted error, got:\n%s", out)
	}
	if !strings.Contains(out, "[job-cmd]   | compiling package foo") {
		t.Errorf("expected normal stdout line, got:\n%s", out)
	}
}

func TestStepLogger_ANSIColorPalette(t *testing.T) {
	var buf bytes.Buffer
	sl := NewStepLogger("color-job", &buf, true) // true = ANSI colors enabled

	writer := sl.NewWriter("stdout")
	_, _ = writer.Write([]byte("colored output line\n"))
	writer.Flush()

	out := buf.String()
	// Should contain ANSI escape codes
	if !strings.Contains(out, "\033[") {
		t.Errorf("expected ANSI escape code in colored mode, got:\n%s", out)
	}
}

func TestStepWriter_CallbackAndNewLineWriter(t *testing.T) {
	var loggedLines []string
	writer := NewLineWriter(func(line string) {
		loggedLines = append(loggedLines, line)
	})

	_, _ = writer.Write([]byte("first line\r\nsecond line\npartial"))
	if len(loggedLines) != 2 {
		t.Fatalf("expected 2 logged lines before flush, got %d: %v", len(loggedLines), loggedLines)
	}
	if loggedLines[0] != "first line" || loggedLines[1] != "second line" {
		t.Errorf("unexpected logged lines: %v", loggedLines)
	}

	writer.Flush()
	if len(loggedLines) != 3 {
		t.Fatalf("expected 3 logged lines after flush, got %d: %v", len(loggedLines), loggedLines)
	}
	if loggedLines[2] != "partial" {
		t.Errorf("expected 'partial', got %q", loggedLines[2])
	}
}

func TestStepWriter_SuppressesSensitiveWorkflowCommands(t *testing.T) {
	var loggedLines []string
	writer := NewLineWriter(func(line string) {
		loggedLines = append(loggedLines, line)
	})

	input := "echo hello\n::add-mask::super-secret-token\n::set-output name=token::secret\n::save-state name=s::val\n::add-path::/opt/bin\n::warning::be careful\necho goodbye\n"
	_, _ = writer.Write([]byte(input))
	writer.Flush()

	expected := []string{
		"echo hello",
		"::warning::be careful",
		"echo goodbye",
	}

	if len(loggedLines) != len(expected) {
		t.Fatalf("expected %d logged lines, got %d: %v", len(expected), len(loggedLines), loggedLines)
	}
	for i, exp := range expected {
		if loggedLines[i] != exp {
			t.Errorf("line %d: expected %q, got %q", i, exp, loggedLines[i])
		}
	}
}

func TestStepLogger_AddMask_DynamicSecretRedaction(t *testing.T) {
	var buf bytes.Buffer
	sl := NewStepLogger("mask-job", &buf, false)
	writer := sl.NewWriter("stdout")

	input := "echo ::add-mask::supersecret\npassword is supersecret\n"
	_, err := writer.Write([]byte(input))
	if err != nil {
		t.Fatalf("Write error: %v", err)
	}
	writer.Flush()

	out := buf.String()
	if strings.Contains(out, "supersecret") {
		t.Errorf("expected output to NOT contain 'supersecret', got:\n%s", out)
	}
	if !strings.Contains(out, "password is ***") {
		t.Errorf("expected output to contain 'password is ***', got:\n%s", out)
	}
	if strings.Contains(out, "add-mask") {
		t.Errorf("expected add-mask command to be suppressed, got:\n%s", out)
	}
}

func TestStepWriter_AddMask_NewLineWriter(t *testing.T) {
	var loggedLines []string
	writer := NewLineWriter(func(line string) {
		loggedLines = append(loggedLines, line)
	})

	input := "echo ::add-mask::supersecret\npassword is supersecret\n"
	_, err := writer.Write([]byte(input))
	if err != nil {
		t.Fatalf("Write error: %v", err)
	}
	writer.Flush()

	if len(loggedLines) != 1 {
		t.Fatalf("expected 1 logged line (add-mask suppressed), got %d: %v", len(loggedLines), loggedLines)
	}
	if loggedLines[0] != "password is ***" {
		t.Errorf("expected 'password is ***', got %q", loggedLines[0])
	}
	if strings.Contains(loggedLines[0], "supersecret") {
		t.Errorf("expected secret to be masked, got %q", loggedLines[0])
	}
}

func TestStepLogger_SharedMaskerBetweenStreams(t *testing.T) {
	var buf bytes.Buffer
	sl := NewStepLogger("shared-mask", &buf, false)
	stdoutWriter := sl.NewWriter("stdout")
	stderrWriter := sl.NewWriter("stderr")

	// Emit mask on stdout
	_, _ = stdoutWriter.Write([]byte("::add-mask::stream-token-xyz\n"))
	stdoutWriter.Flush()

	// Emit secret on stderr
	_, _ = stderrWriter.Write([]byte("error with stream-token-xyz occurred\n"))
	stderrWriter.Flush()

	out := buf.String()
	if strings.Contains(out, "stream-token-xyz") {
		t.Errorf("expected secret to be masked across streams, got:\n%s", out)
	}
	if !strings.Contains(out, "error with *** occurred") {
		t.Errorf("expected masked line 'error with *** occurred', got:\n%s", out)
	}
}

func TestSecretMasker_LongerSecretsMaskedFirst(t *testing.T) {
	sm := NewSecretMasker()
	sm.AddMask("short")
	sm.AddMask("short_and_long")

	result := sm.Mask("access short_and_long and short token")
	expected := "access *** and *** token"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

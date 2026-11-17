package logger

import (
	"bytes"
	"strings"
	"sync"
	"testing"
)

func TestLoggerLevels(t *testing.T) {
	var buf bytes.Buffer
	Init(false, &buf)

	Debug("debug event", "key", "val1")
	if buf.Len() != 0 {
		t.Fatalf("expected debug to be silenced when debug=false, got: %s", buf.String())
	}

	Info("info event", "status", "ok")
	if !strings.Contains(buf.String(), "info event") || !strings.Contains(buf.String(), "status=ok") {
		t.Fatalf("expected info log output, got: %s", buf.String())
	}

	Warn("warn event", "code", 123)
	if !strings.Contains(buf.String(), "warn event") || !strings.Contains(buf.String(), "code=123") {
		t.Fatalf("expected warn log output, got: %s", buf.String())
	}

	Error("error event", "err", "fail")
	if !strings.Contains(buf.String(), "error event") || !strings.Contains(buf.String(), "err=fail") {
		t.Fatalf("expected error log output, got: %s", buf.String())
	}

	buf.Reset()
	Init(true, &buf)
	Debug("debug event", "key", "val2")
	if !strings.Contains(buf.String(), "debug event") || !strings.Contains(buf.String(), "key=val2") {
		t.Fatalf("expected debug log when debug=true, got: %s", buf.String())
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (n int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func TestConcurrentLogging(t *testing.T) {
	var buf syncBuffer
	Init(true, &buf)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			Debug("concurrent debug", "id", id)
			Info("concurrent info", "id", id)
			Warn("concurrent warn", "id", id)
			Error("concurrent error", "id", id)
		}(i)
	}
	wg.Wait()

	out := buf.String()
	if !strings.Contains(out, "concurrent debug") {
		t.Fatalf("expected concurrent debug logs in output")
	}
}

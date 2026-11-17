package monitor

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestClient_GetSnapshot_NonExistentSocket(t *testing.T) {
	client := NewClient(filepath.Join(t.TempDir(), "nonexistent.sock"))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	snap, err := client.GetSnapshot(ctx)
	if err == nil {
		t.Fatalf("expected error fetching snapshot from non-existent socket, got snapshot: %+v", snap)
	}
}

func TestClient_StreamEvents_NonExistentSocket(t *testing.T) {
	client := NewClient(filepath.Join(t.TempDir(), "nonexistent.sock"))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	events, errs := client.StreamEvents(ctx)
	select {
	case err := <-errs:
		if err == nil {
			t.Fatal("expected non-nil error from errs channel")
		}
	case ev, ok := <-events:
		if ok {
			t.Fatalf("unexpected event received: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for stream connection error")
	}
}

func TestClient_StreamEvents_ContextCancellation(t *testing.T) {
	sockPath := testSocketPath(t)

	server := NewServer(sockPath, "test-runner")
	if err := server.Start(); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	defer func() { _ = server.Stop() }()

	client := NewClient(sockPath)
	ctx, cancel := context.WithCancel(context.Background())

	events, errs := client.StreamEvents(ctx)

	// Publish an initial event and verify it's received
	server.Publish(Event{
		Type:    EventStepLog,
		StepID:  "step-1",
		LogLine: "first log",
	})

	select {
	case ev := <-events:
		if ev.LogLine != "first log" {
			t.Fatalf("expected 'first log', got %s", ev.LogLine)
		}
	case err := <-errs:
		t.Fatalf("unexpected error: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
	}

	// Now cancel context and verify channels close cleanly
	cancel()

	done := make(chan struct{})
	go func() {
		for range events {
		}
		for range errs {
		}
		close(done)
	}()

	select {
	case <-done:
		// success: stream terminated after context cancellation
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for stream to close after cancel")
	}
}

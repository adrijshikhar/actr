package power

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

type mockInhibitor struct {
	acquires atomic.Int32
	releases atomic.Int32
	held     atomic.Bool
	failAcq  bool
}

func (m *mockInhibitor) Acquire() error {
	m.acquires.Add(1)
	if m.failAcq {
		return errors.New("acquire failed")
	}
	m.held.Store(true)
	return nil
}

func (m *mockInhibitor) Release() error {
	m.releases.Add(1)
	m.held.Store(false)
	return nil
}

func TestManager_RefCountingAndIdempotency(t *testing.T) {
	mock := &mockInhibitor{}
	mgr := &Manager{backend: mock}

	if mgr.ActiveCount() != 0 {
		t.Fatalf("expected 0 active holds, got %d", mgr.ActiveCount())
	}

	rel1 := mgr.Acquire()
	if mgr.ActiveCount() != 1 || mock.acquires.Load() != 1 || !mock.held.Load() {
		t.Fatalf("expected first Acquire to activate backend once")
	}

	// Second concurrent job must NOT re-acquire backend
	rel2 := mgr.Acquire()
	if mgr.ActiveCount() != 2 || mock.acquires.Load() != 1 {
		t.Fatalf("expected second Acquire to increment count without re-acquiring backend")
	}

	// Releasing first hold keeps backend active because count is still 1
	rel1()
	// Repeated call to rel1 must be a safe no-op
	rel1()
	if mgr.ActiveCount() != 1 || mock.releases.Load() != 0 || !mock.held.Load() {
		t.Fatalf("expected release to remain active while 1 hold remains; count=%d releases=%d", mgr.ActiveCount(), mock.releases.Load())
	}

	// Releasing final hold drops count to 0 and releases backend immediately
	rel2()
	rel2()
	if mgr.ActiveCount() != 0 || mock.releases.Load() != 1 || mock.held.Load() {
		t.Fatalf("expected final release to deactivate backend immediately; count=%d releases=%d", mgr.ActiveCount(), mock.releases.Load())
	}

	// Re-acquiring after returning to 0 starts fresh
	rel3 := mgr.Acquire()
	if mgr.ActiveCount() != 1 || mock.acquires.Load() != 2 || !mock.held.Load() {
		t.Fatalf("expected fresh acquire after drop to 0")
	}
	rel3()
	if mgr.ActiveCount() != 0 || mock.releases.Load() != 2 || mock.held.Load() {
		t.Fatalf("expected second cycle release")
	}
}

func TestManager_ConcurrentAcquireRelease(t *testing.T) {
	mock := &mockInhibitor{}
	mgr := &Manager{backend: mock}

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rel := mgr.Acquire()
			defer rel()
		}()
	}
	wg.Wait()

	if mgr.ActiveCount() != 0 {
		t.Fatalf("expected 0 active holds after concurrent jobs, got %d", mgr.ActiveCount())
	}
	if mock.held.Load() {
		t.Fatalf("expected backend to be released after all goroutines exited")
	}
	if mock.acquires.Load() != mock.releases.Load() {
		t.Fatalf("expected balanced acquires (%d) and releases (%d)", mock.acquires.Load(), mock.releases.Load())
	}
}

func TestManager_CloseReleasesAndBlocksFutureAcquires(t *testing.T) {
	mock := &mockInhibitor{}
	mgr := &Manager{backend: mock}

	_ = mgr.Acquire()
	if !mock.held.Load() {
		t.Fatalf("expected hold active before Close")
	}

	if err := mgr.Close(); err != nil {
		t.Fatalf("unexpected error on Close: %v", err)
	}
	if mock.held.Load() || mgr.ActiveCount() != 0 || mock.releases.Load() != 1 {
		t.Fatalf("expected Close to release active hold immediately")
	}

	// Post-close Acquire must be a no-op
	relAfter := mgr.Acquire()
	relAfter()
	if mock.acquires.Load() != 1 || mgr.ActiveCount() != 0 {
		t.Fatalf("expected Acquire after Close to be no-op")
	}
}

func TestManager_NilSafeAndAcquireErrorHandled(t *testing.T) {
	var nilMgr *Manager
	rel := nilMgr.Acquire()
	rel()
	if nilMgr.ActiveCount() != 0 {
		t.Fatalf("expected nil Manager ActiveCount to be 0")
	}
	if err := nilMgr.Close(); err != nil {
		t.Fatalf("expected nil Manager Close to succeed")
	}

	mock := &mockInhibitor{failAcq: true}
	mgr := &Manager{backend: mock}
	r := mgr.Acquire()
	if mgr.ActiveCount() != 1 {
		t.Fatalf("expected count to still track hold gracefully on backend error")
	}
	r()
	if mgr.ActiveCount() != 0 {
		t.Fatalf("expected release to balance count")
	}
}

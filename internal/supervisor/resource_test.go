package supervisor

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResourceManager_AcquireRelease(t *testing.T) {
	rm, err := NewResourceManager(2)
	if err != nil {
		t.Fatalf("NewResourceManager failed: %v", err)
	}

	ctx := context.Background()
	p1, err := rm.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire p1 failed: %v", err)
	}
	if rm.ActiveCount() != 1 {
		t.Errorf("expected active 1, got %d", rm.ActiveCount())
	}

	p2, err := rm.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire p2 failed: %v", err)
	}
	if rm.ActiveCount() != 2 {
		t.Errorf("expected active 2, got %d", rm.ActiveCount())
	}

	// Third acquire should block until released
	ctxTimeout, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	_, err = rm.Acquire(ctxTimeout)
	if err != context.DeadlineExceeded {
		t.Errorf("expected context.DeadlineExceeded, got %v", err)
	}

	// Release p1 and acquire p3
	p1.Release()
	if rm.ActiveCount() != 1 {
		t.Errorf("expected active 1 after release, got %d", rm.ActiveCount())
	}

	p3, err := rm.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire p3 failed: %v", err)
	}
	p2.Release()
	p3.Release()
	if rm.ActiveCount() != 0 {
		t.Errorf("expected active 0, got %d", rm.ActiveCount())
	}
}

func TestResourceManager_SlotAvailable(t *testing.T) {
	rm, err := NewResourceManager(1)
	if err != nil {
		t.Fatalf("NewResourceManager failed: %v", err)
	}
	ctx := context.Background()
	p, err := rm.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire failed: %v", err)
	}

	slotCh := rm.SlotAvailable()
	select {
	case <-slotCh:
		t.Fatal("expected slotCh to be blocking when saturated")
	default:
	}

	p.Release()
	select {
	case <-slotCh:
		// success
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected slotCh to notify when slot freed")
	}
}

func TestResourceManager_InvalidMaxJobs(t *testing.T) {
	testCases := []int{0, -1, -100}
	for _, tc := range testCases {
		rm, err := NewResourceManager(tc)
		if err == nil {
			t.Errorf("expected error for maxConcurrentJobs=%d, got rm=%v", tc, rm)
		}
	}

	rm, err := NewResourceManager(1)
	if err != nil {
		t.Fatalf("unexpected error for maxConcurrentJobs=1: %v", err)
	}
	if rm.MaxConcurrentJobs() != 1 {
		t.Errorf("expected MaxConcurrentJobs 1, got %d", rm.MaxConcurrentJobs())
	}
}

func TestResourceManager_ContextCancellation(t *testing.T) {
	rm, err := NewResourceManager(1)
	if err != nil {
		t.Fatalf("NewResourceManager failed: %v", err)
	}

	// Pre-canceled context
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = rm.Acquire(canceledCtx)
	if err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if rm.ActiveCount() != 0 {
		t.Fatalf("expected active count 0, got %d", rm.ActiveCount())
	}

	// Normal acquire
	p, err := rm.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire failed: %v", err)
	}
	defer p.Release()

	// Waiter canceled while blocked
	ctxWait, cancelWait := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelWait()
	_, err = rm.Acquire(ctxWait)
	if err != context.DeadlineExceeded {
		t.Fatalf("expected context.DeadlineExceeded, got %v", err)
	}
	if rm.ActiveCount() != 1 {
		t.Fatalf("expected active count 1, got %d", rm.ActiveCount())
	}
}

func TestResourceManager_PermitIdempotency(t *testing.T) {
	rm, err := NewResourceManager(2)
	if err != nil {
		t.Fatalf("NewResourceManager failed: %v", err)
	}

	p, err := rm.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire failed: %v", err)
	}
	if rm.ActiveCount() != 1 {
		t.Fatalf("expected active count 1, got %d", rm.ActiveCount())
	}

	// Release multiple times
	p.Release()
	p.Release()
	p.Release()

	if rm.ActiveCount() != 0 {
		t.Fatalf("expected active count 0 after multiple releases, got %d", rm.ActiveCount())
	}
}

func TestResourceManager_Resize(t *testing.T) {
	rm, err := NewResourceManager(2)
	if err != nil {
		t.Fatalf("NewResourceManager failed: %v", err)
	}

	if err := rm.Resize(0); err == nil {
		t.Error("expected error resizing to 0")
	}
	if err := rm.Resize(-5); err == nil {
		t.Error("expected error resizing to negative")
	}

	if err := rm.Resize(4); err != nil {
		t.Fatalf("unexpected error resizing to 4: %v", err)
	}
	if rm.MaxConcurrentJobs() != 4 {
		t.Errorf("expected MaxConcurrentJobs 4, got %d", rm.MaxConcurrentJobs())
	}
}

func TestResourceManager_ConcurrentAcquireRelease(t *testing.T) {
	const maxJobs = 4
	const totalWorkers = 30
	const iterations = 20

	rm, err := NewResourceManager(maxJobs)
	if err != nil {
		t.Fatalf("NewResourceManager failed: %v", err)
	}

	var maxObserved int64
	var wg sync.WaitGroup

	for i := 0; i < totalWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				permit, err := rm.Acquire(context.Background())
				if err != nil {
					t.Errorf("acquire failed: %v", err)
					return
				}

				current := int64(rm.ActiveCount())
				for {
					old := atomic.LoadInt64(&maxObserved)
					if current <= old || atomic.CompareAndSwapInt64(&maxObserved, old, current) {
						break
					}
				}

				if current > maxJobs {
					t.Errorf("active jobs %d exceeded max %d", current, maxJobs)
				}

				// Simulate brief work
				time.Sleep(time.Millisecond)

				permit.Release()
			}
		}()
	}

	wg.Wait()

	if rm.ActiveCount() != 0 {
		t.Errorf("expected active count 0 after all workers finished, got %d", rm.ActiveCount())
	}
	if atomic.LoadInt64(&maxObserved) > maxJobs {
		t.Errorf("max observed active jobs %d exceeded ceiling %d", maxObserved, maxJobs)
	}
}

func TestResourceManager_ResizeUpAndDownWorks(t *testing.T) {
	rm, err := NewResourceManager(1)
	require.NoError(t, err)

	// Scale UP 1 -> 3: must be able to hold 3 permits simultaneously
	require.NoError(t, rm.Resize(3))
	p1, err := rm.Acquire(context.Background())
	require.NoError(t, err)
	p2, err := rm.Acquire(context.Background())
	require.NoError(t, err)
	p3, err := rm.Acquire(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 3, rm.ActiveCount())

	// Scale DOWN 3 -> 1: 4th Acquire must block even after releasing 1 (2 > 1)
	require.NoError(t, rm.Resize(1))
	p3.Release()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = rm.Acquire(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	p2.Release()
	p1.Release()

	// Now 0 < 1, Acquire succeeds
	p4, err := rm.Acquire(context.Background())
	require.NoError(t, err)
	p4.Release()
}

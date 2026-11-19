package supervisor

import (
	"context"
	"fmt"
	"sync"
)

// Permit represents an acquired execution slot that must be released upon job completion.
type Permit interface {
	Release()
}

type permit struct {
	rm   *ResourceManager
	once sync.Once
}

func (p *permit) Release() {
	p.once.Do(func() {
		p.rm.release()
	})
}

// ResourceManager limits global host job concurrency using a counting semaphore
// and non-blocking notification channels to gate worker polling.
type ResourceManager struct {
	mu         sync.Mutex
	maxJobs    int
	activeJobs int
	sem        chan struct{}
	notifyCh   chan struct{}
}

// NewResourceManager initializes a new concurrency manager with the specified limit.
func NewResourceManager(maxConcurrentJobs int) (*ResourceManager, error) {
	if maxConcurrentJobs <= 0 {
		return nil, fmt.Errorf("maxConcurrentJobs must be >= 1, got %d", maxConcurrentJobs)
	}
	return &ResourceManager{
		maxJobs:  maxConcurrentJobs,
		sem:      make(chan struct{}, maxConcurrentJobs),
		notifyCh: make(chan struct{}, 1),
	}, nil
}

// Acquire blocks until a slot is available or ctx is canceled.
func (r *ResourceManager) Acquire(ctx context.Context) (Permit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r.sem <- struct{}{}:
		r.mu.Lock()
		r.activeJobs++
		r.mu.Unlock()
		return &permit{rm: r}, nil
	}
}

func (r *ResourceManager) release() {
	r.mu.Lock()
	r.activeJobs--
	r.mu.Unlock()
	<-r.sem

	// Broadcast slot availability without blocking
	select {
	case r.notifyCh <- struct{}{}:
	default:
	}
}

// ActiveCount returns the current number of active permits.
func (r *ResourceManager) ActiveCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.activeJobs
}

// MaxConcurrentJobs returns the maximum allowed concurrent jobs.
func (r *ResourceManager) MaxConcurrentJobs() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.maxJobs
}

// SlotAvailable returns a channel notified whenever a permit is released.
func (r *ResourceManager) SlotAvailable() <-chan struct{} {
	return r.notifyCh
}

// Resize dynamically adjusts the logical maximum concurrent jobs.
func (r *ResourceManager) Resize(newMax int) error {
	if newMax <= 0 {
		return fmt.Errorf("newMax must be >= 1, got %d", newMax)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// Channel capacity in Go is fixed; update logical ceiling
	r.maxJobs = newMax
	return nil
}

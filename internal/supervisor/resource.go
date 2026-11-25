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

// Reservation represents a pre-flight capacity reservation before polling the broker.
// Call Activate once a job is received to promote it to an active execution Permit,
// or Release if polling fails or returns no job.
type Reservation interface {
	Activate() Permit
	Release()
}

type permit struct {
	rm   *ResourceManager
	once sync.Once
}

func (p *permit) Release() {
	p.once.Do(func() {
		if p.rm != nil {
			p.rm.releaseActive()
		}
	})
}

type reservation struct {
	rm   *ResourceManager
	once sync.Once
	p    *permit
}

func (rv *reservation) Activate() Permit {
	rv.once.Do(func() {
		rv.rm.promoteReservation()
		rv.p = &permit{rm: rv.rm}
	})
	if rv.p == nil {
		return &permit{}
	}
	return rv.p
}

func (rv *reservation) Release() {
	rv.once.Do(func() {
		rv.rm.releaseReservation()
	})
}

// ResourceManager limits global host job concurrency using mutex-guarded counters
// and broadcast signaling channels to support dynamic resizing.
type ResourceManager struct {
	mu           sync.Mutex
	maxJobs      int
	activeJobs   int
	reservedJobs int
	waitCh       chan struct{}
	notifyCh     chan struct{}
}

// NewResourceManager initializes a new concurrency manager with the specified limit.
func NewResourceManager(maxConcurrentJobs int) (*ResourceManager, error) {
	if maxConcurrentJobs <= 0 {
		return nil, fmt.Errorf("maxConcurrentJobs must be >= 1, got %d", maxConcurrentJobs)
	}
	return &ResourceManager{
		maxJobs:  maxConcurrentJobs,
		waitCh:   make(chan struct{}),
		notifyCh: make(chan struct{}, 1),
	}, nil
}

// Acquire blocks until an active execution slot is available or ctx is canceled.
func (r *ResourceManager) Acquire(ctx context.Context) (Permit, error) {
	if err := r.waitAndClaim(ctx, false); err != nil {
		return nil, err
	}
	return &permit{rm: r}, nil
}

// Reserve blocks until capacity is available and reserves a slot without counting it
// as an active executing job until Activate() is called.
func (r *ResourceManager) Reserve(ctx context.Context) (Reservation, error) {
	if err := r.waitAndClaim(ctx, true); err != nil {
		return nil, err
	}
	return &reservation{rm: r}, nil
}

func (r *ResourceManager) waitAndClaim(ctx context.Context, asReservation bool) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		r.mu.Lock()
		if r.activeJobs+r.reservedJobs < r.maxJobs {
			if asReservation {
				r.reservedJobs++
			} else {
				r.activeJobs++
			}
			r.mu.Unlock()
			return nil
		}
		ch := r.waitCh
		r.mu.Unlock()

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ch:
		}
	}
}

func (r *ResourceManager) promoteReservation() {
	r.mu.Lock()
	if r.reservedJobs > 0 {
		r.reservedJobs--
	}
	r.activeJobs++
	r.mu.Unlock()
}

func (r *ResourceManager) releaseActive() {
	r.mu.Lock()
	if r.activeJobs > 0 {
		r.activeJobs--
	}
	close(r.waitCh)
	r.waitCh = make(chan struct{})
	r.mu.Unlock()

	// Broadcast slot availability without blocking
	select {
	case r.notifyCh <- struct{}{}:
	default:
	}
}

func (r *ResourceManager) releaseReservation() {
	r.mu.Lock()
	if r.reservedJobs > 0 {
		r.reservedJobs--
	}
	close(r.waitCh)
	r.waitCh = make(chan struct{})
	r.mu.Unlock()

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

// Resize dynamically adjusts the maximum concurrent jobs and wakes waiting acquirers.
func (r *ResourceManager) Resize(newMax int) error {
	if newMax <= 0 {
		return fmt.Errorf("newMax must be >= 1, got %d", newMax)
	}
	r.mu.Lock()
	r.maxJobs = newMax
	close(r.waitCh)
	r.waitCh = make(chan struct{})
	r.mu.Unlock()

	select {
	case r.notifyCh <- struct{}{}:
	default:
	}
	return nil
}

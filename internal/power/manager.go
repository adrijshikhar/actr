package power

import (
	"sync"

	"github.com/adrijshikhar/actr/internal/logger"
)

type inhibitor interface {
	Acquire() error
	Release() error
}

// Manager reference-counts active sleep prevention holds across concurrent jobs.
// It acquires the platform sleep inhibitor when active holds rise from 0 to 1,
// and releases it immediately when active holds drop back to 0.
type Manager struct {
	mu      sync.Mutex
	count   int
	closed  bool
	backend inhibitor
}

// NewManager constructs a sleep assertion Manager using the platform's native sleep inhibitor.
func NewManager() *Manager {
	return &Manager{
		backend: newPlatformInhibitor(),
	}
}

// Acquire increments the active job hold count and enables system sleep prevention
// on the 0 -> 1 transition. Callers must invoke the returned release function when done;
// the release function is idempotent and safe to invoke multiple times.
func (m *Manager) Acquire() func() {
	if m == nil {
		return func() {}
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return func() {}
	}
	m.count++
	if m.count == 1 && m.backend != nil {
		if err := m.backend.Acquire(); err != nil {
			logger.Warn("failed to acquire system sleep assertion", "error", err)
		}
	}
	m.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			m.release()
		})
	}
}

func (m *Manager) release() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.count <= 0 {
		m.count = 0
		return
	}
	m.count--
	if m.count == 0 && m.backend != nil {
		if err := m.backend.Release(); err != nil {
			logger.Warn("failed to release system sleep assertion", "error", err)
		}
	}
}

// ActiveCount returns the number of active sleep assertion holds.
func (m *Manager) ActiveCount() int {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.count
}

// Close marks the Manager closed and immediately releases any held sleep assertion.
func (m *Manager) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return nil
	}
	m.closed = true
	wasHeld := m.count > 0
	m.count = 0
	if wasHeld && m.backend != nil {
		return m.backend.Release()
	}
	return nil
}

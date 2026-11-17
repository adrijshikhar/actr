package monitor

import "sync"

// RingBuffer is a thread-safe circular log buffer with fixed maximum capacity.
type RingBuffer struct {
	mu       sync.RWMutex
	capacity int
	data     []string
	start    int
	size     int
}

// NewRingBuffer creates a new RingBuffer with the given maximum capacity.
// It initializes with zero allocated line slots, growing dynamically up to capacity.
func NewRingBuffer(capacity int) *RingBuffer {
	if capacity <= 0 {
		capacity = 1000
	}
	return &RingBuffer{
		capacity: capacity,
	}
}

// Append adds a line to the ring buffer, evicting the oldest line if at capacity.
func (r *RingBuffer) Append(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.data) < r.capacity {
		r.data = append(r.data, line)
		r.size = len(r.data)
		return
	}

	r.data[r.start] = line
	r.start = (r.start + 1) % r.capacity
}

// Lines returns a snapshot copy of all lines currently in the buffer from oldest to newest.
func (r *RingBuffer) Lines() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.size == 0 {
		return []string{}
	}
	result := make([]string, r.size)
	for i := 0; i < r.size; i++ {
		result[i] = r.data[(r.start+i)%r.capacity]
	}
	return result
}

// Len returns the current number of lines stored in the buffer.
func (r *RingBuffer) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.size
}

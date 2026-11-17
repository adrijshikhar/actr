package monitor

import (
	"fmt"
	"sync"
	"testing"
)

func TestRingBuffer_CapacityAndWrap(t *testing.T) {
	rb := NewRingBuffer(3)
	if rb.Len() != 0 {
		t.Fatalf("expected len 0, got %d", rb.Len())
	}

	rb.Append("line 1")
	rb.Append("line 2")
	if rb.Len() != 2 {
		t.Fatalf("expected len 2, got %d", rb.Len())
	}

	lines := rb.Lines()
	if len(lines) != 2 || lines[0] != "line 1" || lines[1] != "line 2" {
		t.Fatalf("unexpected lines: %v", lines)
	}

	rb.Append("line 3")
	rb.Append("line 4") // wraps, evicts "line 1"

	if rb.Len() != 3 {
		t.Fatalf("expected len 3, got %d", rb.Len())
	}

	lines = rb.Lines()
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}
	if lines[0] != "line 2" || lines[1] != "line 3" || lines[2] != "line 4" {
		t.Fatalf("unexpected wrapped lines: %v", lines)
	}
}

func TestRingBuffer_DefaultCapacity(t *testing.T) {
	rb := NewRingBuffer(0)
	if rb.capacity != 1000 {
		t.Fatalf("expected default capacity 1000, got %d", rb.capacity)
	}

	rbNeg := NewRingBuffer(-5)
	if rbNeg.capacity != 1000 {
		t.Fatalf("expected default capacity 1000, got %d", rbNeg.capacity)
	}
}

func TestRingBuffer_Empty(t *testing.T) {
	rb := NewRingBuffer(10)
	if rb.Len() != 0 {
		t.Fatalf("expected len 0, got %d", rb.Len())
	}
	lines := rb.Lines()
	if len(lines) != 0 {
		t.Fatalf("expected 0 lines, got %d", len(lines))
	}
}

func TestRingBuffer_Concurrency(t *testing.T) {
	rb := NewRingBuffer(50)
	var wg sync.WaitGroup

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				rb.Append(fmt.Sprintf("worker %d line %d", id, j))
				_ = rb.Lines()
			}
		}(i)
	}

	wg.Wait()
	if rb.Len() != 50 {
		t.Fatalf("expected capped len 50, got %d", rb.Len())
	}
}

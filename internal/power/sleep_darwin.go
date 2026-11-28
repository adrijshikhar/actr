//go:build darwin

package power

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"
)

var execCommand = exec.CommandContext

type darwinInhibitor struct {
	mu     sync.Mutex
	active bool
	cancel context.CancelFunc
	done   chan struct{}
}

func newPlatformInhibitor() inhibitor {
	return &darwinInhibitor{}
}

// Acquire starts `caffeinate -i -s -w <pid>` to prevent idle/AC system sleep while allowing
// the display to sleep normally. Binding -w to actr's own PID ensures no orphan caffeinate
// process survives if actr terminates abruptly.
func (d *darwinInhibitor) Acquire() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.active {
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := execCommand(ctx, "/usr/bin/caffeinate", "-i", "-s", "-w", strconv.Itoa(os.Getpid()))
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			return cmd.Process.Signal(syscall.SIGTERM)
		}
		return nil
	}
	cmd.WaitDelay = 1 * time.Second

	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("start caffeinate: %w", err)
	}

	done := make(chan struct{})
	d.active = true
	d.cancel = cancel
	d.done = done

	go func() {
		defer close(done)
		_ = cmd.Wait()
	}()

	return nil
}

// Release terminates the active caffeinate subprocess and waits briefly for assertion cleanup.
func (d *darwinInhibitor) Release() error {
	d.mu.Lock()
	if !d.active {
		d.mu.Unlock()
		return nil
	}
	cancel := d.cancel
	done := d.done
	d.active = false
	d.cancel = nil
	d.done = nil
	d.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
		}
	}
	return nil
}

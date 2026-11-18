//go:build unix || darwin || linux

package action

import (
	"context"
	"fmt"
	"os"
	"syscall"
	"time"
)

func lockFile(ctx context.Context, f *os.File) error {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()

	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN && err != syscall.EINTR {
			return fmt.Errorf("flock failed: %w", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

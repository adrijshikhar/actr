//go:build windows

package action

import (
	"context"
	"fmt"
	"os"
	"syscall"
	"time"
	"unsafe"
)

var (
	kernel32DLL      = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx   = kernel32DLL.NewProc("LockFileEx")
	procUnlockFileEx = kernel32DLL.NewProc("UnlockFileEx")
)

const (
	lockfileExclusiveLock   = 0x00000002
	lockfileFailImmediately = 0x00000001
)

func lockFile(ctx context.Context, f *os.File) error {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()

	var overlapped syscall.Overlapped
	for {
		r1, _, err := procLockFileEx.Call(
			f.Fd(),
			uintptr(lockfileExclusiveLock|lockfileFailImmediately),
			0,
			1,
			0,
			uintptr(unsafe.Pointer(&overlapped)),
		)
		if r1 != 0 {
			return nil
		}
		// If another process holds the lock, Windows returns ERROR_LOCK_VIOLATION (33)
		errno, ok := err.(syscall.Errno)
		if ok && errno != 33 && errno != 0 {
			return fmt.Errorf("lock file failed: %w", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func unlockFile(f *os.File) error {
	var overlapped syscall.Overlapped
	r1, _, err := procUnlockFileEx.Call(
		f.Fd(),
		0,
		1,
		0,
		uintptr(unsafe.Pointer(&overlapped)),
	)
	if r1 == 0 {
		return fmt.Errorf("unlock file failed: %w", err)
	}
	return nil
}

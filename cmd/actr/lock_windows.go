//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

var ErrAlreadyLocked = errors.New("process lock is already held by another instance")

type fileLock struct {
	f *os.File
}

func acquireFileLock(path string) (*fileLock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open lock file %s: %w", path, err)
	}

	h := syscall.Handle(f.Fd())
	var overlapped syscall.Overlapped
	flags := uint32(2 | 1) // LOCKFILE_EXCLUSIVE_LOCK | LOCKFILE_FAIL_IMMEDIATELY
	modkernel32 := syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx := modkernel32.NewProc("LockFileEx")

	r1, _, errSys := procLockFileEx.Call(
		uintptr(h),
		uintptr(flags),
		0,
		1, 0,
		uintptr(unsafe.Pointer(&overlapped)),
	)
	if r1 == 0 {
		_ = f.Close()
		var errno syscall.Errno
		if errors.As(errSys, &errno) {
			if errno == 33 || errno == 997 {
				return nil, ErrAlreadyLocked
			}
		}
		if errSys != nil && errSys != syscall.Errno(0) {
			return nil, fmt.Errorf("acquire lock on %s: %w", path, errSys)
		}
		return nil, ErrAlreadyLocked
	}
	return &fileLock{f: f}, nil
}

func (l *fileLock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	defer func() {
		_ = l.f.Close()
		l.f = nil
	}()
	h := syscall.Handle(l.f.Fd())
	var overlapped syscall.Overlapped
	modkernel32 := syscall.NewLazyDLL("kernel32.dll")
	procUnlockFileEx := modkernel32.NewProc("UnlockFileEx")
	r1, _, errSys := procUnlockFileEx.Call(
		uintptr(h),
		0,
		1, 0,
		uintptr(unsafe.Pointer(&overlapped)),
	)
	if r1 == 0 && errSys != nil && errSys != syscall.Errno(0) {
		return fmt.Errorf("unlock file: %w", errSys)
	}
	return nil
}

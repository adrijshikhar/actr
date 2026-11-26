//go:build !windows

package main

import (
	"errors"
	"fmt"
	"os"
	"syscall"
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
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, ErrAlreadyLocked
		}
		return nil, fmt.Errorf("acquire lock on %s: %w", path, err)
	}
	return &fileLock{f: f}, nil
}

func (l *fileLock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	flockErr := syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	closeErr := l.f.Close()
	if flockErr != nil {
		return flockErr
	}
	return closeErr
}

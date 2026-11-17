//go:build darwin

package host

import (
	"context"
	"sync"
	"syscall"
	"time"

	"github.com/adrijshikhar/actr/internal/config"
)

type darwinLimiter struct {
	BaseLimiter
	watchdogInterval time.Duration
}

func newPlatformLimiter() HostLimiter {
	return &darwinLimiter{
		watchdogInterval: 100 * time.Millisecond,
	}
}

func (l *darwinLimiter) Apply(ctx context.Context, pid int, limits *config.LimitsConfig) (cleanup func(), err error) {
	if limits == nil || (limits.CPU <= 0 && limits.Memory <= 0 && limits.Pids <= 0) {
		return func() {}, nil
	}

	// 1. Lower CPU priority if CPU limits are specified
	if limits.CPU > 0 {
		if err := syscall.Setpriority(syscall.PRIO_PGRP, pid, 10); err != nil {
			_ = syscall.Setpriority(syscall.PRIO_PROCESS, pid, 10)
		}
	}

	// 2. Start userspace memory watchdog if memory limits are configured
	var watchdog *processWatchdog
	if limits.Memory > 0 {
		watchdog = newProcessWatchdog(pid, limits.Memory, l.watchdogInterval, func(p int) {
			l.MarkOOMKilled(p)
		})
		watchdog.Start()
	}

	var once sync.Once
	cleanup = func() {
		once.Do(func() {
			if watchdog != nil {
				watchdog.Stop()
			}
		})
	}

	return cleanup, nil
}

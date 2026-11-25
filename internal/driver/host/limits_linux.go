//go:build linux

package host

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/adrijshikhar/actr/internal/config"
)

type linuxLimiter struct {
	BaseLimiter
	watchdogInterval time.Duration
	cgroupRoot       string
}

func newPlatformLimiter() HostLimiter {
	return &linuxLimiter{
		watchdogInterval: 100 * time.Millisecond,
		cgroupRoot:       "/sys/fs/cgroup",
	}
}

func (l *linuxLimiter) Apply(ctx context.Context, pid int, limits *config.LimitsConfig) (cleanup func(), err error) {
	if limits == nil || (limits.CPU <= 0 && limits.Memory <= 0 && limits.Pids <= 0) {
		return func() {}, nil
	}

	// Tier 1: Direct cgroups v2 write
	if cl, err := l.tryCgroupsV2(pid, limits); err == nil {
		return cl, nil
	}

	// Tier 2: In-process userspace watchdog fallback
	return l.applyWatchdog(pid, limits)
}

func (l *linuxLimiter) tryCgroupsV2(pid int, limits *config.LimitsConfig) (func(), error) {
	controllersPath := filepath.Join(l.cgroupRoot, "cgroup.controllers")
	if _, err := os.Stat(controllersPath); err != nil {
		return nil, fmt.Errorf("cgroup v2 controllers not found: %w", err)
	}

	actrDir := filepath.Join(l.cgroupRoot, "actr")
	if err := os.MkdirAll(actrDir, 0755); err == nil {
		subtreeControl := filepath.Join(actrDir, "cgroup.subtree_control")
		_ = os.WriteFile(subtreeControl, []byte("+cpu +memory +pids"), 0644)
	}

	cgroupDir := filepath.Join(actrDir, fmt.Sprintf("job-%d", pid))
	if err := os.MkdirAll(cgroupDir, 0755); err != nil {
		return nil, fmt.Errorf("create cgroup directory: %w", err)
	}

	if limits.CPU > 0 {
		quota := int64(limits.CPU * 100000)
		if err := os.WriteFile(filepath.Join(cgroupDir, "cpu.max"), []byte(fmt.Sprintf("%d 100000\n", quota)), 0644); err != nil {
			_ = os.Remove(cgroupDir)
			return nil, fmt.Errorf("write cpu.max: %w", err)
		}
	}

	if limits.Memory > 0 {
		if err := os.WriteFile(filepath.Join(cgroupDir, "memory.max"), []byte(fmt.Sprintf("%d\n", limits.Memory)), 0644); err != nil {
			_ = os.Remove(cgroupDir)
			return nil, fmt.Errorf("write memory.max: %w", err)
		}
	}

	if limits.Pids > 0 {
		if err := os.WriteFile(filepath.Join(cgroupDir, "pids.max"), []byte(fmt.Sprintf("%d\n", limits.Pids)), 0644); err != nil {
			_ = os.Remove(cgroupDir)
			return nil, fmt.Errorf("write pids.max: %w", err)
		}
	}

	procsPath := filepath.Join(cgroupDir, "cgroup.procs")
	if err := os.WriteFile(procsPath, []byte(fmt.Sprintf("%d\n", pid)), 0644); err != nil {
		_ = os.Remove(cgroupDir)
		return nil, fmt.Errorf("attach pid to cgroup.procs: %w", err)
	}

	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			eventsPath := filepath.Join(cgroupDir, "memory.events")
			if data, err := os.ReadFile(eventsPath); err == nil {
				if hasOOMKill(string(data)) {
					l.MarkOOMKilled(pid)
				}
			}

			procsData, err := os.ReadFile(filepath.Join(cgroupDir, "cgroup.procs"))
			if err == nil {
				for _, line := range strings.Split(string(procsData), "\n") {
					line = strings.TrimSpace(line)
					if line == "" {
						continue
					}
					if p, err := strconv.Atoi(line); err == nil && p > 0 {
						killProcessGroupOrPID(p)
					}
				}
			}

			_ = os.Remove(cgroupDir)
		})
	}

	return cleanup, nil
}

func (l *linuxLimiter) applyWatchdog(pid int, limits *config.LimitsConfig) (func(), error) {
	if limits.CPU > 0 {
		if err := syscall.Setpriority(syscall.PRIO_PGRP, pid, 10); err != nil {
			_ = syscall.Setpriority(syscall.PRIO_PROCESS, pid, 10)
		}
	}

	var watchdog *processWatchdog
	if limits.Memory > 0 {
		watchdog = newProcessWatchdog(pid, limits.Memory, l.watchdogInterval, func(p int) {
			l.MarkOOMKilled(p)
		})
		watchdog.Start()
	}

	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			if watchdog != nil {
				watchdog.Stop()
			}
		})
	}
	return cleanup, nil
}

func hasOOMKill(events string) bool {
	lines := strings.Split(events, "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) >= 2 && (fields[0] == "oom_kill" || fields[0] == "oom") {
			if count, err := strconv.Atoi(fields[1]); err == nil && count > 0 {
				return true
			}
		}
	}
	return false
}

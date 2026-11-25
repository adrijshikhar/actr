//go:build darwin || linux

package host

import "syscall"

func killProcessGroupOrPID(pid int) {
	if pid <= 0 {
		return
	}
	// Attempt to kill process group first (negative pid)
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

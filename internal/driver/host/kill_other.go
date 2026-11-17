//go:build !darwin && !linux

package host

import "os"

func killProcessGroupOrPID(pid int) {
	if proc, err := os.FindProcess(pid); err == nil {
		_ = proc.Kill()
	}
}

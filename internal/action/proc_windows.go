//go:build windows

package action

import (
	"os/exec"
	"strconv"
	"syscall"
)

func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
}

func killProcessGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil || cmd.Process.Pid <= 0 {
		return nil
	}
	// Use taskkill with /T (tree kill) and /F (force) to ensure child processes are not orphaned.
	killCmd := exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(cmd.Process.Pid))
	if err := killCmd.Run(); err != nil {
		// Fallback to direct process kill if taskkill fails or process already exited
		return cmd.Process.Kill()
	}
	return nil
}

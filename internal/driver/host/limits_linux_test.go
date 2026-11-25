//go:build linux

package host

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/adrijshikhar/actr/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHasOOMKill(t *testing.T) {
	sampleEvents := "low 0\nhigh 0\nmax 0\noom 0\noom_kill 2\noom_group_kill 0\n"
	assert.True(t, hasOOMKill(sampleEvents))

	zeroEvents := "low 0\nhigh 0\nmax 0\noom 0\noom_kill 0\noom_group_kill 0\n"
	assert.False(t, hasOOMKill(zeroEvents))

	oomFallback := "oom 1\n"
	assert.True(t, hasOOMKill(oomFallback))
}

func TestLinuxLimiter_DirectCgroupV2(t *testing.T) {
	tmpCgroup := t.TempDir()
	// Mock cgroup.controllers
	require.NoError(t, os.WriteFile(filepath.Join(tmpCgroup, "cgroup.controllers"), []byte("cpu memory pids\n"), 0644))

	limiter := &linuxLimiter{
		cgroupRoot: tmpCgroup,
	}

	limits := &config.LimitsConfig{
		CPU:    1.5,
		Memory: 256 * 1024 * 1024,
		Pids:   50,
	}

	cmd := exec.Command("sleep", "30")
	require.NoError(t, cmd.Start())
	defer func() {
		_ = cmd.Process.Kill()
	}()

	pid := cmd.Process.Pid
	cleanup, err := limiter.tryCgroupsV2(pid, limits)
	require.NoError(t, err)

	subtree, err := os.ReadFile(filepath.Join(tmpCgroup, "actr", "cgroup.subtree_control"))
	require.NoError(t, err)
	assert.Equal(t, "+cpu +memory +pids", string(subtree))

	cgroupDir := filepath.Join(tmpCgroup, "actr", fmt.Sprintf("job-%d", pid))
	cpuMax, err := os.ReadFile(filepath.Join(cgroupDir, "cpu.max"))
	require.NoError(t, err)
	assert.Equal(t, "150000 100000\n", string(cpuMax))

	memMax, err := os.ReadFile(filepath.Join(cgroupDir, "memory.max"))
	require.NoError(t, err)
	assert.Equal(t, fmt.Sprintf("%d\n", limits.Memory), string(memMax))

	pidsMax, err := os.ReadFile(filepath.Join(cgroupDir, "pids.max"))
	require.NoError(t, err)
	assert.Equal(t, "50\n", string(pidsMax))

	// Write mock oom_kill event and mock lingering procs before cleanup
	require.NoError(t, os.WriteFile(filepath.Join(cgroupDir, "memory.events"), []byte("oom_kill 1\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(cgroupDir, "cgroup.procs"), []byte(fmt.Sprintf("%d\n999999\n", pid)), 0644))

	cleanup()
	assert.Error(t, cmd.Wait(), "sacrificial child process should have been terminated by cleanup")
	assert.True(t, limiter.IsOOMKilled(pid))
}

package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/docker/docker/api/types/container"
)

// DockerStatsClient provides access to streaming container stats from Docker Engine.
type DockerStatsClient interface {
	ContainerStats(ctx context.Context, containerID string, stream bool) (container.StatsResponseReader, error)
}

// CalculateDockerCPUPercent calculates the container CPU usage percentage according to
// Docker's standard delta formula:
// (cpu_delta / system_delta) * online_cpus * 100.
func CalculateDockerCPUPercent(v *container.StatsResponse, prevCPU, prevSystem uint64) float64 {
	if v == nil {
		return 0.0
	}

	var cpuDelta float64
	var systemDelta float64

	if prevCPU > 0 || prevSystem > 0 {
		cpuDelta = float64(v.CPUStats.CPUUsage.TotalUsage) - float64(prevCPU)
		systemDelta = float64(v.CPUStats.SystemUsage) - float64(prevSystem)
	} else if v.PreCPUStats.SystemUsage > 0 {
		cpuDelta = float64(v.CPUStats.CPUUsage.TotalUsage) - float64(v.PreCPUStats.CPUUsage.TotalUsage)
		systemDelta = float64(v.CPUStats.SystemUsage) - float64(v.PreCPUStats.SystemUsage)
	}

	onlineCPUs := float64(v.CPUStats.OnlineCPUs)
	if onlineCPUs == 0 {
		onlineCPUs = float64(len(v.CPUStats.CPUUsage.PercpuUsage))
	}
	if onlineCPUs == 0 {
		onlineCPUs = 1.0
	}

	if systemDelta > 0 && cpuDelta > 0 {
		return (cpuDelta / systemDelta) * onlineCPUs * 100.0
	}
	return 0.0
}

// CalculateDockerMemoryRSS computes RSS memory consumption excluding inactive cache/files
// across cgroups v1, v2, and Windows platforms.
func CalculateDockerMemoryRSS(mem container.MemoryStats) uint64 {
	if mem.PrivateWorkingSet > 0 {
		return mem.PrivateWorkingSet
	}

	var cache uint64
	if v, ok := mem.Stats["inactive_file"]; ok {
		cache = v
	} else if v, ok := mem.Stats["total_inactive_file"]; ok {
		cache = v
	} else if v, ok := mem.Stats["cache"]; ok {
		cache = v
	}

	if cache > 0 {
		if mem.Usage > cache {
			return mem.Usage - cache
		}
		return 0
	}
	return mem.Usage
}

// CalculateDockerNetworkIO aggregates cumulative Rx and Tx bytes across all container networks.
func CalculateDockerNetworkIO(networks map[string]container.NetworkStats) (rxBytes uint64, txBytes uint64) {
	for _, net := range networks {
		rxBytes += net.RxBytes
		txBytes += net.TxBytes
	}
	return rxBytes, txBytes
}

// ParseDockerStatsStream decodes sequential JSON container stats frames from a reader.
func ParseDockerStatsStream(ctx context.Context, r io.Reader, onStats func(*container.StatsResponse) error) error {
	dec := json.NewDecoder(r)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		var stats container.StatsResponse
		if err := dec.Decode(&stats); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if err := onStats(&stats); err != nil {
			return err
		}
	}
}

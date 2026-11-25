package monitor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockDockerStatsClient struct {
	mu          sync.Mutex
	called      bool
	lastCID     string
	lastStream  bool
	statsReader io.ReadCloser
	errToReturn error
}

func (m *mockDockerStatsClient) ContainerStats(ctx context.Context, containerID string, stream bool) (container.StatsResponseReader, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.called = true
	m.lastCID = containerID
	m.lastStream = stream
	if m.errToReturn != nil {
		return container.StatsResponseReader{}, m.errToReturn
	}
	return container.StatsResponseReader{
		Body:   m.statsReader,
		OSType: "linux",
	}, nil
}

func TestCalculateDockerCPUPercent(t *testing.T) {
	t.Run("calculates CPU percent using PreCPUStats", func(t *testing.T) {
		stats := &container.StatsResponse{
			Stats: container.Stats{
				CPUStats: container.CPUStats{
					CPUUsage: container.CPUUsage{
						TotalUsage: 2_000_000_000,
					},
					SystemUsage: 10_000_000_000,
					OnlineCPUs:  2,
				},
				PreCPUStats: container.CPUStats{
					CPUUsage: container.CPUUsage{
						TotalUsage: 1_000_000_000,
					},
					SystemUsage: 8_000_000_000,
					OnlineCPUs:  2,
				},
			},
		}

		// cpuDelta = 1e9, sysDelta = 2e9, onlineCPUs = 2
		// (1e9 / 2e9) * 2 * 100 = 100.0%
		cpu := CalculateDockerCPUPercent(stats, 0, 0)
		assert.InDelta(t, 100.0, cpu, 0.001)
	})

	t.Run("calculates CPU percent using passed previous values", func(t *testing.T) {
		stats := &container.StatsResponse{
			Stats: container.Stats{
				CPUStats: container.CPUStats{
					CPUUsage: container.CPUUsage{
						TotalUsage: 3_500_000_000,
					},
					SystemUsage: 12_000_000_000,
					OnlineCPUs:  4,
				},
			},
		}

		prevCPU := uint64(2_000_000_000)
		prevSys := uint64(10_000_000_000)
		// cpuDelta = 1.5e9, sysDelta = 2.0e9, onlineCPUs = 4
		// (1.5 / 2.0) * 4 * 100 = 300.0%
		cpu := CalculateDockerCPUPercent(stats, prevCPU, prevSys)
		assert.InDelta(t, 300.0, cpu, 0.001)
	})

	t.Run("zero delta or invalid system usage returns zero", func(t *testing.T) {
		stats := &container.StatsResponse{
			Stats: container.Stats{
				CPUStats: container.CPUStats{
					CPUUsage: container.CPUUsage{
						TotalUsage: 1_000_000_000,
					},
					SystemUsage: 10_000_000_000,
					OnlineCPUs:  2,
				},
			},
		}
		// prev == current -> delta 0
		cpu := CalculateDockerCPUPercent(stats, 1_000_000_000, 10_000_000_000)
		assert.Equal(t, 0.0, cpu)

		// nil stats
		assert.Equal(t, 0.0, CalculateDockerCPUPercent(nil, 0, 0))
	})

	t.Run("online CPUs fallback to percpu usage length or 1", func(t *testing.T) {
		stats := &container.StatsResponse{
			Stats: container.Stats{
				CPUStats: container.CPUStats{
					CPUUsage: container.CPUUsage{
						TotalUsage:  2_000_000_000,
						PercpuUsage: []uint64{1_000_000_000, 1_000_000_000},
					},
					SystemUsage: 10_000_000_000,
					OnlineCPUs:  0, // Not explicitly set
				},
				PreCPUStats: container.CPUStats{
					CPUUsage: container.CPUUsage{
						TotalUsage: 1_000_000_000,
					},
					SystemUsage: 8_000_000_000,
				},
			},
		}
		// onlineCPUs inferred from len(PercpuUsage) = 2
		cpu := CalculateDockerCPUPercent(stats, 0, 0)
		assert.InDelta(t, 100.0, cpu, 0.001)
	})
}

func TestCalculateDockerMemoryRSS(t *testing.T) {
	t.Run("cgroup v2 inactive_file deducted from usage", func(t *testing.T) {
		mem := container.MemoryStats{
			Usage: 1024 * 1024 * 1024, // 1 GB
			Stats: map[string]uint64{
				"inactive_file": 256 * 1024 * 1024, // 256 MB
			},
		}
		rss := CalculateDockerMemoryRSS(mem)
		assert.Equal(t, uint64(768*1024*1024), rss)
	})

	t.Run("cgroup v1 total_inactive_file deducted from usage", func(t *testing.T) {
		mem := container.MemoryStats{
			Usage: 500 * 1024 * 1024,
			Stats: map[string]uint64{
				"total_inactive_file": 100 * 1024 * 1024,
			},
		}
		rss := CalculateDockerMemoryRSS(mem)
		assert.Equal(t, uint64(400*1024*1024), rss)
	})

	t.Run("legacy cache key deducted from usage", func(t *testing.T) {
		mem := container.MemoryStats{
			Usage: 300 * 1024 * 1024,
			Stats: map[string]uint64{
				"cache": 50 * 1024 * 1024,
			},
		}
		rss := CalculateDockerMemoryRSS(mem)
		assert.Equal(t, uint64(250*1024*1024), rss)
	})

	t.Run("cache greater than or equal to usage returns zero", func(t *testing.T) {
		mem := container.MemoryStats{
			Usage: 100 * 1024 * 1024,
			Stats: map[string]uint64{
				"inactive_file": 150 * 1024 * 1024,
			},
		}
		rss := CalculateDockerMemoryRSS(mem)
		assert.Equal(t, uint64(0), rss)
	})

	t.Run("windows PrivateWorkingSet takes precedence", func(t *testing.T) {
		mem := container.MemoryStats{
			Usage:             1024 * 1024 * 1024,
			PrivateWorkingSet: 512 * 1024 * 1024,
		}
		rss := CalculateDockerMemoryRSS(mem)
		assert.Equal(t, uint64(512*1024*1024), rss)
	})

	t.Run("empty or nil stats map returns raw usage", func(t *testing.T) {
		mem := container.MemoryStats{
			Usage: 128 * 1024 * 1024,
		}
		rss := CalculateDockerMemoryRSS(mem)
		assert.Equal(t, uint64(128*1024*1024), rss)
	})
}

func TestCalculateDockerNetworkIO(t *testing.T) {
	t.Run("aggregates multiple network interfaces", func(t *testing.T) {
		networks := map[string]container.NetworkStats{
			"eth0": {
				RxBytes: 1_000_000,
				TxBytes: 500_000,
			},
			"eth1": {
				RxBytes: 250_000,
				TxBytes: 100_000,
			},
		}
		rx, tx := CalculateDockerNetworkIO(networks)
		assert.Equal(t, uint64(1_250_000), rx)
		assert.Equal(t, uint64(600_000), tx)
	})

	t.Run("empty or nil networks returns zero", func(t *testing.T) {
		rx, tx := CalculateDockerNetworkIO(nil)
		assert.Equal(t, uint64(0), rx)
		assert.Equal(t, uint64(0), tx)
	})
}

func TestTelemetrySampler_SetTargetContainer_Streaming(t *testing.T) {
	t.Run("samples container metrics from JSON stream", func(t *testing.T) {
		// Frame 1
		frame1 := container.StatsResponse{
			Stats: container.Stats{
				Read: time.Date(2026, 11, 16, 18, 0, 0, 0, time.UTC),
				CPUStats: container.CPUStats{
					CPUUsage:    container.CPUUsage{TotalUsage: 1_000_000_000},
					SystemUsage: 5_000_000_000,
					OnlineCPUs:  2,
				},
				PreCPUStats: container.CPUStats{
					CPUUsage:    container.CPUUsage{TotalUsage: 500_000_000},
					SystemUsage: 4_000_000_000,
					OnlineCPUs:  2,
				},
				MemoryStats: container.MemoryStats{
					Usage: 1024 * 1024 * 1024, // 1 GB
					Stats: map[string]uint64{
						"inactive_file": 256 * 1024 * 1024, // 256 MB
					},
				},
				PidsStats: container.PidsStats{
					Current: 8,
				},
			},
			Networks: map[string]container.NetworkStats{
				"eth0": {
					RxBytes: 100_000,
					TxBytes: 50_000,
				},
			},
		}

		// Frame 2 (1 second later: delta rx=50_000 -> 50 KB/s, delta tx=20_000 -> 20 KB/s)
		frame2 := container.StatsResponse{
			Stats: container.Stats{
				Read: time.Date(2026, 11, 16, 18, 0, 1, 0, time.UTC),
				CPUStats: container.CPUStats{
					CPUUsage:    container.CPUUsage{TotalUsage: 2_000_000_000},
					SystemUsage: 6_000_000_000,
					OnlineCPUs:  2,
				},
				PreCPUStats: container.CPUStats{
					CPUUsage:    container.CPUUsage{TotalUsage: 1_000_000_000},
					SystemUsage: 5_000_000_000,
					OnlineCPUs:  2,
				},
				MemoryStats: container.MemoryStats{
					Usage: 1024 * 1024 * 1024,
					Stats: map[string]uint64{
						"inactive_file": 256 * 1024 * 1024,
					},
				},
				PidsStats: container.PidsStats{
					Current: 12,
				},
			},
			Networks: map[string]container.NetworkStats{
				"eth0": {
					RxBytes: 150_000,
					TxBytes: 70_000,
				},
			},
		}

		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		require.NoError(t, enc.Encode(frame1))
		require.NoError(t, enc.Encode(frame2))

		pr, pw := io.Pipe()
		go func() {
			_, _ = io.Copy(pw, &buf)
			_ = pw.Close()
		}()

		mockCli := &mockDockerStatsClient{
			statsReader: pr,
		}

		sampler := NewTelemetrySampler()
		defer sampler.Close()

		sampler.SetTargetContainer("test-container-id", mockCli)

		// Wait until sampler processes frame 2
		require.Eventually(t, func() bool {
			s := sampler.Sample()
			return s != nil && s.ProcessCount == 12 && s.NetRxRate > 0
		}, 2*time.Second, 20*time.Millisecond)

		sample := sampler.Sample()
		require.NotNil(t, sample)
		assert.Equal(t, 12, sample.ProcessCount)
		assert.Equal(t, uint64(768*1024*1024), sample.StepRSSBytes)
		// Frame 2: cpuDelta=1e9, sysDelta=1e9, 2 cpus -> 200%
		assert.InDelta(t, 200.0, sample.StepCPU, 1.0)
		assert.Equal(t, uint64(50_000), sample.NetRxRate)
		assert.Equal(t, uint64(20_000), sample.NetTxRate)
	})

	t.Run("reverting to nil container restores host process sampling", func(t *testing.T) {
		frame := container.StatsResponse{
			Stats: container.Stats{
				CPUStats: container.CPUStats{
					CPUUsage:    container.CPUUsage{TotalUsage: 500_000_000},
					SystemUsage: 2_000_000_000,
					OnlineCPUs:  1,
				},
				MemoryStats: container.MemoryStats{
					Usage: 50 * 1024 * 1024,
				},
			},
		}

		var buf bytes.Buffer
		_ = json.NewEncoder(&buf).Encode(frame)

		mockCli := &mockDockerStatsClient{
			statsReader: io.NopCloser(strings.NewReader(buf.String())),
		}

		sampler := NewTelemetrySampler()
		defer sampler.Close()

		sampler.SetTargetContainer("c1", mockCli)
		require.Eventually(t, func() bool {
			s := sampler.Sample()
			return s != nil && s.StepRSSBytes == 50*1024*1024
		}, 2*time.Second, 20*time.Millisecond)

		// Revert to host
		sampler.SetTargetContainer("", nil)

		// Set a dummy PID that does not exist
		sampler.SetTargetPID(999999999, 999999999)
		sample := sampler.Sample()
		require.NotNil(t, sample)
		// When host PID does not exist, StepRSSBytes should be 0
		assert.Equal(t, uint64(0), sample.StepRSSBytes)
	})

	t.Run("handles error from ContainerStats gracefully", func(t *testing.T) {
		mockCli := &mockDockerStatsClient{
			errToReturn: errors.New("container not found"),
		}

		sampler := NewTelemetrySampler()
		defer sampler.Close()

		sampler.SetTargetContainer("non-existent-cid", mockCli)
		sample := sampler.Sample()
		require.NotNil(t, sample)
		assert.Equal(t, 1, sample.ProcessCount)
	})
}

func TestParseDockerStatsStream(t *testing.T) {
	t.Run("decodes stream of frames until EOF", func(t *testing.T) {
		frame1 := container.StatsResponse{ID: "c1"}
		frame2 := container.StatsResponse{ID: "c2"}

		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		require.NoError(t, enc.Encode(frame1))
		require.NoError(t, enc.Encode(frame2))

		var collected []string
		err := ParseDockerStatsStream(context.Background(), &buf, func(stats *container.StatsResponse) error {
			collected = append(collected, stats.ID)
			return nil
		})

		require.NoError(t, err)
		assert.Equal(t, []string{"c1", "c2"}, collected)
	})

	t.Run("aborts on context cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		pr, _ := io.Pipe()
		err := ParseDockerStatsStream(ctx, pr, func(stats *container.StatsResponse) error {
			return nil
		})
		assert.ErrorIs(t, err, context.Canceled)
	})
}

type blockingStatsReader struct {
	mu      sync.Mutex
	closed  int
	unblock chan struct{}
	once    sync.Once
	payload []byte
	readPos int
}

func (b *blockingStatsReader) Read(p []byte) (int, error) {
	b.mu.Lock()
	if b.readPos < len(b.payload) {
		n := copy(p, b.payload[b.readPos:])
		b.readPos += n
		b.mu.Unlock()
		return n, nil
	}
	b.mu.Unlock()
	<-b.unblock
	return 0, io.EOF
}

func (b *blockingStatsReader) Close() error {
	b.mu.Lock()
	b.closed++
	b.mu.Unlock()
	b.once.Do(func() { close(b.unblock) })
	return nil
}

func TestTelemetrySampler_CancelStopsStaleWritesAndClosesOnce(t *testing.T) {
	frame := container.StatsResponse{
		Stats: container.Stats{
			MemoryStats: container.MemoryStats{Usage: 123 * 1024 * 1024},
			PidsStats:   container.PidsStats{Current: ^uint64(0)},
		},
	}
	var buf bytes.Buffer
	require.NoError(t, json.NewEncoder(&buf).Encode(frame))

	reader := &blockingStatsReader{
		unblock: make(chan struct{}),
		payload: buf.Bytes(),
	}
	mockCli := &mockDockerStatsClient{statsReader: reader}

	sampler := NewTelemetrySampler()
	sampler.SetTargetContainer("stale-cid", mockCli)

	require.Eventually(t, func() bool {
		s := sampler.Sample()
		return s != nil && s.StepRSSBytes == 123*1024*1024
	}, 2*time.Second, 10*time.Millisecond)

	sample := sampler.Sample()
	require.NotNil(t, sample)
	assert.Positive(t, sample.ProcessCount)

	// Cancel container target; stats must remain cleared and reader closed exactly once
	sampler.SetTargetContainer("", nil)
	require.Eventually(t, func() bool {
		reader.mu.Lock()
		defer reader.mu.Unlock()
		return reader.closed >= 1
	}, 2*time.Second, 10*time.Millisecond)

	sampler.cStatsMu.RLock()
	cStats := sampler.containerStats
	sampler.cStatsMu.RUnlock()
	assert.Nil(t, cStats)

	reader.mu.Lock()
	closedCount := reader.closed
	reader.mu.Unlock()
	assert.Equal(t, 1, closedCount)
}

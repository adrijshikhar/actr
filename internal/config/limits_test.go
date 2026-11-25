package config

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseCPU(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected float64
		wantErr  bool
	}{
		{"integer cores", "2", 2.0, false},
		{"float cores", "2.0", 2.0, false},
		{"sub-core float", "0.5", 0.5, false},
		{"millicores half core", "500m", 0.5, false},
		{"millicores 1.5 cores", "1500m", 1.5, false},
		{"zero string", "0", 0.0, false},
		{"empty string", "", 0.0, false},
		{"zero millicores", "0m", 0.0, false},
		{"whitespace trimmed", "  2.5  ", 2.5, false},
		{"invalid string", "invalid", 0.0, true},
		{"invalid millicores", "abcm", 0.0, true},
		{"just m", "m", 0.0, true},
		{"double m suffix", "500mm", 0.0, true},
		{"negative cpu", "-1", 0.0, true},
		{"negative millicores", "-500m", 0.0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			val, err := ParseCPU(tt.input)
			if tt.wantErr {
				assert.Error(t, err, "input: %s", tt.input)
			} else {
				require.NoError(t, err, "input: %s", tt.input)
				assert.InDelta(t, tt.expected, val, 1e-9, "input: %s", tt.input)
			}
		})
	}
}

func TestParseMemory(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected int64
		wantErr  bool
	}{
		{"decimal megabytes", "512MB", 512 * 1000 * 1000, false},
		{"decimal megabytes short", "512M", 512 * 1000 * 1000, false},
		{"binary mebibytes", "512MiB", 512 * 1024 * 1024, false},
		{"decimal gigabytes", "4GB", 4 * 1000 * 1000 * 1000, false},
		{"binary gibibytes", "4GiB", 4 * 1024 * 1024 * 1024, false},
		{"decimal gigabytes short", "10G", 10 * 1000 * 1000 * 1000, false},
		{"raw bytes integer", "1024", 1024, false},
		{"explicit bytes suffix", "1024B", 1024, false},
		{"decimal kilobytes", "100KB", 100 * 1000, false},
		{"decimal kilobytes short", "100K", 100 * 1000, false},
		{"binary kibibytes", "100KiB", 100 * 1024, false},
		{"decimal terabytes", "2TB", 2 * 1000 * 1000 * 1000 * 1000, false},
		{"binary tebibytes", "2TiB", 2 * 1024 * 1024 * 1024 * 1024, false},
		{"zero string", "0", 0, false},
		{"empty string", "", 0, false},
		{"whitespace trimmed", "  512MB  ", 512 * 1000 * 1000, false},
		{"case insensitive", "512mib", 512 * 1024 * 1024, false},
		{"fractional gibibytes", "1.5GiB", int64(1.5 * 1024 * 1024 * 1024), false},
		{"invalid string", "invalid", 0, true},
		{"invalid unit", "512XB", 0, true},
		{"negative memory", "-512MB", 0, true},
		{"unit only", "MB", 0, true},
		{"multiple decimal points", "1.2.3MB", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			val, err := ParseMemory(tt.input)
			if tt.wantErr {
				assert.Error(t, err, "input: %s", tt.input)
			} else {
				require.NoError(t, err, "input: %s", tt.input)
				assert.Equal(t, tt.expected, val, "input: %s", tt.input)
			}
		})
	}
}

func TestLimitsConfig(t *testing.T) {
	cfg := LimitsConfig{
		CPU:     2.5,
		Memory:  4 * 1024 * 1024 * 1024,
		Pids:    512,
		Timeout: 30 * time.Minute,
	}

	assert.Equal(t, 2.5, cfg.CPU)
	assert.Equal(t, int64(4294967296), cfg.Memory)
	assert.Equal(t, int64(512), cfg.Pids)
	assert.Equal(t, 30*time.Minute, cfg.Timeout)
}

func TestParseMemory_RejectsBoundaryInt64Overflow(t *testing.T) {
	_, err := ParseMemory("8589934592GiB") // 2^33 * 2^30 = 2^63 > MaxInt64
	require.Error(t, err)
	_, err = ParseMemory("9223372036854775808B") // 2^63 > MaxInt64
	require.Error(t, err)
	_, err = ParseMemory("9223372036854775808")
	require.Error(t, err)
	_, err = ParseMemory("1e309B") // Inf
	require.Error(t, err)
	assert.Contains(t, err.Error(), "out of range")
	digits310 := strings.Repeat("9", 310) + "B"
	_, err = ParseMemory(digits310)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "out of range")
}

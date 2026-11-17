package config

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type LimitsConfig struct {
	CPU     float64       `json:"cpu" yaml:"cpu"`
	Memory  int64         `json:"memory" yaml:"memory"`
	Pids    int64         `json:"pids" yaml:"pids"`
	Timeout time.Duration `json:"timeout" yaml:"timeout"`
}

func ParseCPU(s string) (float64, error) {
	cleaned := strings.TrimSpace(s)
	if cleaned == "" || cleaned == "0" {
		return 0, nil
	}
	if strings.HasSuffix(cleaned, "m") {
		milliStr := strings.TrimSuffix(cleaned, "m")
		if milliStr == "" {
			return 0, fmt.Errorf("invalid millicores value %q", s)
		}
		milli, err := strconv.ParseFloat(milliStr, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid millicores %q: %w", s, err)
		}
		if math.IsNaN(milli) || math.IsInf(milli, 0) || milli < 0 {
			return 0, fmt.Errorf("cpu limit cannot be negative or invalid: %f", milli)
		}
		return milli / 1000.0, nil
	}
	val, err := strconv.ParseFloat(cleaned, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid cpu value %q: %w", s, err)
	}
	if math.IsNaN(val) || math.IsInf(val, 0) || val < 0 {
		return 0, fmt.Errorf("cpu limit cannot be negative or invalid: %f", val)
	}
	return val, nil
}

func ParseMemory(s string) (int64, error) {
	cleaned := strings.TrimSpace(s)
	if cleaned == "" || cleaned == "0" {
		return 0, nil
	}

	// Separate numeric prefix from unit suffix
	var numParts strings.Builder
	var unitParts strings.Builder
	for i, r := range cleaned {
		if unicode.IsDigit(r) || (r == '.' && i > 0) {
			if unitParts.Len() > 0 {
				return 0, fmt.Errorf("invalid memory string: %s", s)
			}
			numParts.WriteRune(r)
		} else {
			unitParts.WriteRune(r)
		}
	}

	if numParts.Len() == 0 {
		return 0, fmt.Errorf("invalid memory string (no numeric part): %s", s)
	}

	val, err := strconv.ParseFloat(numParts.String(), 64)
	if err != nil {
		return 0, fmt.Errorf("invalid memory numeric value %q: %w", s, err)
	}
	if math.IsNaN(val) || math.IsInf(val, 0) || val < 0 {
		return 0, fmt.Errorf("memory cannot be negative or invalid: %f", val)
	}

	unit := strings.ToUpper(strings.TrimSpace(unitParts.String()))
	var multiplier float64

	switch unit {
	case "", "B":
		multiplier = 1
	case "K", "KB":
		multiplier = 1e3
	case "KIB":
		multiplier = 1024
	case "M", "MB":
		multiplier = 1e6
	case "MIB":
		multiplier = 1024 * 1024
	case "G", "GB":
		multiplier = 1e9
	case "GIB":
		multiplier = 1024 * 1024 * 1024
	case "T", "TB":
		multiplier = 1e12
	case "TIB":
		multiplier = 1024 * 1024 * 1024 * 1024
	default:
		return 0, fmt.Errorf("unknown memory unit %q in %s", unit, s)
	}

	if val > 0 && multiplier > float64(math.MaxInt64)/val {
		return 0, fmt.Errorf("memory value exceeds maximum int64: %s", s)
	}

	return int64(val * multiplier), nil
}

//go:build !linux && !darwin

package host

import (
	"context"

	"github.com/adrijshikhar/actr/internal/config"
)

type otherLimiter struct {
	BaseLimiter
}

func newPlatformLimiter() HostLimiter {
	return &otherLimiter{}
}

func (l *otherLimiter) Apply(ctx context.Context, pid int, limits *config.LimitsConfig) (cleanup func(), err error) {
	return func() {}, nil
}

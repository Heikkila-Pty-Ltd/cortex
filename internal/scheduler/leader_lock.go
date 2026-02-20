package scheduler

import (
	"context"
	"log/slog"
	"time"

	"github.com/antigravity-dev/cortex/internal/store"
)

type leaderLock interface {
	Acquire(context.Context) error
	Release(context.Context) error
}

type noopLeaderLock struct{}

func (n noopLeaderLock) Acquire(_ context.Context) error {
	return nil
}

func (n noopLeaderLock) Release(_ context.Context) error {
	return nil
}

func NewLeaderLock(s *store.Store, instanceID string, ttl time.Duration, logger *slog.Logger) leaderLock {
	if s == nil {
		if logger != nil {
			logger.Warn("scheduler running without persistence-backed leader lock", "instance", instanceID, "ttl", ttl)
		}
	}
	return noopLeaderLock{}
}

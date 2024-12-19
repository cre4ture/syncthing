package utils

import (
	"context"
	"time"
)

func AbortableTimeSleep(ctx context.Context, duration time.Duration) error {
	select {
	case <-ctx.Done():
		return context.Canceled
	case <-time.After(duration):
		return nil
	}
}

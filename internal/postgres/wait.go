package postgres

import (
	"context"
	"time"
)

func waitFor(ctx context.Context, budget time.Duration, probe func(context.Context) error, onRetry func(attempt int, err error)) error {
	deadline := time.Now().Add(budget)

	for attempt := 1; ; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := probe(attemptCtx)
		cancel()

		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return err
		}

		onRetry(attempt, err)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

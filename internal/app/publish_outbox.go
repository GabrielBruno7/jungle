package app

import (
	"context"
	"fmt"
	"math"
	"time"

	"jungle/internal/observability"
)

type Publisher interface {
	Publish(ctx context.Context, rec OutboxRecord) error
}

type PublishPolicy struct {
	BatchSize       int
	MaxPassesPerRun int
	LockFor         time.Duration
	BaseBackoff     time.Duration
	MaxBackoff      time.Duration
}

func DefaultPublishPolicy() PublishPolicy {
	return PublishPolicy{
		BatchSize:       100,
		MaxPassesPerRun: 20,
		LockFor:         30 * time.Second,
		BaseBackoff:     time.Second,
		MaxBackoff:      time.Minute,
	}
}

func (p PublishPolicy) backoffFor(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	shift := attempt - 1
	if shift > 20 {
		shift = 20
	}
	delay := time.Duration(math.Pow(2, float64(shift))) * p.BaseBackoff
	if delay > p.MaxBackoff || delay <= 0 {
		return p.MaxBackoff
	}
	return delay
}

type PublishOutbox struct {
	uow       UnitOfWork
	publisher Publisher
	clock     Clock
	owner     string
	policy    PublishPolicy
}

func NewPublishOutbox(uow UnitOfWork, publisher Publisher, clock Clock, owner InstanceID, policy PublishPolicy) *PublishOutbox {
	return &PublishOutbox{
		uow:       uow,
		publisher: publisher,
		clock:     clock,
		owner:     string(owner),
		policy:    policy,
	}
}

func (uc *PublishOutbox) RunOnce(ctx context.Context) (int, error) {
	total := 0

	for pass := 0; pass < uc.policy.MaxPassesPerRun; pass++ {
		published, drained, err := uc.publishBatch(ctx)
		total += published
		if err != nil {
			return total, err
		}
		if drained || ctx.Err() != nil {
			break
		}
	}

	return total, nil
}

func (uc *PublishOutbox) publishBatch(ctx context.Context) (int, bool, error) {
	now := uc.clock.Now()

	var batch []OutboxRecord
	err := uc.uow.Within(ctx, func(ctx context.Context, repos Repositories) error {
		lag, pending, lagErr := repos.Outbox().OldestPending(ctx, now)
		if lagErr != nil {
			return lagErr
		}
		if pending {
			observability.OutboxLagSeconds.Set(lag.Seconds())
		} else {
			observability.OutboxLagSeconds.Set(0)
		}

		var err error
		batch, err = repos.Outbox().Claim(ctx, uc.owner, uc.policy.BatchSize, uc.policy.LockFor, now)
		return err
	})
	if err != nil {
		return 0, false, fmt.Errorf("claiming outbox batch: %w", err)
	}

	published := 0
	for _, rec := range batch {
		if err := ctx.Err(); err != nil {
			return published, true, err
		}

		publishErr := uc.publisher.Publish(ctx, rec)

		markErr := uc.uow.Within(ctx, func(ctx context.Context, repos Repositories) error {
			if publishErr != nil {
				attempts := rec.Attempts + 1
				return repos.Outbox().Reschedule(ctx, rec.EventID, attempts, uc.clock.Now().Add(uc.policy.backoffFor(attempts)))
			}
			return repos.Outbox().MarkPublished(ctx, rec.EventID, uc.clock.Now())
		})
		if markErr != nil {
			continue
		}

		if publishErr == nil {
			published++
		}
	}

	return published, len(batch) < uc.policy.BatchSize, nil
}

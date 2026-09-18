package app

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"jungle/internal/domain/event"
	"jungle/internal/domain/wagertx"
	"jungle/internal/observability"
)

type ReferencePolicy struct {
	MaxAttempts int
	BaseBackoff time.Duration
	MaxBackoff  time.Duration
	TTL         time.Duration
}

func DefaultReferencePolicy() ReferencePolicy {
	return ReferencePolicy{
		MaxAttempts: 10,
		BaseBackoff: 2 * time.Second,
		MaxBackoff:  time.Minute,
		TTL:         15 * time.Minute,
	}
}

func (p ReferencePolicy) backoffFor(attempt int) time.Duration {
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

type ResolveReferences struct {
	uow       UnitOfWork
	processor *ProcessWager
	clock     Clock
	policy    ReferencePolicy
	logger    *zap.Logger
	batchSize int
}

func NewResolveReferences(uow UnitOfWork, processor *ProcessWager, clock Clock, policy ReferencePolicy, logger *zap.Logger) *ResolveReferences {
	return &ResolveReferences{
		uow:       uow,
		processor: processor,
		clock:     clock,
		policy:    policy,
		logger:    logger,
		batchSize: 50,
	}
}

func (uc *ResolveReferences) RunOnce(ctx context.Context) (int, error) {
	now := uc.clock.Now()

	var due []PendingReferenceRecord
	err := uc.uow.Within(ctx, func(ctx context.Context, repos Repositories) error {
		backlog, countErr := repos.Transactions().CountPendingReference(ctx)
		if countErr != nil {
			return countErr
		}
		observability.PendingReferences.Set(float64(backlog))

		var err error
		due, err = repos.Transactions().ClaimPendingReference(ctx, uc.batchSize, now)
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("claiming pending references: %w", err)
	}

	settled := 0
	for _, record := range due {
		moved, err := uc.settleOne(ctx, record)
		if err != nil {
			if uc.recordPermanentFailure(ctx, record, err) {
				settled++
			}
			continue
		}
		if moved {
			settled++
		}
	}

	return settled, nil
}

func (uc *ResolveReferences) recordPermanentFailure(ctx context.Context, record PendingReferenceRecord, cause error) bool {
	if ctx.Err() != nil {
		return false
	}
	if record.Attempts+1 < uc.policy.MaxAttempts {
		return false
	}

	now := uc.clock.Now()
	recorded := false

	err := uc.uow.Within(ctx, func(ctx context.Context, repos Repositories) error {
		tx, err := repos.Transactions().GetByID(ctx, record.Transaction.ID())
		if err != nil {
			return err
		}
		if tx.Status() != wagertx.PendingReference {
			return nil
		}

		if err := tx.MarkFailed(wagertx.FailureInfrastructure, now); err != nil {
			return err
		}
		if err := repos.Transactions().Update(ctx, tx); err != nil {
			return err
		}

		evt, err := event.NewWagerTransactionRejected(tx, event.Meta{
			CorrelationID: uuid.New(),
			OccurredAt:    now,
		})
		if err != nil {
			return err
		}
		if err := repos.Outbox().Insert(ctx, evt); err != nil {
			return err
		}

		recorded = true
		return nil
	})
	if err != nil {
		uc.logger.Error("could not record a permanent failure for audit",
			zap.String("transactionId", record.Transaction.ID().String()),
			zap.NamedError("cause", cause),
			zap.Error(err))
		return false
	}

	if recorded {
		uc.logger.Error("pending reference abandoned after a permanent infrastructure failure",
			zap.String("transactionId", record.Transaction.ID().String()),
			zap.Int("attempts", record.Attempts+1),
			zap.NamedError("cause", cause))
		observability.PermanentFailures.Inc()
	}
	return recorded
}

func (uc *ResolveReferences) settleOne(ctx context.Context, record PendingReferenceRecord) (bool, error) {
	now := uc.clock.Now()
	attempt := record.Attempts + 1
	moved := false

	err := uc.uow.Within(ctx, func(ctx context.Context, repos Repositories) error {
		tx, err := repos.Transactions().GetByID(ctx, record.Transaction.ID())
		if err != nil {
			return err
		}
		if tx.Status() != wagertx.PendingReference {
			return nil
		}

		w, err := repos.Wallets().GetForUpdate(ctx, tx.WalletID())
		if err != nil {
			return err
		}

		d, err := uc.processor.decideReversal(ctx, repos, &tx)
		if err != nil {
			return err
		}

		meta := event.Meta{CorrelationID: uuid.New(), OccurredAt: now}

		if d.kind == decisionPendingReference {
			exhausted := attempt >= uc.policy.MaxAttempts ||
				now.Sub(record.FirstSeenAt) >= uc.policy.TTL

			if !exhausted {
				next := now.Add(uc.policy.backoffFor(attempt))
				return repos.Transactions().RecordReferenceAttempt(ctx, tx.ID(), attempt, next)
			}

			if _, err := uc.processor.reject(
				ctx, repos, &tx, w,
				wagertx.FailureReferenceNotFound,
				meta, now,
				repos.Transactions().Update,
			); err != nil {
				return err
			}
			moved = true
			return nil
		}

		if _, err := uc.processor.settle(ctx, repos, &tx, w, d, meta, now, repos.Transactions().Update); err != nil {
			return err
		}
		moved = true
		return nil
	})
	if err != nil {
		return false, err
	}

	return moved, nil
}

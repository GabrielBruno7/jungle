package httpapi

import (
	"errors"
	"time"

	"jungle/internal/app"
	"jungle/internal/observability"
)

func observeAuthFailure(reason string) {
	observability.AuthFailures.WithLabelValues(reason).Inc()
}

func observeReconciliationDivergence() {
	observability.ReconciliationDivergences.Inc()
}

func observeSubmission(started time.Time, result app.ProcessWagerResult, err error) {
	observability.ProcessingLatency.WithLabelValues("http").Observe(time.Since(started).Seconds())

	if err != nil {
		switch {
		case errors.Is(err, app.ErrConcurrencyConflict):
			observability.ConcurrencyConflicts.Inc()
		case errors.Is(err, app.ErrIdempotencyConflict):
			observability.Duplicates.WithLabelValues("payload-conflict").Inc()
		case errors.Is(err, app.ErrOperationIdentityConflict):
			observability.Duplicates.WithLabelValues("operation-identity").Inc()
		}
		return
	}

	observability.TransactionResults.WithLabelValues(
		string(result.Transaction.Kind()),
		string(result.Transaction.Status()),
		"http",
	).Inc()

	if result.IdempotentReplay {
		observability.Duplicates.WithLabelValues("idempotency-key").Inc()
	}
}

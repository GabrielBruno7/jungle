package observability

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	TransactionResults = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "jungle_wager_transaction_results_total",
		Help: "Wager transactions by kind and final status.",
	}, []string{"kind", "status", "source"})

	Duplicates = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "jungle_duplicate_operations_total",
		Help: "Operations short-circuited as duplicates, by detection mechanism.",
	}, []string{"mechanism"})

	ConcurrencyConflicts = promauto.NewCounter(prometheus.CounterOpts{
		Name: "jungle_concurrency_conflicts_total",
		Help: "Wallet updates rejected because another writer got there first.",
	})

	ProcessingLatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "jungle_wager_processing_seconds",
		Help:    "Time to settle one wager transaction.",
		Buckets: prometheus.DefBuckets,
	}, []string{"source"})

	MessageRetries = promauto.NewCounter(prometheus.CounterOpts{
		Name: "jungle_message_retries_total",
		Help: "Messages released back to the queue after a transient failure.",
	})

	MessagesDeadLettered = promauto.NewCounter(prometheus.CounterOpts{
		Name: "jungle_messages_dead_lettered_total",
		Help: "Messages rejected as permanently unprocessable.",
	})

	OutboxPublished = promauto.NewCounter(prometheus.CounterOpts{
		Name: "jungle_outbox_published_total",
		Help: "Integration events published from the outbox.",
	})

	OutboxRetries = promauto.NewCounter(prometheus.CounterOpts{
		Name: "jungle_outbox_retries_total",
		Help: "Outbox publish attempts that failed and were rescheduled.",
	})

	OutboxLagSeconds = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "jungle_outbox_lag_seconds",
		Help: "Age of the oldest unpublished outbox event.",
	})

	ReconciliationDivergences = promauto.NewCounter(prometheus.CounterOpts{
		Name: "jungle_reconciliation_divergences_total",
		Help: "Reconciliations that found the stored balance disagreeing with the ledger.",
	})

	AuthFailures = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "jungle_auth_failures_total",
		Help: "Requests rejected for missing or invalid credentials.",
	}, []string{"reason"})

	PermanentFailures = promauto.NewCounter(prometheus.CounterOpts{
		Name: "jungle_permanent_failures_total",
		Help: "Transactions recorded as FAILED after a permanent infrastructure failure.",
	})

	PendingReferences = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "jungle_pending_references",
		Help: "Reversals currently waiting on an unresolved reference.",
	})
)

package wagertx

// Status is where a WagerTransaction sits in its state machine.
type Status string

const (
	// Pending is the starting status of every external transaction:
	// accepted, processing not yet concluded.
	Pending Status = "PENDING"

	// PendingReference means the transaction depends on a reference (a
	// REFUND/ROLLBACK's target) that has not arrived yet. Only REFUND and
	// ROLLBACK can ever reach this status.
	PendingReference Status = "PENDING_REFERENCE"

	// Processed is a terminal status: the operation completed
	// successfully.
	Processed Status = "PROCESSED"

	// Rejected is a terminal status: the operation was refused by a
	// business rule (always paired with a FailureCode).
	Rejected Status = "REJECTED"

	// Failed is a terminal status: a permanent infrastructure failure was
	// recorded for audit (always paired with a FailureCode).
	Failed Status = "FAILED"
)

// allowedTransitions enumerates every legal status change. Any (from, to)
// pair missing here — including every transition out of a terminal status
// — is rejected by transitionTo.
var allowedTransitions = map[Status]map[Status]bool{
	Pending: {
		Processed:        true,
		Rejected:         true,
		Failed:           true,
		PendingReference: true,
	},
	PendingReference: {
		Processed: true,
		Rejected:  true,
		Failed:    true,
	},
}

var validStatuses = map[Status]bool{
	Pending:          true,
	PendingReference: true,
	Processed:        true,
	Rejected:         true,
	Failed:           true,
}

// IsValid reports whether s is one of the five recognized statuses.
func (s Status) IsValid() bool {
	return validStatuses[s]
}

// IsTerminal reports whether s is one of PROCESSED, REJECTED, or FAILED —
// a status that must never transition again.
func (s Status) IsTerminal() bool {
	return s == Processed || s == Rejected || s == Failed
}

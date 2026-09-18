package wagertx

type Status string

const (
	Pending Status = "PENDING"

	PendingReference Status = "PENDING_REFERENCE"

	Processed Status = "PROCESSED"

	Rejected Status = "REJECTED"

	Failed Status = "FAILED"
)

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

func (s Status) IsValid() bool {
	return validStatuses[s]
}

func (s Status) IsTerminal() bool {
	return s == Processed || s == Rejected || s == Failed
}

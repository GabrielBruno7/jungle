package wagertx

type Kind string

const (
	Opening Kind = "OPENING"

	Bet Kind = "BET"

	Win Kind = "WIN"

	Loss Kind = "LOSS"

	Refund Kind = "REFUND"

	Rollback Kind = "ROLLBACK"
)

var externalKinds = map[Kind]bool{
	Bet:      true,
	Win:      true,
	Loss:     true,
	Refund:   true,
	Rollback: true,
}

func (k Kind) IsValidExternal() bool {
	return externalKinds[k]
}

func (k Kind) IsValid() bool {
	return k == Opening || externalKinds[k]
}

func (k Kind) RequiresReference() bool {
	return k == Refund || k == Rollback
}

func (k Kind) AllowsReference() bool {
	return k == Win || k.RequiresReference()
}

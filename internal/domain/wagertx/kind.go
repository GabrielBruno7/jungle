// Package wagertx models WagerTransaction: the record of one operation a
// game provider sends about a player's wallet (a bet, a win, a reversal...)
// or, internally, a wallet's opening credit. Like the other internal/domain
// packages, it has no dependency on Fx, HTTP, SQL, or any other
// infrastructure package.
package wagertx

// Kind is the type of movement a WagerTransaction represents.
type Kind string

const (
	// Opening is reserved for the internal wallet-opening credit. It must
	// never be accepted from HTTP or SQS — NewExternal rejects it outright.
	Opening Kind = "OPENING"

	// Bet debits the wallet: the player staked money on a round.
	Bet Kind = "BET"

	// Win credits the wallet: the player won a round. It may optionally
	// reference the BET from the same round.
	Win Kind = "WIN"

	// Loss moves no money — it just records that a round resolved without
	// a payout. The stake was already debited by the round's BET.
	Loss Kind = "LOSS"

	// Refund credits the wallet: it fully reverses a specific, already
	// processed BET. It always requires a reference.
	Refund Kind = "REFUND"

	// Rollback undoes a specific, already processed BET, WIN, or REFUND —
	// crediting or debiting depending on what it undoes. It always
	// requires a reference.
	Rollback Kind = "ROLLBACK"
)

// externalKinds are the kinds a provider may submit via HTTP or SQS.
// Opening is deliberately excluded: it is internal-only.
var externalKinds = map[Kind]bool{
	Bet:      true,
	Win:      true,
	Loss:     true,
	Refund:   true,
	Rollback: true,
}

// IsValidExternal reports whether k is a kind a provider may submit.
func (k Kind) IsValidExternal() bool {
	return externalKinds[k]
}

// IsValid reports whether k is any recognized kind, external or internal.
func (k Kind) IsValid() bool {
	return k == Opening || externalKinds[k]
}

// RequiresReference reports whether k must carry a
// referenceExternalTransactionId (REFUND and ROLLBACK).
func (k Kind) RequiresReference() bool {
	return k == Refund || k == Rollback
}

// AllowsReference reports whether k may optionally carry a reference (WIN,
// in addition to the kinds that require one).
func (k Kind) AllowsReference() bool {
	return k == Win || k.RequiresReference()
}

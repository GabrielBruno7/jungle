package event

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"jungle/internal/domain/money"
	"jungle/internal/domain/wagertx"
	"jungle/internal/domain/wallet"
)

type Type string

const (
	TypeWagerTransactionProcessed        Type = "WagerTransactionProcessed"
	TypeWagerTransactionRejected         Type = "WagerTransactionRejected"
	TypeWagerTransactionPendingReference Type = "WagerTransactionPendingReference"
	TypeWalletBalanceChanged             Type = "WalletBalanceChanged"
)

const currentVersion = 1

var ErrInvalidEvent = errors.New("event: invalid event")

type Payload interface {
	Type() Type
}

type Envelope struct {
	EventID       uuid.UUID
	EventType     Type
	AggregateID   string
	CorrelationID uuid.UUID
	CausationID   *uuid.UUID
	OccurredAt    time.Time
	Version       int
	Data          Payload
}

type Meta struct {
	CorrelationID uuid.UUID
	CausationID   *uuid.UUID
	OccurredAt    time.Time
}

func newEnvelope(aggregateID string, data Payload, meta Meta) (Envelope, error) {
	if aggregateID == "" {
		return Envelope{}, fmt.Errorf("%w: aggregateId is required", ErrInvalidEvent)
	}
	if meta.CorrelationID == uuid.Nil {
		return Envelope{}, fmt.Errorf("%w: correlationId is required", ErrInvalidEvent)
	}
	if meta.OccurredAt.IsZero() {
		return Envelope{}, fmt.Errorf("%w: occurredAt is required", ErrInvalidEvent)
	}

	return Envelope{
		EventID:       uuid.New(),
		EventType:     data.Type(),
		AggregateID:   aggregateID,
		CorrelationID: meta.CorrelationID,
		CausationID:   meta.CausationID,
		OccurredAt:    meta.OccurredAt.UTC(),
		Version:       currentVersion,
		Data:          data,
	}, nil
}

type envelopeJSON struct {
	EventID       string          `json:"eventId"`
	EventType     Type            `json:"eventType"`
	AggregateID   string          `json:"aggregateId"`
	CorrelationID string          `json:"correlationId"`
	CausationID   *string         `json:"causationId,omitempty"`
	OccurredAt    string          `json:"occurredAt"`
	Version       int             `json:"version"`
	Data          json.RawMessage `json:"data"`
}

func (e Envelope) MarshalJSON() ([]byte, error) {
	data, err := json.Marshal(e.Data)
	if err != nil {
		return nil, fmt.Errorf("marshaling event data: %w", err)
	}

	var causation *string
	if e.CausationID != nil {
		s := e.CausationID.String()
		causation = &s
	}

	return json.Marshal(envelopeJSON{
		EventID:       e.EventID.String(),
		EventType:     e.EventType,
		AggregateID:   e.AggregateID,
		CorrelationID: e.CorrelationID.String(),
		CausationID:   causation,
		OccurredAt:    e.OccurredAt.UTC().Format(time.RFC3339Nano),
		Version:       e.Version,
		Data:          data,
	})
}

type WagerTransactionProcessedData struct {
	TransactionID         string      `json:"transactionId"`
	WalletID              string      `json:"walletId"`
	PlayerID              string      `json:"playerId"`
	ProviderID            string      `json:"providerId,omitempty"`
	ExternalTransactionID string      `json:"externalTransactionId,omitempty"`
	RoundID               string      `json:"roundId,omitempty"`
	GameID                string      `json:"gameId,omitempty"`
	Kind                  string      `json:"kind"`
	Money                 money.Money `json:"money"`
	Balance               money.Money `json:"balance"`
	ProcessedAt           string      `json:"processedAt"`
}

func (WagerTransactionProcessedData) Type() Type { return TypeWagerTransactionProcessed }

func NewWagerTransactionProcessed(tx wagertx.WagerTransaction, balance money.Money, meta Meta) (Envelope, error) {
	return newEnvelope(tx.ID().String(), WagerTransactionProcessedData{
		TransactionID:         tx.ID().String(),
		WalletID:              tx.WalletID().String(),
		PlayerID:              tx.PlayerID().String(),
		ProviderID:            tx.ProviderID(),
		ExternalTransactionID: tx.ExternalTransactionID(),
		RoundID:               tx.RoundID(),
		GameID:                tx.GameID(),
		Kind:                  string(tx.Kind()),
		Money:                 tx.Money(),
		Balance:               balance,
		ProcessedAt:           tx.UpdatedAt().UTC().Format(time.RFC3339Nano),
	}, meta)
}

type WagerTransactionRejectedData struct {
	TransactionID         string      `json:"transactionId"`
	WalletID              string      `json:"walletId"`
	PlayerID              string      `json:"playerId"`
	ProviderID            string      `json:"providerId,omitempty"`
	ExternalTransactionID string      `json:"externalTransactionId,omitempty"`
	RoundID               string      `json:"roundId,omitempty"`
	GameID                string      `json:"gameId,omitempty"`
	Kind                  string      `json:"kind"`
	Money                 money.Money `json:"money"`
	FailureCode           string      `json:"failureCode"`
	RejectedAt            string      `json:"rejectedAt"`
}

func (WagerTransactionRejectedData) Type() Type { return TypeWagerTransactionRejected }

func NewWagerTransactionRejected(tx wagertx.WagerTransaction, meta Meta) (Envelope, error) {
	return newEnvelope(tx.ID().String(), WagerTransactionRejectedData{
		TransactionID:         tx.ID().String(),
		WalletID:              tx.WalletID().String(),
		PlayerID:              tx.PlayerID().String(),
		ProviderID:            tx.ProviderID(),
		ExternalTransactionID: tx.ExternalTransactionID(),
		RoundID:               tx.RoundID(),
		GameID:                tx.GameID(),
		Kind:                  string(tx.Kind()),
		Money:                 tx.Money(),
		FailureCode:           string(tx.FailureCode()),
		RejectedAt:            tx.UpdatedAt().UTC().Format(time.RFC3339Nano),
	}, meta)
}

type WagerTransactionPendingReferenceData struct {
	TransactionID                  string      `json:"transactionId"`
	WalletID                       string      `json:"walletId"`
	PlayerID                       string      `json:"playerId"`
	ProviderID                     string      `json:"providerId"`
	ExternalTransactionID          string      `json:"externalTransactionId"`
	ReferenceExternalTransactionID string      `json:"referenceExternalTransactionId"`
	Kind                           string      `json:"kind"`
	Money                          money.Money `json:"money"`
	PendingSince                   string      `json:"pendingSince"`
}

func (WagerTransactionPendingReferenceData) Type() Type {
	return TypeWagerTransactionPendingReference
}

func NewWagerTransactionPendingReference(tx wagertx.WagerTransaction, meta Meta) (Envelope, error) {
	return newEnvelope(tx.ID().String(), WagerTransactionPendingReferenceData{
		TransactionID:                  tx.ID().String(),
		WalletID:                       tx.WalletID().String(),
		PlayerID:                       tx.PlayerID().String(),
		ProviderID:                     tx.ProviderID(),
		ExternalTransactionID:          tx.ExternalTransactionID(),
		ReferenceExternalTransactionID: tx.ReferenceExternalTransactionID(),
		Kind:                           string(tx.Kind()),
		Money:                          tx.Money(),
		PendingSince:                   tx.UpdatedAt().UTC().Format(time.RFC3339Nano),
	}, meta)
}

type WalletBalanceChangedData struct {
	WalletID      string      `json:"walletId"`
	TransactionID string      `json:"transactionId"`
	Direction     string      `json:"direction"`
	Money         money.Money `json:"money"`
	BalanceBefore money.Money `json:"balanceBefore"`
	BalanceAfter  money.Money `json:"balanceAfter"`
	WalletVersion int64       `json:"walletVersion"`
	ChangedAt     string      `json:"changedAt"`
}

func (WalletBalanceChangedData) Type() Type { return TypeWalletBalanceChanged }

func NewWalletBalanceChanged(entry wallet.WalletLedgerEntry, walletVersion int64, meta Meta) (Envelope, error) {
	return newEnvelope(entry.WalletID().String(), WalletBalanceChangedData{
		WalletID:      entry.WalletID().String(),
		TransactionID: entry.TransactionID().String(),
		Direction:     string(entry.Direction()),
		Money:         entry.Amount(),
		BalanceBefore: entry.BalanceBefore(),
		BalanceAfter:  entry.BalanceAfter(),
		WalletVersion: walletVersion,
		ChangedAt:     entry.CreatedAt().UTC().Format(time.RFC3339Nano),
	}, meta)
}

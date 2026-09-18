package httpapi

import (
	"fmt"

	"jungle/internal/domain/money"
	"jungle/internal/domain/wagertx"
	"jungle/internal/domain/wallet"
)

type moneyDTO struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

func (d moneyDTO) toMoney() (money.Money, error) {
	currency, err := money.NewCurrency(d.Currency)
	if err != nil {
		return money.Money{}, err
	}
	return money.ParseNonNegative(d.Amount, currency)
}

func moneyOut(m money.Money) moneyDTO {
	return moneyDTO{Amount: m.DecimalString(), Currency: string(m.Currency())}
}

type openWalletRequest struct {
	PlayerID       string   `json:"playerId"`
	InitialBalance moneyDTO `json:"initialBalance"`
}

type walletResponse struct {
	ID       string   `json:"id"`
	PlayerID string   `json:"playerId"`
	Balance  moneyDTO `json:"balance"`
	Version  int64    `json:"version"`
}

func walletOut(w wallet.Wallet) walletResponse {
	return walletResponse{
		ID:       w.ID().String(),
		PlayerID: w.PlayerID().String(),
		Balance:  moneyOut(w.Balance()),
		Version:  w.Version(),
	}
}

type ledgerEntryResponse struct {
	ID            string   `json:"id"`
	WalletID      string   `json:"walletId"`
	TransactionID string   `json:"transactionId"`
	Direction     string   `json:"direction"`
	Money         moneyDTO `json:"money"`
	BalanceBefore moneyDTO `json:"balanceBefore"`
	BalanceAfter  moneyDTO `json:"balanceAfter"`
	CreatedAt     string   `json:"createdAt"`
}

type ledgerPageResponse struct {
	Entries    []ledgerEntryResponse `json:"entries"`
	NextCursor string                `json:"nextCursor,omitempty"`
}

type submitTransactionRequest struct {
	ProviderID                     string   `json:"providerId"`
	ExternalTransactionID          string   `json:"externalTransactionId"`
	PlayerID                       string   `json:"playerId"`
	WalletID                       string   `json:"walletId"`
	RoundID                        string   `json:"roundId"`
	GameID                         string   `json:"gameId"`
	Kind                           string   `json:"kind"`
	Money                          moneyDTO `json:"money"`
	ReferenceExternalTransactionID string   `json:"referenceExternalTransactionId,omitempty"`
}

type submitTransactionResponse struct {
	TransactionID    string    `json:"transactionId"`
	Status           string    `json:"status"`
	Balance          *moneyDTO `json:"balance,omitempty"`
	FailureCode      string    `json:"failureCode,omitempty"`
	IdempotentReplay bool      `json:"idempotentReplay"`
}

func submitOut(tx wagertx.WagerTransaction, balance money.Money, replay bool) submitTransactionResponse {
	resp := submitTransactionResponse{
		TransactionID:    tx.ID().String(),
		Status:           string(tx.Status()),
		FailureCode:      string(tx.FailureCode()),
		IdempotentReplay: replay,
	}

	if tx.Status() == wagertx.Processed {
		out := moneyOut(balance)
		resp.Balance = &out
	}
	return resp
}

type transactionResponse struct {
	TransactionID                  string    `json:"transactionId"`
	ProviderID                     string    `json:"providerId,omitempty"`
	ExternalTransactionID          string    `json:"externalTransactionId,omitempty"`
	WalletID                       string    `json:"walletId"`
	PlayerID                       string    `json:"playerId"`
	RoundID                        string    `json:"roundId,omitempty"`
	GameID                         string    `json:"gameId,omitempty"`
	Kind                           string    `json:"kind"`
	Status                         string    `json:"status"`
	Money                          moneyDTO  `json:"money"`
	ReferenceExternalTransactionID string    `json:"referenceExternalTransactionId,omitempty"`
	FailureCode                    string    `json:"failureCode,omitempty"`
	Balance                        *moneyDTO `json:"balance,omitempty"`
	CreatedAt                      string    `json:"createdAt"`
	UpdatedAt                      string    `json:"updatedAt"`
}

func transactionOut(tx wagertx.WagerTransaction) transactionResponse {
	resp := transactionResponse{
		TransactionID:                  tx.ID().String(),
		ProviderID:                     tx.ProviderID(),
		ExternalTransactionID:          tx.ExternalTransactionID(),
		WalletID:                       tx.WalletID().String(),
		PlayerID:                       tx.PlayerID().String(),
		RoundID:                        tx.RoundID(),
		GameID:                         tx.GameID(),
		Kind:                           string(tx.Kind()),
		Status:                         string(tx.Status()),
		Money:                          moneyOut(tx.Money()),
		ReferenceExternalTransactionID: tx.ReferenceExternalTransactionID(),
		FailureCode:                    string(tx.FailureCode()),
		CreatedAt:                      rfc3339(tx.CreatedAt()),
		UpdatedAt:                      rfc3339(tx.UpdatedAt()),
	}
	if balance, ok := tx.ResultingBalance(); ok {
		out := moneyOut(balance)
		resp.Balance = &out
	}
	return resp
}

type reconciliationResponse struct {
	WalletID          string   `json:"walletId"`
	StoredBalance     moneyDTO `json:"storedBalance"`
	CalculatedBalance moneyDTO `json:"calculatedBalance"`
	Difference        moneyDTO `json:"difference"`
	Consistent        bool     `json:"consistent"`
	CheckedEntries    int      `json:"checkedEntries"`
}

func parseKind(raw string) (wagertx.Kind, error) {
	kind := wagertx.Kind(raw)
	if !kind.IsValid() {
		return "", fmt.Errorf("%w: %q", wagertx.ErrInvalidKind, raw)
	}
	return kind, nil
}

package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

type PayloadFields struct {
	ProviderID                     string
	ExternalTransactionID          string
	PlayerID                       string
	WalletID                       string
	RoundID                        string
	GameID                         string
	Kind                           string
	MoneyAmount                    string
	MoneyCurrency                  string
	ReferenceExternalTransactionID string
}

func PayloadHash(f PayloadFields) (string, error) {
	canonical := map[string]string{
		"providerId":                     f.ProviderID,
		"externalTransactionId":          f.ExternalTransactionID,
		"playerId":                       f.PlayerID,
		"walletId":                       f.WalletID,
		"roundId":                        f.RoundID,
		"gameId":                         f.GameID,
		"kind":                           f.Kind,
		"money.amount":                   f.MoneyAmount,
		"money.currency":                 f.MoneyCurrency,
		"referenceExternalTransactionId": f.ReferenceExternalTransactionID,
	}

	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("canonicalizing payload: %w", err)
	}

	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

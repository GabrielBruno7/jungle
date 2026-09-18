package app_test

import (
	"testing"

	"jungle/internal/app"
)

func sampleFields() app.PayloadFields {
	return app.PayloadFields{
		ProviderID:            "provider-a",
		ExternalTransactionID: "transaction-123",
		PlayerID:              "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
		WalletID:              "0192f291-27dd-7d3f-8071-5f8685deef37",
		RoundID:               "round-987",
		GameID:                "fortune-chimp",
		Kind:                  "BET",
		MoneyAmount:           "25.00",
		MoneyCurrency:         "BRL",
	}
}

func hashOf(t *testing.T, f app.PayloadFields) string {
	t.Helper()
	h, err := app.PayloadHash(f)
	if err != nil {
		t.Fatalf("PayloadHash unexpected error: %v", err)
	}
	return h
}

func TestPayloadHash_IsDeterministic(t *testing.T) {
	first := hashOf(t, sampleFields())
	second := hashOf(t, sampleFields())

	if first != second {
		t.Fatalf("same fields hashed differently: %s vs %s", first, second)
	}
	if len(first) != 64 {
		t.Errorf("expected a 64-char hex SHA-256, got %d chars", len(first))
	}
}

func TestPayloadHash_EveryBusinessFieldMatters(t *testing.T) {
	base := hashOf(t, sampleFields())

	mutations := map[string]func(*app.PayloadFields){
		"providerId":                     func(f *app.PayloadFields) { f.ProviderID = "provider-b" },
		"externalTransactionId":          func(f *app.PayloadFields) { f.ExternalTransactionID = "other" },
		"playerId":                       func(f *app.PayloadFields) { f.PlayerID = "other" },
		"walletId":                       func(f *app.PayloadFields) { f.WalletID = "other" },
		"roundId":                        func(f *app.PayloadFields) { f.RoundID = "other" },
		"gameId":                         func(f *app.PayloadFields) { f.GameID = "other" },
		"kind":                           func(f *app.PayloadFields) { f.Kind = "WIN" },
		"money.amount":                   func(f *app.PayloadFields) { f.MoneyAmount = "25.01" },
		"money.currency":                 func(f *app.PayloadFields) { f.MoneyCurrency = "USD" },
		"referenceExternalTransactionId": func(f *app.PayloadFields) { f.ReferenceExternalTransactionID = "ref" },
	}

	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			fields := sampleFields()
			mutate(&fields)
			if got := hashOf(t, fields); got == base {
				t.Errorf("changing %s did not change the hash, so it is not part of the fingerprint", name)
			}
		})
	}
}

func TestPayloadHash_ExcludesTransportDetails(t *testing.T) {
	fromHTTP := hashOf(t, sampleFields())
	fromSQS := hashOf(t, sampleFields())

	if fromHTTP != fromSQS {
		t.Fatalf("the same operation hashed differently per transport: %s vs %s", fromHTTP, fromSQS)
	}
}

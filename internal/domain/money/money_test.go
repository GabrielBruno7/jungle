package money_test

import (
	"encoding/json"
	"errors"
	"math"
	"testing"

	"jungle/internal/domain/money"
)

func mustParse(t *testing.T, s string, cur money.Currency) money.Money {
	t.Helper()
	m, err := money.Parse(s, cur)
	if err != nil {
		t.Fatalf("Parse(%q) unexpected error: %v", s, err)
	}
	return m
}

func TestParse_Valid(t *testing.T) {
	cases := []struct {
		input      string
		wantMinor  int64
		wantString string
	}{
		{"25.00", 2500, "25.00"},
		{"0.00", 0, "0.00"},
		{"-0.00", 0, "0.00"},
		{"1000.00", 100000, "1000.00"},
		{"0.01", 1, "0.01"},
		{"-5.00", -500, "-5.00"},
		{"007.50", 750, "7.50"},
	}

	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			m := mustParse(t, tc.input, money.BRL)
			if m.MinorUnits() != tc.wantMinor {
				t.Errorf("MinorUnits() = %d, want %d", m.MinorUnits(), tc.wantMinor)
			}
			if got := m.DecimalString(); got != tc.wantString {
				t.Errorf("DecimalString() = %q, want %q", got, tc.wantString)
			}
			if m.Currency() != money.BRL {
				t.Errorf("Currency() = %q, want %q", m.Currency(), money.BRL)
			}
		})
	}
}

func TestParse_InvalidFormat(t *testing.T) {
	cases := []string{
		"",
		"NaN",
		"Infinity",
		"-Infinity",
		"1e10",
		"1E10",
		"25",
		"25.0",
		"25.000",
		"25,00",
		"+25.00",
		"25.00 ",
		" 25.00",
		"twenty-five",
		"25.0a",
		"1,000.00",
		".00",
	}

	for _, input := range cases {
		t.Run(input, func(t *testing.T) {
			_, err := money.Parse(input, money.BRL)
			if err == nil {
				t.Fatalf("Parse(%q) = nil error, want error", input)
			}
			if input == "" {
				if !errors.Is(err, money.ErrEmptyAmount) {
					t.Errorf("Parse(%q) error = %v, want ErrEmptyAmount", input, err)
				}
				return
			}
			if !errors.Is(err, money.ErrInvalidFormat) {
				t.Errorf("Parse(%q) error = %v, want ErrInvalidFormat", input, err)
			}
		})
	}
}

func TestParse_Overflow(t *testing.T) {
	huge := "999999999999999999999999.00"

	_, err := money.Parse(huge, money.BRL)
	if !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("Parse(huge) error = %v, want ErrOverflow", err)
	}
}

func TestParseNonNegative(t *testing.T) {
	if _, err := money.ParseNonNegative("-5.00", money.BRL); !errors.Is(err, money.ErrNegativeAmount) {
		t.Fatalf("ParseNonNegative(-5.00) error = %v, want ErrNegativeAmount", err)
	}

	m, err := money.ParseNonNegative("25.00", money.BRL)
	if err != nil {
		t.Fatalf("ParseNonNegative(25.00) unexpected error: %v", err)
	}
	if m.MinorUnits() != 2500 {
		t.Fatalf("MinorUnits() = %d, want 2500", m.MinorUnits())
	}

	if _, err := money.ParseNonNegative("-0.00", money.BRL); err != nil {
		t.Fatalf("ParseNonNegative(-0.00) unexpected error: %v", err)
	}
}

func TestNewCurrency(t *testing.T) {
	if _, err := money.NewCurrency("BRL"); err != nil {
		t.Fatalf("NewCurrency(BRL) unexpected error: %v", err)
	}

	invalid := []string{"", "brl", "BR", "BRLL", "12L", "R$"}
	for _, code := range invalid {
		if _, err := money.NewCurrency(code); !errors.Is(err, money.ErrInvalidCurrency) {
			t.Errorf("NewCurrency(%q) error = %v, want ErrInvalidCurrency", code, err)
		}
	}
}

func TestArithmetic(t *testing.T) {
	a := mustParse(t, "100.00", money.BRL)
	b := mustParse(t, "80.00", money.BRL)

	sum, err := a.Add(b)
	if err != nil {
		t.Fatalf("Add unexpected error: %v", err)
	}
	if sum.DecimalString() != "180.00" {
		t.Errorf("Add = %s, want 180.00", sum.DecimalString())
	}

	diff, err := a.Sub(b)
	if err != nil {
		t.Fatalf("Sub unexpected error: %v", err)
	}
	if diff.DecimalString() != "20.00" {
		t.Errorf("Sub = %s, want 20.00", diff.DecimalString())
	}

	negDiff, err := b.Sub(a)
	if err != nil {
		t.Fatalf("Sub (negative) unexpected error: %v", err)
	}
	if !negDiff.IsNegative() || negDiff.DecimalString() != "-20.00" {
		t.Errorf("Sub (negative) = %s, want -20.00 and IsNegative()", negDiff.DecimalString())
	}

	negated, err := diff.Negate()
	if err != nil {
		t.Fatalf("Negate unexpected error: %v", err)
	}
	if negated.DecimalString() != "-20.00" {
		t.Errorf("Negate = %s, want -20.00", negated.DecimalString())
	}
}

func TestArithmetic_Overflow(t *testing.T) {
	max := money.FromMinorUnits(math.MaxInt64, money.BRL)
	one := mustParse(t, "0.01", money.BRL)

	if _, err := max.Add(one); !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("Add overflow error = %v, want ErrOverflow", err)
	}

	min := money.FromMinorUnits(math.MinInt64, money.BRL)
	if _, err := min.Negate(); !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("Negate(MinInt64) error = %v, want ErrOverflow", err)
	}
	if _, err := min.Sub(one); !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("Sub overflow error = %v, want ErrOverflow", err)
	}
}

func TestCurrencyMismatch(t *testing.T) {
	usd, err := money.NewCurrency("USD")
	if err != nil {
		t.Fatalf("NewCurrency(USD) unexpected error: %v", err)
	}

	brlAmount := mustParse(t, "10.00", money.BRL)
	usdAmount := mustParse(t, "10.00", usd)

	if _, err := brlAmount.Add(usdAmount); !errors.Is(err, money.ErrCurrencyMismatch) {
		t.Errorf("Add cross-currency error = %v, want ErrCurrencyMismatch", err)
	}
	if _, err := brlAmount.Sub(usdAmount); !errors.Is(err, money.ErrCurrencyMismatch) {
		t.Errorf("Sub cross-currency error = %v, want ErrCurrencyMismatch", err)
	}
	if _, err := brlAmount.Compare(usdAmount); !errors.Is(err, money.ErrCurrencyMismatch) {
		t.Errorf("Compare cross-currency error = %v, want ErrCurrencyMismatch", err)
	}
	if brlAmount.Equal(usdAmount) {
		t.Errorf("Equal cross-currency = true, want false")
	}
}

func TestCompareAndEqual(t *testing.T) {
	a := mustParse(t, "10.00", money.BRL)
	b := mustParse(t, "20.00", money.BRL)
	c := mustParse(t, "10.00", money.BRL)

	if cmp, err := a.Compare(b); err != nil || cmp != -1 {
		t.Errorf("a.Compare(b) = %d, %v, want -1, nil", cmp, err)
	}
	if cmp, err := b.Compare(a); err != nil || cmp != 1 {
		t.Errorf("b.Compare(a) = %d, %v, want 1, nil", cmp, err)
	}
	if cmp, err := a.Compare(c); err != nil || cmp != 0 {
		t.Errorf("a.Compare(c) = %d, %v, want 0, nil", cmp, err)
	}
	if !a.Equal(c) {
		t.Errorf("a.Equal(c) = false, want true")
	}
	if a.Equal(b) {
		t.Errorf("a.Equal(b) = true, want false")
	}
}

func TestZeroAndPredicates(t *testing.T) {
	zero := money.Zero(money.BRL)
	if !zero.IsZero() {
		t.Errorf("Zero().IsZero() = false, want true")
	}
	if zero.IsNegative() || zero.IsPositive() {
		t.Errorf("Zero() should be neither negative nor positive")
	}

	pos := mustParse(t, "1.00", money.BRL)
	if !pos.IsPositive() || pos.IsNegative() || pos.IsZero() {
		t.Errorf("1.00 should be positive only")
	}

	neg := mustParse(t, "-1.00", money.BRL)
	if !neg.IsNegative() || neg.IsPositive() || neg.IsZero() {
		t.Errorf("-1.00 should be negative only")
	}
}

func TestJSON_RoundTrip(t *testing.T) {
	original := mustParse(t, "25.00", money.BRL)

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal unexpected error: %v", err)
	}

	want := `{"amount":"25.00","currency":"BRL"}`
	if string(data) != want {
		t.Errorf("Marshal = %s, want %s", data, want)
	}

	var decoded money.Money
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal unexpected error: %v", err)
	}
	if !decoded.Equal(original) {
		t.Errorf("round-tripped value = %v, want %v", decoded, original)
	}
}

func TestJSON_Unmarshal_Invalid(t *testing.T) {
	cases := []string{
		`{"amount":"25.0","currency":"BRL"}`,
		`{"amount":"NaN","currency":"BRL"}`,
		`{"amount":"25.00","currency":"brl"}`,
		`{"amount":"1e10","currency":"BRL"}`,
	}

	for _, input := range cases {
		t.Run(input, func(t *testing.T) {
			var m money.Money
			if err := json.Unmarshal([]byte(input), &m); err == nil {
				t.Errorf("Unmarshal(%s) = nil error, want error", input)
			}
		})
	}
}

func TestFromMinorUnits_Rehydration(t *testing.T) {
	m := money.FromMinorUnits(2500, money.BRL)
	if m.DecimalString() != "25.00" {
		t.Errorf("DecimalString() = %s, want 25.00", m.DecimalString())
	}
	if m.MinorUnits() != 2500 {
		t.Errorf("MinorUnits() = %d, want 2500", m.MinorUnits())
	}
}

func TestDecimalString_ExtremeValues(t *testing.T) {
	cases := []struct {
		minorUnits int64
		want       string
	}{
		{math.MaxInt64, "92233720368547758.07"},
		{math.MinInt64, "-92233720368547758.08"},
		{-1, "-0.01"},
		{1, "0.01"},
		{0, "0.00"},
		{-100, "-1.00"},
	}

	for _, tc := range cases {
		got := money.FromMinorUnits(tc.minorUnits, money.BRL).DecimalString()
		if got != tc.want {
			t.Errorf("FromMinorUnits(%d).DecimalString() = %q, want %q", tc.minorUnits, got, tc.want)
		}
	}
}

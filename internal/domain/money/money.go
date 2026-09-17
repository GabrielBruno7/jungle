// Package money provides Money, an immutable value object for exact
// monetary amounts. It never uses float32/float64 at any point — amounts
// are stored as an integer count of minor units (e.g. cents for BRL) — and
// it has no dependency on Fx, HTTP, SQL, or any other infrastructure
// package: it is plain Go, safe to import from anywhere in the domain.
package money

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
)

var (
	ErrEmptyAmount      = errors.New("money: amount is empty")
	ErrInvalidFormat    = errors.New("money: invalid decimal format, expected exactly two decimal places (e.g. \"25.00\")")
	ErrOverflow         = errors.New("money: value overflows int64 minor units")
	ErrCurrencyMismatch = errors.New("money: currency mismatch")
	ErrNegativeAmount   = errors.New("money: negative amount is not allowed here")
	ErrInvalidCurrency  = errors.New("money: invalid currency code, expected a 3-letter ISO 4217 code (e.g. \"BRL\")")
)

// Currency is a validated ISO 4217-shaped currency code. This package only
// validates the format (three uppercase letters); it does not maintain the
// list of officially assigned codes.
type Currency string

// BRL is the Brazilian real, the only currency exercised by this
// challenge's primary scenarios.
const BRL Currency = "BRL"

var currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)

// NewCurrency validates code as a 3-letter uppercase ISO 4217-shaped code.
// Every Currency value used with this package should come from here (or
// from an already-validated constant like BRL) — a bare type conversion
// (Currency("xx")) bypasses validation and is a programming error, not a
// domain error this package can catch.
func NewCurrency(code string) (Currency, error) {
	if !currencyPattern.MatchString(code) {
		return "", fmt.Errorf("%w: %q", ErrInvalidCurrency, code)
	}
	return Currency(code), nil
}

const scaleFactor = 100

// decimalPattern matches an optionally-signed decimal string with exactly
// two digits after the point: "25.00", "-5.00", "0.00". Because it anchors
// the whole string and never matches letters, it rejects empty strings,
// missing/extra decimal digits, NaN, Infinity, and scientific notation
// ("1e10") all by construction, with no separate special-casing needed.
var decimalPattern = regexp.MustCompile(`^(-)?([0-9]+)\.([0-9]{2})$`)

// Money is an immutable value object representing an exact monetary amount
// in a given currency, stored as an integer count of minor units (cents for
// BRL) rather than a floating point number, so it never loses precision in
// parsing, arithmetic, or serialization. minorUnits is an int64, so it can
// represent amounts up to (and down to) roughly +/-92,233,720,368,547,758.07
// in whatever currency's minor unit; every operation below that could leave
// this range returns ErrOverflow instead of silently wrapping.
type Money struct {
	minorUnits int64
	currency   Currency
}

// Zero returns the additive identity for currency: an amount of 0.00.
func Zero(currency Currency) Money {
	return Money{minorUnits: 0, currency: currency}
}

// FromMinorUnits reconstructs a Money value from an already-validated minor
// unit count and currency — e.g. when rehydrating a row read back from
// Postgres. It performs no parsing and must never be used on externally
// supplied input; use Parse or ParseNonNegative for that.
func FromMinorUnits(minorUnits int64, currency Currency) Money {
	return Money{minorUnits: minorUnits, currency: currency}
}

// Parse converts a decimal string in the external contract's shape
// (e.g. "25.00", "-5.00") into a Money value in currency. It rejects empty
// strings and any format other than an optional leading '-' followed by
// digits, a '.', and exactly two decimal digits — which includes rejecting
// NaN, Infinity, and scientific notation. Negative amounts are accepted
// here, since they are legitimate for internal differences; use
// ParseNonNegative at the boundary of an external financial input, where
// the challenge requires rejecting negative amounts outright.
func Parse(s string, currency Currency) (Money, error) {
	if s == "" {
		return Money{}, ErrEmptyAmount
	}

	matches := decimalPattern.FindStringSubmatch(s)
	if matches == nil {
		return Money{}, fmt.Errorf("%w: %q", ErrInvalidFormat, s)
	}

	negative := matches[1] == "-"
	integerPart, fractionPart := matches[2], matches[3]

	whole, err := strconv.ParseInt(integerPart, 10, 64)
	if err != nil {
		return Money{}, fmt.Errorf("%w: %q", ErrOverflow, s)
	}

	frac, err := strconv.ParseInt(fractionPart, 10, 64)
	if err != nil {
		return Money{}, fmt.Errorf("%w: %q", ErrInvalidFormat, s)
	}

	wholeMinor, ok := mulInt64(whole, scaleFactor)
	if !ok {
		return Money{}, fmt.Errorf("%w: %q", ErrOverflow, s)
	}

	minorUnits, ok := addInt64(wholeMinor, frac)
	if !ok {
		return Money{}, fmt.Errorf("%w: %q", ErrOverflow, s)
	}

	if negative && minorUnits != 0 {
		negated, ok := negateInt64(minorUnits)
		if !ok {
			return Money{}, fmt.Errorf("%w: %q", ErrOverflow, s)
		}
		minorUnits = negated
	}

	return Money{minorUnits: minorUnits, currency: currency}, nil
}

// ParseNonNegative behaves like Parse but additionally rejects negative
// amounts. Use it for every external financial input field (bet/win
// amounts, initial wallet balance, etc.) — the challenge requires that
// these never silently accept a negative value.
func ParseNonNegative(s string, currency Currency) (Money, error) {
	m, err := Parse(s, currency)
	if err != nil {
		return Money{}, err
	}
	if m.IsNegative() {
		return Money{}, fmt.Errorf("%w: %q", ErrNegativeAmount, s)
	}
	return m, nil
}

// Currency returns m's currency.
func (m Money) Currency() Currency {
	return m.currency
}

// MinorUnits returns the exact integer minor-unit representation, e.g. for
// mapping to a BIGINT column.
func (m Money) MinorUnits() int64 {
	return m.minorUnits
}

// DecimalString renders m back into the external contract's fixed-scale
// decimal form, e.g. "25.00" or "-5.00".
func (m Money) DecimalString() string {
	minorUnits := m.minorUnits
	sign := ""
	if minorUnits < 0 {
		sign = "-"
		minorUnits = -minorUnits
	}
	return fmt.Sprintf("%s%d.%02d", sign, minorUnits/scaleFactor, minorUnits%scaleFactor)
}

// String implements fmt.Stringer.
func (m Money) String() string {
	return fmt.Sprintf("%s %s", m.DecimalString(), m.currency)
}

// IsZero reports whether m is exactly 0.00.
func (m Money) IsZero() bool { return m.minorUnits == 0 }

// IsNegative reports whether m is less than 0.00.
func (m Money) IsNegative() bool { return m.minorUnits < 0 }

// IsPositive reports whether m is greater than 0.00.
func (m Money) IsPositive() bool { return m.minorUnits > 0 }

func (m Money) sameCurrency(other Money) error {
	if m.currency != other.currency {
		return fmt.Errorf("%w: %s vs %s", ErrCurrencyMismatch, m.currency, other.currency)
	}
	return nil
}

// Add returns m + other. Both must share a currency.
func (m Money) Add(other Money) (Money, error) {
	if err := m.sameCurrency(other); err != nil {
		return Money{}, err
	}
	sum, ok := addInt64(m.minorUnits, other.minorUnits)
	if !ok {
		return Money{}, ErrOverflow
	}
	return Money{minorUnits: sum, currency: m.currency}, nil
}

// Sub returns m - other. Both must share a currency. The result may be
// negative — that is a legitimate internal difference, not a wallet
// balance, which is validated by the Wallet aggregate instead.
func (m Money) Sub(other Money) (Money, error) {
	if err := m.sameCurrency(other); err != nil {
		return Money{}, err
	}
	diff, ok := subInt64(m.minorUnits, other.minorUnits)
	if !ok {
		return Money{}, ErrOverflow
	}
	return Money{minorUnits: diff, currency: m.currency}, nil
}

// Negate returns -m.
func (m Money) Negate() (Money, error) {
	negated, ok := negateInt64(m.minorUnits)
	if !ok {
		return Money{}, ErrOverflow
	}
	return Money{minorUnits: negated, currency: m.currency}, nil
}

// Compare returns -1, 0, or 1 as m is less than, equal to, or greater than
// other. Both must share a currency.
func (m Money) Compare(other Money) (int, error) {
	if err := m.sameCurrency(other); err != nil {
		return 0, err
	}
	switch {
	case m.minorUnits < other.minorUnits:
		return -1, nil
	case m.minorUnits > other.minorUnits:
		return 1, nil
	default:
		return 0, nil
	}
}

// Equal reports whether m and other represent the same amount and
// currency. Unlike Compare, it never errors: a currency mismatch simply
// means they are not equal.
func (m Money) Equal(other Money) bool {
	return m.minorUnits == other.minorUnits && m.currency == other.currency
}

type jsonMoney struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

// MarshalJSON renders m in the external contract's shape:
// {"amount":"25.00","currency":"BRL"}.
func (m Money) MarshalJSON() ([]byte, error) {
	return json.Marshal(jsonMoney{Amount: m.DecimalString(), Currency: string(m.currency)})
}

// UnmarshalJSON parses the external contract's shape. It accepts negative
// amounts (see Parse) — callers that must reject them for a specific field
// (e.g. a bet amount) should check IsNegative() explicitly, or parse the
// raw amount/currency strings with ParseNonNegative instead of unmarshaling
// straight into Money.
func (m *Money) UnmarshalJSON(data []byte) error {
	var j jsonMoney
	if err := json.Unmarshal(data, &j); err != nil {
		return err
	}
	currency, err := NewCurrency(j.Currency)
	if err != nil {
		return err
	}
	parsed, err := Parse(j.Amount, currency)
	if err != nil {
		return err
	}
	*m = parsed
	return nil
}

// --- overflow-checked int64 arithmetic ---

func addInt64(a, b int64) (int64, bool) {
	sum := a + b
	if (b > 0 && sum < a) || (b < 0 && sum > a) {
		return 0, false
	}
	return sum, true
}

func subInt64(a, b int64) (int64, bool) {
	negB, ok := negateInt64(b)
	if !ok {
		return 0, false
	}
	return addInt64(a, negB)
}

func negateInt64(a int64) (int64, bool) {
	if a == math.MinInt64 {
		return 0, false
	}
	return -a, true
}

func mulInt64(a, b int64) (int64, bool) {
	if a == 0 || b == 0 {
		return 0, true
	}
	if (a == math.MinInt64 && b == -1) || (b == math.MinInt64 && a == -1) {
		return 0, false
	}
	result := a * b
	if result/b != a {
		return 0, false
	}
	return result, true
}

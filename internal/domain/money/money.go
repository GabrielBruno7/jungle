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

type Currency string

const BRL Currency = "BRL"

var currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)

func NewCurrency(code string) (Currency, error) {
	if !currencyPattern.MatchString(code) {
		return "", fmt.Errorf("%w: %q", ErrInvalidCurrency, code)
	}
	return Currency(code), nil
}

const scaleFactor = 100

var decimalPattern = regexp.MustCompile(`^(-)?([0-9]+)\.([0-9]{2})$`)

type Money struct {
	minorUnits int64
	currency   Currency
}

func Zero(currency Currency) Money {
	return Money{minorUnits: 0, currency: currency}
}

func FromMinorUnits(minorUnits int64, currency Currency) Money {
	return Money{minorUnits: minorUnits, currency: currency}
}

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

func (m Money) Currency() Currency {
	return m.currency
}

func (m Money) MinorUnits() int64 {
	return m.minorUnits
}

func (m Money) DecimalString() string {
	sign := ""
	magnitude := uint64(m.minorUnits)
	if m.minorUnits < 0 {
		sign = "-"
		magnitude = uint64(-(m.minorUnits + 1)) + 1
	}
	return fmt.Sprintf("%s%d.%02d", sign, magnitude/scaleFactor, magnitude%scaleFactor)
}

func (m Money) String() string {
	return fmt.Sprintf("%s %s", m.DecimalString(), m.currency)
}

func (m Money) IsZero() bool { return m.minorUnits == 0 }

func (m Money) IsNegative() bool { return m.minorUnits < 0 }

func (m Money) IsPositive() bool { return m.minorUnits > 0 }

func (m Money) sameCurrency(other Money) error {
	if m.currency != other.currency {
		return fmt.Errorf("%w: %s vs %s", ErrCurrencyMismatch, m.currency, other.currency)
	}
	return nil
}

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

func (m Money) Negate() (Money, error) {
	negated, ok := negateInt64(m.minorUnits)
	if !ok {
		return Money{}, ErrOverflow
	}
	return Money{minorUnits: negated, currency: m.currency}, nil
}

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

func (m Money) Equal(other Money) bool {
	return m.minorUnits == other.minorUnits && m.currency == other.currency
}

type jsonMoney struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

func (m Money) MarshalJSON() ([]byte, error) {
	return json.Marshal(jsonMoney{Amount: m.DecimalString(), Currency: string(m.currency)})
}

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

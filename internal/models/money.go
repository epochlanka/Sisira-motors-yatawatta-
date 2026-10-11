package models

import (
	"errors"
	"strconv"
	"strings"
	"sync"
)

// Money is an amount in integer minor currency units (e.g. cents).
type Money int64

// Qty is a quantity stored in thousandths (1.5 hours = 1500).
type Qty int64

const QtyScale = 1000

// Limits used by input validation to keep arithmetic far from int64 overflow.
const (
	MaxQty   Qty   = 1_000_000 * QtyScale
	MaxMoney Money = 100_000_000_000 // 1e11 minor units
)

// LineTotal returns quantity * unit price, rounded half away from zero to a whole minor unit.
func LineTotal(q Qty, price Money) Money {
	v := int64(q) * int64(price)
	if v >= 0 {
		return Money((v + QtyScale/2) / QtyScale)
	}
	return Money((v - QtyScale/2) / QtyScale)
}

// PercentBP applies a rate in basis points (1800 = 18%) with half-up rounding.
func PercentBP(m Money, bp int64) Money {
	v := int64(m) * bp
	if v >= 0 {
		return Money((v + 5000) / 10000)
	}
	return Money((v - 5000) / 10000)
}

// CurrencyFormat controls how money is displayed. It is updated from business settings.
type CurrencyFormat struct {
	Code        string
	Symbol      string
	Decimals    int
	SymbolAfter bool
}

var (
	curMu  sync.RWMutex
	curFmt = CurrencyFormat{Code: "XXX", Symbol: "", Decimals: 2}
)

func SetCurrency(c CurrencyFormat) {
	if c.Decimals < 0 || c.Decimals > 3 {
		c.Decimals = 2
	}
	curMu.Lock()
	curFmt = c
	curMu.Unlock()
}

func Currency() CurrencyFormat {
	curMu.RLock()
	defer curMu.RUnlock()
	return curFmt
}

func pow10(n int) int64 {
	p := int64(1)
	for i := 0; i < n; i++ {
		p *= 10
	}
	return p
}

func groupThousands(s string) string {
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
	}
	for i := pre; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

// Plain formats without a currency symbol, with thousands separators: 1,234.50
func (m Money) Plain() string {
	c := Currency()
	neg := m < 0
	v := int64(m)
	if neg {
		v = -v
	}
	p := pow10(c.Decimals)
	out := groupThousands(strconv.FormatInt(v/p, 10))
	if c.Decimals > 0 {
		frac := strconv.FormatInt(v%p, 10)
		for len(frac) < c.Decimals {
			frac = "0" + frac
		}
		out += "." + frac
	}
	if neg {
		out = "-" + out
	}
	return out
}

// String formats with the currency symbol.
func (m Money) String() string {
	c := Currency()
	if c.Symbol == "" {
		return m.Plain()
	}
	if c.SymbolAfter {
		return m.Plain() + " " + c.Symbol
	}
	return c.Symbol + " " + m.Plain()
}

// Input formats for an <input> value: no separators, e.g. 1234.50
func (m Money) Input() string {
	return strings.ReplaceAll(m.Plain(), ",", "")
}

var ErrBadNumber = errors.New("enter a valid number")

// parseScaled parses a non-negative decimal string into an integer scaled by 10^decimals.
func parseScaled(s string, decimals int) (int64, error) {
	s = strings.TrimSpace(strings.ReplaceAll(s, ",", ""))
	if s == "" {
		return 0, ErrBadNumber
	}
	neg := false
	if strings.HasPrefix(s, "-") {
		neg = true
		s = s[1:]
	}
	whole, frac, hasDot := strings.Cut(s, ".")
	if whole == "" && (!hasDot || frac == "") {
		return 0, ErrBadNumber
	}
	if whole == "" {
		whole = "0"
	}
	for _, r := range whole + frac {
		if r < '0' || r > '9' {
			return 0, ErrBadNumber
		}
	}
	if len(frac) > decimals {
		return 0, errors.New("too many decimal places (max " + strconv.Itoa(decimals) + ")")
	}
	if len(whole) > 15 {
		return 0, errors.New("number is too large")
	}
	for len(frac) < decimals {
		frac += "0"
	}
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, ErrBadNumber
	}
	var f int64
	if decimals > 0 {
		f, err = strconv.ParseInt(frac, 10, 64)
		if err != nil {
			return 0, ErrBadNumber
		}
	}
	v := w*pow10(decimals) + f
	if neg {
		v = -v
	}
	return v, nil
}

// ParseMoney parses a user-entered amount using the configured number of decimals.
func ParseMoney(s string) (Money, error) {
	v, err := parseScaled(s, Currency().Decimals)
	if err != nil {
		return 0, err
	}
	if Money(v) > MaxMoney || Money(v) < -MaxMoney {
		return 0, errors.New("amount is too large")
	}
	return Money(v), nil
}

// ParseQty parses a quantity with up to 3 decimal places.
func ParseQty(s string) (Qty, error) {
	v, err := parseScaled(s, 3)
	if err != nil {
		return 0, err
	}
	if Qty(v) > MaxQty || Qty(v) < -MaxQty {
		return 0, errors.New("quantity is too large")
	}
	return Qty(v), nil
}

// String formats a quantity trimming trailing zeros: 1500 -> "1.5".
func (q Qty) String() string {
	neg := q < 0
	v := int64(q)
	if neg {
		v = -v
	}
	s := strconv.FormatInt(v/QtyScale, 10)
	if frac := v % QtyScale; frac != 0 {
		fs := strconv.FormatInt(frac+QtyScale, 10)[1:]
		s += "." + strings.TrimRight(fs, "0")
	}
	if neg {
		s = "-" + s
	}
	return s
}

// Signed formats with an explicit + for positive values.
func (q Qty) Signed() string {
	if q > 0 {
		return "+" + q.String()
	}
	return q.String()
}

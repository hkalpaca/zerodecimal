package zerodecimal

import "math/bits"

// MulInt returns d × n at d's precision. It is identical to
// d.Mul(NewFromInt(n)) under every build tag, bit for bit, including
// ErrOverflow and the canonical zero of a zero product, but multiplies by the
// int64 directly: one 128×64-bit product whose high word is the overflow test.
// The product is exact whenever d.Prec() ≤ DefaultPrec, which always holds in
// the default build. Under a reduced-precision tag, a d with more than
// DefaultPrec places has its product truncated to DefaultPrec digits, exactly
// as Mul does. MinInt64 is a valid operand.
func (d Decimal) MulInt(n int64) (Decimal, error) {
	if DefaultPrec < MaxPrec && d.prec > DefaultPrec {
		// Reachable only under zerodecimal_prec9 or zerodecimal_prec12, where
		// Mul truncates the product to DefaultPrec digits.
		return d.Mul(NewFromInt(n))
	}
	neg := n < 0
	//nolint:gosec // deliberate two's-complement reinterpretation, negated just below
	mag := uint64(n)
	if neg {
		mag = -mag // exact for MinInt64: -(uint64(-2^63)) wraps back to 2^63
	}
	coef, overflow := mul128by64(d.coef, mag)
	if overflow != 0 {
		return Decimal{}, ErrOverflow
	}
	return newDecimal(coef, d.neg != neg, d.prec), nil
}

// DivInt returns d ÷ n with Div's legacy contract: the quotient truncated
// toward zero at the largest precision p ≤ DefaultPrec whose coefficient fits
// 128 bits. It is identical to d.Div(NewFromInt(n)) under every build tag, bit
// for bit, including canonical zero. Its only error is ErrDivideByZero for
// n == 0: an integer divisor never enlarges a magnitude, so the quotient
// always fits at some precision. MinInt64 is a valid operand.
//
// The divisor's magnitude fits one limb, so Div's common case, a quotient
// that fits at DefaultPrec, runs here without building a Decimal operand or
// dispatching on the divisor's width. The rare rest, a quotient past 2^128 at
// DefaultPrec (it needs |d| ≥ 2^128/10^DefaultPrec), takes Div's
// precision-lowering path.
func (d Decimal) DivInt(n int64) (Decimal, error) {
	if n == 0 {
		return Decimal{}, ErrDivideByZero
	}
	if DefaultPrec < MaxPrec && d.prec > DefaultPrec {
		// Reachable only under zerodecimal_prec9 or zerodecimal_prec12, where
		// Div scales the divisor instead of the dividend.
		return d.Div(NewFromInt(n))
	}
	neg := n < 0
	//nolint:gosec // deliberate two's-complement reinterpretation, negated just below
	mag := uint64(n)
	if neg {
		mag = -mag // exact for MinInt64: -(uint64(-2^63)) wraps back to 2^63
	}
	// Div's numerator at p = DefaultPrec: d.coef·10^f with f = DefaultPrec -
	// d.prec ≤ MaxPrec, so 10^f fits one limb and the product three.
	w2, w1, w0 := mul128by64to192(d.coef, pow10u64[(DefaultPrec-d.prec)&31])
	if w2 == 0 {
		// A numerator below 2^128 yields a quotient that fits. Each bits.Div64
		// gets a high word below mag, its no-trap precondition.
		var q u128
		if w1 < mag {
			q.lo, _ = bits.Div64(w1, w0, mag)
		} else {
			q.hi = w1 / mag
			q.lo, _ = bits.Div64(w1%mag, w0, mag)
		}
		return newDecimal(q, d.neg != neg, DefaultPrec), nil
	}
	// div256by64 fails exactly when the quotient reaches 2^128, where Div
	// starts lowering the precision.
	if q, _, err := div256by64(u256{d0: w0, d1: w1, d2: w2}, mag); err == nil {
		return newDecimal(q, d.neg != neg, DefaultPrec), nil
	}
	return d.Div(NewFromInt(n))
}

// MustMulInt is MulInt for operands with proven bounds: it panics on error.
func (d Decimal) MustMulInt(n int64) Decimal {
	r, err := d.MulInt(n)
	if err != nil {
		panic(err)
	}
	return r
}

// MustDivInt is DivInt for operands with proven bounds: it panics on error.
func (d Decimal) MustDivInt(n int64) Decimal {
	r, err := d.DivInt(n)
	if err != nil {
		panic(err)
	}
	return r
}

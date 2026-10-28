package zerodecimal

// WeightedAvg is a running weighted mean Σ(value·weight) / Σweight, such as a
// volume-weighted average price. Its zero value is empty and ready to use. It
// is not safe for concurrent use.
//
// The numerator and the total weight are two Accumulators, so Add and
// AddTotal never fail, the result does not depend on the order of the rows,
// and neither sum has to fit 128 bits until a finisher reads it. Total and
// Weight return the two sums, Avg divides them with Div's truncation, and
// AvgRound rounds the exact quotient once. Reading does not change the sums,
// so a stream may continue after a finisher. Weights may have either sign; a
// zero total weight, as in an empty WeightedAvg, makes Avg and AvgRound
// return ErrDivideByZero and leaves what an empty mean means to the caller.
type WeightedAvg struct {
	num, weight Accumulator
}

// Add adds a row: the numerator gains value×weight truncated toward zero at
// min(value.Prec()+weight.Prec(), DefaultPrec) places, the value
// value.Mul(weight) has (see Accumulator.AddMul), and the total weight gains
// weight.
func (w *WeightedAvg) Add(value, weight Decimal) {
	w.num.AddMul(value, weight)
	w.weight.Add(weight)
}

// AddTotal adds a row whose numerator term, value×weight, the caller has
// already computed, such as price.Mul(multiplier).Mul(qty): the numerator
// gains total and the total weight gains weight.
func (w *WeightedAvg) AddTotal(total, weight Decimal) {
	w.num.Add(total)
	w.weight.Add(weight)
}

// Avg returns Σ(value·weight) / Σweight with Div's legacy contract: the exact
// quotient truncated toward zero at the greatest precision ≤ DefaultPrec
// whose coefficient fits 128 bits. It equals Total() followed by Decimal.Div
// by Weight() whenever both sums fit 128 bits, and it still succeeds when only
// the numerator does not.
//
// ErrOverflow when the total weight does not fit 128 bits; otherwise
// ErrDivideByZero when it is zero, then ErrOverflow when the numerator
// outgrew its Accumulator or the quotient does not fit even at 0 places.
func (w *WeightedAvg) Avg() (Decimal, error) {
	den, err := w.weight.Sum()
	if err != nil {
		return Decimal{}, err
	}
	return w.num.Div(den)
}

// AvgRound returns Σ(value·weight) / Σweight rounded once to places
// fractional digits in mode, from the exact sums: the numerator is never
// truncated first, even past 128 bits. A nonzero result carries precision
// places; zero is canonical Decimal{}. It equals Total() followed by
// Decimal.DivRound by Weight() whenever both sums fit 128 bits.
//
// Validation follows DivRound: ErrPrecOutOfRange, then
// ErrInvalidRoundingMode, then the total weight as for Avg (ErrOverflow past
// 128 bits, ErrDivideByZero at zero), then ErrOverflow when the numerator
// outgrew its Accumulator or the rounded coefficient does not fit 128 bits.
//
// Rounding Avg's truncated result later agrees with AvgRound in two modes
// whenever Avg keeps more than places digits, that is, for places <
// DefaultPrec while the quotient's magnitude stays below 2^128/10^(places+1)
// (about 3.4e21 for 16 places): Avg().Round(places) equals AvgRound(places,
// ToNearestAway), and Avg().Truncate(places) equals AvgRound(places,
// TowardZero). Truncation is monotone, and the points these modes round at,
// the multiples of 10^-places and the half-way points (10k+5)·10^-(places+1),
// lie on Avg's finer grid, so truncating to that grid cannot carry a quotient
// across one. At the default DefaultPrec of 19, Avg().Round(16) is therefore
// AvgRound(16, ToNearestAway) below that bound. The other modes can differ by
// one unit in the last place: ToNearestEven when the exact quotient's
// magnitude exceeds a tie by less than one unit in Avg's last place, and
// AwayFromZero, TowardPositive and TowardNegative when Avg's digits past
// places are all zero but the exact quotient's are not.
func (w *WeightedAvg) AvgRound(places uint8, mode RoundingMode) (Decimal, error) {
	// Validate before reading the weight, so a weight past 128 bits cannot
	// pre-empt DivRound's argument order.
	if places > MaxPrec {
		return Decimal{}, ErrPrecOutOfRange
	}
	if !mode.valid() {
		return Decimal{}, ErrInvalidRoundingMode
	}
	den, err := w.weight.Sum()
	if err != nil {
		return Decimal{}, err
	}
	return w.num.DivRound(den, places, mode)
}

// Total returns the numerator Σ(value·weight) with Accumulator.Sum's
// contract: the exact sum at the greatest precision added, or ErrOverflow
// when it does not fit 128 bits or a same-sign total outgrew the
// Accumulator's width.
func (w *WeightedAvg) Total() (Decimal, error) {
	return w.num.Sum()
}

// Weight returns the total weight Σweight with Accumulator.Sum's contract.
func (w *WeightedAvg) Weight() (Decimal, error) {
	return w.weight.Sum()
}

// Reset empties both sums.
func (w *WeightedAvg) Reset() {
	*w = WeightedAvg{}
}

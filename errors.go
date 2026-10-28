package zerodecimal

import "errors"

// underflowError makes ErrUnderflow a specialized ErrInexact condition
// without wrapping on the operation's hot error path. The package-created
// value is allocated once, so returning it remains allocation-free. Like all
// exported Go error variables, ErrUnderflow and ErrInexact must not be
// reassigned by callers.
type underflowError struct{}

func (underflowError) Error() string {
	return "zerodecimal: nonzero exact result is below minimum precision"
}

func (underflowError) Is(target error) bool {
	return target == ErrInexact
}

// Sentinel errors for every fallible operation in the package. They are
// returned bare (never wrapped) on hot paths so that error returns stay
// allocation-free; match with errors.Is.
var (
	// ErrOverflow is returned when the exact result of an operation does not
	// fit a 128-bit coefficient at the result precision. The finishers of an
	// Accumulator or WeightedAvg also return it once a same-sign running total
	// has outgrown the accumulator's 256-bit width.
	ErrOverflow = errors.New("zerodecimal: value overflows 128-bit coefficient")

	// ErrDivideByZero is returned when a divisor is zero: by Div, DivExact,
	// DivRound, MulDivRound, DivInt (for n == 0), QuoRem, and Mod; by
	// NewDivisor, and by DivBy, DivRoundBy, and MulDivRoundBy given a zero or
	// nil Divisor; by RoundToMultiple for a zero step; by Accumulator.Div,
	// Accumulator.DivRound, WeightedAvg.Avg, and WeightedAvg.AvgRound for a zero
	// denominator or total weight; by Allocate when there are no weights or they
	// sum to zero; and by Split when n ≤ 0. The Must twins of those that have
	// one panic with it instead.
	ErrDivideByZero = errors.New("zerodecimal: division by zero")

	// ErrInexact is returned by exact arithmetic when the mathematical result
	// cannot be represented without discarding nonzero fractional digits, and
	// by Allocate and Split when total has nonzero digits beyond places.
	ErrInexact = errors.New("zerodecimal: exact result is not representable")

	// ErrUnderflow is returned by exact arithmetic when a nonzero mathematical
	// result has magnitude below 10^-MaxPrec. It is a specialized ErrInexact:
	// errors.Is(ErrUnderflow, ErrInexact) is true.
	ErrUnderflow error = underflowError{}

	// ErrInvalidRoundingMode is returned when a RoundingMode value is not one
	// of the six modes defined by this package.
	ErrInvalidRoundingMode = errors.New("zerodecimal: invalid rounding mode")

	// ErrPrecOutOfRange is returned when a requested or parsed precision
	// exceeds MaxPrec fractional digits.
	ErrPrecOutOfRange = errors.New("zerodecimal: precision out of range")

	// ErrNegativeWeight is returned by Allocate when a weight is negative.
	ErrNegativeWeight = errors.New("zerodecimal: negative allocation weight")

	// ErrInvalidFormat is returned when parsing input that is not a valid decimal.
	ErrInvalidFormat = errors.New("zerodecimal: invalid format")

	// ErrEmptyString is returned when parsing empty input.
	ErrEmptyString = errors.New("zerodecimal: empty input")

	// ErrMaxStrLen is returned when parsing input longer than 200 bytes.
	ErrMaxStrLen = errors.New("zerodecimal: input exceeds 200 bytes")

	// ErrJSONNull is returned when unmarshaling the JSON null literal into a
	// Decimal. Use NullDecimal or StrictNullDecimal when null is valid.
	ErrJSONNull = errors.New("zerodecimal: cannot unmarshal JSON null into Decimal")

	// ErrNilReceiver is returned by every fallible pointer-receiver decoder
	// and Scanner when called on nil. It takes precedence over input validation
	// and conversion errors, and is always returned as a bare sentinel.
	ErrNilReceiver = errors.New("zerodecimal: nil receiver")

	// ErrInvalidFloat is returned by float constructors for NaN or infinities,
	// and for a PostgreSQL NUMERIC NaN or ±Infinity by ParsePgNumeric and, for
	// their text forms, by the pgxdecimal codec.
	ErrInvalidFloat = errors.New("zerodecimal: NaN or Infinity")

	// ErrIntPartOverflow is returned when the integer part does not fit int64.
	ErrIntPartOverflow = errors.New("zerodecimal: integer part overflows int64")

	// ErrInvalidBinaryData is returned by UnmarshalBinary and ParsePgNumeric
	// for malformed input.
	ErrInvalidBinaryData = errors.New("zerodecimal: invalid binary data")

	// ErrScanNil is returned when scanning SQL NULL into a Decimal; use
	// NullDecimal or StrictNullDecimal for nullable columns.
	ErrScanNil = errors.New("zerodecimal: cannot scan nil into Decimal")

	// ErrScanType is returned when scanning an unsupported source type.
	ErrScanType = errors.New("zerodecimal: unsupported Scan source type")

	// ErrScanFloat is returned by StrictSQLDecimal.Scan and
	// StrictNullDecimal.Scan for float32 and float64 sources. It is deliberately
	// distinct from ErrInexact: rejecting floating-point provenance is a
	// boundary policy even when the value (for example, 0.5) is exact.
	ErrScanFloat = errors.New("zerodecimal: floating-point Scan source rejected by strict SQL policy")
)

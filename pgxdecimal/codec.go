// Package pgxdecimal registers zerodecimal's types with pgx v5, so that
// PostgreSQL NUMERIC values travel between the database and Decimal,
// NullDecimal, StrictSQLDecimal and StrictNullDecimal without going through
// math/big, pgtype.Numeric or an intermediate string.
//
// Call Register once on each connection's type map before the connection is
// used: after pgx.Connect, or in pgxpool.Config.AfterConnect.
//
// # Wire formats
//
// The codec prefers PostgreSQL's binary format, so pgx's extended protocol
// sends parameters and receives result columns in it, and CopyFrom always
// uses it. Binary values decode with [zerodecimal.ParsePgNumeric] and encode
// with [zerodecimal.Decimal.AppendPgNumeric], whose display scale (dscale) is
// the Decimal's Prec. The text format, which pgx uses for
// QueryExecModeSimpleProtocol and QueryExecModeExec, decodes with
// [zerodecimal.ParseBytes] and encodes with [zerodecimal.Decimal.AppendFixed]
// at Prec places, the same digits the binary encoder sends, so 1.50 stays
// 1.50 in an unconstrained numeric column whichever protocol writes it.
//
// Scans return the canonical value, as parsing does: trailing fractional
// zeros are dropped (1.50 scans as 1.5, Prec 1) and the column's dscale is
// not kept. Binary and text scans of the same value are == for every text
// rendering within ParseBytes' 200-byte input limit. Past it they differ:
// numeric_out spells the display scale out in full, so a value of a column
// whose scale is near 200 or more, such as numeric(1000,500), fails a text
// scan with ErrMaxStrLen, while a binary scan still decodes it, or fails
// with the range error its value calls for.
//
// # NULL
//
// SQL NULL scans into NullDecimal and StrictNullDecimal as Valid = false,
// and into Decimal and StrictSQLDecimal fails with ErrScanNil, leaving the
// destination unchanged. Scanning into a **T target sets it to nil instead,
// as pgx does for every pointer target. An invalid NullDecimal or
// StrictNullDecimal, or a nil pointer of any of the four types, encodes as
// NULL.
//
// # Errors
//
// Scan errors are zerodecimal's sentinels, returned bare, never wrapped, so
// no scan path allocates, failures included; pgx adds the column context
// around them (rows.Scan returns a pgx.ScanArgError), and errors.Is matches
// through it. Encoding cannot fail.
//
// NaN, Infinity and -Infinity fail with ErrInvalidFloat in both formats:
// ParsePgNumeric returns it for the binary encodings, and the codec maps
// numeric_out's renderings of them, which ParseBytes rejects as
// ErrInvalidFormat, to the same sentinel. Other failures are ParsePgNumeric's
// or ParseBytes' own sentinels (ErrInvalidBinaryData, ErrInvalidFormat,
// ErrEmptyString, ErrPrecOutOfRange, ErrOverflow, ErrMaxStrLen). A failed
// scan leaves a Decimal or StrictSQLDecimal unchanged and clears a
// NullDecimal or StrictNullDecimal, as the types' Scan methods do. A typed
// nil destination returns ErrNilReceiver.
//
// # Scope
//
// The codec handles only the four zerodecimal types and their pointers;
// numeric[] values reach it element by element through pgx's array codec. It
// delegates every other Go type to the numeric codec registered before it
// (pgtype.NumericCodec unless something else was registered), so
// pgtype.Numeric, float64 and the other targets behave as before, and
// rows.Values still returns pgtype.Numeric. Columns of other types, such as
// float8 or int8, never reach the codec: pgx scans them into the four types
// through their Scan methods, so StrictSQLDecimal and StrictNullDecimal
// still reject a float8 column with ErrScanFloat.
package pgxdecimal

import (
	"database/sql/driver"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	zerodecimal "github.com/AlexandrosKyriakakis/zerodecimal"
)

// Register maps zerodecimal.Decimal, NullDecimal, StrictSQLDecimal and
// StrictNullDecimal (value and pointer forms) to PostgreSQL numeric in m. It
// prefers the binary format and is idempotent. Call it once per connection
// type map (e.g. after pgx.Connect, or in pgxpool.Config.AfterConnect).
//
// Register wraps the codec m has registered for numeric at the time of the
// call and leaves every other Go type to it, so it composes with numeric
// registrations made before it; a numeric codec registered after it
// replaces it. A second call on the same map does nothing.
//
// Where pgx knows no OID, it looks the Go type up instead: the simple
// protocol and QueryExecModeExec encode arguments for OID 0, and a column of
// an unregistered type has no codec. For those, Register maps the four types
// and their pointers to numeric, and their slice and pgtype.Array forms to
// numeric[], as pgx maps its own types.
func Register(m *pgtype.Map) {
	name, next := "numeric", pgtype.Codec(pgtype.NumericCodec{})
	if t, ok := m.TypeForOID(pgtype.NumericOID); ok {
		if _, done := t.Codec.(*codec); done {
			return
		}
		name, next = t.Name, t.Codec
	}
	arrayName := "_numeric"
	if t, ok := m.TypeForOID(pgtype.NumericArrayOID); ok {
		arrayName = t.Name
	}
	m.RegisterType(&pgtype.Type{Name: name, OID: pgtype.NumericOID, Codec: &codec{next: next}})
	registerVariants[zerodecimal.Decimal](m, name, arrayName)
	registerVariants[zerodecimal.NullDecimal](m, name, arrayName)
	registerVariants[zerodecimal.StrictSQLDecimal](m, name, arrayName)
	registerVariants[zerodecimal.StrictNullDecimal](m, name, arrayName)
}

// registerVariants maps T and *T to name, and the slice and array forms of
// either to arrayName: the variants pgx registers for each of its own types.
func registerVariants[T any](m *pgtype.Map, name, arrayName string) {
	var (
		value    T
		slice    []T
		ptrs     []*T
		arr      pgtype.Array[T]
		arrPtrs  pgtype.Array[*T]
		flat     pgtype.FlatArray[T]
		flatPtrs pgtype.FlatArray[*T]
	)
	m.RegisterDefaultPgType(value, name)
	m.RegisterDefaultPgType(&value, name)
	for _, v := range []any{slice, &slice, ptrs, &ptrs, arr, &arr, arrPtrs, &arrPtrs, flat, &flat, flatPtrs, &flatPtrs} {
		m.RegisterDefaultPgType(v, arrayName)
	}
}

// codec plans the four zerodecimal types itself and delegates everything
// else to next, the numeric codec it replaced.
type codec struct {
	next pgtype.Codec
}

func (*codec) FormatSupported(format int16) bool {
	return format == pgtype.TextFormatCode || format == pgtype.BinaryFormatCode
}

func (*codec) PreferredFormat() int16 {
	return pgtype.BinaryFormatCode
}

func (c *codec) PlanEncode(m *pgtype.Map, oid uint32, format int16, value any) pgtype.EncodePlan {
	switch value.(type) {
	case zerodecimal.Decimal, *zerodecimal.Decimal,
		zerodecimal.StrictSQLDecimal, *zerodecimal.StrictSQLDecimal,
		zerodecimal.NullDecimal, *zerodecimal.NullDecimal,
		zerodecimal.StrictNullDecimal, *zerodecimal.StrictNullDecimal:
		switch format {
		case pgtype.BinaryFormatCode:
			return encodePlan{encode: zerodecimal.Decimal.AppendPgNumeric}
		case pgtype.TextFormatCode:
			return encodePlan{encode: appendText}
		}
		return nil
	}
	return c.next.PlanEncode(m, oid, format, value)
}

func (c *codec) PlanScan(m *pgtype.Map, oid uint32, format int16, target any) pgtype.ScanPlan {
	switch target.(type) {
	case *zerodecimal.Decimal, *zerodecimal.StrictSQLDecimal,
		*zerodecimal.NullDecimal, *zerodecimal.StrictNullDecimal:
		switch format {
		case pgtype.BinaryFormatCode:
			return scanPlan{decode: zerodecimal.ParsePgNumeric}
		case pgtype.TextFormatCode:
			return scanPlan{decode: parseText}
		}
		return nil
	}
	return c.next.PlanScan(m, oid, format, target)
}

func (c *codec) DecodeDatabaseSQLValue(m *pgtype.Map, oid uint32, format int16, src []byte) (driver.Value, error) {
	return c.next.DecodeDatabaseSQLValue(m, oid, format, src)
}

func (c *codec) DecodeValue(m *pgtype.Map, oid uint32, format int16, src []byte) (any, error) {
	return c.next.DecodeValue(m, oid, format, src)
}

// appendText writes d with exactly Prec fractional digits, trailing zeros
// included, which numeric_in reads back at the dscale AppendPgNumeric sends.
func appendText(d zerodecimal.Decimal, b []byte) []byte {
	return d.AppendFixed(b, d.Prec())
}

// parseText decodes numeric_out's rendering. ParseBytes rejects NaN,
// Infinity and -Infinity with ErrInvalidFormat; they become ErrInvalidFloat,
// the sentinel ParsePgNumeric returns for their binary encodings, so the
// special values fail alike on both protocols. Only these exact renderings
// are mapped: numeric_out spells them no other way.
func parseText(src []byte) (zerodecimal.Decimal, error) {
	d, err := zerodecimal.ParseBytes(src)
	if err != nil {
		switch string(src) {
		case textNaN, textInfinity, textNegInfinity:
			return zerodecimal.Decimal{}, zerodecimal.ErrInvalidFloat
		}
	}
	return d, err
}

// numeric_out's renderings of the special values.
const (
	textNaN         = "NaN"
	textInfinity    = "Infinity"
	textNegInfinity = "-Infinity"
)

// encodePlan encodes a value of any of the eight planned forms with encode.
type encodePlan struct {
	encode func(zerodecimal.Decimal, []byte) []byte
}

func (p encodePlan) Encode(value any, buf []byte) ([]byte, error) {
	var d zerodecimal.Decimal
	switch v := value.(type) {
	case zerodecimal.Decimal:
		d = v
	case *zerodecimal.Decimal:
		if v == nil {
			return nil, nil
		}
		d = *v
	case zerodecimal.StrictSQLDecimal:
		d = v.Decimal
	case *zerodecimal.StrictSQLDecimal:
		if v == nil {
			return nil, nil
		}
		d = v.Decimal
	case zerodecimal.NullDecimal:
		if !v.Valid {
			return nil, nil
		}
		d = v.Decimal
	case *zerodecimal.NullDecimal:
		if v == nil || !v.Valid {
			return nil, nil
		}
		d = v.Decimal
	case zerodecimal.StrictNullDecimal:
		if !v.Valid {
			return nil, nil
		}
		d = v.Decimal
	case *zerodecimal.StrictNullDecimal:
		if v == nil || !v.Valid {
			return nil, nil
		}
		d = v.Decimal
	default:
		return nil, fmt.Errorf("pgxdecimal: cannot encode %T", value)
	}
	return p.encode(d, buf), nil
}

// scanPlan scans into any of the four planned destination types with decode,
// following each type's Scan contract for NULL and for errors.
type scanPlan struct {
	decode func([]byte) (zerodecimal.Decimal, error)
}

func (p scanPlan) Scan(src []byte, dst any) error {
	switch dst := dst.(type) {
	case *zerodecimal.Decimal:
		if dst == nil {
			return zerodecimal.ErrNilReceiver
		}
		if src == nil {
			return zerodecimal.ErrScanNil
		}
		d, err := p.decode(src)
		if err != nil {
			return err
		}
		*dst = d
	case *zerodecimal.StrictSQLDecimal:
		if dst == nil {
			return zerodecimal.ErrNilReceiver
		}
		if src == nil {
			return zerodecimal.ErrScanNil
		}
		d, err := p.decode(src)
		if err != nil {
			return err
		}
		*dst = zerodecimal.NewStrictSQLDecimal(d)
	case *zerodecimal.NullDecimal:
		if dst == nil {
			return zerodecimal.ErrNilReceiver
		}
		if src == nil {
			*dst = zerodecimal.NullDecimal{}
			return nil
		}
		d, err := p.decode(src)
		if err != nil {
			*dst = zerodecimal.NullDecimal{}
			return err
		}
		*dst = zerodecimal.NewNullDecimal(d)
	case *zerodecimal.StrictNullDecimal:
		if dst == nil {
			return zerodecimal.ErrNilReceiver
		}
		if src == nil {
			*dst = zerodecimal.StrictNullDecimal{}
			return nil
		}
		d, err := p.decode(src)
		if err != nil {
			*dst = zerodecimal.StrictNullDecimal{}
			return err
		}
		*dst = zerodecimal.NewStrictNullDecimal(d)
	default:
		return fmt.Errorf("pgxdecimal: cannot scan into %T", dst)
	}
	return nil
}

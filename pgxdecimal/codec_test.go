package pgxdecimal

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	zd "github.com/AlexandrosKyriakakis/zerodecimal"
)

// formats are the two wire formats the codec plans for.
var formats = []struct {
	name string
	code int16
}{
	{"binary", pgtype.BinaryFormatCode},
	{"text", pgtype.TextFormatCode},
}

// kind describes one of the four zerodecimal types Register maps, so every
// test runs over all of them alike.
type kind struct {
	name     string
	nullable bool
	value    func(zd.Decimal) any // the valid value form, as an Encode argument
	pointer  func(zd.Decimal) any // the valid pointer form
	nulls    []any                // forms that must encode as SQL NULL
	newDst   func() any           // a fresh *T scan destination
	ptrDst   func() any           // a fresh **T scan destination
	nilDst   any                  // a typed nil *T
	read     func(dst any) (zd.Decimal, bool)
	readPtr  func(dst any) (zd.Decimal, bool, bool) // value, Valid, *T != nil
	preset   func(dst any, d zd.Decimal)            // stores a valid d in *T
}

var kinds = []kind{
	{
		name:    "Decimal",
		value:   func(d zd.Decimal) any { return d },
		pointer: func(d zd.Decimal) any { return &d },
		nulls:   []any{(*zd.Decimal)(nil)},
		newDst:  func() any { return new(zd.Decimal) },
		ptrDst:  func() any { return new(*zd.Decimal) },
		nilDst:  (*zd.Decimal)(nil),
		read:    func(dst any) (zd.Decimal, bool) { return *dst.(*zd.Decimal), true },
		readPtr: func(dst any) (zd.Decimal, bool, bool) {
			p := *dst.(**zd.Decimal)
			if p == nil {
				return zd.Decimal{}, false, false
			}
			return *p, true, true
		},
		preset: func(dst any, d zd.Decimal) { *dst.(*zd.Decimal) = d },
	},
	{
		name:    "StrictSQLDecimal",
		value:   func(d zd.Decimal) any { return zd.NewStrictSQLDecimal(d) },
		pointer: func(d zd.Decimal) any { s := zd.NewStrictSQLDecimal(d); return &s },
		nulls:   []any{(*zd.StrictSQLDecimal)(nil)},
		newDst:  func() any { return new(zd.StrictSQLDecimal) },
		ptrDst:  func() any { return new(*zd.StrictSQLDecimal) },
		nilDst:  (*zd.StrictSQLDecimal)(nil),
		read:    func(dst any) (zd.Decimal, bool) { return dst.(*zd.StrictSQLDecimal).Decimal, true },
		readPtr: func(dst any) (zd.Decimal, bool, bool) {
			p := *dst.(**zd.StrictSQLDecimal)
			if p == nil {
				return zd.Decimal{}, false, false
			}
			return p.Decimal, true, true
		},
		preset: func(dst any, d zd.Decimal) { *dst.(*zd.StrictSQLDecimal) = zd.NewStrictSQLDecimal(d) },
	},
	{
		name:     "NullDecimal",
		nullable: true,
		value:    func(d zd.Decimal) any { return zd.NewNullDecimal(d) },
		pointer:  func(d zd.Decimal) any { n := zd.NewNullDecimal(d); return &n },
		nulls: []any{
			zd.NullDecimal{},
			&zd.NullDecimal{},
			(*zd.NullDecimal)(nil),
			zd.NullDecimal{Decimal: zd.NewFromInt(7)}, // Valid alone decides
		},
		newDst: func() any { return new(zd.NullDecimal) },
		ptrDst: func() any { return new(*zd.NullDecimal) },
		nilDst: (*zd.NullDecimal)(nil),
		read: func(dst any) (zd.Decimal, bool) {
			n := *dst.(*zd.NullDecimal)
			return n.Decimal, n.Valid
		},
		readPtr: func(dst any) (zd.Decimal, bool, bool) {
			p := *dst.(**zd.NullDecimal)
			if p == nil {
				return zd.Decimal{}, false, false
			}
			return p.Decimal, p.Valid, true
		},
		preset: func(dst any, d zd.Decimal) { *dst.(*zd.NullDecimal) = zd.NewNullDecimal(d) },
	},
	{
		name:     "StrictNullDecimal",
		nullable: true,
		value:    func(d zd.Decimal) any { return zd.NewStrictNullDecimal(d) },
		pointer:  func(d zd.Decimal) any { n := zd.NewStrictNullDecimal(d); return &n },
		nulls: []any{
			zd.StrictNullDecimal{},
			&zd.StrictNullDecimal{},
			(*zd.StrictNullDecimal)(nil),
			zd.StrictNullDecimal{Decimal: zd.NewFromInt(7)},
		},
		newDst: func() any { return new(zd.StrictNullDecimal) },
		ptrDst: func() any { return new(*zd.StrictNullDecimal) },
		nilDst: (*zd.StrictNullDecimal)(nil),
		read: func(dst any) (zd.Decimal, bool) {
			n := *dst.(*zd.StrictNullDecimal)
			return n.Decimal, n.Valid
		},
		readPtr: func(dst any) (zd.Decimal, bool, bool) {
			p := *dst.(**zd.StrictNullDecimal)
			if p == nil {
				return zd.Decimal{}, false, false
			}
			return p.Decimal, p.Valid, true
		},
		preset: func(dst any, d zd.Decimal) { *dst.(*zd.StrictNullDecimal) = zd.NewStrictNullDecimal(d) },
	},
}

// stale is the value destinations hold before a scan that must not change
// them (or must clear them, for the nullable kinds).
var stale = zd.RequireFromString("-424242.4242")

func registeredMap() *pgtype.Map {
	m := pgtype.NewMap()
	Register(m)
	return m
}

func wireBytes(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.ReplaceAll(s, " ", ""))
	require.NoError(t, err, "bad hex %q", s)
	return b
}

// requireBare asserts that err is the sentinel want itself, not a wrapper:
// the codec returns the root package's sentinels unwrapped.
func requireBare(t *testing.T, want, err error) {
	t.Helper()
	require.ErrorIs(t, err, want)
	require.NoError(t, errors.Unwrap(err), "want the bare sentinel %v, got a wrapper", want)
}

// scanInto scans src through m's plan for kind k, starting from a
// destination preset to stale, and returns what the destination holds after.
func scanInto(t *testing.T, m *pgtype.Map, k kind, format int16, src []byte) (zd.Decimal, bool, error) {
	t.Helper()
	dst := k.newDst()
	k.preset(dst, stale)
	err := m.Scan(pgtype.NumericOID, format, src, dst)
	d, valid := k.read(dst)
	return d, valid, err
}

// requireScanFailed asserts the per-kind error contract: Decimal and
// StrictSQLDecimal keep their previous value, while the nullable kinds clear
// to NULL so a reused destination never holds a stale amount.
func requireScanFailed(t *testing.T, k kind, d zd.Decimal, valid bool) {
	t.Helper()
	if k.nullable {
		require.False(t, valid, "a failed scan clears %s", k.name)
		require.Equal(t, zd.Decimal{}, d, "a failed scan clears %s", k.name)
		return
	}
	require.Equal(t, stale, d, "a failed scan leaves %s unchanged", k.name)
}

// randDecimal draws a coefficient width uniformly from 1..128 bits, a
// precision from 0..MaxPrec and a sign, so every encoder and parser path
// (one- and two-limb coefficients, pure fractions, trailing zeros) is hit.
func randDecimal(rng *rand.Rand) zd.Decimal {
	width := 1 + rng.UintN(128)
	hi, lo := rng.Uint64(), rng.Uint64()
	if width <= 64 {
		hi = 0
		lo >>= 64 - width
	} else {
		hi >>= 128 - width
	}
	d, err := zd.NewFromHiLo(rng.UintN(2) == 0, hi, lo, uint8(rng.UintN(uint(zd.MaxPrec)+1)))
	if err != nil {
		panic(err)
	}
	return d
}

// roundTripValues are the vectors' representable values at the scale
// PostgreSQL sent them with (so trailing zeros are kept), plus random ones.
func roundTripValues(t *testing.T) []zd.Decimal {
	t.Helper()
	var values []zd.Decimal
	for _, v := range pgNumericPostgresVectors {
		d, err := zd.NewFromString(v.text)
		if err != nil {
			continue
		}
		dscale := binary.BigEndian.Uint16(wireBytes(t, v.hex)[6:])
		if dscale <= uint16(zd.MaxPrec) && !d.IsZero() {
			if r, err := d.Rescale(uint8(dscale)); err == nil {
				d = r
			}
		}
		values = append(values, d)
	}
	rng := rand.New(rand.NewPCG(0x7067786463, 0x726f756e64))
	for range 2000 {
		values = append(values, randDecimal(rng))
	}
	return values
}

// textScale returns the number of digits after the decimal point in s.
func textScale(s []byte) int {
	i := strings.IndexByte(string(s), '.')
	if i < 0 {
		return 0
	}
	return len(s) - i - 1
}

// TestRegisterPlansEveryForm pins that Register routes every value and
// pointer form through the codec's own plans in both formats, rather than
// through pgx's driver.Valuer and sql.Scanner fallbacks, which go through
// text and pgtype.Numeric.
func TestRegisterPlansEveryForm(t *testing.T) {
	m := registeredMap()
	d := zd.RequireFromString("-12.345")
	for _, f := range formats {
		for _, k := range kinds {
			for _, v := range []any{k.value(d), k.pointer(d)} {
				plan := m.PlanEncode(pgtype.NumericOID, f.code, v)
				require.IsType(t, encodePlan{}, plan, "%s encode of %T", f.name, v)
			}
			plan := m.PlanScan(pgtype.NumericOID, f.code, k.newDst())
			require.IsType(t, scanPlan{}, plan, "%s scan into %s", f.name, k.name)
		}
	}
}

// TestRoundTrip encodes every value and pointer form through the map and
// scans the bytes back into every kind, in both formats. Scanning returns the
// canonical value, d.Trim(): decoding does not keep the display scale. The
// encodings themselves keep it: binary is numeric_send's bytes at dscale =
// Prec(), and text spells out exactly Prec() fractional digits, so both
// protocols hand PostgreSQL the same digits and display scale.
func TestRoundTrip(t *testing.T) {
	t.Parallel()
	m := registeredMap()
	for _, d := range roundTripValues(t) {
		bin := d.AppendPgNumeric(nil)
		for _, f := range formats {
			for _, k := range kinds {
				for _, v := range []any{k.value(d), k.pointer(d)} {
					buf, err := m.Encode(pgtype.NumericOID, f.code, v, nil)
					require.NoError(t, err)
					if f.code == pgtype.BinaryFormatCode {
						require.Equal(t, bin, buf, "binary encoding of %T %v", v, d)
					} else {
						require.Equal(t, int(binary.BigEndian.Uint16(bin[6:])), textScale(buf),
							"text %q carries the binary dscale", buf)
						fromText, parseErr := zd.ParseBytes(buf)
						require.NoError(t, parseErr)
						require.Equal(t, d.Trim(), fromText, "text %q", buf)
					}

					got, valid, err := scanInto(t, m, k, f.code, buf)
					require.NoError(t, err, "%s scan of %x into %s", f.name, buf, k.name)
					require.True(t, valid)
					require.Equal(t, d.Trim(), got, "%s round trip of %v into %s", f.name, d, k.name)

					dst := k.ptrDst()
					require.NoError(t, m.Scan(pgtype.NumericOID, f.code, buf, dst))
					got, valid, set := k.readPtr(dst)
					require.True(t, set && valid, "a non-NULL value allocates the **%s target", k.name)
					require.Equal(t, d.Trim(), got)
				}
			}
		}
	}
}

// TestPostgresVectors feeds PostgreSQL's own renderings of each value — the
// numeric_send bytes and the numeric_out text — to the binary and text scan
// plans. Binary scans return exactly ParsePgNumeric's value or sentinel, and
// text scans exactly ParseBytes', except that NaN and ±Infinity fail with
// ErrInvalidFloat on both protocols. For these renderings, all within
// ParseBytes' 200-byte limit, the two protocols agree.
func TestPostgresVectors(t *testing.T) {
	m := registeredMap()
	for _, v := range pgNumericPostgresVectors {
		bin := wireBytes(t, v.hex)
		text := []byte(v.text)
		require.LessOrEqual(t, len(text), 200)

		want, wantErr := zd.ParsePgNumeric(bin)
		textWant, textErr := zd.ParseBytes(text)
		switch v.text {
		case "NaN", "Infinity", "-Infinity":
			require.ErrorIs(t, wantErr, zd.ErrInvalidFloat)
			require.ErrorIs(t, textErr, zd.ErrInvalidFormat, "the root parser rejects %q", v.text)
			textErr = zd.ErrInvalidFloat // the codec's mapping
		}
		if wantErr == nil {
			require.NoError(t, textErr, "%q", v.text)
			require.Equal(t, want, textWant, "%q", v.text)
		} else {
			require.ErrorIs(t, textErr, wantErr, "%q", v.text)
		}

		for _, k := range kinds {
			for _, c := range []struct {
				format int16
				src    []byte
			}{{pgtype.BinaryFormatCode, bin}, {pgtype.TextFormatCode, text}} {
				got, valid, err := scanInto(t, m, k, c.format, c.src)
				if wantErr != nil {
					requireBare(t, wantErr, err)
					requireScanFailed(t, k, got, valid)
					continue
				}
				require.NoError(t, err, "%q into %s", v.text, k.name)
				require.True(t, valid)
				require.Equal(t, want, got, "%q into %s", v.text, k.name)
			}
		}
	}
}

// TestPlanVectors replays plan 02's hand-checked table as raw binary src,
// including pgx's own ±Infinity encoding (dscale 0, where PostgreSQL sends
// 0x20) and a zero at dscale 2, which decodes to canonical zero.
func TestPlanVectors(t *testing.T) {
	m := registeredMap()
	tests := []struct {
		wire  string
		value string
		err   error
	}{
		{wire: "0003 0001 0000 0004 0001 0929 1A85", value: "12345.6789"},
		{wire: "0001 FFFF 0000 0004 0001", value: "0.0001"},
		{wire: "0002 0000 4000 0001 0001 1388", value: "-1.5"},
		{wire: "0001 0002 0000 0000 0001", value: "100000000"},
		{wire: "0001 FFFD 0000 0009 03E8", value: "0.000000001"},
		{wire: "0000 0000 0000 0000", value: "0"},
		{wire: "0000 0000 0000 0002", value: "0"},
		{wire: "0000 0000 C000 0000", err: zd.ErrInvalidFloat},
		{wire: "0000 0000 D000 0000", err: zd.ErrInvalidFloat},
		{wire: "0000 0000 F000 0000", err: zd.ErrInvalidFloat},
	}
	for _, tc := range tests {
		for _, k := range kinds {
			got, valid, err := scanInto(t, m, k, pgtype.BinaryFormatCode, wireBytes(t, tc.wire))
			if tc.err != nil {
				requireBare(t, tc.err, err)
				requireScanFailed(t, k, got, valid)
				continue
			}
			require.NoError(t, err)
			require.True(t, valid)
			require.Equal(t, zd.RequireFromString(tc.value), got, "%s into %s", tc.wire, k.name)
		}
	}
}

// TestSpecialValuesFailAlike pins the one behaviour for NaN and ±Infinity on
// both protocols: the bare ErrInvalidFloat sentinel, which ParsePgNumeric
// returns for the binary encodings and the codec maps numeric_out's text
// renderings to (ParseBytes alone reports ErrInvalidFormat for them). Only
// the exact renderings PostgreSQL sends are mapped; other spellings stay
// ErrInvalidFormat.
func TestSpecialValuesFailAlike(t *testing.T) {
	m := registeredMap()
	tests := []struct {
		name   string
		format int16
		src    []byte
		err    error
	}{
		{"binary NaN", pgtype.BinaryFormatCode, wireBytes(t, "0000 0000 C000 0000"), zd.ErrInvalidFloat},
		{"binary +Infinity, PostgreSQL 18 dscale 0x20", pgtype.BinaryFormatCode, wireBytes(t, "0000 0000 D000 0020"), zd.ErrInvalidFloat},
		{"binary -Infinity, PostgreSQL 18 dscale 0x20", pgtype.BinaryFormatCode, wireBytes(t, "0000 0000 F000 0020"), zd.ErrInvalidFloat},
		{"binary +Infinity, pgx dscale 0", pgtype.BinaryFormatCode, wireBytes(t, "0000 0000 D000 0000"), zd.ErrInvalidFloat},
		{"binary -Infinity, pgx dscale 0", pgtype.BinaryFormatCode, wireBytes(t, "0000 0000 F000 0000"), zd.ErrInvalidFloat},
		{"binary NaN with a bad digit", pgtype.BinaryFormatCode, wireBytes(t, "0001 0000 C000 0000 2710"), zd.ErrInvalidBinaryData},
		{"text NaN", pgtype.TextFormatCode, []byte("NaN"), zd.ErrInvalidFloat},
		{"text Infinity", pgtype.TextFormatCode, []byte("Infinity"), zd.ErrInvalidFloat},
		{"text -Infinity", pgtype.TextFormatCode, []byte("-Infinity"), zd.ErrInvalidFloat},
		{"text nan", pgtype.TextFormatCode, []byte("nan"), zd.ErrInvalidFormat},
		{"text +Infinity", pgtype.TextFormatCode, []byte("+Infinity"), zd.ErrInvalidFormat},
		{"text inf", pgtype.TextFormatCode, []byte("inf"), zd.ErrInvalidFormat},
		{"text NaN with a space", pgtype.TextFormatCode, []byte("NaN "), zd.ErrInvalidFormat},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, k := range kinds {
				got, valid, err := scanInto(t, m, k, tc.format, tc.src)
				requireBare(t, tc.err, err)
				requireScanFailed(t, k, got, valid)
			}
		})
	}
	for _, s := range []string{"NaN", "Infinity", "-Infinity"} {
		_, err := zd.ParseBytes([]byte(s))
		require.ErrorIs(t, err, zd.ErrInvalidFormat, "the mapping is the codec's, not the root parser's")
	}
}

// TestScanErrors checks that every decode failure surfaces as the root
// package's bare sentinel, matchable with errors.Is, on both protocols, and
// that the destination follows its kind's error contract.
func TestScanErrors(t *testing.T) {
	m := registeredMap()
	tests := []struct {
		name   string
		format int16
		src    []byte
		err    error
	}{
		{"binary empty", pgtype.BinaryFormatCode, []byte{}, zd.ErrInvalidBinaryData},
		{"binary short header", pgtype.BinaryFormatCode, wireBytes(t, "0001 0000 0000"), zd.ErrInvalidBinaryData},
		{"binary length mismatch", pgtype.BinaryFormatCode, wireBytes(t, "0002 0000 0000 0000 0001"), zd.ErrInvalidBinaryData},
		{"binary digit 10000", pgtype.BinaryFormatCode, wireBytes(t, "0001 0000 0000 0000 2710"), zd.ErrInvalidBinaryData},
		{"binary unknown sign", pgtype.BinaryFormatCode, wireBytes(t, "0001 0000 8000 0000 0001"), zd.ErrInvalidBinaryData},
		{"binary 1e-20", pgtype.BinaryFormatCode, wireBytes(t, "0001 FFFB 0000 0014 0001"), zd.ErrPrecOutOfRange},
		{"binary 2^128", pgtype.BinaryFormatCode, wireBytes(t, "000a00090000000001540b071a2403aa121a18c111ff10dd1aa505b0"), zd.ErrOverflow},
		{"text empty", pgtype.TextFormatCode, []byte{}, zd.ErrEmptyString},
		{"text garbage", pgtype.TextFormatCode, []byte("12,5"), zd.ErrInvalidFormat},
		{"text 1e-20", pgtype.TextFormatCode, []byte("0.00000000000000000001"), zd.ErrPrecOutOfRange},
		{"text 2^128", pgtype.TextFormatCode, []byte("340282366920938463463374607431768211456"), zd.ErrOverflow},
		{"text over 200 bytes", pgtype.TextFormatCode, []byte("1." + strings.Repeat("0", 199)), zd.ErrMaxStrLen},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, k := range kinds {
				got, valid, err := scanInto(t, m, k, tc.format, tc.src)
				requireBare(t, tc.err, err)
				requireScanFailed(t, k, got, valid)
			}
		})
	}
}

// TestTextOver200Bytes pins where the protocols disagree. numeric_out spells
// the display scale out in full, so a value of a wide-scale column renders
// past ParseBytes' 200-byte limit and its text scan fails with ErrMaxStrLen,
// while the binary scan still decodes it (or fails with the range sentinel
// its value deserves). The wire bytes are what PostgreSQL sends for these.
func TestTextOver200Bytes(t *testing.T) {
	m := registeredMap()
	tests := []struct {
		name    string
		binary  string
		text    string
		value   string // binary result; "" with binErr
		binErr  error
		textErr error
	}{
		{
			name:   "1.5::numeric(1000,500)",
			binary: "0002 0000 0000 01F4 0001 1388",
			text:   "1.5" + strings.Repeat("0", 499),
			value:  "1.5",
		},
		{
			name:   "0.1::numeric(400,199)",
			binary: "0001 FFFF 0000 00C7 03E8",
			text:   "0.1" + strings.Repeat("0", 198),
			value:  "0.1",
		},
		{
			name:   "0::numeric(1000,500)",
			binary: "0000 0000 0000 01F4",
			text:   "0." + strings.Repeat("0", 500),
			value:  "0",
		},
		{
			name:   "1e-300::numeric",
			binary: "0001 FFB5 0000 012C 0001",
			text:   "0." + strings.Repeat("0", 299) + "1",
			binErr: zd.ErrPrecOutOfRange,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Greater(t, len(tc.text), 200)
			for _, k := range kinds {
				got, valid, err := scanInto(t, m, k, pgtype.TextFormatCode, []byte(tc.text))
				requireBare(t, zd.ErrMaxStrLen, err)
				requireScanFailed(t, k, got, valid)

				got, valid, err = scanInto(t, m, k, pgtype.BinaryFormatCode, wireBytes(t, tc.binary))
				if tc.binErr != nil {
					requireBare(t, tc.binErr, err)
					requireScanFailed(t, k, got, valid)
					continue
				}
				require.NoError(t, err)
				require.True(t, valid)
				require.Equal(t, zd.RequireFromString(tc.value), got)
			}
		})
	}
}

// TestNullScan pins SQL NULL (src == nil) on both protocols: Decimal and
// StrictSQLDecimal fail with the bare ErrScanNil and keep their value, the
// nullable kinds become invalid zero values, and **T targets become nil.
func TestNullScan(t *testing.T) {
	m := registeredMap()
	for _, f := range formats {
		for _, k := range kinds {
			got, valid, err := scanInto(t, m, k, f.code, nil)
			if k.nullable {
				require.NoError(t, err)
				require.False(t, valid)
				require.Equal(t, zd.Decimal{}, got)
			} else {
				requireBare(t, zd.ErrScanNil, err)
				require.Equal(t, stale, got, "NULL leaves %s unchanged", k.name)
			}

			// A **T target set by an earlier row goes back to nil.
			dst := k.ptrDst()
			src, err := m.Encode(pgtype.NumericOID, f.code, k.value(stale), nil)
			require.NoError(t, err)
			require.NoError(t, m.Scan(pgtype.NumericOID, f.code, src, dst))
			_, _, set := k.readPtr(dst)
			require.True(t, set)
			require.NoError(t, m.Scan(pgtype.NumericOID, f.code, nil, dst))
			_, _, set = k.readPtr(dst)
			require.False(t, set, "NULL sets the **%s target to nil", k.name)
		}
	}
}

// TestNullEncode pins that invalid NullDecimal and StrictNullDecimal values,
// and nil pointers of every kind, encode as SQL NULL — (nil, nil), pgx's
// NULL — both through Map.Encode, which catches nil pointers itself, and
// through the codec's plan used directly. pgx memoizes encode plans by Go
// type, so the plan planned for a valid value also meets the NULL forms of
// that type.
func TestNullEncode(t *testing.T) {
	m := registeredMap()
	for _, f := range formats {
		for _, k := range kinds {
			for _, v := range k.nulls {
				buf, err := m.Encode(pgtype.NumericOID, f.code, v, []byte("prefix"))
				require.NoError(t, err)
				require.Nil(t, buf, "%s encode of %#v is NULL", f.name, v)

				plan := m.PlanEncode(pgtype.NumericOID, f.code, v)
				require.IsType(t, encodePlan{}, plan)
				buf, err = plan.Encode(v, []byte("prefix"))
				require.NoError(t, err)
				require.Nil(t, buf, "%s plan encode of %#v is NULL", f.name, v)
			}
		}
	}
}

// TestNilDestination pins the root package's nil-receiver contract: a typed
// nil destination returns the bare ErrNilReceiver, NULL or not, and never
// panics.
func TestNilDestination(t *testing.T) {
	m := registeredMap()
	srcs := map[int16][]byte{
		pgtype.BinaryFormatCode: zd.NewFromInt(1).AppendPgNumeric(nil),
		pgtype.TextFormatCode:   []byte("1"),
	}
	for _, f := range formats {
		for _, k := range kinds {
			for _, src := range [][]byte{srcs[f.code], nil} {
				err := m.Scan(pgtype.NumericOID, f.code, src, k.nilDst)
				requireBare(t, zd.ErrNilReceiver, err)
			}
		}
	}
}

// TestRegisterIdempotent pins that a second Register changes nothing: the
// numeric type and codec stay the ones the first call installed (the codec
// never wraps itself), no wrap functions are added, and the map encodes and
// scans exactly like a map registered once.
func TestRegisterIdempotent(t *testing.T) {
	once := registeredMap()
	twice := registeredMap()
	first, ok := twice.TypeForOID(pgtype.NumericOID)
	require.True(t, ok)
	Register(twice)
	second, ok := twice.TypeForOID(pgtype.NumericOID)
	require.True(t, ok)
	require.Same(t, first, second, "the second Register replaced the numeric type")
	c, ok := second.Codec.(*codec)
	require.True(t, ok)
	require.IsType(t, pgtype.NumericCodec{}, c.next, "the codec wraps pgx's, not itself")

	fresh := pgtype.NewMap()
	require.Len(t, twice.TryWrapEncodePlanFuncs, len(fresh.TryWrapEncodePlanFuncs))
	require.Len(t, twice.TryWrapScanPlanFuncs, len(fresh.TryWrapScanPlanFuncs))

	rng := rand.New(rand.NewPCG(1, 2))
	srcs := [][]byte{nil, wireBytes(t, "0000 0000 C000 0000"), []byte("NaN"), []byte("x")}
	for range 200 {
		d := randDecimal(rng)
		for _, f := range formats {
			for _, k := range kinds {
				a, errA := once.Encode(pgtype.NumericOID, f.code, k.value(d), nil)
				b, errB := twice.Encode(pgtype.NumericOID, f.code, k.value(d), nil)
				require.NoError(t, errA)
				require.NoError(t, errB)
				require.Equal(t, a, b)
				for _, src := range append(srcs, a) {
					gotA, validA, errA := scanInto(t, once, k, f.code, src)
					gotB, validB, errB := scanInto(t, twice, k, f.code, src)
					require.Equal(t, errA, errB)
					require.Equal(t, gotA, gotB)
					require.Equal(t, validA, validB)
				}
			}
		}
	}
}

// TestRegisterKeepsOtherTargets pins that Register changes only the four
// zerodecimal types. Every other Go type keeps pgx's numeric handling, which
// the codec delegates to the codec registered before it (pgtype.NumericCodec
// here): scans into pgtype.Numeric, float64, int64, string and any (what
// rows.Values returns), and encodes of those types, match a map without
// Register.
func TestRegisterKeepsOtherTargets(t *testing.T) {
	plain := pgtype.NewMap()
	m := registeredMap()
	targets := []func() any{
		func() any { return new(pgtype.Numeric) },
		func() any { return new(float64) },
		func() any { return new(int64) },
		func() any { return new(string) },
		func() any { return new(any) },
	}
	for _, v := range pgNumericPostgresVectors {
		srcs := map[int16][]byte{
			pgtype.BinaryFormatCode: wireBytes(t, v.hex),
			pgtype.TextFormatCode:   []byte(v.text),
		}
		for _, f := range formats {
			for i, newDst := range targets {
				if i == 1 && v.text == "NaN" {
					continue // NaN != NaN: the float64 target cannot be compared
				}
				a, b := newDst(), newDst()
				errA := plain.Scan(pgtype.NumericOID, f.code, srcs[f.code], a)
				errB := m.Scan(pgtype.NumericOID, f.code, srcs[f.code], b)
				require.Equal(t, errA, errB, "%s %q into %T", f.name, v.text, a)
				require.Equal(t, a, b, "%s %q into %T", f.name, v.text, a)
			}
		}
	}

	var row any
	require.NoError(t, m.Scan(pgtype.NumericOID, pgtype.BinaryFormatCode, wireBytes(t, "000200004000000100011388"), &row))
	require.IsType(t, pgtype.Numeric{}, row, "rows.Values still yields pgtype.Numeric")

	values := []any{
		pgtype.Numeric{Int: bigInt(t, "-15"), Exp: -1, Valid: true},
		pgtype.Numeric{NaN: true, Valid: true},
		12.5,
		int64(42),
		"3.25",
	}
	for _, f := range formats {
		for _, v := range values {
			a, errA := plain.Encode(pgtype.NumericOID, f.code, v, nil)
			b, errB := m.Encode(pgtype.NumericOID, f.code, v, nil)
			// pgx has no binary numeric plan for a Go string: both fail alike.
			require.Equal(t, errA, errB, "%s encode of %#v", f.name, v)
			require.Equal(t, a, b, "%s encode of %#v", f.name, v)
		}
	}
}

// otherAmount stands for another decimal library's type, handled by a codec
// registered for numeric before Register.
type otherAmount struct{ cents int64 }

type otherCodec struct{ pgtype.NumericCodec }

type otherScanPlan struct{}

func (otherScanPlan) Scan(_ []byte, dst any) error {
	dst.(*otherAmount).cents = 7
	return nil
}

type otherEncodePlan struct{}

func (otherEncodePlan) Encode(_ any, buf []byte) ([]byte, error) { return append(buf, "other"...), nil }

func (c otherCodec) PlanScan(m *pgtype.Map, oid uint32, format int16, target any) pgtype.ScanPlan {
	if _, ok := target.(*otherAmount); ok {
		return otherScanPlan{}
	}
	return c.NumericCodec.PlanScan(m, oid, format, target)
}

func (c otherCodec) PlanEncode(m *pgtype.Map, oid uint32, format int16, value any) pgtype.EncodePlan {
	if _, ok := value.(otherAmount); ok {
		return otherEncodePlan{}
	}
	return c.NumericCodec.PlanEncode(m, oid, format, value)
}

// TestRegisterWrapsPreviousCodec pins that Register composes with a numeric
// codec registered before it: types the codec does not own keep reaching
// the previous codec, the type name is kept, and binary stays preferred.
func TestRegisterWrapsPreviousCodec(t *testing.T) {
	m := pgtype.NewMap()
	m.RegisterType(&pgtype.Type{Name: "numeric", OID: pgtype.NumericOID, Codec: otherCodec{}})
	Register(m)

	typ, ok := m.TypeForOID(pgtype.NumericOID)
	require.True(t, ok)
	require.Equal(t, "numeric", typ.Name)
	require.IsType(t, &codec{}, typ.Codec)
	require.IsType(t, otherCodec{}, typ.Codec.(*codec).next)
	require.Equal(t, int16(pgtype.BinaryFormatCode), m.FormatCodeForOID(pgtype.NumericOID))
	require.True(t, typ.Codec.FormatSupported(pgtype.TextFormatCode))
	require.True(t, typ.Codec.FormatSupported(pgtype.BinaryFormatCode))
	require.False(t, typ.Codec.FormatSupported(2))

	for _, f := range formats {
		var amount otherAmount
		require.NoError(t, m.Scan(pgtype.NumericOID, f.code, []byte("1"), &amount))
		require.Equal(t, int64(7), amount.cents)
		buf, err := m.Encode(pgtype.NumericOID, f.code, otherAmount{}, nil)
		require.NoError(t, err)
		require.Equal(t, "other", string(buf))

		var d zd.Decimal
		require.IsType(t, scanPlan{}, m.PlanScan(pgtype.NumericOID, f.code, &d))
		require.IsType(t, encodePlan{}, m.PlanEncode(pgtype.NumericOID, f.code, d))
	}
}

// TestDefaultPgTypes pins the OID-agnostic contexts: Register maps all eight
// value and pointer forms to numeric with RegisterDefaultPgType, so pgx's
// simple protocol, which encodes arguments as text for OID 0, and scans of
// a column whose OID the map does not know both reach the codec.
func TestDefaultPgTypes(t *testing.T) {
	m := registeredMap()
	d := zd.RequireFromString("-1.5").MustRescale(2)
	const unknownOID = 999_999
	for _, k := range kinds {
		for _, v := range []any{k.value(d), k.pointer(d)} {
			typ, ok := m.TypeForValue(v)
			require.True(t, ok, "%T", v)
			require.Equal(t, uint32(pgtype.NumericOID), typ.OID)

			buf, err := m.Encode(0, pgtype.TextFormatCode, v, nil)
			require.NoError(t, err)
			require.Equal(t, "-1.50", string(buf), "simple-protocol argument %T", v)
		}

		dst := k.newDst()
		require.IsType(t, scanPlan{}, m.PlanScan(unknownOID, pgtype.TextFormatCode, dst))
		require.NoError(t, m.Scan(unknownOID, pgtype.TextFormatCode, []byte("-1.50"), dst))
		got, valid := k.read(dst)
		require.True(t, valid)
		require.Equal(t, d.Trim(), got)
	}
}

// arrayForms returns vals in each slice and pgtype array form pgx maps to an
// array type for its own element types: []T, []*T, pgtype.Array and
// pgtype.FlatArray of either, and pointers to all of them.
func arrayForms[T any](vals []T) []any {
	ptrs := make([]*T, len(vals))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	dims := []pgtype.ArrayDimension{{Length: int32(len(vals)), LowerBound: 1}}
	arr := pgtype.Array[T]{Elements: vals, Dims: dims, Valid: true}
	arrPtrs := pgtype.Array[*T]{Elements: ptrs, Dims: dims, Valid: true}
	flat := pgtype.FlatArray[T](vals)
	flatPtrs := pgtype.FlatArray[*T](ptrs)
	return []any{vals, &vals, ptrs, &ptrs, arr, &arr, arrPtrs, &arrPtrs, flat, &flat, flatPtrs, &flatPtrs}
}

// TestDefaultPgTypeArrays pins the array forms in OID-agnostic contexts. In
// the extended protocol the server names the parameter type, but
// QueryExecModeExec and the simple protocol encode for OID 0, where pgx
// finds numeric[] only through the Go type: Register maps the slice and
// pgtype array forms of the four types to numeric[], as pgx maps its own
// types.
func TestDefaultPgTypeArrays(t *testing.T) {
	m := registeredMap()
	d := zd.RequireFromString("-1.5").MustRescale(2)
	tests := []struct {
		forms []any
		text  string
	}{
		{arrayForms([]zd.Decimal{d, d}), "{-1.50,-1.50}"},
		{arrayForms([]zd.StrictSQLDecimal{zd.NewStrictSQLDecimal(d), zd.NewStrictSQLDecimal(d)}), "{-1.50,-1.50}"},
		{arrayForms([]zd.NullDecimal{zd.NewNullDecimal(d), {}}), "{-1.50,NULL}"},
		{arrayForms([]zd.StrictNullDecimal{zd.NewStrictNullDecimal(d), {}}), "{-1.50,NULL}"},
	}
	for _, tc := range tests {
		for _, v := range tc.forms {
			typ, ok := m.TypeForValue(v)
			require.True(t, ok, "%T", v)
			require.Equal(t, uint32(pgtype.NumericArrayOID), typ.OID, "%T", v)
			buf, err := m.Encode(0, pgtype.TextFormatCode, v, nil)
			require.NoError(t, err, "%T", v)
			require.Equal(t, tc.text, string(buf), "simple-protocol argument %T", v)
		}
	}

	const unknownOID = 999_999
	var got []zd.NullDecimal
	require.NoError(t, m.Scan(unknownOID, pgtype.TextFormatCode, []byte("{-1.50,NULL}"), &got))
	require.Equal(t, []zd.NullDecimal{zd.NewNullDecimal(d.Trim()), {}}, got)
}

// TestStrictPolicyOnOtherColumns pins that the strict types keep their source
// policy. A numeric column decodes to the same exact value for every kind,
// but columns of other types still reach each type's own sql.Scanner, as
// without Register: float8 fails StrictSQLDecimal and StrictNullDecimal with
// ErrScanFloat and is accepted by the legacy Decimal and NullDecimal, while
// int8 is exact for all four.
func TestStrictPolicyOnOtherColumns(t *testing.T) {
	m := registeredMap()
	f8, err := m.Encode(pgtype.Float8OID, pgtype.BinaryFormatCode, 0.5, nil)
	require.NoError(t, err)
	i8, err := m.Encode(pgtype.Int8OID, pgtype.BinaryFormatCode, int64(42), nil)
	require.NoError(t, err)
	for _, k := range kinds {
		dst := k.newDst()
		k.preset(dst, stale)
		err := m.Scan(pgtype.Float8OID, pgtype.BinaryFormatCode, f8, dst)
		got, valid := k.read(dst)
		if strings.HasPrefix(k.name, "Strict") {
			require.ErrorIs(t, err, zd.ErrScanFloat, k.name)
			requireScanFailed(t, k, got, valid)
		} else {
			require.NoError(t, err, k.name)
			require.True(t, valid)
			require.Equal(t, zd.RequireFromString("0.5"), got)
		}

		require.NoError(t, m.Scan(pgtype.Int8OID, pgtype.BinaryFormatCode, i8, dst), k.name)
		got, valid = k.read(dst)
		require.True(t, valid)
		require.Equal(t, zd.NewFromInt(42), got)
	}
}

// TestArrays pins that numeric[] goes through the codec element by element:
// pgx's ArrayCodec plans its elements on the map. The text encoding keeps an
// element's scale (1.50, where the Valuer fallback would send 1.5), a NaN
// element fails with ErrInvalidFloat (the fallback reports
// ErrInvalidFormat), and a NULL element follows the element type's rule.
func TestArrays(t *testing.T) {
	m := registeredMap()
	values := []zd.Decimal{
		zd.RequireFromString("1.5").MustRescale(2),
		zd.RequireFromString("-0.001"),
		{},
		zd.RequireFromString("34028236692093846346.3374607431768211455"),
	}
	trimmed := make([]zd.Decimal, len(values))
	for i, d := range values {
		trimmed[i] = d.Trim()
	}
	for _, f := range formats {
		buf, err := m.Encode(pgtype.NumericArrayOID, f.code, values, nil)
		require.NoError(t, err)
		if f.code == pgtype.TextFormatCode {
			require.Equal(t, "{1.50,-0.001,0,34028236692093846346.3374607431768211455}", string(buf))
		}
		var got []zd.Decimal
		require.NoError(t, m.Scan(pgtype.NumericArrayOID, f.code, buf, &got))
		require.Equal(t, trimmed, got)

		nulls := []zd.NullDecimal{zd.NewNullDecimal(trimmed[0]), {}}
		buf, err = m.Encode(pgtype.NumericArrayOID, f.code, nulls, nil)
		require.NoError(t, err)
		var gotNulls []zd.NullDecimal
		require.NoError(t, m.Scan(pgtype.NumericArrayOID, f.code, buf, &gotNulls))
		require.Equal(t, nulls, gotNulls)
		var notNull []zd.Decimal
		require.ErrorIs(t, m.Scan(pgtype.NumericArrayOID, f.code, buf, &notNull), zd.ErrScanNil)
	}

	nanArray := map[int16][]byte{
		pgtype.TextFormatCode: []byte("{1,NaN}"),
	}
	bin, err := pgtype.NewMap().Encode(pgtype.NumericArrayOID, pgtype.BinaryFormatCode,
		[]pgtype.Numeric{{Int: bigInt(t, "1"), Valid: true}, {NaN: true, Valid: true}}, nil)
	require.NoError(t, err)
	nanArray[pgtype.BinaryFormatCode] = bin
	for _, f := range formats {
		var got []zd.Decimal
		require.ErrorIs(t, m.Scan(pgtype.NumericArrayOID, f.code, nanArray[f.code], &got), zd.ErrInvalidFloat, f.name)
	}
}

func bigInt(t *testing.T, s string) *big.Int {
	t.Helper()
	n, ok := new(big.Int).SetString(s, 10)
	require.True(t, ok, "bad integer %q", s)
	return n
}

// randWire draws a binary NUMERIC input: mostly well-formed headers (any
// sign word PostgreSQL defines, weights and dscales around the representable
// range) over digits that are usually valid, sometimes with the length,
// ndigits or a digit broken, and sometimes plain random bytes.
func randWire(rng *rand.Rand) []byte {
	if rng.UintN(8) == 0 {
		b := make([]byte, rng.IntN(24))
		for i := range b {
			b[i] = byte(rng.UintN(256))
		}
		return b
	}
	n := rng.IntN(13)
	signs := []uint16{0, 0, 0x4000, 0x4000, 0xC000, 0xD000, 0xF000, 0x8000}
	b := binary.BigEndian.AppendUint16(nil, uint16(n))
	b = binary.BigEndian.AppendUint16(b, uint16(int16(rng.IntN(24)-12)))
	b = binary.BigEndian.AppendUint16(b, signs[rng.IntN(len(signs))])
	b = binary.BigEndian.AppendUint16(b, uint16(rng.IntN(40)))
	for range n {
		digit := uint16(rng.IntN(10000))
		if rng.UintN(40) == 0 {
			digit = 10000 + uint16(rng.IntN(1000))
		}
		b = binary.BigEndian.AppendUint16(b, digit)
	}
	if rng.UintN(20) == 0 && len(b) > 0 {
		b = b[:rng.IntN(len(b))]
	}
	return b
}

// randText draws a text input: numeric_out renderings of random values,
// the special values' renderings and near misses, and short strings over a
// decimal-ish alphabet.
func randText(rng *rand.Rand) []byte {
	switch rng.UintN(4) {
	case 0:
		words := []string{"NaN", "Infinity", "-Infinity", "nan", "+Infinity", "inf", "NaN0", "-NaN", ""}
		return []byte(words[rng.IntN(len(words))])
	case 1:
		const alphabet = "0123456789.-+eE "
		b := make([]byte, rng.IntN(30))
		for i := range b {
			b[i] = alphabet[rng.IntN(len(alphabet))]
		}
		return b
	default:
		d := randDecimal(rng)
		return []byte(d.StringFixed(d.Prec() + uint8(rng.UintN(4))))
	}
}

// TestScanMatchesRootParsers is a seeded differential check over arbitrary
// input, standing in for a fuzz target (a Fuzz function in this nested
// module would break the root's scripts/fuzz-all.sh). Every kind must
// return exactly ParsePgNumeric's result for binary input, and ParseBytes'
// for text input except the special values' renderings, which fail with
// ErrInvalidFloat; failures follow the kind's destination contract.
func TestScanMatchesRootParsers(t *testing.T) {
	t.Parallel()
	m := registeredMap()
	rng := rand.New(rand.NewPCG(0x66757a7a, 0x6469666620))
	outcomes := map[string]int{}
	for range 20_000 {
		for _, f := range formats {
			var src []byte
			var want zd.Decimal
			var wantErr error
			if f.code == pgtype.BinaryFormatCode {
				src = randWire(rng)
				want, wantErr = zd.ParsePgNumeric(src)
			} else {
				src = randText(rng)
				want, wantErr = zd.ParseBytes(src)
				switch string(src) {
				case "NaN", "Infinity", "-Infinity":
					wantErr = zd.ErrInvalidFloat
				}
			}
			outcomes[f.name+": "+fmt.Sprint(wantErr)]++
			for _, k := range kinds {
				got, valid, err := scanInto(t, m, k, f.code, src)
				if wantErr != nil {
					require.Equalf(t, wantErr, err, "%s %q into %s", f.name, src, k.name)
					requireScanFailed(t, k, got, valid)
					continue
				}
				require.NoErrorf(t, err, "%s %q into %s", f.name, src, k.name)
				require.True(t, valid)
				require.Equalf(t, want, got, "%s %q into %s", f.name, src, k.name)
			}
		}
	}
	// Every outcome must be reached, or the generators have gone stale.
	for _, want := range []struct {
		format string
		err    error
	}{
		{"binary", nil},
		{"binary", zd.ErrInvalidBinaryData},
		{"binary", zd.ErrInvalidFloat},
		{"binary", zd.ErrPrecOutOfRange},
		{"binary", zd.ErrOverflow},
		{"text", nil},
		{"text", zd.ErrInvalidFloat},
		{"text", zd.ErrInvalidFormat},
		{"text", zd.ErrEmptyString},
	} {
		key := want.format + ": " + fmt.Sprint(want.err)
		require.GreaterOrEqual(t, outcomes[key], 100, "too few inputs with outcome %q: %v", key, outcomes)
	}
}

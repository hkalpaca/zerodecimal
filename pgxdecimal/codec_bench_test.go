package pgxdecimal

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	zd "github.com/AlexandrosKyriakakis/zerodecimal"
)

// benchShape is one costbasis column shape, the five plan 02 benchmarked,
// in every form the benchmarks hand pgx: the Decimal at the column's scale
// and the same value as a pgtype.Numeric, each boxed once as pgx receives
// arguments, and PostgreSQL's binary (numeric_send) and text (numeric_out)
// renderings of it.
type benchShape struct {
	name    string
	decimal any
	numeric any
	src     map[int16][]byte
}

func newBenchShape(name, text string, scale uint8) benchShape {
	d := zd.RequireFromString(text).MustRescale(scale)
	var n pgtype.Numeric
	if err := n.Scan(text); err != nil {
		panic(err)
	}
	return benchShape{
		name:    name,
		decimal: d,
		numeric: n,
		src: map[int16][]byte{
			pgtype.BinaryFormatCode: d.AppendPgNumeric(nil),
			pgtype.TextFormatCode:   []byte(text),
		},
	}
}

// Package-level inputs and sinks, so nothing is optimized away and the
// inputs are boxed outside the timed loops.
var (
	benchShapes = []benchShape{
		newBenchShape("price_9dp", "12.345678900", 9),
		newBenchShape("qty_9dp", "1234.500000000", 9),
		newBenchShape("cost_2dp", "1234567.89", 2),
		newBenchShape("crypto_qty_18dp", "1234.567890123456789012", 18),
		newBenchShape("max_coefficient", "34028236692093846346.3374607431768211455", 19),
	}
	benchBuf     = make([]byte, 0, 128)
	benchOut     []byte
	benchDecimal zd.Decimal
	benchNumeric pgtype.Numeric
	errBench     error
)

// BenchmarkCodec times one numeric value through pgx v5 per op, protocol
// and shape, on three paths:
//
//   - path=zd: Decimal through Register's codec.
//   - path=pgtype: pgtype.Numeric through pgx's own NumericCodec, the
//     big.Int path costbasis's codec converts through.
//   - path=unregistered: Decimal through a map without Register, where pgx
//     falls back to the type's sql.Scanner and driver.Valuer, a text round
//     trip.
//
// Scans run a plan made once, as pgx's rows reuse a column's plan; encodes
// call Map.Encode, as pgx does for every argument, into a reused buffer.
// Sub-benchmark names are key=value pairs, so `benchstat -col /path`
// compares the paths row by row.
func BenchmarkCodec(b *testing.B) {
	registered := pgtype.NewMap()
	Register(registered)
	plain := pgtype.NewMap()
	for _, s := range benchShapes {
		for _, f := range formats {
			src := s.src[f.code]
			scans := []struct {
				path string
				m    *pgtype.Map
				dst  any
			}{
				{"zd", registered, &benchDecimal},
				{"pgtype", plain, &benchNumeric},
				{"unregistered", plain, &benchDecimal},
			}
			for _, sc := range scans {
				b.Run("op=Scan/proto="+f.name+"/path="+sc.path+"/shape="+s.name, func(b *testing.B) {
					plan := sc.m.PlanScan(pgtype.NumericOID, f.code, sc.dst)
					if err := plan.Scan(src, sc.dst); err != nil {
						b.Fatal(err)
					}
					b.ReportAllocs()
					for b.Loop() {
						errBench = plan.Scan(src, sc.dst)
					}
				})
			}
			encodes := []struct {
				path  string
				m     *pgtype.Map
				value any
			}{
				{"zd", registered, s.decimal},
				{"pgtype", plain, s.numeric},
				{"unregistered", plain, s.decimal},
			}
			for _, en := range encodes {
				b.Run("op=Encode/proto="+f.name+"/path="+en.path+"/shape="+s.name, func(b *testing.B) {
					if _, err := en.m.Encode(pgtype.NumericOID, f.code, en.value, benchBuf[:0]); err != nil {
						b.Fatal(err)
					}
					b.ReportAllocs()
					for b.Loop() {
						benchOut, errBench = en.m.Encode(pgtype.NumericOID, f.code, en.value, benchBuf[:0])
					}
				})
			}
		}
	}
}

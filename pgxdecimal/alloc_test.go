//go:build !race

package pgxdecimal

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	zd "github.com/AlexandrosKyriakakis/zerodecimal"
)

// The allocation gates run only without the race detector, which allocates
// on its own; CI runs them as go test -run '^Test.*Alloc' ./..., like the
// root module's gates.

var (
	errSink error
	bufSink []byte
)

// TestScanAllocs requires every scan path to allocate nothing: success, SQL
// NULL, and the error paths (the text NaN mapping included), through a
// planned ScanPlan, as pgx's rows reuse it, and through Map.Scan, which
// plans on every call.
func TestScanAllocs(t *testing.T) {
	m := registeredMap()
	d := zd.RequireFromString("-1234567.8912345678")
	srcs := map[int16][][]byte{
		pgtype.BinaryFormatCode: {
			d.AppendPgNumeric(nil),
			nil,
			wireBytes(t, "0000 0000 C000 0000"),
			wireBytes(t, "0001 0000 0000 0000 2710"),
		},
		pgtype.TextFormatCode: {
			[]byte(d.String()),
			nil,
			[]byte("NaN"),
			[]byte("-Infinity"),
			[]byte("12,5"),
		},
	}
	for _, f := range formats {
		for _, k := range kinds {
			dst := k.newDst()
			plan := m.PlanScan(pgtype.NumericOID, f.code, dst)
			for _, src := range srcs[f.code] {
				allocs := testing.AllocsPerRun(100, func() { errSink = plan.Scan(src, dst) })
				require.Zero(t, allocs, "%s plan scan of %q into %s", f.name, src, k.name)
				allocs = testing.AllocsPerRun(100, func() { errSink = m.Scan(pgtype.NumericOID, f.code, src, dst) })
				require.Zero(t, allocs, "%s Map.Scan of %q into %s", f.name, src, k.name)
			}
		}
	}
}

// TestEncodeAllocs requires Map.Encode to allocate nothing for every value
// and pointer form, NULL forms included, when the argument is already boxed
// (pgx receives arguments as []any) and the buffer has capacity, as pgx's
// reused parameter buffer does.
func TestEncodeAllocs(t *testing.T) {
	m := registeredMap()
	d := zd.RequireFromString("-1234567.8912345678")
	buf := make([]byte, 0, 64)
	for _, f := range formats {
		for _, k := range kinds {
			for _, v := range append([]any{k.value(d), k.pointer(d)}, k.nulls...) {
				allocs := testing.AllocsPerRun(100, func() {
					bufSink, errSink = m.Encode(pgtype.NumericOID, f.code, v, buf[:0])
				})
				require.Zero(t, allocs, "%s encode of %#v", f.name, v)
			}
		}
	}
}

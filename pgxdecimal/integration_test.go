package pgxdecimal

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	zd "github.com/AlexandrosKyriakakis/zerodecimal"
)

// pgDSNEnv enables the integration tests. It must name a local throwaway
// PostgreSQL server, never a shared or production one: the tests refuse any
// host but a loopback address or a Unix socket directory, and they create
// only temporary tables, which vanish with the session. Unset, the tests
// skip, so go test ./... needs no database.
const pgDSNEnv = "ZD_PG_DSN"

// execModes are pgx's query modes with the format each one receives numeric
// result columns in: the extended protocol with a described statement asks
// for the codec's preferred binary format, and the other two get text.
var execModes = []struct {
	name   string
	mode   pgx.QueryExecMode
	format int16
}{
	{"cache_statement", pgx.QueryExecModeCacheStatement, pgtype.BinaryFormatCode},
	{"exec", pgx.QueryExecModeExec, pgtype.TextFormatCode},
	{"simple_protocol", pgx.QueryExecModeSimpleProtocol, pgtype.TextFormatCode},
}

func connectThrowaway(t *testing.T) *pgx.Conn {
	t.Helper()
	dsn := os.Getenv(pgDSNEnv)
	if dsn == "" {
		t.Skipf("set %s to a local throwaway PostgreSQL to run the integration tests", pgDSNEnv)
	}
	cfg, err := pgx.ParseConfig(dsn)
	require.NoError(t, err)
	hosts := []string{cfg.Host}
	for _, fb := range cfg.Fallbacks {
		hosts = append(hosts, fb.Host)
	}
	for _, h := range hosts {
		require.Truef(t, isLocalHost(h),
			"%s must name a local throwaway server (a loopback address or a Unix socket), not %q", pgDSNEnv, h)
	}
	conn, err := pgx.ConnectConfig(t.Context(), cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	Register(conn.TypeMap())
	return conn
}

func isLocalHost(host string) bool {
	if strings.HasPrefix(host, "/") || host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// randScaled draws a value with at most intDigits integer digits and
// exactly scale fractional digits, trailing zeros included, so it fits a
// numeric(intDigits+scale, scale) column unrounded.
func randScaled(rng *rand.Rand, intDigits, scale int) zd.Decimal {
	var s strings.Builder
	if rng.UintN(2) == 0 {
		s.WriteByte('-')
	}
	s.WriteByte('0')
	for range rng.IntN(intDigits + 1) {
		s.WriteByte(byte('0' + rng.IntN(10)))
	}
	if scale > 0 {
		s.WriteByte('.')
		for range scale {
			s.WriteByte(byte('0' + rng.IntN(10)))
		}
	}
	return zd.RequireFromString(s.String()).MustRescale(uint8(scale))
}

// TestIntegrationCopyFromAndQuery writes rows with CopyFrom, which is
// binary-only in pgx, then reads them back in every exec mode, covering
// numeric(38,19), numeric(20,9), an unconstrained numeric and NULLs. The
// unconstrained column's text shows that the server kept the dscale the
// binary encoder sent.
func TestIntegrationCopyFromAndQuery(t *testing.T) {
	conn := connectThrowaway(t)
	ctx := t.Context()
	_, err := conn.Exec(ctx, `CREATE TEMPORARY TABLE zd_pgx_copy (
		id    int PRIMARY KEY,
		n38   numeric(38,19) NOT NULL,
		n20   numeric(20,9) NOT NULL,
		free  numeric,
		snull numeric(20,9)
	)`)
	require.NoError(t, err)

	type row struct {
		n38, n20, free, snull zd.Decimal
		freeNull, snullNull   bool
	}
	rng := rand.New(rand.NewPCG(0x636f7079, 0x66726f6d))
	want := make([]row, 600)
	src := make([][]any, len(want))
	for i := range want {
		r := row{
			n38:       randScaled(rng, 19, rng.IntN(20)),
			n20:       randScaled(rng, 11, rng.IntN(10)),
			free:      randDecimal(rng),
			snull:     randScaled(rng, 11, rng.IntN(10)),
			freeNull:  i%7 == 3,
			snullNull: i%5 == 1,
		}
		want[i] = r
		free := zd.NewNullDecimal(r.free)
		if r.freeNull {
			free = zd.NullDecimal{}
		}
		snull := zd.NewStrictNullDecimal(r.snull)
		if r.snullNull {
			snull = zd.StrictNullDecimal{}
		}
		// Value and pointer forms of all four types.
		src[i] = []any{i, r.n38, zd.NewStrictSQLDecimal(r.n20), free, &snull}
		if i%2 == 1 {
			src[i][1] = &r.n38
		}
	}
	n, err := conn.CopyFrom(ctx, pgx.Identifier{"zd_pgx_copy"},
		[]string{"id", "n38", "n20", "free", "snull"}, pgx.CopyFromRows(src))
	require.NoError(t, err)
	require.Equal(t, int64(len(want)), n)

	for _, em := range execModes {
		t.Run(em.name, func(t *testing.T) {
			rows, err := conn.Query(ctx,
				"SELECT id, n38, n20, free, snull, free::text FROM zd_pgx_copy ORDER BY id", em.mode)
			require.NoError(t, err)
			defer rows.Close()
			for _, fd := range rows.FieldDescriptions()[1:5] {
				require.Equal(t, em.format, fd.Format, "result format of %s", fd.Name)
			}
			i := 0
			for rows.Next() {
				var (
					id       int
					n38      zd.Decimal
					n20      zd.StrictSQLDecimal
					free     zd.NullDecimal
					snull    *zd.StrictNullDecimal
					freeText *string
				)
				require.NoError(t, rows.Scan(&id, &n38, &n20, &free, &snull, &freeText))
				w := want[id]
				require.Equal(t, w.n38.Trim(), n38, "n38 of row %d", id)
				require.Equal(t, w.n20.Trim(), n20.Decimal, "n20 of row %d", id)
				if w.freeNull {
					require.Equal(t, zd.NullDecimal{}, free)
					require.Nil(t, freeText)
				} else {
					require.Equal(t, zd.NewNullDecimal(w.free.Trim()), free, "free of row %d", id)
					require.Equal(t, w.free.StringFixed(w.free.Prec()), *freeText, "dscale of row %d", id)
				}
				if w.snullNull {
					require.Nil(t, snull, "snull of row %d", id)
				} else {
					require.NotNil(t, snull)
					require.Equal(t, zd.NewStrictNullDecimal(w.snull.Trim()), *snull, "snull of row %d", id)
				}
				i++
			}
			require.NoError(t, rows.Err())
			require.Equal(t, len(want), i)
		})
	}
}

// TestIntegrationParameters binds values of all four types, value and
// pointer forms, NULL forms and numeric[] as query parameters in every exec
// mode: binary for the described extended protocol, text for the others.
// Whichever protocol carried a value, PostgreSQL stores the same numeric,
// display scale included, and every mode reads it back alike.
func TestIntegrationParameters(t *testing.T) {
	conn := connectThrowaway(t)
	ctx := t.Context()
	_, err := conn.Exec(ctx, `CREATE TEMPORARY TABLE zd_pgx_param (
		mode text, id int, v numeric, n38 numeric(38,19), n20 numeric(20,9), PRIMARY KEY (mode, id))`)
	require.NoError(t, err)

	rng := rand.New(rand.NewPCG(0x706172, 0x616d73))
	values := []zd.Decimal{
		{},
		zd.RequireFromString("1.5").MustRescale(2),
		zd.RequireFromString("-0.000000001"),
		zd.RequireFromString("340282366920938463463374607431768211455"),
		zd.RequireFromString("-34028236692093846346.3374607431768211455"),
	}
	for range 300 {
		values = append(values, randDecimal(rng))
	}
	for _, em := range execModes {
		for i, d := range values {
			n38 := randScaled(rng, 19, i%20)
			args := []any{em.name, i, d, &n38, zd.NewStrictNullDecimal(zd.RequireFromString("1.25"))}
			switch i % 4 {
			case 1:
				args[2] = zd.NewStrictSQLDecimal(d)
			case 2:
				args[2] = zd.NewNullDecimal(d)
			case 3:
				args[2] = &d
				args[4] = (*zd.StrictNullDecimal)(nil)
			}
			_, err = conn.Exec(ctx, "INSERT INTO zd_pgx_param VALUES ($1, $2, $3, $4, $5)", append([]any{em.mode}, args...)...)
			require.NoError(t, err, "%s row %d", em.name, i)
		}
	}

	// Every protocol stored the same numerics, display scales included.
	var mismatches int
	err = conn.QueryRow(ctx, `SELECT count(*) FROM zd_pgx_param a JOIN zd_pgx_param b USING (id)
		WHERE a.mode < b.mode AND (a.v::text IS DISTINCT FROM b.v::text OR a.n20::text IS DISTINCT FROM b.n20::text)`).Scan(&mismatches)
	require.NoError(t, err)
	require.Zero(t, mismatches)

	for _, em := range execModes {
		t.Run(em.name, func(t *testing.T) {
			for i, d := range values {
				var (
					v     zd.Decimal
					vText string
					n20   zd.StrictNullDecimal
				)
				err := conn.QueryRow(ctx, "SELECT v, v::text, n20 FROM zd_pgx_param WHERE mode = $1 AND id = $2",
					em.mode, em.name, i).Scan(&v, &vText, &n20)
				require.NoError(t, err)
				require.Equal(t, d.Trim(), v, "row %d", i)
				require.Equal(t, d.StringFixed(d.Prec()), vText, "row %d keeps its dscale", i)
				if i%4 == 3 {
					require.False(t, n20.Valid)
				} else {
					require.Equal(t, zd.NewStrictNullDecimal(zd.RequireFromString("1.25")), n20)
				}
			}

			in := values[:5]
			var out []zd.Decimal
			require.NoError(t, conn.QueryRow(ctx, "SELECT $1::numeric[]", em.mode, in).Scan(&out))
			require.Len(t, out, len(in))
			for i := range in {
				require.Equal(t, in[i].Trim(), out[i])
			}

			var null zd.NullDecimal
			require.NoError(t, conn.QueryRow(ctx, "SELECT $1::numeric", em.mode, zd.NullDecimal{}).Scan(&null))
			require.False(t, null.Valid)
		})
	}
}

// TestIntegrationSpecialValues checks the documented edge behaviour against
// a real server in every exec mode: NaN and ±Infinity fail with
// ErrInvalidFloat in both formats, NULL follows each type's rule, and a
// rendering past ParseBytes' 200-byte limit decodes in binary but fails with
// ErrMaxStrLen in text.
func TestIntegrationSpecialValues(t *testing.T) {
	conn := connectThrowaway(t)
	ctx := t.Context()

	// The binary encodings PostgreSQL sends, with dscale 0x20 for ±Infinity.
	for _, tc := range []struct{ expr, hex string }{
		{"'NaN'::numeric", "00000000c0000000"},
		{"'Infinity'::numeric", "00000000d0000020"},
		{"'-Infinity'::numeric", "00000000f0000020"},
		{"1.5::numeric(1000,500)", "00020000000001f400011388"},
	} {
		rows, err := conn.Query(ctx, "SELECT "+tc.expr, pgx.QueryExecModeCacheStatement)
		require.NoError(t, err)
		require.True(t, rows.Next())
		require.Equal(t, tc.hex, hex.EncodeToString(rows.RawValues()[0]), tc.expr)
		rows.Close()
		require.NoError(t, rows.Err())
	}

	for _, em := range execModes {
		t.Run(em.name, func(t *testing.T) {
			scan := func(expr string, dst any) error {
				return conn.QueryRow(ctx, "SELECT "+expr, em.mode).Scan(dst)
			}
			for _, expr := range []string{"'NaN'::numeric", "'Infinity'::numeric", "'-Infinity'::numeric"} {
				for _, k := range kinds {
					dst := k.newDst()
					k.preset(dst, stale)
					err := scan(expr, dst)
					require.ErrorIs(t, err, zd.ErrInvalidFloat, "%s into %s", expr, k.name)
					got, valid := k.read(dst)
					requireScanFailed(t, k, got, valid)
				}
			}
			for _, k := range kinds {
				dst := k.newDst()
				k.preset(dst, stale)
				err := scan("NULL::numeric", dst)
				got, valid := k.read(dst)
				if k.nullable {
					require.NoError(t, err)
					require.False(t, valid)
				} else {
					require.ErrorIs(t, err, zd.ErrScanNil, k.name)
					require.Equal(t, stale, got)
				}
			}

			// numeric_out renders these past 200 bytes: 502 and 302.
			var wide zd.Decimal
			err := scan("1.5::numeric(1000,500)", &wide)
			tinyErr := zd.ErrMaxStrLen
			if em.format == pgtype.BinaryFormatCode {
				require.NoError(t, err)
				require.Equal(t, zd.RequireFromString("1.5"), wide)
				tinyErr = zd.ErrPrecOutOfRange
			} else {
				require.ErrorIs(t, err, zd.ErrMaxStrLen)
			}
			require.ErrorIs(t, scan("1e-300::numeric", &wide), tinyErr)
			require.ErrorIs(t, scan(fmt.Sprintf("%s::numeric", strings.Repeat("9", 40)), &wide), zd.ErrOverflow)
		})
	}
}

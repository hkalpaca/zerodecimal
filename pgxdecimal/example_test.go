package pgxdecimal_test

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	zerodecimal "github.com/AlexandrosKyriakakis/zerodecimal"
	"github.com/AlexandrosKyriakakis/zerodecimal/pgxdecimal"
)

// Register the codec on every connection a pool opens, then scan numeric
// columns straight into zerodecimal types. (Not run: it needs a database.)
func Example() {
	ctx := context.Background()
	config, err := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
	if err != nil {
		panic(err)
	}
	config.AfterConnect = func(_ context.Context, conn *pgx.Conn) error {
		pgxdecimal.Register(conn.TypeMap())
		return nil
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		panic(err)
	}
	defer pool.Close()

	var balance zerodecimal.StrictSQLDecimal
	var lastFee zerodecimal.StrictNullDecimal
	err = pool.QueryRow(
		ctx,
		"SELECT balance, last_fee FROM accounts WHERE id = $1", 42,
	).Scan(&balance, &lastFee)
	if err != nil {
		panic(err)
	}
	fmt.Println(balance, lastFee.Valid)
}

// The codec at work on a bare type map, as pgx uses it for a query's
// arguments and result columns.
func ExampleRegister() {
	m := pgtype.NewMap()
	pgxdecimal.Register(m)

	price, err := zerodecimal.RequireFromString("2.5").MulRound(zerodecimal.NewFromInt(5), 2, zerodecimal.ToNearestEven)
	if err != nil {
		panic(err)
	}

	// Both formats keep the value's two places: dscale 2, and "12.50".
	wire, err := m.Encode(pgtype.NumericOID, pgtype.BinaryFormatCode, price, nil)
	if err != nil {
		panic(err)
	}
	fmt.Printf("% x\n", wire)
	text, err := m.Encode(pgtype.NumericOID, pgtype.TextFormatCode, price, nil)
	if err != nil {
		panic(err)
	}
	fmt.Println(string(text))

	// Scans return the canonical value.
	var got zerodecimal.Decimal
	err = m.Scan(pgtype.NumericOID, pgtype.BinaryFormatCode, wire, &got)
	if err != nil {
		panic(err)
	}
	fmt.Println(got)

	// SQL NULL, and NaN in either format.
	var fee zerodecimal.NullDecimal
	err = m.Scan(pgtype.NumericOID, pgtype.BinaryFormatCode, nil, &fee)
	fmt.Println(fee.Valid, err)
	err = m.Scan(pgtype.NumericOID, pgtype.TextFormatCode, []byte("NaN"), &got)
	fmt.Println(errors.Is(err, zerodecimal.ErrInvalidFloat))
	// Output:
	// 00 02 00 00 00 00 00 02 00 0c 13 88
	// 12.50
	// 12.5
	// false <nil>
	// true
}

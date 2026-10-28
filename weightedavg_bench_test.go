package zerodecimal

import (
	"math/rand/v2"
	"testing"
)

// wavgBenchRows is the stream length of every BenchmarkWeightedAvg row; the
// ns/row metric divides by it.
const wavgBenchRows = 1000

// Package-level inputs and sinks (see alloc_test.go for why neither may be
// local). Every fill is a buy of up to 10 000 units at 9 places.
var (
	wavgBenchSink Decimal
	errWavgBench  error

	wavgBenchQty = wavgBenchMake(func(rng *rand.Rand) Decimal {
		return newDecimal(u128{lo: 1 + rng.Uint64N(10_000*pow10u64[9])}, false, 9)
	})
	// Prices in cents below 50 000: 11-place products, kept exactly.
	wavgBenchCents = wavgBenchMake(func(rng *rand.Rand) Decimal {
		return newDecimal(u128{lo: 1 + rng.Uint64N(50_000*100)}, false, 2)
	})
	// The same products, multiplied ahead of time as the compressor does.
	wavgBenchCentsProducts = func() []Decimal {
		xs := make([]Decimal, wavgBenchRows)
		for i := range xs {
			xs[i] = wavgBenchCents[i].MustMul(wavgBenchQty[i])
		}
		return xs
	}()
	// Prices at 16 places below 1 000, like pnl's carried averages: 25-place
	// products, truncated to DefaultPrec.
	wavgBenchPrices16 = wavgBenchMake(func(rng *rand.Rand) Decimal {
		return newDecimal(u128{lo: 1 + rng.Uint64N(1_000*pow10u64[16])}, false, 16)
	})
)

func wavgBenchMake(gen func(rng *rand.Rand) Decimal) []Decimal {
	rng := rand.New(rand.NewPCG(0x3a7_be4c, 0x0400))
	xs := make([]Decimal, wavgBenchRows)
	for i := range xs {
		xs[i] = gen(rng)
	}
	return xs
}

// The three implementations below add value×qty per row, or, with total
// set, values that already hold the products.

func wavgBenchWeightedAvg(values, qty []Decimal, total, round16 bool) (Decimal, error) {
	var w WeightedAvg
	if total {
		for i := range qty {
			w.AddTotal(values[i], qty[i])
		}
	} else {
		for i := range qty {
			w.Add(values[i], qty[i])
		}
	}
	if round16 {
		return w.AvgRound(16, ToNearestAway)
	}
	return w.Avg()
}

func wavgBenchChain(values, qty []Decimal, total, round16 bool) Decimal {
	num, weight := Decimal{}, Decimal{}
	if total {
		for i := range qty {
			num = num.MustAdd(values[i])
			weight = weight.MustAdd(qty[i])
		}
	} else {
		for i := range qty {
			num = num.MustAdd(values[i].MustMul(qty[i]))
			weight = weight.MustAdd(qty[i])
		}
	}
	if round16 {
		return num.MustDiv(weight).Round(16)
	}
	return num.MustDiv(weight)
}

func wavgBenchAccumulators(values, qty []Decimal, total, round16 bool) (Decimal, error) {
	var num, weight Accumulator
	if total {
		for i := range qty {
			num.Add(values[i])
			weight.Add(qty[i])
		}
	} else {
		for i := range qty {
			num.AddMul(values[i], qty[i])
			weight.Add(qty[i])
		}
	}
	den, err := weight.Sum()
	if err != nil {
		return Decimal{}, err
	}
	if round16 {
		return num.DivRound(den, 16, ToNearestAway)
	}
	return num.Div(den)
}

// BenchmarkWeightedAvg computes a VWAP over 1000 fills per op three ways:
// WeightedAvg, the manual chain (MustMul and MustAdd per fill, then MustDiv),
// and two raw Accumulators (AddMul and Add, then Div by Sum).
//   - shape=vwap_9x2 multiplies 9-place quantities by cents, exact products,
//     through Add, and finishes with the legacy Avg.
//   - shape=addtotal_9x2 is the same VWAP with the products computed ahead
//     of time, as the compressor does: AddTotal, the chain's MustAdd, or
//     Accumulator.Add take them as given.
//   - shape=pnl_9x16 multiplies the quantities by 16-place prices, products
//     truncated to DefaultPrec, and finishes with AvgRound(16,
//     ToNearestAway), which the chain computes as MustDiv then Round(16) and
//     the Accumulators as DivRound.
//
// ns/row is ns/op / 1000.
func BenchmarkWeightedAvg(b *testing.B) {
	reportPerRow := func(b *testing.B) {
		b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/wavgBenchRows, "ns/row")
	}
	for _, shape := range []struct {
		name           string
		values         []Decimal
		total, round16 bool
	}{
		{"vwap_9x2", wavgBenchCents, false, false},
		{"addtotal_9x2", wavgBenchCentsProducts, true, false},
		{"pnl_9x16", wavgBenchPrices16, false, true},
	} {
		values, total, round16 := shape.values, shape.total, shape.round16
		want := wavgBenchChain(values, wavgBenchQty, total, round16)
		got, err := wavgBenchWeightedAvg(values, wavgBenchQty, total, round16)
		raw, rawErr := wavgBenchAccumulators(values, wavgBenchQty, total, round16)
		if err != nil || rawErr != nil || got != want || raw != want {
			b.Fatalf("preflight %s: chain %v, WeightedAvg %v (%v), Accumulators %v (%v)", shape.name, want, got, err, raw, rawErr)
		}
		b.Run("shape="+shape.name+"/impl=WeightedAvg", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				wavgBenchSink, errWavgBench = wavgBenchWeightedAvg(values, wavgBenchQty, total, round16)
			}
			reportPerRow(b)
		})
		b.Run("shape="+shape.name+"/impl=chain", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				wavgBenchSink = wavgBenchChain(values, wavgBenchQty, total, round16)
			}
			reportPerRow(b)
		})
		b.Run("shape="+shape.name+"/impl=Accumulators", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				wavgBenchSink, errWavgBench = wavgBenchAccumulators(values, wavgBenchQty, total, round16)
			}
			reportPerRow(b)
		})
	}
}

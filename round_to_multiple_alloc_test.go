package zerodecimal

import "testing"

// TestRoundToMultipleAllocs gates RoundToMultiple at zero allocations on the
// one-limb and wide paths and on every error path. The operands and sinks are
// the package-level fixtures in round_to_multiple_bench_test.go.
func TestRoundToMultipleAllocs(t *testing.T) {
	if raceEnabled {
		t.Skip("allocation counts are unreliable under -race")
	}
	ops := []struct {
		name string
		fn   func()
	}{
		{"tick", func() {
			roundToMultipleSink, errRoundToMultiple = roundToMultiplePrice.RoundToMultiple(roundToMultipleTick, ToNearestEven)
		}},
		{"share_rule", func() {
			roundToMultipleSink, errRoundToMultiple = roundToMultipleEntitled.RoundToMultiple(roundToMultipleRule, TowardZero)
		}},
		{"wide", func() {
			roundToMultipleSink, errRoundToMultiple = roundToMultipleWideD.RoundToMultiple(roundToMultipleTick, ToNearestEven)
		}},
		{"overflow", func() {
			roundToMultipleSink, errRoundToMultiple = roundToMultipleMax.RoundToMultiple(roundToMultipleFinest, TowardZero)
		}},
		{"divide_by_zero", func() {
			roundToMultipleSink, errRoundToMultiple = roundToMultiplePrice.RoundToMultiple(roundToMultipleZero, ToNearestEven)
		}},
		{"invalid_mode", func() {
			roundToMultipleSink, errRoundToMultiple = roundToMultiplePrice.RoundToMultiple(roundToMultipleTick, RoundingMode(6))
		}},
	}
	for _, op := range ops {
		t.Run(op.name, func(t *testing.T) {
			requireAllocs(t, 0, op.fn)
		})
	}
}

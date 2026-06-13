package builtin

import (
	"math/big"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// TestIntegerBitwiseIntrinsics pins the width-blind bitwise folds the integer
// intrinsics supply: and/or/xor combine two values (a signed value's bits are
// its two's complement, so the negative cases hold), shl multiplies by a power
// of two without wrapping (a sized receiver's overflow is caught later, at the
// assignment), and shr divides by one (an arithmetic, floor-ward shift on a
// negative). The width-dependent complement is not here — it is x.bxor(T.Max),
// pinned by example 0053 — so these never need the receiver's width.
func TestIntegerBitwiseIntrinsics(t *testing.T) {
	ii := integerIntrinsics()
	bin := func(name string, a, b int64) *ir.Constant {
		return ii[name](intConst(a), []*ir.Constant{intConst(b)})
	}

	for _, tc := range []struct {
		op   string
		a, b int64
		want int64
	}{
		{"band", 0b1100, 0b1010, 0b1000},
		{"bor", 0b1100, 0b1010, 0b1110},
		{"bxor", 0b1100, 0b1010, 0b0110},
		{"band", -1, 255, 255}, // -1 is all ones in two's complement
		{"bor", -1, 0, -1},     // all ones | anything is all ones
		{"bxor", 5, -1, -6},    // x ^ all-ones is ~x
		{"shl", 1, 10, 1024},   // width-blind product, no wrap
		{"shr", 0b10110, 2, 0b101},
		{"shr", 255, 100, 0}, // a finite value shifted that far is zero
		{"shr", -8, 1, -4},   // arithmetic (floor-ward) shift
	} {
		got := bin(tc.op, tc.a, tc.b)
		if got == nil || got.Kind != ir.ConstInt || got.Int.Int64() != tc.want {
			t.Errorf("%s(%d, %d) = %v, want %d", tc.op, tc.a, tc.b, got, tc.want)
		}
	}

	// A right shift by an amount too large for uint64 converges to the sign bit:
	// 0 for a non-negative receiver, -1 (all sign bits) for a negative one — the
	// arithmetic-shift limit, consistent with the in-range counts above.
	huge := new(big.Int).Lsh(big.NewInt(1), 64) // 2^64, past uint64
	if got := ii["shr"](intConst(-8), []*ir.Constant{ir.IntConstant(huge)}); got == nil || got.Int.Int64() != -1 {
		t.Errorf("shr(-8, 2^64) = %v, want -1", got)
	}
	if got := ii["shr"](intConst(255), []*ir.Constant{ir.IntConstant(huge)}); got == nil || got.Int.Sign() != 0 {
		t.Errorf("shr(255, 2^64) = %v, want 0", got)
	}

	// On the unbounded nint, shl widens without wrapping: 1 << 64 is kept as 2^64
	// (a sized receiver would instead overflow at its assignment).
	if got := bin("shl", 1, 64); got == nil || got.Int.Cmp(new(big.Int).Lsh(big.NewInt(1), 64)) != 0 {
		t.Errorf("shl(1, 64) = %v, want 2^64", got)
	}

	// A zero receiver folds any non-negative shift to zero cheaply, even past the
	// fold cap and past uint64 — 0 << n is 0, no wide integer to materialize.
	if got := bin("shl", 0, maxShiftFoldBits+1); got == nil || got.Int.Sign() != 0 {
		t.Errorf("shl(0, %d) = %v, want 0", maxShiftFoldBits+1, got)
	}
	if got := ii["shl"](intConst(0), []*ir.Constant{ir.IntConstant(huge)}); got == nil || got.Int.Sign() != 0 {
		t.Errorf("shl(0, 2^64) = %v, want 0", got)
	}

	// A shift amount past the fold cap is left unfolded rather than materialized
	// into an astronomically large value at compile time.
	if got := bin("shl", 1, maxShiftFoldBits+1); got != nil {
		t.Errorf("shl(1, %d) = %v, want nil (past the fold cap)", maxShiftFoldBits+1, got)
	}
}

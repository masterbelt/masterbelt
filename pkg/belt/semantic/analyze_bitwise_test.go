package semantic

import (
	"slices"
	"strings"
	"testing"
)

// TestBitwiseShiftNegativeAmount pins that a negative shift amount is rejected at
// the argument as constant_overflow — it cannot inhabit the nuint operand — the
// way a free function's argument is range-checked, rather than slipping through a
// method call's operand adaptation unchecked (silently in a body, or as a
// misleading budget/gap in a const).
func TestBitwiseShiftNegativeAmount(t *testing.T) {
	for _, src := range []string{
		"const X = byte(1).shl(-1)\n",
		"const X = byte(8).shr(-1)\n",
		"pub fn f(): byte { return byte(1).shl(-1) }\n",
	} {
		_, diags := analyze(src)
		if !slices.Contains(codes(diags), CodeConstantOverflow) {
			t.Errorf("%q: codes = %v, want a constant_overflow", src, codes(diags))
		}
	}
}

// TestBitwiseShiftOverflow pins that a left shift whose result does not fit the
// receiver overflows at the assignment — a folded value the Fits check rejects,
// reported as constant_overflow. Regression: a shift amount past the fold cap
// once folded to nil and surfaced as an internal evaluator-gap diagnostic
// instead of the overflow.
func TestBitwiseShiftOverflow(t *testing.T) {
	_, diags := analyze("const X = byte(1).shl(65537)\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeConstantOverflow {
		t.Errorf("byte(1).shl(65537): codes = %v, want [%s]", got, CodeConstantOverflow)
	}
}

// TestBitwiseZeroShiftFolds pins that a zero receiver folds any non-negative
// shift to zero cheaply — even past the fold cap — rather than refusing the wide
// amount: 0 << n is 0, with no wide integer to build.
func TestBitwiseZeroShiftFolds(t *testing.T) {
	_, diags := analyze("const X: byte = byte(0).shl(2097153)\n")
	if len(diags) != 0 {
		t.Errorf("byte(0).shl(2097153): want no diagnostics (folds to 0), got %v", codes(diags))
	}
}

// TestBitwiseNegativeShiftNotBudget pins that a negative shift amount reaching
// the intrinsic through a non-constant signed parameter is not mislabeled a
// budget refusal: noteBudget fires only for a non-negative oversized shift.
func TestBitwiseNegativeShiftNotBudget(t *testing.T) {
	_, diags := analyze("pub fn f(n: nint): byte { return byte(1).shl(n) }\nconst X = f(-1)\n")
	for _, d := range diags {
		if d.Code == CodeUnfoldedConst && strings.Contains(d.Message, "no compile-time value: depth") {
			t.Errorf("f(-1): a negative shift mislabeled as a budget refusal: %q", d.Message)
		}
	}
}

// TestBitwiseShiftBudget pins that a shift too wide to materialize is classified
// as a budget refusal, not an evaluator gap: it cannot fold (so the const is
// unfolded), but the failure must not read as an internal masterbelt bug.
func TestBitwiseShiftBudget(t *testing.T) {
	_, diags := analyze("const X = byte(1).shl(2097153)\n")
	var msg string
	for _, d := range diags {
		if d.Code == CodeUnfoldedConst {
			msg = d.Message
		}
	}
	if msg == "" {
		t.Fatalf("byte(1).shl(2097153): want an unfolded_const diagnostic, got none (codes %v)", codes(diags))
	}
	if strings.Contains(msg, "no compile-time value: evaluator gap") {
		t.Errorf("byte(1).shl(2097153): classified as an evaluator gap, want a budget refusal: %q", msg)
	}
}

// TestBitwiseBandOperandRangeChecked pins the soundness fix for a masking bitwise
// operator: a bitwise AND clears bits, so an out-of-range operand masks to an
// in-range result the result check accepts (byte(1).band(300) folds to 0), which
// would leave the out-of-range operand unreported. The operand is now range-checked
// at its own site, the way a free function's argument is — in a const and in a body
// alike. Red→green: drop the band operand check and the masked result folds in
// range with no overflow reported.
func TestBitwiseBandOperandRangeChecked(t *testing.T) {
	for _, src := range []string{
		"const X: byte = byte(1).band(300)\n",
		"const X: byte = byte(1).band(-1)\n",
		"pub fn f(): byte { return byte(1).band(300) }\n",
	} {
		_, diags := analyze(src)
		if !slices.Contains(codes(diags), CodeConstantOverflow) {
			t.Errorf("%q: codes = %v, want a constant_overflow (the masked operand)", src, codes(diags))
		}
	}
}

// TestBitwiseBandReceiverRangeChecked pins the same for the receiver: 300.band(byte(1))
// masks the out-of-range receiver to 0, which the result check accepts, so the
// receiver is range-checked at its own site as the operand is.
func TestBitwiseBandReceiverRangeChecked(t *testing.T) {
	_, diags := analyze("const X: byte = 300.band(byte(1))\n")
	if !slices.Contains(codes(diags), CodeConstantOverflow) {
		t.Errorf("300.band(byte(1)): codes = %v, want a constant_overflow (the masked receiver)", codes(diags))
	}
}

// TestBitwiseBandInRangeNoOverflow pins that the masking-operator range check does
// not over-fire: in-range operands and receivers fold cleanly.
func TestBitwiseBandInRangeNoOverflow(t *testing.T) {
	for _, src := range []string{
		"const X: byte = byte(7).band(byte(3))\n",
		"const X: byte = byte(255).band(1)\n",
	} {
		if _, diags := analyze(src); len(diags) != 0 {
			t.Errorf("%q: want no diagnostics, got %v", src, codes(diags))
		}
	}
}

// TestBitwiseOrXorCaughtByResult guards the band-only scope: OR and XOR set the
// bits a too-wide input carries, and a left shift grows the magnitude, so the
// result is out of range and the result check already reports it — no input-site
// check is added for them. If a future evaluator change let one of these fold an
// out-of-range input into range, this would go red and signal that the input check
// must extend to it.
func TestBitwiseOrXorCaughtByResult(t *testing.T) {
	for _, src := range []string{
		"const X: byte = byte(1).bor(300)\n",
		"const X: byte = byte(1).bxor(300)\n",
		"const X: byte = 300.bor(byte(1))\n",
		"const X: byte = 256.bxor(byte(0))\n",
		"const X: byte = 300.shl(1)\n",
	} {
		_, diags := analyze(src)
		if !slices.Contains(codes(diags), CodeConstantOverflow) {
			t.Errorf("%q: codes = %v, want a constant_overflow from the result check", src, codes(diags))
		}
	}
}

// TestOperandCheckLeavesArithmeticAndComparison pins behavior-invariance outside
// the masking bitwise operator: arithmetic still reports only its overflowing
// result (one constant_overflow, not a second at the operand), and a comparison
// folds to a bool with no overflow. The input range check is scoped to band and
// changes neither.
func TestOperandCheckLeavesArithmeticAndComparison(t *testing.T) {
	_, diags := analyze("const X: byte = byte(5) + 300\n")
	if n := countCode(diags, CodeConstantOverflow); n != 1 {
		t.Errorf("byte(5)+300: got %d constant_overflow, want exactly 1 (the result only): %v", n, codes(diags))
	}
	if _, d := analyze("const X: bool = byte(1) == 300\n"); len(d) != 0 {
		t.Errorf("byte(1)==300: want no diagnostics (folds to a bool), got %v", codes(d))
	}
}

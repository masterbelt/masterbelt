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

// A self-unified operand or receiver of an integer operator must inhabit the
// type's width — the same rule a free function's argument follows. The result
// site is not a sufficient backstop: a masking or reducing operator folds an
// out-of-range input to an in-range result it would accept. These tests pin the
// input-site range check across the operators where that happens.

// TestSelfOperandMaskedToRangeChecked pins the soundness fix: an operator that
// folds an out-of-range self operand to an in-range result — AND masks, division
// and remainder reduce, a multiply by zero collapses, an add of a negative shrinks
// — leaves the result check satisfied, so the operand is range-checked at its own
// site. Red→green: drop the self-operand check and these fold in range unreported.
func TestSelfOperandMaskedToRangeChecked(t *testing.T) {
	for _, src := range []string{
		"const X: byte = byte(1).band(300)\n",
		"const X: byte = byte(1).band(-1)\n",
		"const X: byte = byte(1).add(-1)\n",
		"const X: byte = byte(5).div(300)\n",
		"const X: byte = byte(7).mul(300)\n",
		"const X: byte = byte(0) * 300\n",
		"pub fn f(): byte { return byte(1).band(300) }\n",
	} {
		_, diags := analyze(src)
		if !slices.Contains(codes(diags), CodeConstantOverflow) {
			t.Errorf("%q: codes = %v, want a constant_overflow at the masked operand", src, codes(diags))
		}
	}
}

// TestSelfReceiverMaskedToRangeChecked pins the same for the receiver: a too-wide
// receiver an operator reduces into range (300.band(byte(1)) folds to 0,
// 300.rem(byte(5)) to 0) is range-checked at its own site.
func TestSelfReceiverMaskedToRangeChecked(t *testing.T) {
	for _, src := range []string{
		"const X: byte = 300.band(byte(1))\n",
		"const X: byte = 300.rem(byte(5))\n",
	} {
		_, diags := analyze(src)
		if !slices.Contains(codes(diags), CodeConstantOverflow) {
			t.Errorf("%q: codes = %v, want a constant_overflow at the masked receiver", src, codes(diags))
		}
	}
}

// TestComparisonOperandRangeChecked pins that a comparison's operand is range-
// checked too: byte(1) == 300 folds to a bool, so the result is never out of range,
// but 300 cannot inhabit byte and is reported at the operand.
func TestComparisonOperandRangeChecked(t *testing.T) {
	_, diags := analyze("const X: bool = byte(1) == 300\n")
	if !slices.Contains(codes(diags), CodeConstantOverflow) {
		t.Errorf("byte(1)==300: codes = %v, want a constant_overflow at the operand", codes(diags))
	}
}

// TestSelfOperandInRangeNoOverflow pins that the input range check does not
// over-fire: in-range operands and receivers across the operators fold cleanly.
func TestSelfOperandInRangeNoOverflow(t *testing.T) {
	for _, src := range []string{
		"const X: byte = byte(7).band(byte(3))\n",
		"const X: byte = byte(255).band(1)\n",
		"const X: byte = byte(5) + byte(10)\n",
		"const X: byte = 200.rem(byte(7))\n",
		"const X: bool = byte(1) == 100\n",
	} {
		if _, diags := analyze(src); len(diags) != 0 {
			t.Errorf("%q: want no diagnostics, got %v", src, codes(diags))
		}
	}
}

// TestRefinedTypeOperandNotRefinementChecked guards the underlying-width design: a
// self operand of a refined nominal type is range-checked against the sized width
// beneath it, not the refined type's predicate — a comparison bound (self <= 100)
// is not a value of the refined type, so it must not be run through the refinement
// (which would spuriously report a refinement_violation). The bound is in range, so
// no diagnostic fires.
func TestRefinedTypeOperandNotRefinementChecked(t *testing.T) {
	src := "pub type Lvl = sbyte where self.positive() && self <= 100 impl {\n" +
		"  pub positive(): bool {\n    return self > 0\n  }\n}\n" +
		"const Ok: Lvl = 10\n"
	if _, diags := analyze(src); len(diags) != 0 {
		t.Errorf("a comparison bound in a refined where-clause must not be refinement-checked, got %v", codes(diags))
	}
}

// TestBitwiseOrXorStillReported pins that OR, XOR, and a left shift still report an
// out-of-range input — they set bits or grow the magnitude, so the result is out of
// range and reported there, and the input check reports it too.
func TestBitwiseOrXorStillReported(t *testing.T) {
	for _, src := range []string{
		"const X: byte = byte(1).bor(300)\n",
		"const X: byte = byte(1).bxor(300)\n",
		"const X: byte = 300.shl(1)\n",
	} {
		_, diags := analyze(src)
		if !slices.Contains(codes(diags), CodeConstantOverflow) {
			t.Errorf("%q: codes = %v, want a constant_overflow", src, codes(diags))
		}
	}
}

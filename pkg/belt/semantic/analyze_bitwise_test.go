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

package semantic

import (
	"strings"
	"testing"
)

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

// These tests pin the Eval publication rule (enforceEvalPublication): a value
// is published iff its declaration — and what it depends on — is
// diagnostic-free. Soundness withholds the values of broken declarations (the
// internal type-blind fold may well have produced one); totality errs on a
// clean declaration without one (unfolded_const); and the dependent-failure
// suppression is exactly as narrow as designed, so a genuine evaluator gap
// cannot hide behind it.
package semantic

import (
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source"
)

// TestBrokenConstPublishesNoValue is the soundness direction: a declaration
// with an error — an unknown annotation type, an overflow, a refinement
// violation — publishes no Eval, even though the type-blind fold can build
// the structural value.
func TestBrokenConstPublishesNoValue(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{"unknown type", "const M: Bogus = [\"a\": 1]\n"},
		{"overflow", "const X: sbyte = 999\n"},
		{"refinement violation", "pub type Level = sbyte where self >= 0 && self <= 100\nconst Bad: Level = 101\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, diags := analyze(tc.src)
			if len(diags) == 0 {
				t.Fatal("expected a diagnostic")
			}
			last := m.Consts[len(m.Consts)-1]
			if last.Eval != nil {
				t.Errorf("broken const %s published Eval %v, want none (its value is undefined)", last.Name, last.Eval)
			}
			if hasCode(diags, CodeUnfoldedConst) {
				t.Errorf("a broken const must not also be reported unfolded: %v", codes(diags))
			}
		})
	}
}

// TestTaintedReaderPublishesNoValue: a clean declaration reading a broken one
// publishes no value either (the Invalid type propagates), and is not
// re-reported as unfolded — the cause carries its diagnostic at the broken
// declaration.
func TestTaintedReaderPublishesNoValue(t *testing.T) {
	src := "const M: Bogus = [\"a\": 1]\nconst A = M.count()\n"
	m, diags := analyze(src)
	if !hasCode(diags, CodeUnknownType) {
		t.Fatalf("want unknown_type at M, got %v", codes(diags))
	}
	if hasCode(diags, CodeUnfoldedConst) {
		t.Errorf("the reader must not pile on unfolded_const: %v", codes(diags))
	}
	for _, c := range m.Consts {
		if c.Eval != nil {
			t.Errorf("const %s published Eval %v, want none", c.Name, c.Eval)
		}
	}
}

// TestAssertOverBrokenConstNotGreen: an assert whose condition reads a broken
// constant publishes no outcome — the type-blind fold would happily produce
// one, and a green checkmark over an unverified value is exactly the accident
// the soundness rule exists to prevent. No assert diagnostic piles on; the
// cause is the constant's own.
func TestAssertOverBrokenConstNotGreen(t *testing.T) {
	src := "const M: Bogus = [\"a\": 1]\nassert M.len() == 1\n"
	m, diags := analyze(src)
	if !hasCode(diags, CodeUnknownType) {
		t.Fatalf("want unknown_type at M, got %v", codes(diags))
	}
	for _, code := range codes(diags) {
		if strings.Contains(string(code), "assertion") {
			t.Errorf("no assert diagnostic should pile on, got %v", codes(diags))
		}
	}
	if len(m.Asserts) != 1 {
		t.Fatalf("asserts = %d, want 1", len(m.Asserts))
	}
	if m.Asserts[0].Eval != nil {
		t.Errorf("assert over a broken const published Eval %v, want none", m.Asserts[0].Eval)
	}
}

// TestDepthReaderSuppressed: only the constant that exceeded the budget errs;
// a clean reader of it is suppressed (its cause is reported at the
// dependency), so one failure yields one diagnostic.
func TestDepthReaderSuppressed(t *testing.T) {
	src := "pub fn deep(n: nint): nint {\n  if n == 0 {\n    return 0\n  }\n  return deep(n - 1)\n}\n" +
		"const A = deep(300)\n" +
		"const B = A + 1\n"
	m, diags := analyze(src)
	n := 0
	for _, code := range codes(diags) {
		if code == CodeUnfoldedConst {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("unfolded_const count = %d, want exactly 1 (at A; B's cause is A's), got %v", n, codes(diags))
	}
	if !strings.Contains(diags[0].Message, "depth") {
		t.Errorf("message = %q, want the depth reason", diags[0].Message)
	}
	for _, c := range m.Consts {
		if c.Eval != nil {
			t.Errorf("const %s published Eval %v, want none", c.Name, c.Eval)
		}
	}
}

// TestUnfoldedAssocConstReported: the totality side covers associated
// constants too — a clean impl-block constant without a value errs, named
// Type.Name.
func TestUnfoldedAssocConstReported(t *testing.T) {
	src := "pub fn deep(n: nint): nint {\n  if n == 0 {\n    return 0\n  }\n  return deep(n - 1)\n}\n" +
		"pub type C = string impl {\n  pub const D = deep(300)\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeUnfoldedConst) {
		t.Fatalf("want unfolded_const for C.D, got %v", codes(diags))
	}
	found := false
	for _, d := range diags {
		if d.Code == CodeUnfoldedConst && strings.Contains(d.Message, "C.D") {
			found = true
		}
	}
	if !found {
		t.Errorf("the assoc const diagnostic should name C.D: %v", diags)
	}
}

// TestValueRevivesWhenTypeFixed pins the editing story: while an annotation is
// broken the value is withheld (a broken declaration has no value — the hover
// goes blank), and the moment the type is fixed the value returns, through the
// incremental engine.
func TestValueRevivesWhenTypeFixed(t *testing.T) {
	e := newEditable([]byte("const A: Bogus = 1\n"))
	if m := e.prog.Module(soleFileID); m.Consts[0].Eval != nil {
		t.Fatalf("broken A published Eval %v, want none", m.Consts[0].Eval)
	}
	// Fix the annotation: Bogus -> nint. The value must be back immediately.
	e.edit(source.Edit{Start: 9, End: 14, NewText: []byte("nint")})
	m := e.prog.Module(soleFileID)
	if m.Consts[0].Eval == nil || m.Consts[0].Eval.String() != "1" {
		t.Errorf("fixed A Eval = %v, want 1 (the value revives with the type)", m.Consts[0].Eval)
	}
}

// TestValueBrokenReaderWithheld: a value-range-broken constant (overflow —
// its IR type stays a concrete sized type, so no Invalid taint flows) still
// withholds its readers' values through the published-dependency walk, and
// the reader is not re-reported.
func TestValueBrokenReaderWithheld(t *testing.T) {
	m, diags := analyze("const X: sbyte = 999\nconst Y: nint = X + 1\n")
	if !hasCode(diags, CodeConstantOverflow) {
		t.Fatalf("want constant_overflow at X, got %v", codes(diags))
	}
	if hasCode(diags, CodeUnfoldedConst) {
		t.Errorf("the reader must not pile on unfolded_const: %v", codes(diags))
	}
	for _, c := range m.Consts {
		if c.Eval != nil {
			t.Errorf("const %s published Eval %v, want none", c.Name, c.Eval)
		}
	}
}

// TestAssertOverValueBrokenConstNotGreen: the assert twin — a condition
// reading an overflowing constant must not publish a green outcome, even
// though its condition type is a perfectly healthy bool.
func TestAssertOverValueBrokenConstNotGreen(t *testing.T) {
	m, diags := analyze("const X: sbyte = 999\nassert X == 999\n")
	if !hasCode(diags, CodeConstantOverflow) {
		t.Fatalf("want constant_overflow at X, got %v", codes(diags))
	}
	if m.Asserts[0].Eval != nil {
		t.Errorf("assert over an overflowing const published Eval %v, want none", m.Asserts[0].Eval)
	}
}

// TestLambdaReaderSuppressed: the dependency walk reaches through an applied
// function literal's body, so a reader of a failed constant through a lambda
// is suppressed exactly as a direct reader — one failure, one diagnostic.
func TestLambdaReaderSuppressed(t *testing.T) {
	src := "pub fn deep(n: nint): nint {\n  if n == 0 {\n    return 0\n  }\n  return deep(n - 1)\n}\n" +
		"const D = deep(300)\n" +
		"const A = (fn(): nint -> D + 1)()\n"
	_, diags := analyze(src)
	n := 0
	for _, code := range codes(diags) {
		if code == CodeUnfoldedConst {
			n++
		}
	}
	if n != 1 {
		t.Errorf("unfolded_const count = %d, want exactly 1 (at D; A reads it through the lambda), got %v", n, codes(diags))
	}
}

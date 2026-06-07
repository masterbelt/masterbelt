// These tests pin the member-aware value-range check: a sized-integer value
// flowing into a union is range-checked against the *member* it tags, not the
// union it passes through (whose Fits is a pass-through) — the gap that let an
// out-of-range arithmetic result or literal fold tagged with a sized member a
// later match would trust.
package semantic

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// constEval returns the named constant's folded value, or nil when it did not
// fold (the soundness refusal) or the constant is absent.
func constEval(m *ir.Module, name string) *ir.Constant {
	for _, c := range m.Consts {
		if c.Name == name {
			return c.Eval
		}
	}
	return nil
}

// countCode counts how many diagnostics carry the given code.
func countCode(diags []diagnostic.Diagnostic, code diagnostic.Code) int {
	n := 0
	for _, d := range diags {
		if d.Code == code {
			n++
		}
	}
	return n
}

// TestUnionMemberArithOverflow covers the range gap: a sized-integer arithmetic
// result (or a literal) flowing into a union is range-checked against the selected
// sized member, not the union. The overflowing value must not fold tagged.
func TestUnionMemberArithOverflow(t *testing.T) {
	cases := map[string]string{
		"add into union":     "const A: sbyte | error = sbyte(100) + sbyte(100)\n",
		"sub into union":     "const A: byte | error = byte(0) - byte(1)\n",
		"mul into union":     "const A: sbyte | error = sbyte(100) * sbyte(100)\n",
		"nominal member":     "pub type Lv = sbyte\npub type U = Lv | error\nconst A: U = Lv(100) + Lv(100)\n",
		"literal into union": "const A: byte | error = 70000\n",
		"record union field": "pub type R = { v: sbyte | error }\nconst A: R = { v: sbyte(100) + sbyte(100) }\n",
		"list union element": "const A: list<sbyte | error> = [sbyte(100) + sbyte(100)]\n",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			m, diags := analyze(src)
			if n := countCode(diags, CodeConstantOverflow); n != 1 {
				t.Fatalf("got %d constant_overflow, want exactly 1 (%v)", n, codes(diags))
			}
			// The out-of-range value must not fold — no wrong constant tagged with the
			// sized member reaches a later match dispatch.
			if v := constEval(m, "A"); v != nil {
				assertNoTaggedScalar(t, v)
			}
		})
	}
}

// assertNoTaggedScalar fails if a scalar (int) carries a union tag — the wrong-
// value fold the soundness check exists to prevent. A composite (record, list)
// may fold around an untagged element; the tagged-member channel is what a match
// dispatches on.
func assertNoTaggedScalar(t *testing.T, v *ir.Constant) {
	t.Helper()
	if v.Kind == ir.ConstInt && v.UnionTag != nil {
		t.Errorf("an out-of-range value folded tagged as %v, want unfolded", v.UnionTag)
	}
}

// TestNestedPositionOverflowTwin pins the nested-position range check: a collection
// element, a record field, and a function argument each report constant_overflow
// for an out-of-range sized value — the positions the Checked hook covers.
func TestNestedPositionOverflowTwin(t *testing.T) {
	cases := map[string]string{
		"collection element": "const L: list<short> = [70000]\n",
		"record field":       "pub type R = { x: short }\nconst V: R = { x: 70000 }\n",
		"function argument":  "fn g(x: short): nint { return 0 }\nconst R = g(70000)\n",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			_, diags := analyze(src)
			if n := countCode(diags, CodeConstantOverflow); n != 1 {
				t.Fatalf("got %d constant_overflow, want exactly 1 (%v)", n, codes(diags))
			}
		})
	}
}

// TestReturnOverflowSoundness covers the return-value site: a function or method
// whose declared result type is sized checks a returned constant against it — the
// position left wholly unchecked before (no overflow fired on a body return).
func TestReturnOverflowSoundness(t *testing.T) {
	cases := map[string]string{
		"fn overflow":     "fn make(): short { return 70000 }\n",
		"method overflow": "pub type T = nint {\n  fn make(): short { return 70000 }\n}\n",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			_, diags := analyze(src)
			if n := countCode(diags, CodeConstantOverflow); n != 1 {
				t.Fatalf("got %d constant_overflow, want exactly 1 (%v)", n, codes(diags))
			}
		})
	}
}

// TestUnionMemberInRangeFolds checks the member check does not over-fire: an
// in-range arithmetic result flows in, folds, and carries its member tag.
func TestUnionMemberInRangeFolds(t *testing.T) {
	m, diags := analyze("const A: sbyte | error = sbyte(10) + sbyte(20)\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	v := constEval(m, "A")
	if v == nil || v.String() != "30" {
		t.Fatalf("A = %v, want 30", v)
	}
	if v.UnionTag == nil {
		t.Errorf("A = %v, want a union member tag", v)
	}
}

// TestDownstreamDispatchBlockedOverflow is the end-to-end soundness contract: a
// wrong sized value never reaches a match dispatch. The overflowing const does not
// fold, so the downstream const that match-dispatches on it does not fold either.
func TestDownstreamDispatchBlockedOverflow(t *testing.T) {
	src := "pub type SB = sbyte | error\nconst A: SB = sbyte(100) + sbyte(100)\n" +
		"pub fn classify(v: SB): string { match v { sbyte s -> return \"num\"  error e -> return \"err\" } }\n" +
		"const C: string = classify(A)\n"
	m, diags := analyze(src)
	if n := countCode(diags, CodeConstantOverflow); n < 1 {
		t.Fatalf("got %d constant_overflow, want at least 1 (%v)", n, codes(diags))
	}
	if v := constEval(m, "A"); v != nil && v.Kind == ir.ConstInt && v.UnionTag != nil {
		t.Errorf("A folded tagged %v, want unfolded", v.UnionTag)
	}
	if v := constEval(m, "C"); v != nil {
		t.Errorf("C folded to %v on a wrong value, want unfolded", v)
	}
}

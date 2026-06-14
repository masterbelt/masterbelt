// These tests pin the member-aware value-soundness check: a value flowing into a
// union member (or a refined/sized type in any nested position) is range- and
// refinement-checked against the *member* it tags, not the union it passes
// through — the gap that let an out-of-range arithmetic result or a where-clause
// violation fold tagged with a member a later match would trust. The two systems
// (range and refinement) are enforced at the same set of positions, so each case
// here has an overflow twin and a refinement twin.
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

// TestUnionSameKindInflowTags pins that a value flowing into a union whose
// members share a constant kind (short | byte, both integer-backed) publishes
// the member it tags, not an untagged value. The type-blind value query cannot
// tag such an inflow on a composite (ternary) or reference node — its kind
// fallback is ambiguous — so it now defers, and the published re-fold tags it
// through the checker's explicit Adapt, exactly as the direct conversion does.
// The soundness end is the dispatch: an untagged same-kind union value would
// make a downstream match unable to choose an arm (a spurious unfolded_const);
// all three inflow forms must dispatch to the short arm.
func TestUnionSameKindInflowTags(t *testing.T) {
	src := "pub type n = short | byte\n" +
		"pub fn which(x: n): nint {\n  match x {\n    short s -> return 1\n    byte b -> return 2\n  }\n}\n" +
		"pub fn make(): n {\n  return true ? short(20) : byte(5)\n}\n" +
		"pub type Box = { x: n }\n" +
		"const Tern: n = true ? short(20) : byte(5)\n" +
		"const Ref0: short = 5\n" +
		"const Ref: n = Ref0\n" +
		"const Direct: n = short(20)\n" +
		"const Call: n = make()\n" +
		"const Alias: n = Tern\n" +
		"const Nested: Box = { x: true ? short(20) : byte(5) }\n" +
		"const RT = which(Tern)\n" +
		"const RR = which(Ref)\n" +
		"const RD = which(Direct)\n" +
		"const RC = which(Call)\n" +
		"const RA = which(Alias)\n" +
		"const RN = which(Nested.x)\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("a same-kind union inflow must fold clean, got %v", codes(diags))
	}
	for _, name := range []string{"Tern", "Ref", "Direct", "Call", "Alias"} {
		c := constEval(m, name)
		if c == nil || c.UnionTag == nil || c.UnionTag.String() != "short" {
			tag := "<nil>"
			if c != nil && c.UnionTag != nil {
				tag = c.UnionTag.String()
			}
			t.Errorf("%s published tag %s, want short (the member it flows in as)", name, tag)
		}
	}
	// Each inflow with no static type the blind query reads — a ternary, a
	// reference, a call returning the union, an alias of a union const, and a
	// union nested in a record field — must carry its tag so the match dispatches.
	for _, name := range []string{"RT", "RR", "RD", "RC", "RA", "RN"} {
		if c := constEval(m, name); c == nil || c.String() != "1" {
			got := "<unfolded>"
			if c != nil {
				got = c.String()
			}
			t.Errorf("%s = %s, want 1 (the match dispatched to the short arm)", name, got)
		}
	}
}

// TestUnionMemberArithOverflow covers system 1: a sized-integer arithmetic result
// flowing into a union is range-checked against the selected sized member, not the
// union (whose Fits passes through). The overflowing value must not fold tagged.
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

// TestUnionMemberRefinementViolation covers system 2: a where-predicate violation
// flowing into a union member runs the member's predicate (the refinedDef gate on
// the union itself misses it), so the violating value is rejected and never folds
// tagged with the refined member.
func TestUnionMemberRefinementViolation(t *testing.T) {
	port := "pub type Port = nint where self > 0\n"
	cases := map[string]string{
		"literal into union":    port + "const P: Port | error = -5\n",
		"conversion into union": port + "const P: Port | error = Port(-5)\n",
		// A named union alias unwraps through UnionType, so the member is selected
		// (and its predicate run) exactly as for the bare union.
		"named union alias": port + "pub type X = Port | error\nconst P: X = -5\n",
		// A generic union alias (optional<T> = T | null) unwraps the same way.
		"generic union alias": port + "const P: optional<Port> = -5\n",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			m, diags := analyze(src)
			if n := countCode(diags, CodeRefinementViolation); n != 1 {
				t.Fatalf("got %d refinement_violation, want exactly 1 (%v)", n, codes(diags))
			}
			if v := constEval(m, "P"); v != nil && v.Kind == ir.ConstInt && v.UnionTag != nil {
				t.Errorf("a violating value folded tagged as %v, want unfolded", v.UnionTag)
			}
		})
	}
}

// TestRefinementInNestedPositions covers system 2's broader blast radius: the
// where-predicate is enforced in every nested value position the range check
// already covers — a collection element, a record field, a function argument —
// not only the top-level annotation.
func TestRefinementInNestedPositions(t *testing.T) {
	port := "pub type Port = nint where self > 0\n"
	cases := map[string]string{
		"collection element": port + "const L: list<Port> = [-5]\n",
		"record field":       port + "pub type R = { x: Port }\nconst V: R = { x: -5 }\n",
		"function argument":  port + "fn g(x: Port): nint { return 0 }\nconst R = g(-5)\n",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			_, diags := analyze(src)
			if n := countCode(diags, CodeRefinementViolation); n != 1 {
				t.Fatalf("got %d refinement_violation, want exactly 1 (%v)", n, codes(diags))
			}
		})
	}
}

// TestNestedPositionOverflowTwin pins the range twin of the refinement nested
// cases: the same nested positions report constant_overflow for an out-of-range
// sized value, so the two systems cover the same positions.
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

// TestReturnValueSoundness covers the return-value site: a function or method
// whose declared result type is sized or refined checks a returned constant
// against it — the position left wholly unchecked before (neither overflow nor
// refinement fired on a body return).
func TestReturnValueSoundness(t *testing.T) {
	port := "pub type Port = nint where self > 0\n"
	cases := []struct {
		name, src string
		code      diagnostic.Code
	}{
		{"fn refined block", port + "fn make(): Port { return -5 }\n", CodeRefinementViolation},
		{"fn overflow", "fn make(): short { return 70000 }\n", CodeConstantOverflow},
		{"fn refined union", port + "fn make(): Port | error { return -5 }\n", CodeRefinementViolation},
		{"method refined", "pub type Port = nint where self > 0\npub type T = nint {\n  fn make(): Port { return -5 }\n}\n", CodeRefinementViolation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, diags := analyze(tc.src)
			if n := countCode(diags, tc.code); n != 1 {
				t.Fatalf("got %d %v, want exactly 1 (%v)", n, tc.code, codes(diags))
			}
		})
	}
}

// TestReturnValueDynamicUnchecked pins the conservative boundary: a return whose
// value is a parameter (not a constant) does not fold, so it is left to the
// runtime — no false positive, exactly as the const path leaves a dynamic value.
func TestReturnValueDynamicUnchecked(t *testing.T) {
	src := "pub type Port = nint where self > 0\nfn pass(x: nint): Port { return x }\n"
	_, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("a non-constant return must not be checked, got %v", codes(diags))
	}
}

// TestUnionMemberInRangeFolds checks the member check does not over-fire: an
// in-range arithmetic result and a predicate-satisfying value flow in, fold, and
// carry their member tag.
func TestUnionMemberInRangeFolds(t *testing.T) {
	cases := []struct {
		name, src, want string
	}{
		{"arith in range", "const A: sbyte | error = sbyte(10) + sbyte(20)\n", "30"},
		{"refined satisfied", "pub type Port = nint where self > 0\nconst A: Port | error = 5\n", "5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, diags := analyze(tc.src)
			if len(diags) != 0 {
				t.Fatalf("unexpected diagnostics: %v", codes(diags))
			}
			v := constEval(m, "A")
			if v == nil || v.String() != tc.want {
				t.Fatalf("A = %v, want %q", v, tc.want)
			}
			if v.UnionTag == nil {
				t.Errorf("A = %v, want a union member tag", v)
			}
		})
	}
}

// TestDownstreamDispatchBlocked is the end-to-end soundness contract: a wrong
// value never reaches a match dispatch. The violating const does not fold, so the
// downstream classify/recover const that match-dispatches on it does not fold
// either — no wrong arm is taken silently.
func TestDownstreamDispatchBlocked(t *testing.T) {
	cases := map[string]struct {
		src        string
		violating  string
		downstream string
		code       diagnostic.Code
	}{
		"overflow classify": {
			src: "pub type SB = sbyte | error\nconst A: SB = sbyte(100) + sbyte(100)\n" +
				"pub fn classify(v: SB): string { match v { sbyte s -> return \"num\"  error e -> return \"err\" } }\n" +
				"const C: string = classify(A)\n",
			violating: "A", downstream: "C", code: CodeConstantOverflow,
		},
		"refinement recover": {
			src: "pub type Port = nint where self >= 1 && self <= 65535\n" +
				"pub fn portOr(v: Port | error, fb: nint): nint { match v { Port p -> return 1  error e -> return fb } }\n" +
				"const UnionBad: Port | error = 70000\nconst Recovered = portOr(UnionBad, 999)\n",
			violating: "UnionBad", downstream: "Recovered", code: CodeRefinementViolation,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m, diags := analyze(tc.src)
			if n := countCode(diags, tc.code); n < 1 {
				t.Fatalf("got %d %v, want at least 1 (%v)", n, tc.code, codes(diags))
			}
			if v := constEval(m, tc.violating); v != nil && v.Kind == ir.ConstInt && v.UnionTag != nil {
				t.Errorf("%s folded tagged %v, want unfolded", tc.violating, v.UnionTag)
			}
			if v := constEval(m, tc.downstream); v != nil {
				t.Errorf("%s folded to %v on a wrong value, want unfolded", tc.downstream, v)
			}
		})
	}
}

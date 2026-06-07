// These tests pin the explicit-adaption write-back (F-3 §2.2): every implicit
// conversion the checker accepts — a literal width settle, a nominal adaption,
// a union inflow — becomes an ir.Adapt node in the value graph, so nothing
// converts silently in the IR and the union tag is readable off the structure
// (the inner value's type is the member the fold's UnionTag records).
package semantic

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// adaptOf asserts v is an Adapt and returns it.
func adaptOf(t *testing.T, label string, v ir.Value) *ir.Adapt {
	t.Helper()
	a, ok := v.(*ir.Adapt)
	if !ok {
		t.Fatalf("%s: value = %T, want *ir.Adapt", label, v)
	}
	return a
}

// TestAdaptWriteBack checks the three adaption kinds wrap their nodes: a
// default integer settling into a sized type, a literal adapting to a nominal
// type, and a value flowing into a union — the last nesting the width settle
// inside the tag, so the member the value tags is the inner node's type.
func TestAdaptWriteBack(t *testing.T) {
	src := "pub type Level = sbyte\n" +
		"const W: short = 1\n" +
		"const N: Level = 5\n" +
		"const U: short | error = 2\n" +
		"const P: nint = 3\n"
	module, _ := analyzeWithQueries(t, src)

	// W: the width settle — Adapt to short around the nint literal.
	w := adaptOf(t, "W", module.Consts[0].Value)
	if w.To.String() != "short" {
		t.Errorf("W adapts to %s, want short", w.To)
	}
	if lit, ok := w.Value.(*ir.IntLiteral); !ok || lit.Type.String() != "nint" {
		t.Errorf("W's inner = %T (%s), want the nint literal", w.Value, ir.TypeOf(w.Value))
	}

	// N: the nominal adaption — Adapt to Level.
	n := adaptOf(t, "N", module.Consts[1].Value)
	if n.To.String() != "Level" {
		t.Errorf("N adapts to %s, want Level", n.To)
	}

	// U: the union inflow — the tag outside, the member's width settle inside,
	// so codegen reads the tag (short) off the inner node's type.
	u := adaptOf(t, "U", module.Consts[2].Value)
	if u.To.String() != "short | error" {
		t.Errorf("U adapts to %s, want short | error", u.To)
	}
	inner := adaptOf(t, "U's member settle", u.Value)
	if inner.To.String() != "short" {
		t.Errorf("U's member settle adapts to %s, want short", inner.To)
	}

	// P: an identical expectation wraps nothing — the literal stays bare.
	if _, ok := module.Consts[3].Value.(*ir.IntLiteral); !ok {
		t.Errorf("P's value = %T, want the bare *ir.IntLiteral (no adaption)", module.Consts[3].Value)
	}
}

// TestAdaptOperands checks a call's operands adapt where the checker unified
// them: a self-typed argument against a nominal receiver takes the unified
// operand (Level + 1 adapts the literal to Level).
func TestAdaptOperands(t *testing.T) {
	src := "pub type Level = sbyte\n" +
		"const Base: Level = 5\n" +
		"const Bumped = Base.add(1)\n"
	module, _ := analyzeWithQueries(t, src)
	call, ok := module.Consts[1].Value.(*ir.Call)
	if !ok {
		t.Fatalf("Bumped's value = %T, want *ir.Call", module.Consts[1].Value)
	}
	arg := adaptOf(t, "Bumped's argument", call.Args[0])
	if arg.To.String() != "Level" {
		t.Errorf("argument adapts to %s, want Level (the unified self operand)", arg.To)
	}
}

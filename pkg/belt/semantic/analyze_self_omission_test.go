// These tests pin self omission (§4.1): in a body with a receiver, a bare name
// reads one of self's readable members (a field or getter), so power means
// self.power. The bare and explicit forms desugar to the same field/getter read
// and fold to the same value; a bare name that also names a parameter or local
// is the §4.1 ambiguity ("列名と引数名の衝突はエラー") and is reported rather than
// silently shadowed. Only readable members resolve bare — a method still needs
// self.foo(), and a table-level associated constant is read off the type
// (Type.Max), not bare.
package semantic

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// TestSelfOmissionFoldsLikeExplicit pins that a bare field read in a method body
// folds to the same value the explicit self. form does — the additive guarantee
// of §4.1. It is a red→green gate: drop the self-member step from the body leaf
// and lowering and the bare reads no longer resolve, so the const stops folding
// and the test fails.
func TestSelfOmissionFoldsLikeExplicit(t *testing.T) {
	src := "pub type Fighter = { power: nint, defense: nint } impl {\n" +
		"  pub rating(): nint {\n    return power + defense\n  }\n" +
		"}\n" +
		"pub type FighterX = { power: nint, defense: nint } impl {\n" +
		"  pub rating(): nint {\n    return self.power + self.defense\n  }\n" +
		"}\n" +
		"const Bare: Fighter = { power: 7, defense: 3 }\n" +
		"const Explicit: FighterX = { power: 7, defense: 3 }\n" +
		"const BareTotal = Bare.rating()\n" +
		"const ExplicitTotal = Explicit.rating()\n"
	bare := evalOf(t, src, "BareTotal")
	explicit := evalOf(t, src, "ExplicitTotal")
	if bare.Kind != ir.ConstInt || explicit.Kind != ir.ConstInt {
		t.Fatalf("kinds = %v/%v, want int/int", bare.Kind, explicit.Kind)
	}
	if bare.Int.Int64() != 10 || explicit.Int.Int64() != 10 {
		t.Fatalf("BareTotal=%s ExplicitTotal=%s, want 10/10", bare.Int, explicit.Int)
	}
}

// TestSelfOmissionGetterReadsGetter pins that a getter body reads a sibling
// getter bare (doubledPower means self.doubledPower) and folds — readable members
// are field ∪ getter, so a bare getter read resolves like a bare field read.
func TestSelfOmissionGetterReadsGetter(t *testing.T) {
	src := "pub type Fighter = { power: nint } impl {\n" +
		"  pub get doubled(): nint {\n    return power * 2\n  }\n" +
		"  pub get quad(): nint {\n    return doubled * 2\n  }\n" +
		"}\n" +
		"const Hero: Fighter = { power: 5 }\n" +
		"const Quad = Hero.quad\n"
	if got := evalOf(t, src, "Quad").Int.Int64(); got != 20 {
		t.Fatalf("Quad = %d, want 20 (5*2*2)", got)
	}
}

// TestSelfOmissionLowersToSelfFieldAccess pins that a bare member read lowers to
// the same FieldAccess-over-SelfValue the explicit self. form does — the §4.1
// "same desugar" guarantee, checked on the IR rather than only the folded value.
func TestSelfOmissionLowersToSelfFieldAccess(t *testing.T) {
	src := "pub type Fighter = { power: nint } impl {\n" +
		"  pub rating(): nint {\n    return power\n  }\n" +
		"}\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	body := methodBody(t, m, "Fighter", "rating")
	ret, ok := body[0].(*ir.Return)
	if !ok {
		t.Fatalf("body[0] = %T, want *ir.Return", body[0])
	}
	fa, ok := ret.Value.(*ir.FieldAccess)
	if !ok {
		t.Fatalf("return value = %T, want *ir.FieldAccess (bare power desugared to self.power)", ret.Value)
	}
	if fa.Field != "power" {
		t.Fatalf("field = %q, want power", fa.Field)
	}
	if _, ok := fa.Receiver.(*ir.SelfValue); !ok {
		t.Fatalf("receiver = %T, want *ir.SelfValue (the implicit self)", fa.Receiver)
	}
	if fa.Type == nil || fa.Type.String() != "nint" {
		t.Fatalf("field access type = %v, want nint (write-back typed the bare read)", fa.Type)
	}
}

// TestSelfMemberNameClashParam pins the §4.1 ambiguity: a bare name that is at
// once a readable member of self and a parameter is reported, not resolved to
// either. It is the canonical "列名と引数名の衝突はエラー" rule.
func TestSelfMemberNameClashParam(t *testing.T) {
	src := "pub type Box = { value: nint } impl {\n" +
		"  pub pick(value: nint): nint {\n    return value\n  }\n" +
		"}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeSelfMemberNameClash) {
		t.Fatalf("want self_member_name_clash (value is both a field and a parameter), got %v", codes(diags))
	}
}

// TestSelfMemberNameClashLocal pins that the clash rule covers a let-bound local
// the same as a parameter — the general body has lets the scope DSL does not, and
// §4.1's rule is applied uniformly rather than split between body kinds.
func TestSelfMemberNameClashLocal(t *testing.T) {
	src := "pub type Box = { value: nint } impl {\n" +
		"  pub pick(): nint {\n    let value = 1\n    return value\n  }\n" +
		"}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeSelfMemberNameClash) {
		t.Fatalf("want self_member_name_clash (value is both a field and a local), got %v", codes(diags))
	}
}

// TestSelfMemberNameClashReportedOnce pins that the several body walks sharing a
// scope yield exactly one clash diagnostic per offending use, keyed by node.
func TestSelfMemberNameClashReportedOnce(t *testing.T) {
	src := "pub type Box = { value: nint } impl {\n" +
		"  pub pick(value: nint): nint {\n    return value\n  }\n" +
		"}\n"
	_, diags := analyze(src)
	n := 0
	for _, d := range diags {
		if d.Code == CodeSelfMemberNameClash {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("self_member_name_clash count = %d, want 1 (all: %v)", n, codes(diags))
	}
}

// TestSelfOmissionParameterStillReadsBare pins that a parameter that does NOT
// clash with a member is read bare as before — the clash rule fires only on a
// shared name, leaving an ordinary parameter read untouched.
func TestSelfOmissionParameterStillReadsBare(t *testing.T) {
	src := "pub type Fighter = { power: nint } impl {\n" +
		"  pub boosted(bonus: nint): nint {\n    return power + bonus\n  }\n" +
		"}\n" +
		"const Hero: Fighter = { power: 7 }\n" +
		"const Boosted = Hero.boosted(5)\n"
	if got := evalOf(t, src, "Boosted").Int.Int64(); got != 12 {
		t.Fatalf("Boosted = %d, want 12 (7+5)", got)
	}
}

// TestSelfOmissionMethodNotBareResolved pins decision #3: a bare method name (no
// parens) is not a readable member, so it does not resolve the way a field or
// getter does — a method still needs self.foo(). The bare name lowers to nothing
// rather than silently to self.area(), so a const consuming it does not fold
// (unfolded_const) — unlike a bare field read, which folds.
func TestSelfOmissionMethodNotBareResolved(t *testing.T) {
	src := "pub type Widget = { size: nint } impl {\n" +
		"  pub area(): nint {\n    return size\n  }\n" +
		"  pub aliasArea(): nint {\n    return area\n  }\n" +
		"}\n" +
		"const W: Widget = { size: 9 }\n" +
		"const Aliased = W.aliasArea()\n"
	m, diags := analyze(src)
	if !hasCode(diags, CodeUnfoldedConst) {
		t.Fatalf("want unfolded_const (bare area is a method, not a readable member, so it does not resolve), got %v", codes(diags))
	}
	// The body return value did not lower to a self.area() call: the bare method
	// name resolved to nothing, not silently to an implicit-self method call.
	body := methodBody(t, m, "Widget", "aliasArea")
	if ret, ok := body[0].(*ir.Return); ok {
		if _, isCall := ret.Value.(*ir.Call); isCall {
			t.Fatalf("bare method name lowered to a Call (silent self.area()); want it unresolved")
		}
	}
}

// TestSelfOmissionAssocConstNotBareResolved pins decision #4: a table-level
// associated constant is read off the type (Type.Max), not bare — it is not a
// row-level readable member (field ∪ getter), so a bare reference does not
// resolve to it and a const consuming it does not fold.
func TestSelfOmissionAssocConstNotBareResolved(t *testing.T) {
	src := "pub type Health = nint impl {\n" +
		"  pub const Max = 100\n" +
		"  pub get headroom(): nint {\n    return Max\n  }\n" +
		"}\n" +
		"const H: Health = 5\n" +
		"const Headroom = H.headroom\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeUnfoldedConst) {
		t.Fatalf("want unfolded_const (bare Max is an associated constant, not a field or getter; read it as Health.Max), got %v", codes(diags))
	}
}

// methodBody returns the lowered statement body of a named method on a named
// type — the IR the bare-read desugar produces.
func methodBody(t *testing.T, m *ir.Module, typ, method string) []ir.Stmt {
	t.Helper()
	for _, def := range m.Types {
		if def.Name != typ {
			continue
		}
		for _, mt := range def.Methods {
			if mt.Name == method {
				return mt.Body
			}
		}
	}
	t.Fatalf("method %s.%s not found", typ, method)
	return nil
}

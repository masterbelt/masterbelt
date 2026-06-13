// These tests pin self omission: in a body with a receiver, a bare name reads
// one of self's readable members (a field or getter), so power means self.power.
// Resolution is last resort — a bare name reads a self member only where it
// resolves no other way (a local, parameter, type, function, namespace, or enum
// member of the same name takes that reading instead), so the feature is purely
// additive: a name that already resolves keeps its meaning, and self.X is the
// explicit form for a member that collides with another name. The bare and
// explicit forms desugar to the same field/getter read and fold to the same value.
package semantic

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// TestSelfOmissionFoldsLikeExplicit pins that a bare field read in a method body
// folds to the same value the explicit self. form does. It is a red→green gate:
// drop the self-member step from the body leaf and lowering and the bare reads no
// longer resolve, so the const stops folding and the test fails.
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
// getter bare (doubled means self.doubled) and folds — readable members are
// field ∪ getter, so a bare getter read resolves like a bare field read.
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
// the same FieldAccess-over-SelfValue the explicit self. form does — the
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

// TestSelfOmissionParameterWins pins that self omission is additive: a parameter
// (or local) named like a self field takes the parameter reading, not the self
// member, so a name that already resolves keeps its meaning and no diagnostic is
// raised.
func TestSelfOmissionParameterWins(t *testing.T) {
	src := "pub type Fighter = { power: nint } impl {\n" +
		"  pub boosted(power: nint): nint {\n    return power\n  }\n" +
		"}\n" +
		"const Hero: Fighter = { power: 7 }\n" +
		"const Boosted = Hero.boosted(5)\n"
	if got := evalOf(t, src, "Boosted").Int.Int64(); got != 5 {
		t.Fatalf("Boosted = %d, want 5 (the parameter wins over the self field)", got)
	}
}

// TestSelfOmissionTypeMemberWins pins that a member access whose receiver names a
// type reads the type member (Item.Max, an associated constant) even when the
// receiver also names a self field — self omission is last resort, so the type
// reading wins and self.Item.Max is the explicit form.
func TestSelfOmissionTypeMemberWins(t *testing.T) {
	src := "pub type Item = nint impl { pub const Max = 100 }\n" +
		"pub type Holder = { Item: Item } impl {\n" +
		"  pub peek(): nint { return Item.Max }\n" +
		"}\n" +
		"const H: Holder = { Item: 3 }\n" +
		"const V = H.peek()\n"
	if got := evalOf(t, src, "V").Int.Int64(); got != 100 {
		t.Fatalf("V = %d, want 100 (Item.Max reads the type's associated constant, not self.Item.Max)", got)
	}
}

// TestSelfOmissionTopLevelFunctionWins pins that a bare callee that names a
// top-level function calls that function even when the receiver type has a
// function-valued field of the same name — self omission is last resort, so the
// top-level function wins and self.f() is the explicit form.
func TestSelfOmissionTopLevelFunctionWins(t *testing.T) {
	src := "pub fn f(): nint { return 99 }\n" +
		"pub type Box = { f: fn(): nint } impl {\n" +
		"  pub call(): nint { return f() }\n" +
		"}\n" +
		"const B: Box = { f: fn(): nint { return 1 } }\n" +
		"const V = B.call()\n"
	if got := evalOf(t, src, "V").Int.Int64(); got != 99 {
		t.Fatalf("V = %d, want 99 (the top-level function wins over the self field)", got)
	}
}

// TestSelfOmissionMethodNotBareResolved pins that a bare method name (no parens)
// is not a readable member, so it does not resolve the way a field or getter does
// — a method still needs self.foo(). The bare name lowers to nothing rather than
// silently to self.area(), so a const consuming it does not fold (unfolded_const).
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
	body := methodBody(t, m, "Widget", "aliasArea")
	if ret, ok := body[0].(*ir.Return); ok {
		if _, isCall := ret.Value.(*ir.Call); isCall {
			t.Fatalf("bare method name lowered to a Call (silent self.area()); want it unresolved")
		}
	}
}

// TestSelfOmissionAssocConstNotBareResolved pins that a table-level associated
// constant is read off the type (Type.Max), not bare — it is not a row-level
// readable member (field ∪ getter), so a bare reference does not resolve to it
// and a const consuming it does not fold.
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

// TestSelfOmissionStaticBodyReadsConstNotSelf pins that a static method body has
// no receiver: a bare name there reads a top-level constant (or a type), never an
// implicit self.field. The lowering matches the checker, which types a static body
// with self unbound, so the const folds rather than mismatching as a self read.
func TestSelfOmissionStaticBodyReadsConstNotSelf(t *testing.T) {
	src := "pub type Box = { value: nint } impl {\n" +
		"  pub static fetch(): nint {\n    return value\n  }\n" +
		"}\n" +
		"const value = 5\n" +
		"const Fetched = Box.fetch()\n"
	if got := evalOf(t, src, "Fetched").Int.Int64(); got != 5 {
		t.Fatalf("Fetched = %d, want 5 (a static body reads the top-level const, not self.value)", got)
	}
}

// TestSelfOmissionBareEnumFieldNotUnknownMember pins that a bare enum-typed self
// field used where an enum is expected (a comparison desugars to an enum-typed
// method argument) is recognized as a resolved self read, not flagged as an
// unknown enum shorthand.
func TestSelfOmissionBareEnumFieldNotUnknownMember(t *testing.T) {
	src := "pub enum Rarity { Common, Rare }\n" +
		"pub type Card = { rarity: Rarity } impl {\n" +
		"  pub same(): bool {\n    return rarity == rarity\n  }\n" +
		"}\n" +
		"const C: Card = { rarity: Rarity.Rare }\n" +
		"const Same = C.same()\n"
	m, diags := analyze(src)
	if hasCode(diags, CodeUnknownEnumMember) {
		t.Fatalf("bare enum-typed self field reported as unknown_enum_member; want it recognized as a self read: %v", codes(diags))
	}
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	if ev := constEval(m, "Same"); ev == nil || ev.Kind != ir.ConstBool || !ev.Bool {
		t.Fatalf("Same did not fold to true (bare self enum field == itself)")
	}
}

// TestSelfOmissionMasterValidateBareFields pins that a master per-row check reads
// the row's fields bare — the validate block is a body over the row (self), so the
// reference-checking pass recognizes a bare field as a self read rather than an
// undefined name.
func TestSelfOmissionMasterValidateBareFields(t *testing.T) {
	src := "pub master Skill {\n" +
		"  record {\n    id: int,\n    cost: int,\n    power: int,\n  }\n" +
		"  primary id\n" +
		"  validate {\n    each {\n      assert power >= cost\n    }\n  }\n" +
		"}\n"
	_, diags := analyze(src)
	if hasCode(diags, CodeUndefinedName) {
		t.Fatalf("bare row field in a master validate reported as undefined_name; want it read as a self field: %v", codes(diags))
	}
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
}

// TestSelfOmissionTopLevelConstWins pins that a top-level constant wins over a
// same-named self field — self omission is last resort, and the lowering reads
// the constant before the self fallback, so the checker must too (it leaves the
// name to the constant scope rather than typing it as the field). Without this
// the checker would see the field while the lowering reads the constant — a
// divergence that shows as a spurious type_mismatch when the types differ.
func TestSelfOmissionTopLevelConstWins(t *testing.T) {
	src := "const value = 5\n" +
		"pub type Box = { value: bool } impl {\n" +
		"  pub get v(): nint { return value }\n" +
		"}\n" +
		"const B: Box = { value: false }\n" +
		"const V = B.v\n"
	m, diags := analyze(src)
	if hasCode(diags, CodeTypeMismatch) {
		t.Fatalf("type_mismatch: the checker read the bool self field while the lowering read the nint const — they must agree on the const: %v", codes(diags))
	}
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	if ev := constEval(m, "V"); ev == nil || ev.Int.Int64() != 5 {
		t.Fatalf("V did not fold to 5 (the top-level const wins over the self field)")
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

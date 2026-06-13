// These tests pin self omission: in a body with a receiver, a bare name reads
// one of self's readable members (a field or getter), so power means self.power.
// The bare and explicit forms desugar to the same field/getter read and fold to
// the same value; a bare name that also names a parameter or local is ambiguous
// and is reported rather than silently shadowed. Only readable members resolve
// bare — a method still needs self.foo(), and a table-level associated constant
// is read off the type (Type.Max), not bare.
package semantic

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// TestSelfOmissionFoldsLikeExplicit pins that a bare field read in a method body
// folds to the same value the explicit self. form does — the additive guarantee
// of self omission. It is a red→green gate: drop the self-member step from the body leaf
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

// TestSelfMemberNameClashParam pins the ambiguity: a bare name that is at once a
// readable member of self and a parameter is reported, not resolved to either.
// A column name and an argument name that collide are an error, not a shadow.
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
// the clash rule is applied uniformly rather than split between body kinds.
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

// TestSelfOmissionMethodNotBareResolved pins that a bare method name (no parens)
// is not a readable member, so it does not resolve the way a field or getter does
// — a method still needs self.foo(). The bare name lowers to nothing
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

// TestSelfOmissionBareEnumFieldNotUnknownMember pins that a bare enum-typed self
// field used where an enum is expected (a comparison desugars to an enum-typed
// method argument) is recognized as a resolved self read, not flagged as an
// unknown enum shorthand. The bare-enum-argument check exempts readable self
// members the same way it exempts a parameter, local, function, or constant.
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

// TestSelfMemberNameClashLambdaParam pins that the clash rule reaches a function
// literal's own binding: a lambda inside a method body inherits self, so a lambda
// parameter that shadows a readable self member is the same ambiguity a method
// parameter is, and is reported rather than silently taking the lambda parameter.
func TestSelfMemberNameClashLambdaParam(t *testing.T) {
	src := "pub type Box = { value: nint } impl {\n" +
		"  pub apply(): nint {\n" +
		"    let f = fn(value: nint): nint { return value }\n" +
		"    return f(1)\n  }\n" +
		"}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeSelfMemberNameClash) {
		t.Fatalf("want self_member_name_clash (lambda parameter shadows a self member), got %v", codes(diags))
	}
}

// TestSelfMemberNameClashEnumShorthand pins that a bare name that is both a
// readable member of self and a valid enum shorthand (against the expected type)
// is a clash, not silently the enum member: the checker would otherwise accept the
// enum member while the implicit-self read sees the field, a silent divergence.
func TestSelfMemberNameClashEnumShorthand(t *testing.T) {
	src := "pub enum Rarity { Rare }\n" +
		"pub type Card = { Rare: bool } impl {\n" +
		"  pub pick(): Rarity {\n    return Rare\n  }\n" +
		"}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeSelfMemberNameClash) {
		t.Fatalf("want self_member_name_clash (Rare is both a self field and an enum shorthand), got %v", codes(diags))
	}
}

// TestSelfMemberNameClashCallableFieldAndMethod pins that a bare callee that is
// both a function-valued readable member of self and a method of self is a clash:
// applying the field value or the implicit self-method call would otherwise differ
// silently between the checker and the lowering.
func TestSelfMemberNameClashCallableFieldAndMethod(t *testing.T) {
	src := "pub type Box = { f: fn(): nint } impl {\n" +
		"  pub f(): nint { return 9 }\n" +
		"  pub call(): nint { return f() }\n" +
		"}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeSelfMemberNameClash) {
		t.Fatalf("want self_member_name_clash (f is both a function-valued field and a method), got %v", codes(diags))
	}
}

// TestSelfMemberNameClashTypeMemberReceiver pins that a member-access receiver
// that is both a readable member of self and a type name is a clash: reading
// Item.Max as the type's associated constant or as self.Item.Max would otherwise
// be silently ambiguous.
func TestSelfMemberNameClashTypeMemberReceiver(t *testing.T) {
	src := "pub type Item = nint impl { pub const Max = 100 }\n" +
		"pub type Holder = { Item: Item } impl {\n" +
		"  pub peek(): nint { return Item.Max }\n" +
		"}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeSelfMemberNameClash) {
		t.Fatalf("want self_member_name_clash (Item is both a self field and a type with member Max), got %v", codes(diags))
	}
}

// TestSelfOmissionEnumShorthandMatchesExplicit pins that a bare enum-typed self
// field used in a comparison behaves exactly as the explicit self. form — both
// type-check the same way, so self omission stays additive. (Folding such a
// comparison through a member receiver is a separate, pre-existing concern that
// affects the explicit self. form identically and is out of this scope.)
func TestSelfOmissionEnumShorthandMatchesExplicit(t *testing.T) {
	bareSrc := "pub enum Rarity { Common, Rare }\n" +
		"pub type Card = { rarity: Rarity } impl {\n  pub isRare(): bool {\n    return rarity == Rare\n  }\n}\n"
	explicitSrc := "pub enum Rarity { Common, Rare }\n" +
		"pub type Card = { rarity: Rarity } impl {\n  pub isRare(): bool {\n    return self.rarity == Rare\n  }\n}\n"
	_, bareDiags := analyze(bareSrc)
	_, explicitDiags := analyze(explicitSrc)
	if hasCode(bareDiags, CodeSelfMemberNameClash) {
		t.Fatalf("bare rarity == Rare should not clash (rarity is the field, Rare the enum shorthand): %v", codes(bareDiags))
	}
	// Neither form reports a type error on Rare; they resolve it the same way.
	if hasCode(bareDiags, CodeUnknownEnumMember) != hasCode(explicitDiags, CodeUnknownEnumMember) {
		t.Fatalf("bare and explicit disagree on Rare resolution: bare=%v explicit=%v", codes(bareDiags), codes(explicitDiags))
	}
}

// TestSelfMemberNameClashEnumAssignment pins that the clash reaches the bare-enum
// assignment fast path: a bare value that is both an enum member and a readable
// member of self is a clash on the right-hand side of a reassignment too, so the
// checker (enum member) and the lowering (self read) cannot silently diverge.
func TestSelfMemberNameClashEnumAssignment(t *testing.T) {
	src := "pub enum Rarity { Common, Rare }\n" +
		"pub type Card = { Rare: bool } impl {\n" +
		"  pub pick(): Rarity {\n" +
		"    let r: Rarity = Rarity.Common\n" +
		"    r = Rare\n" +
		"    return r\n  }\n" +
		"}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeSelfMemberNameClash) {
		t.Fatalf("want self_member_name_clash (Rare is both a self field and an enum member on an assignment RHS), got %v", codes(diags))
	}
}

// TestSelfOmissionSwitchScrutineeMatchesExplicit pins that a bare enum-typed self
// field as a switch scrutinee behaves exactly as the explicit self. form. (Folding
// a switch over a member-receiver scrutinee is a separate, pre-existing concern
// that affects the explicit self. form identically and is out of this scope.)
func TestSelfOmissionSwitchScrutineeMatchesExplicit(t *testing.T) {
	body := func(scrut string) string {
		return "pub enum Rarity { Common, Rare }\n" +
			"pub type Card = { rarity: Rarity } impl {\n  pub tag(): nint {\n    switch " + scrut +
			" {\n      Rare -> return 1\n      _ -> return 0\n    }\n  }\n}\n"
	}
	_, bareDiags := analyze(body("rarity"))
	_, explicitDiags := analyze(body("self.rarity"))
	if hasCode(bareDiags, CodeSelfMemberNameClash) {
		t.Fatalf("bare switch rarity should not clash (rarity is the field, Rare an enum arm): %v", codes(bareDiags))
	}
	if hasCode(bareDiags, CodeUnknownEnumMember) != hasCode(explicitDiags, CodeUnknownEnumMember) {
		t.Fatalf("bare and explicit switch disagree on arm resolution: bare=%v explicit=%v", codes(bareDiags), codes(explicitDiags))
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

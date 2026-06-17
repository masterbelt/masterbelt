package semantic

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// TestMasterStaticSelfRelation checks that a master's static fn reads self as its
// relation: average_cost composes where/sum/count over self, type-checks clean, and
// its body lowers self to the master's relation — the same MasterRelation a bare
// master name lowers to — ready for the data-fold driver. A non-master static fn's
// self stays an error (TestStaticDiagnostics "static body cannot use self").
func TestMasterStaticSelfRelation(t *testing.T) {
	src := "pub enum Rarity {\n  Common, Rare, Legend\n}\n" +
		"pub master Cards {\n  record { id: int, cost: int, rarity: Rarity } impl {\n" +
		"    pub static fn average_cost(r: Rarity): int {\n" +
		"      let matching = self.where(fn(c) -> c.rarity == r)\n" +
		"      return matching.sum(fn(c) -> c.cost) / matching.count()\n" +
		"    }\n  }\n  primary id\n}\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("average_cost should type-check with self as the relation: %v", codes(diags))
	}
	var avg *ir.Method
	for _, def := range m.Types {
		if def.Master == nil {
			continue
		}
		for _, mm := range def.Methods {
			if mm.Name == "average_cost" {
				avg = mm
			}
		}
	}
	if avg == nil {
		t.Fatal("did not resolve the master static fn average_cost")
	}
	found := false
	ir.WalkBody(avg.Body, func(v ir.Value) bool {
		if _, ok := v.(*ir.MasterRelation); ok {
			found = true
		}
		return true
	})
	if !found {
		t.Error("average_cost body should lower self to the master's relation (MasterRelation)")
	}
}

// TestMasterStaticImplicitSelfRelation checks that an implicit self-call in a master
// static fn (a query method written without self) reads self as the relation too:
// a bare count() lowers to the same MasterRelation receiver the explicit self.count()
// does — so the data driver anchors it whether or not self was written, rather than
// to a self value it cannot anchor.
func TestMasterStaticImplicitSelfRelation(t *testing.T) {
	src := "pub master Cards {\n  record { id: int, cost: int } impl {\n" +
		"    pub static fn total(): nint {\n      return count()\n    }\n" +
		"  }\n  primary id\n}\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("an implicit self relation call should type-check: %v", codes(diags))
	}
	found := false
	for _, def := range m.Types {
		if def.Master == nil {
			continue
		}
		for _, mm := range def.Methods {
			if mm.Name != "total" {
				continue
			}
			ir.WalkBody(mm.Body, func(v ir.Value) bool {
				if _, ok := v.(*ir.MasterRelation); ok {
					found = true
				}
				return true
			})
		}
	}
	if !found {
		t.Error("an implicit self-call in a master static fn should lower to the master's relation (MasterRelation)")
	}
}

// TestStaticCall checks a static fn is called Type.name(...) and folds — the
// Type.Name path, scoped to the type.
func TestStaticCall(t *testing.T) {
	src := "pub type Celsius = { deg: nint } impl {\n" +
		"  pub static fn freezing(): Celsius {\n    return Celsius{ deg: 0 }\n  }\n" +
		"}\n" +
		"const F = Celsius.freezing()\n" +
		"assert F.deg == 0\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	if f := m.Consts[0]; f.Type.String() != "Celsius" || f.Eval == nil {
		t.Errorf("F = %v (type %s), want a folded Celsius", f.Eval, f.Type)
	}
	if !m.Asserts[0].Held() {
		t.Errorf("assert did not hold: %s", m.Asserts[0].Diagram)
	}
}

// TestStaticOverload checks an overloaded static fn selects by argument type and
// folds the right body.
func TestStaticOverload(t *testing.T) {
	src := "pub type Celsius = { deg: nint } impl {\n" +
		"  pub static fn mk(v: nint): Celsius {\n    return Celsius{ deg: v }\n  }\n" +
		"  pub static fn mk(c: Celsius): Celsius {\n    return c\n  }\n" +
		"}\n" +
		"const A = Celsius.mk(7)\n" +
		"const B = Celsius.mk(Celsius.mk(9))\n" +
		"assert A.deg == 7\n" +
		"assert B.deg == 9\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	for i, a := range m.Asserts {
		if !a.Held() {
			t.Errorf("assert %d did not hold: %s", i, a.Diagram)
		}
	}
}

// TestStaticOnEnum checks a static fn on an enum impl is called EnumName.name()
// and folds.
func TestStaticOnEnum(t *testing.T) {
	src := "pub enum Element {\n  Fire, Water\n} impl {\n  pub static fn base(): Element {\n    return Element.Fire\n  }\n}\n" +
		"const D = Element.base()\n" +
		"assert D == Element.Fire\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	if !m.Asserts[0].Held() {
		t.Errorf("assert did not hold: %s", m.Asserts[0].Diagram)
	}
}

// TestStaticDiagnostics covers the static call and body error paths.
func TestStaticDiagnostics(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want diagnostic.Code
	}{
		{
			"unknown static fn",
			"pub type C = { d: nint } impl {\n  pub static fn make(): C {\n    return C{ d: 0 }\n  }\n}\nconst X = C.nope()\n",
			CodeUnknownStatic,
		},
		{
			"static body cannot use self",
			"pub type C = { d: nint } impl {\n  pub static fn bad(): nint {\n    return self.d\n  }\n}\n",
			CodeSelfOutsideMethod,
		},
		{
			"static collides with an enum member",
			"pub enum E {\n  A, B\n} impl {\n  pub static fn A(): E {\n    return E.A\n  }\n}\n",
			CodeStaticCollision,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, diags := analyze(tc.src)
			if !hasCode(diags, tc.want) {
				t.Fatalf("codes = %v, want %s", codes(diags), tc.want)
			}
		})
	}
}

// TestStaticEffect checks an effectful static fn's effect propagates: a pure
// context calling it is rejected, and a function declaring the effect is clean.
func TestStaticEffectInPureContext(t *testing.T) {
	// A static fn that declares io and a const initializer calling it: the const
	// is a pure (compile-time) context, so the io effect is rejected there.
	src := "pub type C = { d: nint } impl {\n" +
		"  pub static fn io load(): C {\n    return C{ d: 0 }\n  }\n" +
		"}\n" +
		"const X = C.load()\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeEffectInPureContext) {
		t.Fatalf("codes = %v, want effect_in_pure_context", codes(diags))
	}
}

// TestStaticAndMethodNamespacesCoexist checks an instance method parse() and a
// static fn parse() of one name live in different name spaces (value.parse() vs
// Type.parse()), so neither collides.
func TestStaticAndMethodNamespacesCoexist(t *testing.T) {
	src := "pub type C = { d: nint } impl {\n" +
		"  pub parse(): nint {\n    return self.d\n  }\n" +
		"  pub static fn parse(): C {\n    return C{ d: 0 }\n  }\n" +
		"}\n"
	_, diags := analyze(src)
	if hasCode(diags, CodeAccessorCollision) || hasCode(diags, CodeStaticCollision) || hasCode(diags, CodeDuplicateOverload) {
		t.Fatalf("a method and a static fn of one name must coexist: %v", codes(diags))
	}
}

// TestGetterAndAssocConstCoexist checks a getter max and an associated constant
// Max coexist (value.max vs Type.Max).
func TestGetterAndAssocConstCoexist(t *testing.T) {
	src := "pub type C = { d: nint } impl {\n" +
		"  pub const Max = 100\n" +
		"  pub get max(): nint {\n    return self.d\n  }\n" +
		"}\n"
	_, diags := analyze(src)
	if hasCode(diags, CodeAccessorCollision) || hasCode(diags, CodeStaticCollision) {
		t.Fatalf("a getter and an associated constant of differing-case names must coexist: %v", codes(diags))
	}
}

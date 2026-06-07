package semantic

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
)

// TestGetterReadRecord checks a getter on a record type reads as a field would
// (value.name) and folds in a constant initializer, with its result type taken
// from the getter's declared result.
func TestGetterReadRecord(t *testing.T) {
	src := "pub type Celsius = { deg: nint } impl {\n" +
		"  pub get fahrenheit(): nint {\n    return self.deg * 9 / 5 + 32\n  }\n" +
		"}\n" +
		"const C: Celsius = Celsius{ deg: 0 }\n" +
		"const F = C.fahrenheit\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	f := m.Consts[1]
	if f.Type.String() != "nint" {
		t.Errorf("F type = %s, want nint", f.Type)
	}
	if f.Eval == nil || f.Eval.String() != "32" {
		t.Errorf("F = %v, want 32", f.Eval)
	}
}

// TestGetterReadNominal checks a getter on a nominal type over a builtin base
// reads and folds (the getter computes from self).
func TestGetterReadNominal(t *testing.T) {
	src := "pub type Health = nint impl {\n  pub get band(): nint {\n    return nint(self) / 10\n  }\n}\n" +
		"const H: Health = Health(70)\n" +
		"const B = H.band\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	if b := m.Consts[1]; b.Eval == nil || b.Eval.String() != "7" {
		t.Errorf("B = %v, want 7", b.Eval)
	}
}

// TestGetterReadEnum checks a getter on an enum impl reads off a member and
// folds to a bool.
func TestGetterReadEnum(t *testing.T) {
	src := "pub enum Element {\n  Fire, Water\n} impl {\n  pub get hot(): bool {\n    return self == Element.Fire\n  }\n}\n" +
		"const A = Element.Fire.hot\n" +
		"const B = Element.Water.hot\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	if a := m.Consts[0]; a.Eval == nil || a.Eval.String() != "true" {
		t.Errorf("A = %v, want true", a.Eval)
	}
	if b := m.Consts[1]; b.Eval == nil || b.Eval.String() != "false" {
		t.Errorf("B = %v, want false", b.Eval)
	}
}

// TestGetterReadInAssert checks a getter read folds inside an assertion, the
// compile-time check the feature exists for.
func TestGetterReadInAssert(t *testing.T) {
	src := "pub type Celsius = { deg: nint } impl {\n" +
		"  pub get fahrenheit(): nint {\n    return self.deg * 9 / 5 + 32\n  }\n" +
		"}\n" +
		"const C: Celsius = Celsius{ deg: 100 }\n" +
		"assert C.fahrenheit == 212\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	if !m.Asserts[0].Held() {
		t.Errorf("assert did not hold: %s", m.Asserts[0].Diagram)
	}
}

// TestAccessorDeclDiagnostics covers the declaration-site checks for accessors
// and static fns: signature rules, name-space collisions, and generic static.
func TestAccessorDeclDiagnostics(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want diagnostic.Code
	}{
		{
			"getter with a parameter",
			"type C = { d: nint } impl {\n  get bad(x: nint): nint {\n    return self.d\n  }\n}\n",
			CodeInvalidGetterSignature,
		},
		{
			"setter without a parameter",
			"type C = { d: nint } impl {\n  set bad(): self {\n    return self\n  }\n}\n",
			CodeInvalidSetterSignature,
		},
		{
			"setter not returning self",
			"type C = { d: nint } impl {\n  set bad(v: nint): nint {\n    return self.d\n  }\n}\n",
			CodeInvalidSetterSignature,
		},
		{
			"getter collides with a field",
			"type C = { deg: nint } impl {\n  get deg(): nint {\n    return 0\n  }\n}\n",
			CodeAccessorCollision,
		},
		{
			"getter collides with a method",
			"type C = { d: nint } impl {\n  deg(): nint {\n    return self.d\n  }\n  get deg(): nint {\n    return self.d\n  }\n}\n",
			CodeAccessorCollision,
		},
		{
			"duplicate getter",
			"type C = { d: nint } impl {\n  get deg(): nint {\n    return self.d\n  }\n  get deg(): nint {\n    return self.d\n  }\n}\n",
			CodeAccessorCollision,
		},
		{
			"static collides with an associated constant",
			"type C = { d: nint } impl {\n  const Max = 1\n  static fn Max(): C {\n    return self\n  }\n}\n",
			CodeStaticCollision,
		},
		{
			"generic static",
			"type C = { d: nint } impl {\n  static fn wrap<T>(x: T): C {\n    return self\n  }\n}\n",
			CodeGenericStatic,
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

// TestGetterAndSetterPairNoCollision checks a getter and a setter of one name
// are the property and do not collide with each other.
func TestGetterAndSetterPairNoCollision(t *testing.T) {
	src := "type C = { d: nint } impl {\n" +
		"  get p(): nint {\n    return self.d\n  }\n" +
		"  set p(v: nint): self {\n    return self\n  }\n" +
		"}\n"
	_, diags := analyze(src)
	if hasCode(diags, CodeAccessorCollision) {
		t.Fatalf("a getter/setter pair must not collide: %v", codes(diags))
	}
}

// TestGetterAndMethodDifferentNamesCoexist checks the prelude interaction: a
// getter read (xs.g) and a method call (xs.g(i)) of one name on a user type live
// in different name spaces — a getter `g` and a method `g` of *different*
// signatures (the getter takes none, the method one) are still a collision, so
// this pins the legal case: a getter and a method of *different names*.
func TestNamespacesCoexist(t *testing.T) {
	src := "type C = { d: nint } impl {\n" +
		"  get band(): nint {\n    return self.d\n  }\n" +
		"  static fn band(): C {\n    return self\n  }\n" +
		"}\n"
	// A getter `band` and a static fn `band` are different name spaces
	// (value.band vs C.band()), so neither the accessor nor the static collision
	// fires.
	_, diags := analyze(src)
	if hasCode(diags, CodeAccessorCollision) || hasCode(diags, CodeStaticCollision) {
		t.Fatalf("a getter and a static fn of one name must coexist: %v", codes(diags))
	}
}

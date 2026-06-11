// These tests pin value-position rejection of a type parameter: a generic
// parameter T is a compile-time type, not a foldable value, so consuming it in
// value position — projecting a member off it (let y = T.x) or using it bare
// (T == string) — cannot fold to a concrete value at the definition site and is
// rejected as type_param_in_value_position. The supported surfaces stay the
// type-position projection (type R = T.x) and the value read off a value of that
// type (v.x). The detection is type-checker-integrated: it fires at the value
// leaf where the name resolves to nothing but a type parameter (not a local,
// parameter, constant, namespace import, or enum member), so the shadowing
// cases below are correct by construction rather than by a syntactic
// approximation.
package semantic

import "testing"

func TestValuePositionProjectionRejected(t *testing.T) {
	// A type parameter is a compile-time type, not a foldable value: projecting a
	// member off it in value position (let y = T.x, return T.x) cannot fold to a
	// concrete type value at the definition site, so it is rejected rather than
	// passing vacuously.
	for _, body := range []string{"  let y = T.x\n  return v.x\n", "  return T.x\n"} {
		src := "pub interface HasX {\n  x: nint\n}\n" +
			"pub fn f<T: HasX>(v: T): nint {\n" + body + "}\n"
		_, diags := analyze(src)
		if !hasCode(diags, CodeTypeParamInValuePosition) {
			t.Fatalf("body %q: want type_param_in_value_position, got %v", body, codes(diags))
		}
	}
}

func TestValuePositionBareTypeParamRejected(t *testing.T) {
	// A bare type parameter consumed as a value (T == string) is the same vacuous
	// fold the projection would be, so it is rejected at the definition site too.
	src := "pub interface HasX {\n  x: nint\n}\n" +
		"pub fn f<T: HasX>(v: T): nint {\n  let y = T == string\n  return v.x\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeTypeParamInValuePosition) {
		t.Fatalf("want type_param_in_value_position (bare T as a value), got %v", codes(diags))
	}
}

func TestValuePositionReportedOnce(t *testing.T) {
	// The body is walked more than once over a single scope (the checking walk
	// streams a member receiver's type and the leaf re-derives it, and the bare-
	// enum-argument walk shares the scope), so each offending use must still yield
	// exactly one diagnostic, keyed by identifier node — not one per walk.
	for _, body := range []string{"  return T.x\n", "  let y = T.x\n  return v.x\n", "  let y = T == string\n  return v.x\n"} {
		src := "pub interface HasX {\n  x: nint\n}\n" +
			"pub fn f<T: HasX>(v: T): nint {\n" + body + "}\n"
		_, diags := analyze(src)
		n := 0
		for _, d := range diags {
			if d.Code == CodeTypeParamInValuePosition {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("body %q: type_param_in_value_position count = %d, want 1 (all: %v)", body, n, codes(diags))
		}
	}
}

func TestValuePositionParameterShadowsDeclaredType(t *testing.T) {
	// A generic parameter whose name also names a declared (or builtin) type wins
	// over that type in value position, mirroring how an annotation in the same
	// scope resolves the name to the parameter — so consuming it as a value is
	// reported rather than read as the declared type's compile-time value.
	src := "pub type T = nint\n" +
		"pub fn f<T>(): bool {\n  let y = T == string\n  return y\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeTypeParamInValuePosition) {
		t.Fatalf("want type_param_in_value_position (the parameter shadows the declared type), got %v", codes(diags))
	}
}

func TestValuePositionBareNamespaceNameStillRejected(t *testing.T) {
	// The namespace-shadow exception applies only to a qualified member read (T.x
	// reads the import's member); a bare use of the name is still the type
	// parameter consumed as a value, since a namespace cannot supply a value, so it
	// is reported.
	diags := analyzeProject(t, map[string]string{
		"mod.belt":  "pub const x: nint = 5\n",
		"main.belt": "use T from \"mod.belt\"\npub fn f<T>(): nint {\n  let y = T == string\n  return 1\n}\n",
	})
	if !hasCode(diags, CodeTypeParamInValuePosition) {
		t.Fatalf("want type_param_in_value_position (bare T is the parameter, not a namespace read), got %v", codes(diags))
	}
}

func TestValuePositionTypeParamStaticCallNoSpuriousEffect(t *testing.T) {
	// A static call through a type parameter (T.foo()) is read consistently by the
	// type checker and the effect walker: the checker treats T as the type parameter
	// (not a same-named declared type), so the effect walker must not follow the
	// declared type's static fn and report effects from a callee that was not
	// selected — no spurious missing_effect.
	src := "pub type T = nint impl {\n  pub fn io foo(): nint {\n    return 1\n  }\n}\n" +
		"pub fn f<T>(): nint {\n  return T.foo()\n}\n"
	_, diags := analyze(src)
	if hasCode(diags, CodeMissingEffect) {
		t.Fatalf("want no missing_effect (the effect walker must not follow a declared type the checker did not select), got %v", codes(diags))
	}
}

func TestValuePositionTypePositionUsesNotRejected(t *testing.T) {
	// A type-position use of the parameter is not a value-position projection: a
	// conversion T(x) names the type, and a let annotation (let y: T) is a type
	// expression, so neither is type_param_in_value_position.
	for _, body := range []string{"  let y = T(v.x)\n  return v.x\n", "  let y: T = v\n  return v.x\n"} {
		src := "pub interface HasX {\n  x: nint\n}\n" +
			"pub fn f<T: HasX>(v: T): nint {\n" + body + "}\n"
		_, diags := analyze(src)
		if hasCode(diags, CodeTypeParamInValuePosition) {
			t.Fatalf("body %q: want no type_param_in_value_position (a type position), got %v", body, codes(diags))
		}
	}
}

func TestValuePositionLocalShadowNotRejected(t *testing.T) {
	// A value binding that reuses the type parameter's name shadows it for the
	// statements it scopes — a let, a loop variable — so the reused name reads the
	// local value and is not flagged. The body checker scopes these bindings, so
	// the value leaf resolves to the local before reaching the type parameter.
	for _, body := range []string{
		"  let T = 1\n  return T\n",
		"  let acc = 0\n  for T of [1, 2] {\n    acc = T\n  }\n  return acc\n",
	} {
		src := "pub fn f<T>(): nint {\n" + body + "}\n"
		_, diags := analyze(src)
		if hasCode(diags, CodeTypeParamInValuePosition) {
			t.Fatalf("body %q: want no type_param_in_value_position (the name is a local), got %v", body, codes(diags))
		}
	}
}

func TestValuePositionLambdaBodyRejected(t *testing.T) {
	// A type parameter projected in a lambda body is the same vacuous value-
	// position read, so it is flagged there too — the checking walk descends into
	// the lambda body carrying the enclosing type-parameter scope.
	src := "pub interface HasX {\n  x: nint\n}\n" +
		"pub fn f<T: HasX>(v: T): fn(): nint {\n  return fn(): nint {\n    return T.x\n  }\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeTypeParamInValuePosition) {
		t.Fatalf("want type_param_in_value_position (T.x in a lambda body), got %v", codes(diags))
	}
}

func TestValuePositionLambdaParamShadowNotRejected(t *testing.T) {
	// A lambda parameter that reuses the type parameter's name shadows it within
	// the lambda body, so the name reads the lambda's value parameter and is not
	// flagged.
	src := "pub fn f<T>(): fn(nint): nint {\n  return fn(T: nint): nint {\n    return T\n  }\n}\n"
	_, diags := analyze(src)
	if hasCode(diags, CodeTypeParamInValuePosition) {
		t.Fatalf("want no type_param_in_value_position (T is the lambda parameter), got %v", codes(diags))
	}
}

func TestValuePositionConstShadowNotRejected(t *testing.T) {
	// A top-level constant of the type parameter's name is a value the body reads
	// before reifying a type, so it shadows the parameter and the bare read is not
	// flagged — the same shadowing the body binder applies.
	src := "pub const T: nint = 1\n" +
		"pub fn f<T>(): nint {\n  return T\n}\n"
	_, diags := analyze(src)
	if hasCode(diags, CodeTypeParamInValuePosition) {
		t.Fatalf("want no type_param_in_value_position (T is a top-level constant), got %v", codes(diags))
	}
}

func TestValuePositionAfterWildcardArmRejected(t *testing.T) {
	// An after-wildcard switch arm is unreachable but still type-checked, so a
	// value-position projection in one is flagged there too, not only in a live arm.
	src := "pub interface HasX {\n  x: nint\n}\n" +
		"pub fn f<T: HasX>(v: T): nint {\n  switch v.x {\n    _ -> {\n      return v.x\n    }\n    1 -> {\n      return T.x\n    }\n  }\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeTypeParamInValuePosition) {
		t.Fatalf("want type_param_in_value_position (T.x in an after-wildcard arm), got %v", codes(diags))
	}
}

func TestValuePositionEnumMemberArmNotRejected(t *testing.T) {
	// A switch arm pattern is matched against the scrutinee, so a bare name there is
	// an enum member of the scrutinee's enum, not a value read of a type parameter
	// — even when an enum member shares a type parameter's name.
	src := "pub enum R {\n  T\n  U\n}\n" +
		"pub fn f<T>(r: R): nint {\n  switch r {\n    T -> {\n      return 1\n    }\n    U -> {\n      return 2\n    }\n  }\n}\n"
	_, diags := analyze(src)
	if hasCode(diags, CodeTypeParamInValuePosition) {
		t.Fatalf("want no type_param_in_value_position (T is an enum member pattern), got %v", codes(diags))
	}
}

func TestValuePositionNonEnumSwitchArmRejected(t *testing.T) {
	// A switch arm value is exempt from the value-position check only when it is an
	// enum member of the scrutinee's enum. A non-enum scrutinee makes a bare type
	// parameter in an arm value a compile-time value comparison, so it is flagged.
	src := "pub interface HasX {\n  x: nint\n}\n" +
		"pub fn f<T: HasX>(v: T, n: nint): nint {\n  switch n {\n    T -> {\n      return 1\n    }\n    _ -> {\n      return 2\n    }\n  }\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeTypeParamInValuePosition) {
		t.Fatalf("want type_param_in_value_position (T compared against a non-enum scrutinee), got %v", codes(diags))
	}
}

func TestValuePositionNamespaceShadowNotRejected(t *testing.T) {
	// A type parameter spelled like a namespace import is read as a namespace member
	// access (the import wins in value position), so T.x is not flagged as a value
	// use of the type parameter.
	diags := analyzeProject(t, map[string]string{
		"mod.belt":  "pub const x: nint = 5\n",
		"main.belt": "use T from \"mod.belt\"\npub fn f<T>(): nint {\n  return T.x\n}\n",
	})
	if hasCode(diags, CodeTypeParamInValuePosition) {
		t.Fatalf("want no type_param_in_value_position (T is a namespace import), got %v", codes(diags))
	}
}

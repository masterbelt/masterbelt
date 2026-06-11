// These tests pin bounded projection: T.X where T is a generic parameter whose
// bound is an interface that requires a readable member X — projecting to X's
// required read type, the bound version of read/projection symmetry. self in the
// required type resolves to the parameter; a member the bound does not require as
// readable (a method, an unknown name) and an unbounded parameter are each the
// matching diagnostic. The body read x.X on a bounded parameter (already resolved
// through the bound) is pinned alongside.
package semantic

import "testing"

func TestBoundedProjectionTypePosition(t *testing.T) {
	// T.x where T's bound requires the readable member x: nint projects to nint,
	// the required read type — symmetric with reading v.x on a value of type T.
	src := "pub interface HasX {\n  x: nint\n}\n" +
		"pub type R<T: HasX> = { y: T.x }\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("want clean (T.x projects the bound's readable requirement), got %v", codes(diags))
	}
	if got := fieldType(m, "R", "y"); got == nil || got.String() != "nint" {
		t.Fatalf("R.y = %v, want nint (T.x off the bound)", got)
	}
}

func TestBoundedProjectionSelf(t *testing.T) {
	// A readable requirement typed self projects to the parameter: T.me where the
	// bound requires me: self is T itself.
	src := "pub interface Selfish {\n  me: self\n}\n" +
		"pub type R<T: Selfish> = { y: T.me }\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("want clean (self resolves to the parameter), got %v", codes(diags))
	}
	if got := fieldType(m, "R", "y"); got == nil || got.String() != "T" {
		t.Fatalf("R.y = %v, want T (self -> the parameter)", got)
	}
}

func TestBoundedProjectionGenericBound(t *testing.T) {
	// A generic bound substitutes its argument into the requirement: T: Box<string>
	// requiring value: U projects T.value to string.
	src := "pub interface Box<U> {\n  value: U\n}\n" +
		"pub type R<T: Box<string>> = { y: T.value }\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("want clean (generic bound argument substituted), got %v", codes(diags))
	}
	if got := fieldType(m, "R", "y"); got == nil || got.String() != "string" {
		t.Fatalf("R.y = %v, want string (Box<string> requires value: string)", got)
	}
}

func TestBoundedProjectionInheritedParent(t *testing.T) {
	// A readable requirement inherited from the bound's parent interface projects
	// too: T: Sub where Sub: Base and Base requires x: nint projects T.x to nint.
	src := "pub interface Base {\n  x: nint\n}\n" +
		"pub interface Sub: Base {}\n" +
		"pub type R<T: Sub> = { y: T.x }\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("want clean (inherited bound requirement), got %v", codes(diags))
	}
	if got := fieldType(m, "R", "y"); got == nil || got.String() != "nint" {
		t.Fatalf("R.y = %v, want nint (x inherited by Sub from Base)", got)
	}
}

func TestBoundedProjectionMethodNotAType(t *testing.T) {
	// A method requirement is not a readable member, so projecting it is
	// member_is_not_a_type, the same as projecting a method off a concrete type.
	src := "pub interface HasM {\n  m(): nint\n}\n" +
		"pub type R<T: HasM> = { y: T.m }\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeMemberIsNotAType) {
		t.Fatalf("want member_is_not_a_type (a method is not readable), got %v", codes(diags))
	}
}

func TestBoundedProjectionUnknownMember(t *testing.T) {
	// A name the bound does not require is not projectable.
	src := "pub interface HasX {\n  x: nint\n}\n" +
		"pub type R<T: HasX> = { y: T.bogus }\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeUnknownField) {
		t.Fatalf("want unknown_field (bogus is not required by the bound), got %v", codes(diags))
	}
}

func TestBoundedProjectionUnbounded(t *testing.T) {
	// An unbounded parameter has no members to project.
	src := "pub type R<T> = { y: T.x }\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeTypeHasNoFields) {
		t.Fatalf("want type_has_no_fields (an unbounded parameter has no members), got %v", codes(diags))
	}
}

func TestBoundedProjectionBodyReadStillWorks(t *testing.T) {
	// The value read v.x on a bounded parameter resolves through the bound to the
	// required type — the projection's value half, pinned here. A field-backed and
	// a getter-backed implementor read the same.
	src := "pub interface HasX {\n  x: nint\n}\n" +
		"pub fn f<T: HasX>(v: T): nint {\n  return v.x\n}\n"
	_, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("want clean (v.x reads the bound's requirement), got %v", codes(diags))
	}
}

func TestBoundedProjectionValuePositionRejected(t *testing.T) {
	// A type parameter is a compile-time type, not a foldable value: projecting a
	// member off it in value position (let y = T.x) cannot fold to a concrete type
	// value at the definition site, so it is rejected rather than passing vacuously.
	// The supported surfaces are the type-position projection (type R = T.x) and
	// the value read off a value of that type (v.x).
	for _, body := range []string{"  let y = T.x\n  return v.x\n", "  return T.x\n"} {
		src := "pub interface HasX {\n  x: nint\n}\n" +
			"pub fn f<T: HasX>(v: T): nint {\n" + body + "}\n"
		_, diags := analyze(src)
		if !hasCode(diags, CodeTypeParamInValuePosition) {
			t.Fatalf("body %q: want type_param_in_value_position, got %v", body, codes(diags))
		}
	}
}

func TestBoundedProjectionBareTypeParamValueRejected(t *testing.T) {
	// A bare type parameter consumed as a value (T == string) is the same vacuous
	// fold the projection would be, so it is rejected at the definition site too.
	src := "pub interface HasX {\n  x: nint\n}\n" +
		"pub fn f<T: HasX>(v: T): nint {\n  let y = T == string\n  return v.x\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeTypeParamInValuePosition) {
		t.Fatalf("want type_param_in_value_position (bare T as a value), got %v", codes(diags))
	}
}

func TestBoundedProjectionTypePositionUsesNotRejected(t *testing.T) {
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

func TestBoundedProjectionLocalShadowNotRejected(t *testing.T) {
	// A value binding that reuses the type parameter's name shadows it for the
	// statements it scopes — a let, a loop variable, a match binding — so the
	// reused name reads the local value and is not flagged. The body checker
	// scopes these bindings; the value-position walk must agree.
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

func TestBoundedProjectionLambdaBodyRejected(t *testing.T) {
	// A type parameter projected in a lambda body is the same vacuous value-
	// position read, so it is flagged there too — the shared expression walk does
	// not enter a lambda body, so the walk descends into it explicitly.
	src := "pub interface HasX {\n  x: nint\n}\n" +
		"pub fn f<T: HasX>(v: T): fn(): nint {\n  return fn(): nint {\n    return T.x\n  }\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeTypeParamInValuePosition) {
		t.Fatalf("want type_param_in_value_position (T.x in a lambda body), got %v", codes(diags))
	}
}

func TestBoundedProjectionLambdaParamShadowNotRejected(t *testing.T) {
	// A lambda parameter that reuses the type parameter's name shadows it within
	// the lambda body, so the name reads the lambda's value parameter and is not
	// flagged.
	src := "pub fn f<T>(): fn(nint): nint {\n  return fn(T: nint): nint {\n    return T\n  }\n}\n"
	_, diags := analyze(src)
	if hasCode(diags, CodeTypeParamInValuePosition) {
		t.Fatalf("want no type_param_in_value_position (T is the lambda parameter), got %v", codes(diags))
	}
}

func TestBoundedProjectionConstShadowNotRejected(t *testing.T) {
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

func TestBoundedProjectionAfterWildcardArmRejected(t *testing.T) {
	// An after-wildcard switch arm is unreachable but still type-checked, so a
	// value-position projection in one is flagged there too, not only in a live arm.
	src := "pub interface HasX {\n  x: nint\n}\n" +
		"pub fn f<T: HasX>(v: T): nint {\n  switch v.x {\n    _ -> {\n      return v.x\n    }\n    1 -> {\n      return T.x\n    }\n  }\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeTypeParamInValuePosition) {
		t.Fatalf("want type_param_in_value_position (T.x in an after-wildcard arm), got %v", codes(diags))
	}
}

func TestBoundedProjectionInSignatureAndBodyAnnotation(t *testing.T) {
	// The type-position projection T.x resolves wherever a type annotation reaches a
	// bounded parameter, not only at a top-level declaration: a function result and
	// a body let annotation both read the bound's required member through the
	// registry-backed resolver.
	for _, src := range []string{
		"pub interface HasX {\n  x: nint\n}\n" + "pub fn f<T: HasX>(v: T): T.x {\n  return v.x\n}\n",
		"pub interface HasX {\n  x: nint\n}\n" + "pub fn f<T: HasX>(v: T): nint {\n  let y: T.x = v.x\n  return y\n}\n",
	} {
		_, diags := analyze(src)
		if len(diags) != 0 {
			t.Fatalf("want clean (T.x resolves in a signature/body annotation), got %v", codes(diags))
		}
	}
}

func TestBoundedProjectionMethodTypeParamAnnotation(t *testing.T) {
	// A method's own bounded type parameter carries its bound into the body scope,
	// so a body annotation projecting off it (let y: T.x where T: HasX) resolves
	// through the bound exactly as a function type parameter does.
	src := "pub interface HasX {\n  x: nint\n}\n" +
		"pub type Box = { z: nint } impl {\n  pub fn g<T: HasX>(v: T): nint {\n    let y: T.x = v.x\n    return y\n  }\n}\n"
	_, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("want clean (a method type parameter's bound reaches its body), got %v", codes(diags))
	}
}

func TestBoundedProjectionFunctionLiteralSignature(t *testing.T) {
	// A function literal's signature resolves a bounded projection too: a lambda
	// fn(w: T): T.x reads the bound's member, so its result is nint. Assigning that
	// result where a string is wanted is the mismatch that proves it resolved to
	// nint rather than silently to an invalid type that conflicts with nothing.
	src := "pub interface HasX {\n  x: nint\n}\n" +
		"pub fn f<T: HasX>(v: T): nint {\n  let g = fn(w: T): T.x {\n    return w.x\n  }\n  let bad: string = g(v)\n  return v.x\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeTypeMismatch) {
		t.Fatalf("want type_mismatch (the lambda's T.x result is nint, not string), got %v", codes(diags))
	}
}

func TestBoundedProjectionCallSiteResult(t *testing.T) {
	// A call to a generic function whose result is a bounded projection resolves
	// that result at the call site: get<T: HasX>(v: T): T.x called on a Point is
	// nint, so returning it where a string is declared is a mismatch — the result
	// is not silently invalid, which would lose the call's type checking.
	src := "pub interface HasX {\n  x: nint\n}\n" +
		"pub type Point = { x: nint } impl HasX {}\n" +
		"pub fn get<T: HasX>(v: T): T.x {\n  return v.x\n}\n" +
		"pub fn caller(p: Point): string {\n  return get(p)\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeTypeMismatch) {
		t.Fatalf("want type_mismatch (get(p) is nint, not string), got %v", codes(diags))
	}
}

func TestBoundedProjectionEnumMemberArmNotRejected(t *testing.T) {
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

func TestBoundedProjectionBoundProjectsOffAnotherParam(t *testing.T) {
	// A parameter's bound may project off another parameter's readable member,
	// regardless of declaration order: T: Box<U.x> reads U's required x (nint)
	// whether U is declared before or after T. The bounds are settled before any is
	// read by another, so neither order leaves the projection resolving against an
	// unbounded placeholder.
	prelude := "pub interface HasX {\n  x: nint\n}\n" +
		"pub interface Box<E> {\n  e: E\n}\n"
	for _, params := range []string{"<T: Box<U.x>, U: HasX>", "<U: HasX, T: Box<U.x>>"} {
		src := prelude + "pub fn f" + params + "(u: U): nint {\n  return u.x\n}\n"
		_, diags := analyze(src)
		if len(diags) != 0 {
			t.Fatalf("params %q: want clean (U.x projects U's bound member), got %v", params, codes(diags))
		}
	}
	// A genuinely malformed projection in a bound — U has no member nope — still
	// reports, in the second resolution pass rather than the throwaway first, so a
	// real error is not lost while the ordering is fixed.
	bad := prelude + "pub fn f<T: Box<U.nope>, U: HasX>(u: U): nint {\n  return u.x\n}\n"
	if _, diags := analyze(bad); !hasCode(diags, CodeUnknownField) {
		t.Fatalf("want unknown_field (U has no member nope), got %v", codes(diags))
	}
}

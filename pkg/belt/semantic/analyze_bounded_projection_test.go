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

func TestBoundedProjectionDeclAndMethodBoundProjectOffParam(t *testing.T) {
	// The settle-then-retry bound resolution is shared, so a bound projecting off
	// another parameter resolves in a type declaration, an interface declaration,
	// and a method's own type parameters too, not only in a free function.
	prelude := "pub interface HasX {\n  x: nint\n}\n" +
		"pub interface Box<E> {\n  e: E\n}\n"
	srcs := []string{
		prelude + "pub type R<T: Box<U.x>, U: HasX> = { y: U.x }\n",
		prelude + "pub interface I<T: Box<U.x>, U: HasX> {\n  v: U.x\n}\n",
		prelude + "pub type C = { z: nint } impl {\n  pub fn g<T: Box<U.x>, U: HasX>(u: U): nint {\n    return u.x\n  }\n}\n",
	}
	for _, src := range srcs {
		if _, diags := analyze(src); len(diags) != 0 {
			t.Fatalf("want clean (a bound projects off another parameter), got %v", codes(diags))
		}
	}
}

func TestBoundedProjectionBoundDiagnosticsStillReported(t *testing.T) {
	// Settling the bounds silently before re-resolving must not swallow a bound's
	// own diagnostic: a bound that resolves to a valid type while still reporting —
	// a wrong type-argument count, an argument that does not satisfy a nested
	// bound — is reported in the second pass, not lost in the silent first one.
	arity := "pub interface Box<E> {\n  e: E\n}\n" +
		"pub fn f<T: Box<nint, string>>(): nint {\n  return 1\n}\n"
	if _, diags := analyze(arity); !hasCode(diags, CodeTypeArityMismatch) {
		t.Fatalf("want type_arity_mismatch (Box takes one argument), got %v", codes(diags))
	}
	unsat := "pub interface Ord {\n  cmp(): nint\n}\n" +
		"pub interface Box<E: Ord> {\n  e: E\n}\n" +
		"pub fn f<T: Box<nint>>(): nint {\n  return 1\n}\n"
	if _, diags := analyze(unsat); !hasCode(diags, CodeBoundNotSatisfied) {
		t.Fatalf("want bound_not_satisfied (nint does not satisfy Ord), got %v", codes(diags))
	}
}

func TestBoundedProjectionForwardGenericProjectionBoundSettles(t *testing.T) {
	// A forward-referenced generic whose bound projects off a later parameter
	// (Late<T: Box<U.x>, U: HasX>) settles that bound when projected before its
	// declaration (Late.value<...>), so U.x resolves to the member type rather than
	// staying invalid — no type_has_no_fields. (The separate bound check of the
	// projection's own arguments against a user-implemented interface in type
	// position is a pre-existing materialization-order limitation, not this path.)
	src := "pub interface HasX {\n  x: nint\n}\n" +
		"pub interface Boxed<E> {\n  e: E\n}\n" +
		"pub type Good = { e: nint } impl Boxed<nint> {}\n" +
		"pub type Point = { x: nint } impl HasX {}\n" +
		"pub type Early = Late.value<Good, Point>\n" +
		"pub type Late<T: Boxed<U.x>, U: HasX> = { value: nint }\n"
	_, diags := analyze(src)
	if hasCode(diags, CodeTypeHasNoFields) {
		t.Fatalf("want U.x to settle (no type_has_no_fields), got %v", codes(diags))
	}
}

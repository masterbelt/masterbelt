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

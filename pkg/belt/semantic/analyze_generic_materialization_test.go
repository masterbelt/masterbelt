// These tests pin the materialization order of a generic application's bound
// check: a generic applied to a user-implemented type at a type-declaration or
// field position is accepted, because the argument's interface impls are read
// after every type's impls are attached — not while the declaration body is first
// resolved, when they are not yet there. A genuine bound violation is still
// reported, and an application at a signature position (already correct) is
// unchanged.
package semantic

import "testing"

const hasXBound = "pub interface HasX {\n  x: nint\n}\n"
const genOverHasX = "pub type Gen<T: HasX> = { v: nint }\n"
const userImplHasX = "pub type UserImpl = { x: nint } impl HasX {}\n"

func TestGenericApplicationUserTypeAtTypeDecl(t *testing.T) {
	// type X = Gen<UserImpl>: UserImpl meets HasX, so the application is accepted —
	// the bound is checked after impls are attached, not during body resolution.
	src := hasXBound + userImplHasX + genOverHasX + "pub type X = Gen<UserImpl>\n"
	if _, diags := analyze(src); hasCode(diags, CodeBoundNotSatisfied) {
		t.Fatalf("want no bound_not_satisfied (UserImpl meets HasX), got %v", codes(diags))
	}
}

func TestGenericApplicationUserTypeDeclarationOrderIndependent(t *testing.T) {
	// The acceptance does not depend on declaration order: UserImpl declared after
	// the application that uses it is still seen, since the bound is checked once
	// every impl is attached.
	src := hasXBound + genOverHasX + "pub type X = Gen<UserImpl>\n" + userImplHasX
	if _, diags := analyze(src); hasCode(diags, CodeBoundNotSatisfied) {
		t.Fatalf("want no bound_not_satisfied (order-independent), got %v", codes(diags))
	}
}

func TestGenericApplicationUserTypeAtFieldPosition(t *testing.T) {
	// A field typed by the application (g: Gen<UserImpl>) is resolved in the same
	// body pass, so it benefits from the deferred check too.
	src := hasXBound + userImplHasX + genOverHasX + "pub type Holder = { g: Gen<UserImpl> }\n"
	if _, diags := analyze(src); hasCode(diags, CodeBoundNotSatisfied) {
		t.Fatalf("want no bound_not_satisfied (field position), got %v", codes(diags))
	}
}

func TestGenericApplicationGenuineBoundViolationStillReported(t *testing.T) {
	// A type that does not meet the bound is still rejected: NoX has no x, so
	// Gen<NoX> is bound_not_satisfied — the deferred check reports what is genuinely
	// unmet after impls are attached, not a false negative.
	src := hasXBound + genOverHasX +
		"pub type NoX = { y: nint }\n" +
		"pub type X = Gen<NoX>\n"
	if _, diags := analyze(src); !hasCode(diags, CodeBoundNotSatisfied) {
		t.Fatalf("want bound_not_satisfied (NoX does not meet HasX), got %v", codes(diags))
	}
}

func TestGenericApplicationUserTypeAtSignaturePositionUnchanged(t *testing.T) {
	// A signature position was already correct (resolved after impls); it stays
	// clean.
	src := hasXBound + userImplHasX + genOverHasX +
		"pub fn f(g: Gen<UserImpl>): nint {\n  return g.v\n}\n"
	if _, diags := analyze(src); hasCode(diags, CodeBoundNotSatisfied) {
		t.Fatalf("want no bound_not_satisfied (signature position), got %v", codes(diags))
	}
}

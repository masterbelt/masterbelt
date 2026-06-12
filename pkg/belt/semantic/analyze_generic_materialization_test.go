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

// The deferred bound check (above) also admits F-bounded polymorphism — a self-
// referential opt-in such as `interface Ordered: Comparable<Ordered>` or `type A
// impl J<A>` where the applied generic carries a bound the argument meets only
// through that very application. Checking the bound after impls are attached is
// what lets such a declaration close: the argument is read with the self-edge in
// place. SettleBounds already reads a self-referential parameter bound (T: foo<T>)
// as a free variable, so the language admits self-reference; these tests pin that
// the impl/inheritance sites agree, and — crucially — that the admission is exact:
//
//   - it closes only when the application genuinely supplies the bound interface
//     (the generic inherits it: `J<T: I>: I`); when it does not (`J<T: I>` with no
//     `: I`), the bound stays unmet and is still reported, so the self-edge cannot
//     manufacture satisfaction out of nothing;
//   - it never substitutes for conformance: a closing self-impl whose argument
//     lacks a required method of the bound interface is still rejected, so the
//     circular bound can never make a method appear that the type does not have.
//
// These cases look like the "self-fulfilling" admissions an eager, pre-attachment
// check would reject, but rejecting them would break F-bounded polymorphism and,
// at an interface-parent position, every concrete-type argument (no type's impls
// are attached yet while interfaces resolve). The boundary pinned here is what
// separates the admitted F-bounded form from a genuine violation.

func TestFBoundedInterfaceParentClosesWhenInherited(t *testing.T) {
	// interface J: K<J> where K<T: I>: I. J meets K's bound T: I through inheriting
	// K<J>, which carries I — the canonical F-bounded form. Accepted.
	src := "pub interface I {}\n" +
		"pub interface K<T: I>: I {}\n" +
		"pub interface J: K<J> {}\n"
	if _, diags := analyze(src); hasCode(diags, CodeBoundNotSatisfied) {
		t.Fatalf("want no bound_not_satisfied (F-bounded parent closes via inherited I), got %v", codes(diags))
	}
}

func TestFBoundedInterfaceParentUnmetWhenNotInherited(t *testing.T) {
	// interface J: K<J> where K<T: I> does NOT inherit I. J then meets I through
	// nothing — the self-edge does not supply it — so the bound is genuinely unmet
	// and is reported. This is the boundary the deferral must not cross.
	src := "pub interface I {}\n" +
		"pub interface K<T: I> {}\n" +
		"pub interface J: K<J> {}\n"
	if _, diags := analyze(src); !hasCode(diags, CodeBoundNotSatisfied) {
		t.Fatalf("want bound_not_satisfied (J cannot meet I; K does not supply it), got %v", codes(diags))
	}
}

func TestFBoundedImplClosesWhenInherited(t *testing.T) {
	// type A impl J<A> where J<T: I>: I. A meets J's bound T: I through implementing
	// J<A>, which carries I. Accepted — the impl-site twin of the parent case above.
	src := "pub interface I {}\n" +
		"pub interface J<T: I>: I {}\n" +
		"pub type A = { v: nint } impl J<A> {}\n"
	if _, diags := analyze(src); hasCode(diags, CodeBoundNotSatisfied) {
		t.Fatalf("want no bound_not_satisfied (F-bounded impl closes via inherited I), got %v", codes(diags))
	}
}

func TestFBoundedImplUnmetWhenNotInherited(t *testing.T) {
	// type A impl J<A> where J<T: I> does NOT inherit I. A meets I through nothing,
	// so the bound is genuinely unmet and reported.
	src := "pub interface I {}\n" +
		"pub interface J<T: I> {}\n" +
		"pub type A = { v: nint } impl J<A> {}\n"
	if _, diags := analyze(src); !hasCode(diags, CodeBoundNotSatisfied) {
		t.Fatalf("want bound_not_satisfied (A cannot meet I; J does not supply it), got %v", codes(diags))
	}
}

func TestFBoundedImplStillRequiresConformingMethods(t *testing.T) {
	// A closing self-impl does not bypass conformance: I requires m(), and A does
	// not declare it, so impl J<A> is rejected for the missing method — the bound
	// closing circularly cannot conjure a method A lacks.
	src := "pub interface I {\n  fn m(): nint\n}\n" +
		"pub interface J<T: I>: I {}\n" +
		"pub type A = { v: nint } impl J<A> {}\n"
	if _, diags := analyze(src); !hasCode(diags, CodeMissingRequiredMethod) {
		t.Fatalf("want missing_required_method (A lacks I.m), got %v", codes(diags))
	}
}

func TestFBoundedImplWithConformingMethodAccepted(t *testing.T) {
	// The same self-impl with m() supplied is genuinely sound — A has every method
	// the closed bound implies — and is accepted with no diagnostic.
	src := "pub interface I {\n  fn m(): nint\n}\n" +
		"pub interface J<T: I>: I {}\n" +
		"pub type A = { v: nint } impl J<A> {\n  fn m(): nint {\n    return self.v\n  }\n}\n"
	if _, diags := analyze(src); len(diags) != 0 {
		t.Fatalf("want no diagnostics (A supplies I.m; F-bounded impl is sound), got %v", codes(diags))
	}
}

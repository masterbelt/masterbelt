// These tests pin a static-fn requirement on an interface (static name(): T) and
// its call through a bounded type parameter (T.name()). The requirement is
// conformed by a matching static fn, resolved through the bound the way an
// instance method is, and read consistently by the type checker, the lowering,
// and the effect walker. A name the bound does not require as a static is not a
// static call: it falls through to the value-position reading, so an operator
// desugaring or a typo is not mistaken for one.
package semantic

import "testing"

const hasDefault = "pub interface HasDefault {\n  static defaultValue(): nint\n}\n"
const counterImpl = "pub type Counter = { n: nint } impl HasDefault {\n  pub static fn defaultValue(): nint {\n    return 0\n  }\n}\n"

func TestStaticRequirementConformance(t *testing.T) {
	// A type that names the interface must declare a matching static fn; a missing
	// one, or an instance method of the same name (the wrong kind), is
	// missing_required_static.
	cases := []struct {
		name string
		impl string
		want bool // expect missing_required_static
	}{
		{"matching static fn", "impl HasDefault {\n  pub static fn defaultValue(): nint {\n    return 0\n  }\n}\n", false},
		{"no static fn", "impl HasDefault {}\n", true},
		{"instance method not static", "impl HasDefault {\n  pub fn defaultValue(): nint {\n    return 0\n  }\n}\n", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := hasDefault + "pub type Counter = { n: nint } " + tc.impl
			_, diags := analyze(src)
			if got := hasCode(diags, CodeMissingRequiredStatic); got != tc.want {
				t.Fatalf("missing_required_static = %v, want %v (all: %v)", got, tc.want, codes(diags))
			}
		})
	}
}

func TestStaticRequirementBodyRejected(t *testing.T) {
	// A provided (default) static is not supported, so a static requirement with a
	// body is reported rather than treated as provided.
	src := "pub interface HasDefault {\n  static defaultValue(): nint {\n    return 0\n  }\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeStaticMemberHasBody) {
		t.Fatalf("want static_member_has_body, got %v", codes(diags))
	}
}

func TestStaticRequirementCallThroughBound(t *testing.T) {
	// T.defaultValue() resolves through the bound to the requirement's result
	// (nint): calling it where nint is wanted is clean, and where a string is wanted
	// is the mismatch that proves it typed as nint rather than staying invalid.
	clean := hasDefault + counterImpl + "pub fn s<T: HasDefault>(): nint {\n  return T.defaultValue()\n}\n"
	if _, diags := analyze(clean); len(diags) != 0 {
		t.Fatalf("want clean (T.defaultValue() resolves to nint through the bound), got %v", codes(diags))
	}
	bad := hasDefault + counterImpl + "pub fn s<T: HasDefault>(): string {\n  return T.defaultValue()\n}\n"
	if _, diags := analyze(bad); !hasCode(diags, CodeTypeMismatch) {
		t.Fatalf("want type_mismatch (T.defaultValue() is nint, not string), got %v", codes(diags))
	}
}

func TestStaticRequirementReceiverNotValuePosition(t *testing.T) {
	// The receiver of a resolved static call (T.defaultValue()) is a type position,
	// so it is not reported as a type parameter consumed as a value.
	src := hasDefault + counterImpl + "pub fn s<T: HasDefault>(): nint {\n  return T.defaultValue()\n}\n"
	_, diags := analyze(src)
	if hasCode(diags, CodeTypeParamInValuePosition) {
		t.Fatalf("want no type_param_in_value_position (a static call's receiver is a type position), got %v", codes(diags))
	}
}

func TestStaticRequirementSelfReturnFactory(t *testing.T) {
	// A static requirement typed self is a factory: T.make() returns the parameter
	// itself. Returning it where the parameter is wanted is clean; where a nint is
	// wanted is the mismatch that proves it typed as T.
	prelude := "pub interface Makeable {\n  static make(): self\n}\n" +
		"pub type Widget = { id: nint } impl Makeable {\n  pub static fn make(): Widget {\n    return Widget { id: 0 }\n  }\n}\n"
	if _, diags := analyze(prelude + "pub fn build<T: Makeable>(): T {\n  return T.make()\n}\n"); len(diags) != 0 {
		t.Fatalf("want clean (T.make() returns the parameter), got %v", codes(diags))
	}
	if _, diags := analyze(prelude + "pub fn bad<T: Makeable>(): nint {\n  return T.make()\n}\n"); !hasCode(diags, CodeTypeMismatch) {
		t.Fatalf("want type_mismatch (T.make() is T, not nint), got %v", codes(diags))
	}
}

func TestStaticRequirementGenericBound(t *testing.T) {
	// A generic bound substitutes its argument into the static's result: Box<E>
	// requiring empty(): E, with T: Box<nint>, gives T.empty(): nint.
	prelude := "pub interface Box<E> {\n  static empty(): E\n}\n" +
		"pub type IntBox = { v: nint } impl Box<nint> {\n  pub static fn empty(): nint {\n    return 0\n  }\n}\n"
	if _, diags := analyze(prelude + "pub fn e<T: Box<nint>>(): nint {\n  return T.empty()\n}\n"); len(diags) != 0 {
		t.Fatalf("want clean (T.empty() is nint through Box<nint>), got %v", codes(diags))
	}
	if _, diags := analyze(prelude + "pub fn eb<T: Box<nint>>(): string {\n  return T.empty()\n}\n"); !hasCode(diags, CodeTypeMismatch) {
		t.Fatalf("want type_mismatch (T.empty() is nint, not string), got %v", codes(diags))
	}
}

func TestStaticRequirementInheritedFromParent(t *testing.T) {
	// A static fn required by a parent interface is reachable through a child bound:
	// T: Sub where Sub: Base and Base requires the static resolves T.defaultValue().
	src := hasDefault +
		"pub interface Sub: HasDefault {}\n" +
		"pub fn s<T: Sub>(): nint {\n  return T.defaultValue()\n}\n"
	if _, diags := analyze(src); len(diags) != 0 {
		t.Fatalf("want clean (a parent's static requirement is reachable through the child bound), got %v", codes(diags))
	}
}

func TestStaticRequirementNonStaticMemberFallsThrough(t *testing.T) {
	// A member the bound does not require as a static is not a static call: it falls
	// through to the value-position reading. A typo (T.bogus()) and the operator
	// desugaring T == U (T.eql(U)) are both reported as the parameter consumed as a
	// value, not as a missing static.
	for _, body := range []string{"  return T.bogus()\n", "  let y = T == string\n  return 0\n"} {
		src := hasDefault + "pub fn s<T: HasDefault>(): nint {\n" + body + "}\n"
		_, diags := analyze(src)
		if hasCode(diags, CodeUnknownStatic) {
			t.Fatalf("body %q: want no unknown_static (a non-required member is not a static call), got %v", body, codes(diags))
		}
		if !hasCode(diags, CodeTypeParamInValuePosition) {
			t.Fatalf("body %q: want type_param_in_value_position (the parameter consumed as a value), got %v", body, codes(diags))
		}
	}
}

func TestStaticRequirementNeedsParamList(t *testing.T) {
	// A static requirement needs a parameter list (static x(): T). Without one
	// (static x: T) the modifier still parses, but it is reported rather than
	// silently accepted as a zero-argument static.
	src := "pub interface I {\n  static x: nint\n}\n"
	if _, diags := analyze(src); !hasCode(diags, CodeStaticMemberNeedsParams) {
		t.Fatalf("want static_member_needs_params (static x: nint has no parameter list), got %v", codes(diags))
	}
}

func TestStaticRequirementNotGeneric(t *testing.T) {
	// A static fn is not generic, so a generic static requirement is rejected — the
	// same as a concrete static fn, since the bound-call signature carries no type
	// parameters to instantiate.
	src := "pub interface I {\n  static id<A>(x: A): A\n}\n"
	if _, diags := analyze(src); !hasCode(diags, CodeGenericStatic) {
		t.Fatalf("want generic_static, got %v", codes(diags))
	}
}

func TestStaticRequirementNotCallableOnInterface(t *testing.T) {
	// A static requirement is reachable only through a bounded type parameter or an
	// implementing concrete type, never the interface itself: I.make() has no
	// implementation to call, so it is unknown rather than silently accepted.
	src := "pub interface I {\n  static make(): nint\n}\n" +
		"pub fn f(): nint {\n  return I.make()\n}\n"
	if _, diags := analyze(src); !hasCode(diags, CodeUnknownStatic) {
		t.Fatalf("want unknown_static (a requirement is not callable on the interface), got %v", codes(diags))
	}
}

func TestStaticRequirementSignatureMustMatch(t *testing.T) {
	// Conformance compares the required static's signature, not just the name: an
	// implementor whose static has a different arity or result does not satisfy it,
	// since a call through the bound is typed against the requirement.
	src := "pub interface I {\n  static make(): nint\n}\n" +
		"pub type W = { n: nint } impl I {\n  pub static fn make(s: string): string {\n    return s\n  }\n}\n"
	if _, diags := analyze(src); !hasCode(diags, CodeMissingRequiredStatic) {
		t.Fatalf("want missing_required_static (the static's signature does not match), got %v", codes(diags))
	}
}

func TestStaticRequirementNotInheritedThroughAlias(t *testing.T) {
	// A static is read off the named type itself, so it is not inherited through a
	// nominal alias: an alias over a type with a matching static does not satisfy the
	// requirement unless it declares its own — the conformance and the static-call
	// path stay consistent (Alias.make() would be unknown too).
	src := "pub interface I {\n  static make(): nint\n}\n" +
		"pub type Base = { n: nint } impl {\n  pub static fn make(): nint {\n    return 0\n  }\n}\n" +
		"pub type Alias = Base impl I {}\n"
	if _, diags := analyze(src); !hasCode(diags, CodeMissingRequiredStatic) {
		t.Fatalf("want missing_required_static (a static is not inherited through a nominal alias), got %v", codes(diags))
	}
}

func TestStaticRequirementTypeParameterWinsOverDeclaredType(t *testing.T) {
	// A type parameter wins over a same-named declared type in a static call, in the
	// checker and the lowering alike: f<T: I> calling T.foo() with a top-level type T
	// that also defines foo resolves through the bound I, not the declared type, so
	// the IR points at the callee that was type-checked. It analyzes clean.
	src := "pub interface I {\n  static foo(): nint\n}\n" +
		"pub type T = { v: nint } impl I {\n  pub static fn foo(): nint {\n    return 9\n  }\n}\n" +
		"pub fn f<T: I>(): nint {\n  return T.foo()\n}\n"
	if _, diags := analyze(src); len(diags) != 0 {
		t.Fatalf("want clean (T.foo() resolves through the bound, not the same-named declared type), got %v", codes(diags))
	}
}

func TestStaticRequirementEffectConsistent(t *testing.T) {
	// A static requirement declares no effects, so a pure generic function calling it
	// is not reported as missing one — the effect walker reads T.foo() as a static
	// call (no effect), consistent with the type checker and the lowering, in both a
	// function body and a method body.
	fn := hasDefault + "pub fn s<T: HasDefault>(): nint {\n  return T.defaultValue()\n}\n"
	if _, diags := analyze(fn); hasCode(diags, CodeMissingEffect) {
		t.Fatalf("want no missing_effect in a function body, got %v", codes(diags))
	}
	method := hasDefault + "pub type Box = { n: nint } impl {\n  pub fn m<T: HasDefault>(): nint {\n    return T.defaultValue()\n  }\n}\n"
	if _, diags := analyze(method); hasCode(diags, CodeMissingEffect) {
		t.Fatalf("want no missing_effect in a method body, got %v", codes(diags))
	}
}

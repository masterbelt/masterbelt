package semantic

import (
	"testing"
)

// TestGenericDeclBoundedParamAsConstructorArg is the declaration-site false
// positive (audit finding 1): a type parameter declared with a bound, used as a
// bounded generic constructor's argument (map<K, V> where K: comparable), must
// compile clean — the declared bound satisfies the constructor's bound. Before
// the fix the resolver dropped the bound, leaving a free TypeVar{Bound:nil} that
// failed the constructor's K: comparable check, firing bound_not_satisfied at the
// declaration regardless of any call.
func TestGenericDeclBoundedParamAsConstructorArg(t *testing.T) {
	cases := map[string]string{
		"getOr":  "pub fn getOr<K: comparable, V>(m: map<K, V>, k: K, d: V): V { return d }\n",
		"keysOf": "pub fn keysOf<K: comparable, V>(m: map<K, V>): list<K> { return [] }\n",
		"empty":  "pub fn empty<K: comparable, V>(): map<K, V> { return [] }\n",
		"alias":  "pub type Index<K: comparable> = map<K, nint>\n",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			_, diags := analyze(src)
			if hasCode(diags, CodeBoundNotSatisfied) {
				t.Fatalf("K is declared comparable; want no bound_not_satisfied, got %v", codes(diags))
			}
		})
	}
}

// TestGenericDeclUnboundedParamAsBoundedConstructorArg is the first line of
// defense against smuggling (audit finding 1/2 boundary): a type parameter with
// NO bound (or an insufficient bound), used where a bound is required (map<K, V>,
// K: comparable), is rejected at the declaration site — the free variable's
// undeclared bound cannot satisfy comparable, so map<K, V> is ill-formed
// independent of any call.
func TestGenericDeclUnboundedParamAsBoundedConstructorArg(t *testing.T) {
	cases := map[string]string{
		"fn-param":  "pub fn g<K>(m: map<K, nint>): nint { return 0 }\n",
		"fn-result": "pub fn h<K>(): map<K, nint> { return [] }\n",
		"alias":     "pub type Index<K> = map<K, nint>\n",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			_, diags := analyze(src)
			if !hasCode(diags, CodeBoundNotSatisfied) {
				t.Fatalf("unbounded K cannot satisfy map's K: comparable; want bound_not_satisfied, got %v", codes(diags))
			}
		})
	}
}

// TestGenericCallSmuggledKeyBound is the instantiation-site check (audit finding
// 2): a call that solves a type parameter to a type its bound rejects must be
// reported at the call. wrapKey<K: comparable> is well-formed at its declaration
// (K's bound comparable satisfies map's K: comparable), but a call solving
// K = Point (a non-comparable record) violates K's own bound — surfaced at the
// call by the type-parameter bound check, so the substituted map<Point, bool>
// never escapes silently.
func TestGenericCallSmuggledKeyBound(t *testing.T) {
	src := "pub type Point = { x: nint, y: nint }\n" +
		"pub fn wrapKey<K: comparable>(k: K): map<K, bool> { return [] }\n" +
		"const R = wrapKey(Point{ x: 1, y: 2 })\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeBoundNotSatisfied) {
		t.Fatalf("K solved to a non-comparable record flows into map<K, bool>; want bound_not_satisfied, got %v", codes(diags))
	}
}

// TestGenericDeclInsufficientBoundAsConstructorArg is the smuggling path's first
// line of defense: a type parameter whose declared bound does not imply the
// constructor's bound (K: foldable used as map<K, bool>, foldable does not
// inherit comparable) is rejected at the declaration site — the resolver attaches
// the foldable bound, and Satisfies(TypeVar{Bound: foldable}, comparable) is
// false. This is why no call-site re-validation of substituted constructor bounds
// is needed: a clean declaration means K's bound implies the constructor's, so
// every solution of K satisfies it transitively.
func TestGenericDeclInsufficientBoundAsConstructorArg(t *testing.T) {
	src := genericFoldableSrc +
		"pub fn wrap<K: foldable<nint, nint>>(k: K): map<K, bool> { return [] }\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeBoundNotSatisfied) {
		t.Fatalf("foldable does not imply comparable; want bound_not_satisfied at the declaration, got %v", codes(diags))
	}
}

// TestGenericCallSmuggledKeyBoundOk is the matching no-regression case: a call
// solving K to a comparable type (string) is accepted — the substituted
// map<string, bool> is well-formed.
func TestGenericCallSmuggledKeyBoundOk(t *testing.T) {
	src := "pub fn wrapKey<K: comparable>(k: K): map<K, bool> { return [] }\n" +
		"const R = wrapKey(\"hi\")\n"
	_, diags := analyze(src)
	if hasCode(diags, CodeBoundNotSatisfied) {
		t.Fatalf("string is comparable; want no bound_not_satisfied, got %v", codes(diags))
	}
}

// TestGenericBodyTypeParamAnnotation is the resolveBodyType wiring: a type
// annotation in a generic function body that names a type parameter (let y: T)
// must resolve to the parameter's TypeVar rather than being reported as an
// unknown type. Before the fix resolveBodyType resolved with a nil type-param
// scope, so T was unknown in the body.
func TestGenericBodyTypeParamAnnotation(t *testing.T) {
	src := "pub fn id<T>(x: T): T {\n" +
		"  let y: T = x\n" +
		"  return y\n" +
		"}\n"
	_, diags := analyze(src)
	if hasCode(diags, CodeUnknownType) {
		t.Fatalf("T is the function's type parameter; want no unknown_type for the body annotation, got %v", codes(diags))
	}
}

// TestGenericMethodBodyTypeParamAnnotation is the method counterpart: the
// enclosing type's parameter is in scope for a body annotation.
func TestGenericMethodBodyTypeParamAnnotation(t *testing.T) {
	src := "pub type Box<T> = list<T> impl {\n" +
		"  pub fn first(): T {\n" +
		"    let y: T = self.get(0)\n" +
		"    return y\n" +
		"  }\n" +
		"}\n"
	_, diags := analyze(src)
	if hasCode(diags, CodeUnknownType) {
		t.Fatalf("T is the enclosing type's parameter; want no unknown_type for the body annotation, got %v", codes(diags))
	}
}

// TestGenericMethodBoundEnforced pins that a generic method's type-parameter bound
// is checked at the call — the method twin of the function call's bound check, which
// the method path did not run before any bounded method existed. add<T: numeric>(x:
// T) accepts a numeric argument and rejects a non-numeric one; the bound is a real
// constraint, not a decorative annotation.
func TestGenericMethodBoundEnforced(t *testing.T) {
	const def = "pub type Box = { v: int } impl {\n" +
		"  pub fn add<T: numeric>(x: T): T {\n    return x\n  }\n}\n"
	// A numeric argument satisfies the bound.
	if _, diags := analyze(def + "fn probe(b: Box): int {\n  return b.add(5)\n}\n"); hasCode(diags, CodeBoundNotSatisfied) {
		t.Fatalf("add(5): 5 is numeric, want no bound_not_satisfied, got %v", codes(diags))
	}
	// A non-numeric argument violates it — the bound check the method path now runs.
	if _, diags := analyze(def + "fn probe(b: Box): string {\n  return b.add(\"s\")\n}\n"); !hasCode(diags, CodeBoundNotSatisfied) {
		t.Errorf(`add("s"): string is not numeric, want bound_not_satisfied`)
	}
}

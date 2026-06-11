// These tests pin a conversion through a generic type parameter, T(x): it is a
// type position naming the parameter, so it types as the parameter (its validity
// deferred to the call site that instantiates T), wins over a same-named top-level
// function (a value of the same name still shadows it), and is read the same way by
// the type checker, the lowering, and the effect walker. The lowering already
// lowers T(x) to a conversion to the parameter; these pin that the checker and the
// effect walker agree, so no pass diverges (no invalid-IR-for-accepted-code, no
// effect collected from a function the call does not select).
package semantic

import "testing"

func TestTypeParamConversionTypedAsParameter(t *testing.T) {
	// T(v) names the type parameter in conversion position, so it types as the
	// parameter T — not unresolved. Assigning it where a string is wanted is the
	// mismatch that proves it resolved to T rather than to an invalid value that
	// conflicts with nothing.
	src := "pub fn f<T>(v: T): nint {\n  let bad: string = T(v)\n  return 1\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeTypeMismatch) {
		t.Fatalf("want type_mismatch (T(v) types as the parameter T, not string), got %v", codes(diags))
	}
}

func TestTypeParamConversionInferredLetTypedAsParameter(t *testing.T) {
	// The inferred-let typing carries the type-parameter scope too, so let y = T(v)
	// settles y to the parameter type T — the same type the lowered conversion
	// carries — rather than an unknown type that would leave the binding's IR
	// inconsistent with its value. Reading y where a string is wanted is the
	// mismatch that proves the inferred binding is the parameter, not invalid.
	src := "pub fn f<T>(v: T): nint {\n  let y = T(v)\n  let bad: string = y\n  return 1\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeTypeMismatch) {
		t.Fatalf("want type_mismatch (the inferred y is the parameter T, not string), got %v", codes(diags))
	}
}

func TestTypeParamConversionWinsOverFunction(t *testing.T) {
	// A type parameter wins over a same-named top-level function in callee position:
	// T(v) is a conversion to the parameter, not a call of the function. The function
	// is effectful and the body is pure, so were it called this would be
	// missing_effect; the type parameter shadowing it means the effect walker
	// collects nothing, consistent with the checker reading T(v) as a conversion.
	src := "pub fn io T(x: nint): nint {\n  return x\n}\n" +
		"pub fn f<T>(v: nint): T {\n  return T(v)\n}\n"
	_, diags := analyze(src)
	if hasCode(diags, CodeMissingEffect) {
		t.Fatalf("want no missing_effect (T(v) is a conversion to the parameter, not a call of the effectful function), got %v", codes(diags))
	}
}

func TestTypeParamConversionWinsOverFunctionInMethodBody(t *testing.T) {
	// The same precedence holds in a generic method body, whose effect scope carries
	// the method's (and its type's) type parameters: T(v) is a conversion to the
	// parameter, not a call of the same-named effectful top-level function, so the
	// effect walker collects no effect and the pure method is not missing one.
	src := "pub fn io T(x: nint): nint {\n  return x\n}\n" +
		"pub type Box = { n: nint } impl {\n  pub fn f<T>(v: nint): T {\n    return T(v)\n  }\n}\n"
	_, diags := analyze(src)
	if hasCode(diags, CodeMissingEffect) {
		t.Fatalf("want no missing_effect (a method's T(v) is a conversion, not a call of the effectful function), got %v", codes(diags))
	}
}

func TestTypeParamConversionValueNameStillCallsFunction(t *testing.T) {
	// A value of the same name shadows the type parameter, so the call is a function-
	// value call, not a conversion: a parameter T: fn(...) is applied and the call
	// resolves clean, the function-value path winning over the conversion only when a
	// value actually binds the name.
	src := "pub fn f<T>(T: fn(nint): nint): nint {\n  return T(1)\n}\n"
	_, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("want clean (the parameter T is a callable function value), got %v", codes(diags))
	}
}

func TestTypeParamConversionArgumentChecked(t *testing.T) {
	// The conversion's arguments are value positions and are checked: T(T) reports
	// the argument T as a value-position use of the parameter, even though the callee
	// T is a type position.
	src := "pub fn f<T>(): T {\n  return T(T)\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeTypeParamInValuePosition) {
		t.Fatalf("want type_param_in_value_position (the argument T in T(T)), got %v", codes(diags))
	}
}

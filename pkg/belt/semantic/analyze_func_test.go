// This file tests functions and the callable forms: function and method bodies,
// overload selection, function literals and lambdas, self, and effect
// propagation.
package semantic

import (
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

func TestMethodBodyTypeChecks(t *testing.T) {
	// self + 1 on a nominal integer derives int8's add and returns the nominal
	// type, which matches the declared result `self` — no diagnostic.
	if _, diags := analyze("pub type Level = sbyte impl {\n  pub inc(): self {\n    return self + 1\n  }\n}\n"); len(diags) != 0 {
		t.Errorf("well-typed method body should have no diagnostics, got %v", codes(diags))
	}
}

func TestMethodBodyTypeMismatch(t *testing.T) {
	// A body returning self (an integer) where bool is declared is a mismatch.
	_, diags := analyze("pub type Bad = sbyte impl {\n  pub wrong(): bool {\n    return self\n  }\n}\n")
	if !hasCode(diags, CodeTypeMismatch) {
		t.Errorf("want type_mismatch for a bad method body, got %v", codes(diags))
	}
}

func TestMultiStatementMethodBody(t *testing.T) {
	m, _ := analyze("pub type Level = sbyte impl {\n  pub inc(): self {\n    self + 1\n    return self\n  }\n}\n")
	if len(m.Types) == 0 || len(m.Types[0].Methods) == 0 {
		t.Fatalf("Level.inc not resolved: %+v", m.Types)
	}
	body := m.Types[0].Methods[0].Body
	if len(body) != 2 {
		t.Fatalf("body has %d statements, want 2 (an expr statement and a return)", len(body))
	}
	if _, ok := body[0].(*ir.ExprStmt); !ok {
		t.Errorf("stmt 0 = %T, want *ir.ExprStmt", body[0])
	}
	if _, ok := body[1].(*ir.Return); !ok {
		t.Errorf("stmt 1 = %T, want *ir.Return", body[1])
	}
}

// overloadSrc declares a type with merge overloaded by parameter type — the
// 0013-overload example's Score — for the overload diagnostics tests.
const overloadSrc = `pub type Score = int impl {
  pub fn merge(points: self): self {
    return self + points
  }
  pub fn merge(active: bool): bool {
    return active && self > 0
  }
}
const Base: Score = 100
`

func TestOverloadResolution(t *testing.T) {
	m, diags := analyze(overloadSrc + "const Bumped = Base.merge(50)\nconst Counted = Base.merge(true)\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	// The integer argument picks merge(points: self), the boolean argument
	// merge(active: bool) — the same name resolves per call site.
	if got := m.Consts[1].Type.String(); got != "Score" {
		t.Errorf("Bumped type = %s, want Score", got)
	}
	if got := m.Consts[2].Type.String(); got != "bool" {
		t.Errorf("Counted type = %s, want bool", got)
	}
}

func TestNoMatchingOverload(t *testing.T) {
	m, diags := analyze(overloadSrc + "const X = Base.merge(\"badge\")\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeNoMatchingOverload {
		t.Fatalf("codes = %v, want [no_matching_overload]", got)
	}
	if m.Consts[1].Type.String() != "invalid" {
		t.Errorf("X type = %s, want invalid", m.Consts[1].Type)
	}
	// A single-signature method that does not fit stays invalid_operation —
	// the overload diagnostics never replace it.
	_, diags = analyze("const X = 1 + true\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeInvalidOperation {
		t.Fatalf("codes = %v, want [invalid_operation]", got)
	}
}

func TestAmbiguousOverload(t *testing.T) {
	// The default integer fits both sized overloads at once; the resolution
	// is an annotated operand, never an implicit priority.
	src := `pub type Gauge = int impl {
  pub fn set(v: sbyte): bool {
    return v > 0
  }
  pub fn set(v: short): bool {
    return v > 0
  }
}
const G: Gauge = 1
`
	_, diags := analyze(src + "const X = G.set(5)\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeAmbiguousOverload {
		t.Fatalf("codes = %v, want [ambiguous_overload]", got)
	}
	// An annotated argument is exact: unambiguous.
	m, diags := analyze(src + "const V: short = 5\nconst X = G.set(V)\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if got := m.Consts[2].Type.String(); got != "bool" {
		t.Errorf("X type = %s, want bool", got)
	}
}

func TestAnnotatedFuncLit(t *testing.T) {
	// The annotation is a checking context: it supplies the literal's omitted
	// parameter and result types.
	m, diags := analyze("const Twice: fn(x: nint): nint = fn(x) { return x * 2 }\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if got := m.Consts[0].Type.String(); got != "fn(nint): nint" {
		t.Errorf("Twice type = %s, want fn(nint): nint", got)
	}
	// A fully annotated literal under a matching annotation is fine too (this
	// used to false-positive through types.Compatible, which had no function
	// rule).
	if _, diags := analyze("const Twice: fn(x: nint): nint = fn(x: nint): nint { return x * 2 }\n"); len(diags) != 0 {
		t.Errorf("fully annotated literal: unexpected diagnostics %v", diags)
	}
}

func TestAnnotatedFuncLitDiagnostics(t *testing.T) {
	cases := []struct {
		src  string
		code diagnostic.Code
	}{
		// Parameter-count mismatch against the annotation.
		{"const B: fn(x: nint): nint = fn(x, y) { return x }\n", CodeLambdaArityMismatch},
		// A written parameter annotation must agree with the expectation.
		{"const C: fn(x: nint): nint = fn(x: string) { return \"\" }\n", CodeTypeMismatch},
		// A written result annotation must agree too.
		{"const R: fn(x: nint): nint = fn(x): string { return \"\" }\n", CodeTypeMismatch},
		// A return that does not satisfy the pushed-down result type.
		{"const S: fn(x: nint): nint = fn(x) { return x == 0 }\n", CodeTypeMismatch},
		// A literal under a non-function annotation.
		{"const N: nint = fn() { return 1 }\n", CodeTypeMismatch},
		// An operator error inside a context-typed body still surfaces.
		{"const O: fn(x: nint): nint = fn(x) { return x && x }\n", CodeInvalidOperation},
		// A literal value out of the pushed-down range.
		{"const V: fn(): sbyte = fn() { return 1000 }\n", CodeConstantOverflow},
	}
	for _, tc := range cases {
		_, diags := analyze(tc.src)
		if !hasCode(diags, tc.code) {
			t.Errorf("%q: want %s, got %v", tc.src, tc.code, codes(diags))
		}
	}
}

func TestBidirectionalCall(t *testing.T) {
	// The headline: list<T>.map's signature reaches into an unannotated
	// lambda — T = int binds from the receiver and is pushed into x, R solves
	// from the body — and the call folds at compile time.
	m, diags := analyze("const Doubled = [1, 2, 3].map(fn(x) { return x * 2 })\nconst Evens = [1, 2].map(fn(x) { return x % 2 == 0 })\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if got := m.Consts[0].Type.String(); got != "list<nint>" {
		t.Errorf("Doubled type = %s, want list<nint>", got)
	}
	if got := m.Consts[0].Eval.String(); got != "[2, 4, 6]" {
		t.Errorf("Doubled eval = %s, want [2, 4, 6]", got)
	}
	if got := m.Consts[1].Type.String(); got != "list<bool>" {
		t.Errorf("Evens type = %s, want list<bool>", got)
	}
	if got := m.Consts[1].Eval.String(); got != "[false, true]" {
		t.Errorf("Evens eval = %s, want [false, true]", got)
	}
}

// TestArrowFuncLit checks that an arrow body rides the existing FuncLit paths
// untouched: bidirectional inference, checking against an annotation, and
// compile-time evaluation all behave exactly as the block form does, because
// the arrow normalized to a single return during AST lowering.
func TestArrowFuncLit(t *testing.T) {
	m, diags := analyze("const Doubled = [1, 2, 3].map(fn(x) -> x * 2)\nconst Twice: fn(x: nint): nint = fn(x) -> x * 2\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if got := m.Consts[0].Type.String(); got != "list<nint>" {
		t.Errorf("Doubled type = %s, want list<nint>", got)
	}
	if got := m.Consts[0].Eval.String(); got != "[2, 4, 6]" {
		t.Errorf("Doubled eval = %s, want [2, 4, 6]", got)
	}
	if got := m.Consts[1].Type.String(); got != "fn(nint): nint" {
		t.Errorf("Twice type = %s, want fn(nint): nint", got)
	}

	// Body-type errors surface through the arrow form too.
	if _, diags := analyze("const S: fn(x: nint): nint = fn(x) -> x == 0\n"); !hasCode(diags, CodeTypeMismatch) {
		t.Errorf("want type_mismatch, got %v", codes(diags))
	}
}

func TestBidirectionalCallDiagnostics(t *testing.T) {
	// A lambda whose result type the call cannot solve (no return to bind R
	// from) reports the precise cause, not a generic invalid_operation.
	_, diags := analyze("const D = [1, 2].map(fn(x) {})\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeUninferableResult {
		t.Errorf("codes = %v, want exactly [uninferable_result]", got)
	}
	// An argument that is not a function at all is still the call's error.
	if _, diags := analyze("const E = [1, 2].map(3)\n"); !hasCode(diags, CodeInvalidOperation) {
		t.Errorf("want invalid_operation, got %v", codes(diags))
	}
}

func TestUninferableParameter(t *testing.T) {
	// With no checking context at all, an unannotated parameter has nothing to
	// infer from.
	_, diags := analyze("const A = fn(x) { return x }\n")
	if !hasCode(diags, CodeUninferableParameter) {
		t.Errorf("want uninferable_parameter, got %v", codes(diags))
	}
	// An annotation that pins it reports nothing.
	if _, diags := analyze("const A: fn(x: nint): nint = fn(x) { return x }\n"); len(diags) != 0 {
		t.Errorf("pinned parameter: unexpected diagnostics %v", diags)
	}
}

func TestMethodResultTypeReachesLiteral(t *testing.T) {
	// The method's declared result type checks a returned literal, so its
	// lambda parameters infer.
	src := "pub type T = sbyte impl {\n  pub f(): fn(x: nint): nint {\n    return fn(x) { return x }\n  }\n}\n"
	if _, diags := analyze(src); len(diags) != 0 {
		t.Errorf("unexpected diagnostics: %v", diags)
	}
	// And a literal that does not satisfy it is reported.
	bad := "pub type T = sbyte impl {\n  pub f(): fn(x: nint): nint {\n    return fn(x, y) { return x }\n  }\n}\n"
	if _, diags := analyze(bad); !hasCode(diags, CodeLambdaArityMismatch) {
		t.Errorf("want lambda_arity_mismatch, got %v", codes(diags))
	}
}

func TestFuncLitResultInference(t *testing.T) {
	// An omitted result type is synthesized from the body's returns; declared
	// parameter types carry into the body scope.
	m, diags := analyze("const F = fn(x: nint) { return x * 2 }\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if got := m.Consts[0].Type.String(); got != "fn(nint): nint" {
		t.Errorf("F type = %s, want fn(nint): nint", got)
	}
}

func TestFuncLitBodyDiagnostics(t *testing.T) {
	cases := []struct {
		src  string
		code diagnostic.Code
	}{
		// An operator error inside the body is reported now that the checking
		// walk descends into the literal.
		{"const F = fn(x: nint): nint { return x && x }\n", CodeInvalidOperation},
		// A return that does not satisfy the declared result type.
		{"const F = fn(x: nint): bool { return x }\n", CodeTypeMismatch},
		// Conflicting unannotated returns cannot unify.
		{"const F = fn(x: nint) { return 1\n  return true }\n", CodeTypeMismatch},
		// No result annotation and no return to infer one from.
		{"const F = fn() {}\n", CodeUninferableResult},
		// A division by zero inside the body.
		{"const F = fn(x: nint): nint { return x / 0 }\n", CodeDivisionByZero},
	}
	for _, tc := range cases {
		_, diags := analyze(tc.src)
		if !hasCode(diags, tc.code) {
			t.Errorf("%q: want %s, got %v", tc.src, tc.code, codes(diags))
		}
	}
	// A healthy annotated literal — and one whose result is inferred — report
	// nothing.
	for _, src := range []string{
		"const F = fn(x: nint): nint { return x * 2 }\n",
		"const F = fn(x: nint) { return x % 2 == 0 }\n",
		"const F = fn(): nint {}\n", // the signature is complete without a return
	} {
		if _, diags := analyze(src); len(diags) != 0 {
			t.Errorf("%q: unexpected diagnostics %v", src, diags)
		}
	}
}

// --- top-level functions --------------------------------------------------------

func TestFuncDeclAndCall(t *testing.T) {
	m, diags := analyze("pub fn double(x: nint): nint -> x * 2\nconst A = double(21)\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if len(m.Funcs) != 1 || m.Funcs[0].Name != "double" || !m.Funcs[0].Public {
		t.Fatalf("funcs = %v, want [pub double]", m.Funcs)
	}
	if m.Funcs[0].Result.String() != "nint" || len(m.Funcs[0].Params) != 1 {
		t.Errorf("double signature = %v -> %v, want (x: nint): nint", m.Funcs[0].Params, m.Funcs[0].Result)
	}
	if m.Consts[0].Type.String() != "nint" {
		t.Errorf("A type = %s, want nint", m.Consts[0].Type)
	}
	if ev := m.Consts[0].Eval; ev == nil || ev.Kind != ir.ConstInt || ev.Int.Int64() != 42 {
		t.Errorf("A eval = %v, want 42", ev)
	}
	call, ok := m.Consts[0].Value.(*ir.FuncCall)
	if !ok || call.Target != m.Funcs[0] {
		t.Errorf("A value = %v, want FuncCall -> double", m.Consts[0].Value)
	}
}

func TestFuncDiagnostics(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want diagnostic.Code
	}{
		{"result mismatch", "fn f(): nint { return \"x\" }\n", CodeTypeMismatch},
		{"arity", "fn f(x: nint): nint -> x\nconst A = f(1, 2)\n", CodeArityMismatch},
		{"undefined", "const X = unknownFn(1)\n", CodeUndefinedName},
		{"missing return", "fn g(x: nint): nint { }\n", CodeMissingReturn},
		{"unknown param type", "fn f(x: Bogus): nint -> 1\n", CodeUnknownType},
		{"duplicate signature", "fn f(): nint -> 1\nfn f(): nint -> 2\n", CodeDuplicateFuncOverload},
		{"argument mismatch", "fn f(x: nint): nint -> x\nconst A = f(\"a\")\n", CodeTypeMismatch},
		{"function is not a value", "fn f(): nint -> 1\nconst A = f\n", CodeUndefinedName},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, diags := analyze(tc.src)
			if got := codes(diags); len(got) != 1 || got[0] != tc.want {
				t.Fatalf("codes = %v, want [%s]", got, tc.want)
			}
		})
	}
}

func TestFuncArgumentOverflow(t *testing.T) {
	// The out-of-range argument reports at both boundaries it crosses: the
	// argument against the parameter's int8, and the folded result against
	// the call's int8 type.
	_, diags := analyze("fn f(x: sbyte): sbyte -> x\nconst A = f(1000)\n")
	got := codes(diags)
	if len(got) != 2 || got[0] != CodeConstantOverflow || got[1] != CodeConstantOverflow {
		t.Fatalf("codes = %v, want two constant_overflow", got)
	}
}

func TestFuncMissingReturnNotForArrowOrMissingBody(t *testing.T) {
	// An arrow body always returns; a missing body is the parser's report,
	// not a missing return on top.
	_, diags := analyze("fn f(): nint -> 1\n")
	if len(diags) != 0 {
		t.Fatalf("arrow body: unexpected diagnostics: %v", diags)
	}
	m, diags := analyze("fn f(): nint\n")
	_ = m
	for _, d := range diags {
		if d.Code == CodeMissingReturn {
			t.Fatalf("missing body reported missing_return on top of the parse error: %v", diags)
		}
	}
}

func TestFuncRecursionGuard(t *testing.T) {
	// Infinite recursion bottoms out at the depth guard — not a stack
	// overflow — and the constant that asked for it is an error: a pure
	// constant either folds or errors (unfolded_const, reason depth), never
	// silently lacks a value. The result type stays the declared one.
	m, diags := analyze("fn loop(x: nint): nint -> loop(x)\nconst X = loop(1)\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeUnfoldedConst {
		t.Fatalf("codes = %v, want [unfolded_const]", got)
	}
	if !strings.Contains(diags[0].Message, "depth") {
		t.Errorf("message = %q, want the depth reason", diags[0].Message)
	}
	if m.Consts[0].Type.String() != "nint" {
		t.Errorf("X type = %s, want nint", m.Consts[0].Type)
	}
	if m.Consts[0].Eval != nil {
		t.Errorf("X eval = %v, want unevaluated", m.Consts[0].Eval)
	}
}

func TestFuncCalledFromMethodBody(t *testing.T) {
	src := "fn double(x: nint): nint -> x * 2\n" +
		"pub type T = sbyte impl {\n  pub f(): nint {\n    return double(3)\n  }\n}\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	// The method body's call lowers to a FuncCall bound to the module's
	// function shell.
	ret, ok := m.Types[0].Methods[0].Body[0].(*ir.Return)
	if !ok {
		t.Fatalf("method body = %v, want a return", m.Types[0].Methods[0].Body)
	}
	call, ok := ret.Value.(*ir.FuncCall)
	if !ok || call.Target != m.Funcs[0] {
		t.Errorf("method return = %v, want FuncCall -> double", ret.Value)
	}
}

func TestFuncCalledFromLambda(t *testing.T) {
	m, diags := analyze("fn double(x: nint): nint -> x * 2\nconst D = [1, 2].map(fn(x) -> double(x))\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if got := m.Consts[0].Eval.String(); got != "[2, 4]" {
		t.Errorf("D eval = %s, want [2, 4]", got)
	}
}

func TestLambdaParamShadowsFunc(t *testing.T) {
	// A literal's parameter named like a function shadows it: the body's f is
	// the int element, not the function.
	m, diags := analyze("fn f(x: nint): nint -> x\nconst A = [1].map(fn(f) -> f + 1)\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if got := m.Consts[0].Eval.String(); got != "[2]" {
		t.Errorf("A eval = %s, want [2]", got)
	}
}

func TestFuncMutualRecursionGuard(t *testing.T) {
	// Mutual recursion through two functions bottoms out at the depth guard,
	// and the constant errs exactly as direct recursion does.
	src := "fn a(x: nint): nint -> b(x)\nfn b(x: nint): nint -> a(x)\nconst X = a(1)\n"
	m, diags := analyze(src)
	if got := codes(diags); len(got) != 1 || got[0] != CodeUnfoldedConst {
		t.Fatalf("codes = %v, want [unfolded_const]", got)
	}
	if m.Consts[0].Eval != nil {
		t.Errorf("X eval = %v, want unevaluated", m.Consts[0].Eval)
	}
}

func TestFuncInAssert(t *testing.T) {
	m, diags := analyze("fn area(w: nint, h: nint): nint -> w * h\nassert area(3, 4) == 12\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if len(m.Asserts) != 1 || !m.Asserts[0].Held() {
		t.Fatalf("assert = %v, want held", m.Asserts)
	}
}

func TestSelfOutsideMethod(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{"function arrow body", "fn f(): nint -> self + 1\n"},
		{"function block body", "fn f(): nint { return self }\n"},
		{"lambda in function body", "fn f(): list<nint> -> [1].map(fn(x) -> self)\n"},
		{"const initializer", "const A = self\n"},
		{"lambda in const initializer", "const A = [1].map(fn(x) -> self)\n"},
		{"assert condition", "assert self == 1\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, diags := analyze(tc.src)
			found := false
			for _, d := range diags {
				if d.Code == CodeSelfOutsideMethod {
					found = true
				}
			}
			if !found {
				t.Fatalf("codes = %v, want self_outside_method", codes(diags))
			}
		})
	}
}

func TestSelfAllowedInMethodAndWhere(t *testing.T) {
	// A method body and a where clause have a receiver: self stays legal.
	src := "type Port = int where self >= 1\n" +
		"pub type L = sbyte impl {\n  pub double(): sbyte {\n    return self * 2\n  }\n}\n"
	_, diags := analyze(src)
	for _, d := range diags {
		if d.Code == CodeSelfOutsideMethod {
			t.Fatalf("self_outside_method fired in a legal position: %v", diags)
		}
	}
}

// --- function overloads ---------------------------------------------------------

func TestFuncOverloadSelection(t *testing.T) {
	// Same name, different parameter kinds: the argument type selects the
	// overload, in typing and in folding.
	src := "fn f(x: nint): nint -> 1\nfn f(x: string): nint -> 2\n" +
		"const A = f(9)\nconst B = f(\"a\")\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if got := m.Consts[0].Eval.String(); got != "1" {
		t.Errorf("A eval = %s, want 1 (the nint overload)", got)
	}
	if got := m.Consts[1].Eval.String(); got != "2" {
		t.Errorf("B eval = %s, want 2 (the string overload)", got)
	}
	if len(m.Funcs) != 2 {
		t.Errorf("module funcs = %d, want both overloads", len(m.Funcs))
	}
}

func TestFuncOverloadByArity(t *testing.T) {
	src := "fn f(): nint -> 0\nfn f(x: nint): nint -> x\nconst A = f()\nconst B = f(7)\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if a, b := m.Consts[0].Eval.String(), m.Consts[1].Eval.String(); a != "0" || b != "7" {
		t.Errorf("evals = %s, %s, want 0 and 7", a, b)
	}
}

func TestFuncOverloadDiagnostics(t *testing.T) {
	overloads := "fn f(x: nint): nint -> 1\nfn f(x: string): nint -> 2\n"
	cases := []struct {
		name string
		src  string
		want diagnostic.Code
	}{
		{"no match", overloads + "const A = f(true)\n", CodeNoMatchingFuncOverload},
		{"no match by arity", overloads + "const A = f(1, 2)\n", CodeNoMatchingFuncOverload},
		{"ambiguous", "fn g(x: sbyte): nint -> 1\nfn g(x: int): nint -> 2\nconst A = g(1)\n", CodeAmbiguousFuncOverload},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, diags := analyze(tc.src)
			if got := codes(diags); len(got) != 1 || got[0] != tc.want {
				t.Fatalf("codes = %v, want [%s]", got, tc.want)
			}
		})
	}
}

func TestFuncOverloadAnnotatedArgSelects(t *testing.T) {
	// A concretely typed argument disambiguates same-kind overloads. The
	// type-blind value query stays conservative and does not pick; the
	// assembler's late re-fold then applies the checker's recorded selection,
	// so the constant folds through the right overload rather than staying
	// silently unfolded (gap (d) of the fold-totality plan).
	src := "fn g(x: sbyte): nint -> 1\nfn g(x: int): nint -> 2\n" +
		"const B: sbyte = 1\nconst A = g(B)\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if m.Consts[1].Type.String() != "nint" {
		t.Errorf("A type = %s, want nint", m.Consts[1].Type)
	}
	if m.Consts[1].Eval == nil || m.Consts[1].Eval.String() != "1" {
		t.Errorf("A eval = %v, want 1 (the checker-selected sbyte overload)", m.Consts[1].Eval)
	}
}

func TestFuncOverloadRecordArgDefers(t *testing.T) {
	// An inferred record literal cannot select an overload; a typed one and
	// the other arguments do, and the winner's parameter reaches into it.
	src := "pub type Point = { x: nint, y: nint }\n" +
		"fn f(p: Point, tag: nint): nint -> tag\n" +
		"fn f(p: Point, tag: string): nint -> 9\n" +
		"const A = f({ x: 1, y: 2 }, 5)\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if got := m.Consts[0].Eval.String(); got != "5" {
		t.Errorf("A eval = %s, want 5", got)
	}
}

// nondetRoot is the effects fixture rooted in the registry's real effectful
// native: roll() declares nondet and reads datetime.now(), so the effect
// chain bottoms out in a native the toolchain actually supplies. The extern
// spelling is reserved for the builtin surface, so tests root their effects
// here rather than in an extern declaration.
const nondetRoot = "pub fn nondet roll(): nint {\n" +
	"  return datetime.now() > D1970-01-01T00:00:00.000Z ? 1 : 0\n" +
	"}\n"

// ioAsyncRoot is the io/async fixture: those effects have no registry-supplied
// native yet (dormant until one lands), so the fixture justifies its effects
// through its own recursive await — the machinery stays exercised in pure user
// code, with no extern.
const ioAsyncRoot = "pub fn io async fetch(url: string): string {\n" +
	"  return await fetch(url)\n" +
	"}\n"

func TestEffectDeclarationsPropagate(t *testing.T) {
	// A function calling an effectful one must declare the effects itself;
	// declaring them silences the check, and awaiting consumes async.
	src := ioAsyncRoot +
		"pub fn io async page(url: string): string {\n" +
		"  return await fetch(url)\n" +
		"}\n"
	if _, diags := analyze(src); len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
}

func TestMissingEffect(t *testing.T) {
	// Each undeclared-but-used effect is reported once, at the first site.
	src := ioAsyncRoot +
		"pub fn page(url: string): string {\n" +
		"  return fetch(url)\n" +
		"}\n"
	_, diags := analyze(src)
	n := 0
	for _, d := range diags {
		if d.Code == CodeMissingEffect {
			n++
		}
	}
	if n != 2 { // io and async, both undeclared
		t.Fatalf("missing_effect count = %d, want 2 (got %v)", n, codes(diags))
	}

	// await outside an async declaration is itself a missing async.
	src = nondetRoot +
		"pub fn nondet f(): nint {\n" +
		"  return await roll()\n" +
		"}\n"
	if _, diags := analyze(src); !hasCode(diags, CodeMissingEffect) {
		t.Errorf("await without async: want missing_effect, got %v", codes(diags))
	}
}

func TestMissingEffectOnMethod(t *testing.T) {
	src := ioAsyncRoot +
		"pub type Client = { base: string } impl {\n" +
		"  pub fn get(path: string): string {\n" +
		"    return await fetch(self.base + path)\n" +
		"  }\n" +
		"}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeMissingEffect) {
		t.Fatalf("want missing_effect on the method, got %v", codes(diags))
	}
}

func TestMissingEffectOnImplicitSelfMethodCall(t *testing.T) {
	// A method calling a sibling method with self omitted (roll(), not self.roll())
	// uses that sibling's effects: a pure method making an effectful implicit call
	// is missing the effect, the same as the explicit self.roll() form. The bare
	// callee resolves through self after a top-level function, so the effect walk
	// must reach it there or the implicit call escapes the check.
	src := "pub type T = sbyte impl {\n" +
		"  pub fn nondet roll(): datetime {\n    return datetime.now()\n  }\n" +
		"  pub fn pick(): datetime {\n    return roll()\n  }\n" +
		"}\n"
	if _, diags := analyze(src); !hasCode(diags, CodeMissingEffect) {
		t.Fatalf("want missing_effect for an implicit self-method effectful call, got %v", codes(diags))
	}
	// Declaring the effect on the caller satisfies it — no spurious missing_effect.
	ok := "pub type T = sbyte impl {\n" +
		"  pub fn nondet roll(): datetime {\n    return datetime.now()\n  }\n" +
		"  pub fn nondet pick(): datetime {\n    return roll()\n  }\n" +
		"}\n"
	if _, diags := analyze(ok); hasCode(diags, CodeMissingEffect) {
		t.Fatalf("a caller declaring the effect must not report missing_effect, got %v", codes(diags))
	}
}

func TestEffectChargesSelectedOverloadOnly(t *testing.T) {
	// An effect check charges the overload the checker selected, not the union of
	// every same-named candidate's effects. With a nondet at(string) and a pure
	// at(int), a pure caller selecting at(int) must not be flagged for nondet —
	// the false positive the conservative union produced. Selecting the effectful
	// at(string) must still report, so the precision does not let an effect escape.
	tBody := func(call string) string {
		return "pub type T = sbyte impl {\n" +
			"  pub fn nondet at(s: string): int {\n    return 0\n  }\n" +
			"  pub fn at(n: int): int {\n    return n\n  }\n" +
			"  pub fn pick(): int {\n    return " + call + "\n  }\n" +
			"}\n"
	}
	fnBody := func(call string) string {
		return "pub fn nondet at(s: string): int {\n  return 0\n}\n" +
			"pub fn at(n: int): int {\n  return n\n}\n" +
			"pub fn pick(): int {\n  return " + call + "\n}\n"
	}
	cases := []struct {
		name    string
		src     string
		wantEff bool
	}{
		{"implicit selects pure", tBody("at(1)"), false},
		{"explicit selects pure", tBody("self.at(1)"), false},
		{"top-level selects pure", fnBody("at(1)"), false},
		{"implicit selects effectful", tBody("at(\"x\")"), true},
		{"explicit selects effectful", tBody("self.at(\"x\")"), true},
		{"top-level selects effectful", fnBody("at(\"x\")"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, diags := analyze(tc.src)
			if got := hasCode(diags, CodeMissingEffect); got != tc.wantEff {
				t.Fatalf("missing_effect = %v, want %v (codes %v)", got, tc.wantEff, codes(diags))
			}
		})
	}
}

func TestMissingEffectOnNativeStaticCall(t *testing.T) {
	// The registry's effectful native is a root like any other effectful
	// callee: a pure fn reading datetime.now() directly is missing nondet.
	src := "pub fn f(): datetime {\n  return datetime.now()\n}\n"
	if _, diags := analyze(src); !hasCode(diags, CodeMissingEffect) {
		t.Errorf("want missing_effect for a bare datetime.now() call, got %v", codes(diags))
	}
}

func TestUnusedEffect(t *testing.T) {
	// A declared effect the body never uses is a warning; a declaration whose
	// effect bottoms out in the registry's native root is not.
	_, diags := analyze("pub fn io f(): nint -> 1\n")
	if !hasCode(diags, CodeUnusedEffect) {
		t.Fatalf("want unused_effect, got %v", codes(diags))
	}
	if _, diags := analyze("pub fn nondet stamp(): datetime -> datetime.now()\n"); len(diags) != 0 {
		t.Errorf("a wrapper over the native root should be green, got %v", codes(diags))
	}
}

func TestEffectPropagatesThroughLambda(t *testing.T) {
	// A literal's body executes where it is applied, so its effect uses
	// count toward the enclosing declaration.
	src := nondetRoot +
		"pub fn f(): nint {\n" +
		"  return [1].map(fn(x) -> x + roll()).count()\n" +
		"}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeMissingEffect) {
		t.Errorf("want missing_effect through the lambda, got %v", codes(diags))
	}
}

func TestEffectInPureContext(t *testing.T) {
	// A compile-time position — a constant initializer, an assert condition —
	// must be pure: an effectful call (or an await) cannot appear in it.
	roots := nondetRoot + ioAsyncRoot
	for _, src := range []string{
		roots + "const T = roll()\n",
		roots + "const U = roll() + 1\n",
		roots + "const F = await fetch(\"u\")\n",
		roots + "assert roll() == 1\n",
	} {
		if _, diags := analyze(src); !hasCode(diags, CodeEffectInPureContext) {
			t.Errorf("%q: want effect_in_pure_context, got %v", src, codes(diags))
		}
	}
	// A pure call stays allowed.
	if _, diags := analyze("fn one(): nint -> 1\nconst A = one()\n"); hasCode(diags, CodeEffectInPureContext) {
		t.Errorf("pure call flagged: %v", codes(diags))
	}
}

func TestEffectInPureContextFoldedPositions(t *testing.T) {
	// The other compile-time-folded positions are governed by the same purity
	// rule as a const initializer: an enum member initializer, an associated
	// constant initializer, and a refinement (where) predicate all fold at
	// compile time, so an effectful call cannot appear in any of them.
	roots := nondetRoot
	for _, src := range []string{
		// An enum member initializer.
		roots + "pub enum E: nint {\n  A = roll()\n}\n",
		// An effectful call buried in a member initializer expression.
		roots + "pub enum E: nint {\n  A = roll() + 1\n}\n",
		// An associated constant initializer in a type's impl block.
		roots + "pub type T = nint impl {\n  const C = roll()\n}\n",
		// An associated constant initializer in an enum's impl block.
		roots + "pub enum E: nint {\n  A\n} impl {\n  const C = roll()\n}\n",
		// A refinement predicate calling a top-level effectful function.
		roots + "pub type T = nint where self > roll()\n",
	} {
		if _, diags := analyze(src); !hasCode(diags, CodeEffectInPureContext) {
			t.Errorf("%q: want effect_in_pure_context, got %v", src, codes(diags))
		}
	}

	// A pure call in each position stays allowed.
	pure := "fn dbl(n: nint): nint -> n * 2\n"
	for _, src := range []string{
		pure + "pub enum E: nint {\n  A = dbl(3)\n}\n",
		pure + "pub type T = nint impl {\n  const C = dbl(3)\n}\n",
		pure + "pub type T = nint where self > dbl(1)\n",
	} {
		if _, diags := analyze(src); hasCode(diags, CodeEffectInPureContext) {
			t.Errorf("%q: pure call flagged: %v", src, codes(diags))
		}
	}
}

func TestEffectInTernaryBranch(t *testing.T) {
	// A ternary's branches are part of the body the effect walker must pierce:
	// an effectful call in the then or else branch counts toward the enclosing
	// declaration's effects, exactly as one in a return value does. Before
	// collectEffectUses handled TernaryExpr, the branch was never visited, so
	// the effect slipped past both completeness checks.
	roots := nondetRoot

	// missing_effect: an undeclared effect in a ternary branch of a function body.
	for _, body := range []string{
		"return flag ? roll() : 0",
		"return flag ? 0 : roll()",
		"return (roll() == 1) ? 0 : 0", // the condition counts too
	} {
		src := roots + "pub fn f(flag: bool): nint {\n  " + body + "\n}\n"
		if _, diags := analyze(src); !hasCode(diags, CodeMissingEffect) {
			t.Errorf("%q: want missing_effect for an effect in a ternary, got %v", body, codes(diags))
		}
	}

	// effect_in_pure_context: an effectful call in a ternary branch of a const
	// initializer (and an assert condition) is the harder soundness hole.
	for _, src := range []string{
		roots + "const A = true ? roll() : 0\n",
		roots + "const B = false ? 0 : roll()\n",
		roots + "assert (true ? roll() : 0) == 1\n",
	} {
		if _, diags := analyze(src); !hasCode(diags, CodeEffectInPureContext) {
			t.Errorf("%q: want effect_in_pure_context for an effect in a ternary, got %v", src, codes(diags))
		}
	}
}

func TestEffectfulFunctionNeverFolds(t *testing.T) {
	// Only a pure function folds to a value; an effectful one compiles to
	// runtime code, so a const referencing it gets no value (and the pure
	// check reports the position).
	src := "pub fn nondet f(): nint -> 1\nconst A = f()\n"
	m, diags := analyze(src)
	if !hasCode(diags, CodeEffectInPureContext) {
		t.Fatalf("want effect_in_pure_context, got %v", codes(diags))
	}
	if m.Consts[0].Eval != nil {
		t.Errorf("A eval = %s, want unevaluated", m.Consts[0].Eval)
	}
	// The pure twin folds as ever.
	m, _ = analyze("pub fn g(): nint -> 1\nconst A = g()\n")
	if m.Consts[0].Eval.String() != "1" {
		t.Errorf("pure call eval = %s, want 1", m.Consts[0].Eval)
	}
}

// TestFuncTypeParamChecked pins that a function type's bare parameter is its
// type, so a function value flowing into a function-typed slot is checked against
// it: a fn(string): nint passed where fn(nint): nint is expected is a mismatch, and
// calling a fn(nint): nint value with a string argument is too. The bare parameter
// is lowered to its type (it used to be read as an unnamed parameter's name, leaving
// the parameter type unresolved, so neither mismatch was caught).
func TestFuncTypeParamChecked(t *testing.T) {
	mismatch := "fn takesFn(f: fn(nint): nint): nint {\n  return f(1)\n}\n" +
		"const Bad: fn(string): nint = fn(s) -> 0\n" +
		"const R: nint = takesFn(Bad)\n"
	if _, diags := analyze(mismatch); !hasCode(diags, CodeTypeMismatch) {
		t.Errorf("a fn(string): nint passed where fn(nint): nint is expected should be a type mismatch: %v", codes(diags))
	}
	wrongArg := "fn apply(f: fn(nint): nint): nint {\n  return f(\"x\")\n}\n"
	if _, diags := analyze(wrongArg); !hasCode(diags, CodeTypeMismatch) {
		t.Errorf("calling a fn(nint): nint value with a string argument should be a type mismatch: %v", codes(diags))
	}
}

// TestFuncTypeBareParamResolves pins that a bare function-type parameter resolves
// to its type, not Invalid: const Twice: fn(nint): nint has an nint parameter.
func TestFuncTypeBareParamResolves(t *testing.T) {
	m, _ := analyze("const Twice: fn(nint): nint = fn(x) -> x * 2\n")
	fn, ok := m.Consts[0].Type.(*ir.Func)
	if !ok {
		t.Fatalf("Twice type = %T, want a function type", m.Consts[0].Type)
	}
	if len(fn.Params) != 1 || ir.HasInvalid(fn.Params[0]) {
		t.Errorf("Twice parameter = %v, want a resolved nint (not Invalid)", fn.Params)
	}
}

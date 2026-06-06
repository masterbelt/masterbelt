package semantic

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

func analyze(src string) (*ir.Module, []diagnostic.Diagnostic) {
	return Analyze(abstract.NewDocument([]byte(src)))
}

func codes(diags []diagnostic.Diagnostic) []diagnostic.Code {
	out := make([]diagnostic.Code, len(diags))
	for i, d := range diags {
		out[i] = d.Code
	}
	return out
}

func TestAnnotatedAndInferred(t *testing.T) {
	m, diags := analyze("const A: int32 = 1\nconst B = 0\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if m.Consts[0].Type.String() != "int32" {
		t.Errorf("A type = %s, want int32", m.Consts[0].Type)
	}
	if m.Consts[1].Type.String() != "int" {
		t.Errorf("B type = %s, want int", m.Consts[1].Type)
	}
}

func TestReferenceResolutionAndTypeInheritance(t *testing.T) {
	m, diags := analyze("const A: int8 = 1\nconst B = A\nconst C = 0\nconst D = C\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	// B = A inherits A's concrete int8.
	if m.Consts[1].Type.String() != "int8" {
		t.Errorf("B type = %s, want int8", m.Consts[1].Type)
	}
	ref, ok := m.Consts[1].Value.(*ir.Reference)
	if !ok || ref.Target != m.Consts[0] {
		t.Errorf("B value = %v, want Reference -> A", m.Consts[1].Value)
	}
	// D = C inherits C's int.
	if m.Consts[3].Type.String() != "int" {
		t.Errorf("D type = %s, want int", m.Consts[3].Type)
	}
}

func TestUndefinedName(t *testing.T) {
	m, diags := analyze("const X = Missing\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeUndefinedName {
		t.Fatalf("codes = %v, want [undefined_name]", got)
	}
	if m.Consts[0].Value != nil {
		t.Errorf("X value = %v, want nil (unresolved)", m.Consts[0].Value)
	}
}

func TestDuplicateDeclaration(t *testing.T) {
	_, diags := analyze("const X = 1\nconst X = 2\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeDuplicateDeclaration {
		t.Fatalf("codes = %v, want [duplicate_declaration]", got)
	}
}

func TestLocalTypeAnnotation(t *testing.T) {
	// A file's own type declarations are visible to its const annotations —
	// the same universe imported types join (P-2). The annotated constant's
	// Named type points at the very TypeDef the module publishes.
	m, diags := analyze("pub type Coin = int8\nconst c: Coin = 1\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if m.Consts[0].Type.String() != "Coin" {
		t.Errorf("c type = %s, want Coin", m.Consts[0].Type)
	}
	named, ok := m.Consts[0].Type.(*ir.Named)
	if !ok || named.Def != m.Types[0] {
		t.Errorf("c's Named.Def = %v, want the module's Coin definition", m.Consts[0].Type)
	}
}

func TestLocalTypeAnnotationRangeChecked(t *testing.T) {
	// The named type's underlying range still applies.
	_, diags := analyze("pub type Coin = int8\nconst c: Coin = 1000\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeConstantOverflow {
		t.Fatalf("codes = %v, want [constant_overflow]", got)
	}
}

func TestUnknownType(t *testing.T) {
	m, diags := analyze("const X: notatype = 1\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeUnknownType {
		t.Fatalf("codes = %v, want [unknown_type]", got)
	}
	if m.Consts[0].Type.String() != "invalid" {
		t.Errorf("X type = %s, want invalid", m.Consts[0].Type)
	}
}

func TestCyclicReference(t *testing.T) {
	m, diags := analyze("const A = B\nconst B = A\n")
	// Both declarations are on the cycle, so both are flagged.
	got := codes(diags)
	if len(got) != 2 || got[0] != CodeCyclicReference || got[1] != CodeCyclicReference {
		t.Fatalf("codes = %v, want two cyclic_reference", got)
	}
	if m.Consts[0].Type.String() != "invalid" || m.Consts[1].Type.String() != "invalid" {
		t.Errorf("cyclic consts should have invalid type, got %s/%s", m.Consts[0].Type, m.Consts[1].Type)
	}
}

func TestValueEvaluation(t *testing.T) {
	m, diags := analyze("const A = 100\nconst B = A\nconst C: int64 = B\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	for i, want := range []int64{100, 100, 100} {
		ev := m.Consts[i].Eval
		if ev == nil || ev.Kind != ir.ConstInt || ev.Int.Int64() != want {
			t.Errorf("const %d eval = %v, want %d", i, ev, want)
		}
	}
}

func TestStringLiteral(t *testing.T) {
	m, diags := analyze("const X = \"label\"\npub const Y: string = \"\\u{1F389}\"\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	// Inferred and annotated string constants both have type string.
	if m.Consts[0].Type.String() != "string" || m.Consts[1].Type.String() != "string" {
		t.Fatalf("types = %s/%s, want string/string", m.Consts[0].Type, m.Consts[1].Type)
	}
	// The literal is lowered to a StringLiteral value and folds to a string
	// constant holding the decoded text.
	lit, ok := m.Consts[0].Value.(*ir.StringLiteral)
	if !ok || lit.Value != "label" {
		t.Errorf("X value = %v, want StringLiteral \"label\"", m.Consts[0].Value)
	}
	if ev := m.Consts[0].Eval; ev == nil || ev.Kind != ir.ConstString || ev.Str != "label" {
		t.Errorf("X eval = %v, want string constant \"label\"", ev)
	}
	if ev := m.Consts[1].Eval; ev == nil || ev.Kind != ir.ConstString || ev.Str != "🎉" {
		t.Errorf("Y eval = %v, want string constant \"🎉\"", ev)
	}
}

func TestStringOperationsFold(t *testing.T) {
	m, diags := analyze("const g = \"a\" + \"b\"\nconst eq = \"x\" == \"x\"\nconst lt = \"a\" < \"b\"\nconst banner = \"[\" + g + \"]\"\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	// Concatenation folds to a string; comparisons fold to booleans.
	if ev := m.Consts[0].Eval; ev == nil || ev.Kind != ir.ConstString || ev.Str != "ab" {
		t.Errorf("g eval = %v, want string \"ab\"", ev)
	}
	if m.Consts[0].Type.String() != "string" {
		t.Errorf("g type = %s, want string", m.Consts[0].Type)
	}
	if ev := m.Consts[1].Eval; ev == nil || ev.Kind != ir.ConstBool || ev.Bool != true {
		t.Errorf("eq eval = %v, want bool true", ev)
	}
	if ev := m.Consts[2].Eval; ev == nil || ev.Kind != ir.ConstBool || ev.Bool != true {
		t.Errorf("lt eval = %v, want bool true", ev)
	}
	// Concatenation through a reference folds too.
	if ev := m.Consts[3].Eval; ev == nil || ev.Kind != ir.ConstString || ev.Str != "[ab]" {
		t.Errorf("banner eval = %v, want string \"[ab]\"", ev)
	}
}

func TestCollectionLiteral(t *testing.T) {
	m, diags := analyze("const L: list<int> = [1, 2, 3]\nconst M: map<string, int> = [\"k\": 1]\nconst I = [10, 20]\nconst E: list<int> = []\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if m.Consts[0].Type.String() != "list<int>" {
		t.Errorf("L type = %s, want list<int>", m.Consts[0].Type)
	}
	// The list folds to a collection constant of its elements.
	if ev := m.Consts[0].Eval; ev == nil || ev.Kind != ir.ConstCollection || len(ev.Coll) != 3 ||
		ev.Coll[0].Key != nil || ev.Coll[0].Value.Int.Int64() != 1 {
		t.Errorf("L eval = %v, want collection [1 2 3]", m.Consts[0].Eval)
	}
	if m.Consts[1].Type.String() != "map<string, int>" {
		t.Errorf("M type = %s, want map<string, int>", m.Consts[1].Type)
	}
	if ev := m.Consts[1].Eval; ev == nil || ev.Kind != ir.ConstCollection || len(ev.Coll) != 1 ||
		ev.Coll[0].Key == nil || ev.Coll[0].Key.Str != "k" || ev.Coll[0].Value.Int.Int64() != 1 {
		t.Errorf("M eval = %v, want collection [k: 1]", m.Consts[1].Eval)
	}
	// An un-annotated list infers its element type.
	if m.Consts[2].Type.String() != "list<int>" {
		t.Errorf("I type = %s, want list<int>", m.Consts[2].Type)
	}
	// An empty list takes its type from the annotation and folds to [].
	if m.Consts[3].Type.String() != "list<int>" {
		t.Errorf("E type = %s, want list<int>", m.Consts[3].Type)
	}
	if ev := m.Consts[3].Eval; ev == nil || ev.Kind != ir.ConstCollection || len(ev.Coll) != 0 {
		t.Errorf("E eval = %v, want empty collection", m.Consts[3].Eval)
	}
}

func TestCollectionElementAdaptsAndChecks(t *testing.T) {
	// Integer elements adapt to the annotation's element type, range-checked.
	if _, diags := analyze("const X: list<int8> = [1, 2, 3]\n"); len(diags) != 0 {
		t.Errorf("list<int8> = [1,2,3] should be fine, got %v", codes(diags))
	}
	if m, diags := analyze("const X: map<string, int> = [\"a\": 1, \"b\": 2]\n"); len(diags) != 0 {
		t.Errorf("map literal should be fine, got %v", codes(diags))
	} else if m.Consts[0].Type.String() != "map<string, int>" {
		t.Errorf("type = %s, want map<string, int>", m.Consts[0].Type)
	}
}

func TestCollectionDiagnostics(t *testing.T) {
	cases := []struct {
		src  string
		code diagnostic.Code
	}{
		{"const X: list<int8> = [1, 999]\n", CodeConstantOverflow}, // element out of range
		{"const X: list<int> = [\"a\"]\n", CodeTypeMismatch},       // wrong element type
		{"const X = []\n", CodeUninferableCollection},              // empty, no annotation
		{"const X = [1, \"a\"]\n", CodeUninferableCollection},      // heterogeneous, no annotation
		{"const X: int = [1]\n", CodeTypeMismatch},                 // collection under scalar annotation
		{"const X: list<int> = [\"k\": 1]\n", CodeTypeMismatch},    // map literal, list annotation
		{"const X: map<string, int> = [1, 2]\n", CodeTypeMismatch}, // list literal, map annotation
	}
	for _, tc := range cases {
		_, diags := analyze(tc.src)
		if !hasCode(diags, tc.code) {
			t.Errorf("%q: want %s, got %v", tc.src, tc.code, codes(diags))
		}
	}
}

func TestStringAnnotationMismatch(t *testing.T) {
	// A string initializer under a non-string annotation (and vice versa) is a
	// type mismatch; a string under a string annotation is fine.
	for _, src := range []string{
		"const x: int8 = \"no\"\n",
		"const x: string = 1\n",
		"const x: bool = \"no\"\n",
	} {
		if _, diags := analyze(src); !hasCode(diags, CodeTypeMismatch) {
			t.Errorf("%q: want type_mismatch, got %v", src, codes(diags))
		}
	}
	if _, diags := analyze("const x: string = \"yes\"\n"); hasCode(diags, CodeTypeMismatch) {
		t.Errorf("string = string should not mismatch, got %v", codes(diags))
	}
}

func TestConstantOverflow(t *testing.T) {
	_, diags := analyze("const X: int8 = 1000\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeConstantOverflow {
		t.Fatalf("codes = %v, want [constant_overflow]", got)
	}
}

func TestIntLiteralDoesNotOverflow(t *testing.T) {
	// An un-annotated integer literal is the arbitrary-precision int; only a
	// sized concrete type triggers the range check.
	_, diags := analyze("const X = 99999999999999999999999999\n")
	if len(diags) != 0 {
		t.Fatalf("an int literal should not overflow: %v", diags)
	}
}

func TestOverflowThroughReference(t *testing.T) {
	_, diags := analyze("const A = 1000\nconst B: int8 = A\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeConstantOverflow {
		t.Fatalf("codes = %v, want [constant_overflow]", got)
	}
}

func TestMethodBodyTypeChecks(t *testing.T) {
	// self + 1 on a nominal integer derives int8's add and returns the nominal
	// type, which matches the declared result `self` — no diagnostic.
	if _, diags := analyze("pub type Level = int8 impl {\n  pub inc(): self {\n    return self + 1\n  }\n}\n"); len(diags) != 0 {
		t.Errorf("well-typed method body should have no diagnostics, got %v", codes(diags))
	}
}

func TestMethodBodyTypeMismatch(t *testing.T) {
	// A body returning self (an integer) where bool is declared is a mismatch.
	_, diags := analyze("pub type Bad = int8 impl {\n  pub wrong(): bool {\n    return self\n  }\n}\n")
	if !hasCode(diags, CodeTypeMismatch) {
		t.Errorf("want type_mismatch for a bad method body, got %v", codes(diags))
	}
}

func TestMultiStatementMethodBody(t *testing.T) {
	m, _ := analyze("pub type Level = int8 impl {\n  pub inc(): self {\n    self + 1\n    return self\n  }\n}\n")
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

func hasCode(diags []diagnostic.Diagnostic, code diagnostic.Code) bool {
	for _, c := range codes(diags) {
		if c == code {
			return true
		}
	}
	return false
}

func TestOperatorTypeErrors(t *testing.T) {
	bad := []string{
		"const x = 1 && 2\n",   // logical operator on integers
		"const x = true + 1\n", // arithmetic on a boolean
		"const x = 1 < true\n", // comparison of mixed kinds
		"const x = !1\n",       // not on an integer
		"const x = -true\n",    // neg on a boolean
	}
	for _, src := range bad {
		_, diags := analyze(src)
		if !hasCode(diags, CodeInvalidOperation) {
			t.Errorf("%q: want invalid_operation, got %v", src, codes(diags))
		}
	}
	for _, src := range []string{"const x = 1 + 2\n", "const x = true && false\n", "const x = 1 < 2\n"} {
		if _, diags := analyze(src); len(diags) != 0 {
			t.Errorf("%q: unexpected diagnostics %v", src, codes(diags))
		}
	}
}

func TestOperatorErrorReportedOnce(t *testing.T) {
	// The inner type error is reported once; the outer call sees an Invalid
	// operand and does not pile on.
	_, diags := analyze("const x = 1 && 2 && 3\n")
	n := 0
	for _, c := range codes(diags) {
		if c == CodeInvalidOperation {
			n++
		}
	}
	if n != 1 {
		t.Errorf("want exactly one invalid_operation, got %d: %v", n, codes(diags))
	}
}

// overloadSrc declares a type with merge overloaded by parameter type — the
// 0013-overload example's Score — for the overload diagnostics tests.
const overloadSrc = `pub type Score = int32 impl {
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
	src := `pub type Gauge = int32 impl {
  pub fn set(v: int8): bool {
    return v > 0
  }
  pub fn set(v: int16): bool {
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
	m, diags := analyze(src + "const V: int16 = 5\nconst X = G.set(V)\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if got := m.Consts[2].Type.String(); got != "bool" {
		t.Errorf("X type = %s, want bool", got)
	}
}

func TestDatetimeDurationOperators(t *testing.T) {
	// The full operator table of the two literals — each mixed operation
	// resolves to the overload its argument type names, and folds to the
	// canonical value (UTC instants; largest-units-first durations).
	src := `const Release = D2009-03-31T23:59:59.000Z
const Epoch = D1970-01-01T00:00:00.000Z
const Deadline = Release + 7d
const Span = Release - Epoch
const Shift = Release - 1h
const TwoH = 1h + 1h
const Less = 90m - 1h
const Triple = 5m * 3
const Sooner = 1h + Release
const Backwards = Epoch - Release
`
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	want := []struct{ typ, eval string }{
		{"datetime", "D2009-03-31T23:59:59.000Z"},
		{"datetime", "D1970-01-01T00:00:00.000Z"},
		{"datetime", "D2009-04-07T23:59:59.000Z"}, // dt + dr
		{"duration", "2047w5d23h59m59s"},          // dt - dt
		{"datetime", "D2009-03-31T22:59:59.000Z"}, // dt - dr
		{"duration", "2h"},                        // dr + dr
		{"duration", "30m"},                       // canonical: 90m - 1h
		{"duration", "15m"},                       // dr * int
		{"datetime", "D2009-04-01T00:59:59.000Z"}, // dr + dt
		{"duration", "-2047w5d23h59m59s"},         // a negative computed span
	}
	for i, w := range want {
		c := m.Consts[i]
		if c.Type.String() != w.typ || c.Eval.String() != w.eval {
			t.Errorf("%s: (%s, %s), want (%s, %s)", c.Name, c.Type, c.Eval, w.typ, w.eval)
		}
	}
}

func TestDatetimeDurationDiagnostics(t *testing.T) {
	// An argument fitting no overload of an overloaded name reports
	// no_matching_overload; a single-signature misfit stays invalid_operation.
	_, diags := analyze("const X = 5s + 1\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeNoMatchingOverload {
		t.Fatalf("5s + 1: codes = %v, want [no_matching_overload]", got)
	}
	_, diags = analyze("const X = D2009-03-31T23:59:59.000Z * 2\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeInvalidOperation {
		t.Fatalf("dt * 2: codes = %v, want [invalid_operation]", got)
	}
	_, diags = analyze("const X = 1h > D2009-03-31T23:59:59.000Z\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeInvalidOperation {
		t.Fatalf("dr > dt: codes = %v, want [invalid_operation]", got)
	}
	// A datetime/duration assertion folds like any other constant condition:
	// 1h59m is not more than 2h, and the failure proves the fold ran.
	_, diags = analyze("assert 1h + 59m > 2h\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeAssertionFailed {
		t.Fatalf("assert: codes = %v, want [assertion_failed]", got)
	}
}

func TestAnnotatedFuncLit(t *testing.T) {
	// The annotation is a checking context: it supplies the literal's omitted
	// parameter and result types.
	m, diags := analyze("const Twice: fn(x: int): int = fn(x) { return x * 2 }\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if got := m.Consts[0].Type.String(); got != "fn(int): int" {
		t.Errorf("Twice type = %s, want fn(int): int", got)
	}
	// A fully annotated literal under a matching annotation is fine too (this
	// used to false-positive through types.Compatible, which had no function
	// rule).
	if _, diags := analyze("const Twice: fn(x: int): int = fn(x: int): int { return x * 2 }\n"); len(diags) != 0 {
		t.Errorf("fully annotated literal: unexpected diagnostics %v", diags)
	}
}

func TestAnnotatedFuncLitDiagnostics(t *testing.T) {
	cases := []struct {
		src  string
		code diagnostic.Code
	}{
		// Parameter-count mismatch against the annotation.
		{"const B: fn(x: int): int = fn(x, y) { return x }\n", CodeLambdaArityMismatch},
		// A written parameter annotation must agree with the expectation.
		{"const C: fn(x: int): int = fn(x: string) { return \"\" }\n", CodeTypeMismatch},
		// A written result annotation must agree too.
		{"const R: fn(x: int): int = fn(x): string { return \"\" }\n", CodeTypeMismatch},
		// A return that does not satisfy the pushed-down result type.
		{"const S: fn(x: int): int = fn(x) { return x == 0 }\n", CodeTypeMismatch},
		// A literal under a non-function annotation.
		{"const N: int = fn() { return 1 }\n", CodeTypeMismatch},
		// An operator error inside a context-typed body still surfaces.
		{"const O: fn(x: int): int = fn(x) { return x && x }\n", CodeInvalidOperation},
		// A literal value out of the pushed-down range.
		{"const V: fn(): int8 = fn() { return 1000 }\n", CodeConstantOverflow},
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
	if got := m.Consts[0].Type.String(); got != "list<int>" {
		t.Errorf("Doubled type = %s, want list<int>", got)
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
	m, diags := analyze("const Doubled = [1, 2, 3].map(fn(x) -> x * 2)\nconst Twice: fn(x: int): int = fn(x) -> x * 2\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if got := m.Consts[0].Type.String(); got != "list<int>" {
		t.Errorf("Doubled type = %s, want list<int>", got)
	}
	if got := m.Consts[0].Eval.String(); got != "[2, 4, 6]" {
		t.Errorf("Doubled eval = %s, want [2, 4, 6]", got)
	}
	if got := m.Consts[1].Type.String(); got != "fn(int): int" {
		t.Errorf("Twice type = %s, want fn(int): int", got)
	}

	// Body-type errors surface through the arrow form too.
	if _, diags := analyze("const S: fn(x: int): int = fn(x) -> x == 0\n"); !hasCode(diags, CodeTypeMismatch) {
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
	if _, diags := analyze("const A: fn(x: int): int = fn(x) { return x }\n"); len(diags) != 0 {
		t.Errorf("pinned parameter: unexpected diagnostics %v", diags)
	}
}

func TestAnnotatedEmptyCollection(t *testing.T) {
	// Checking mode gives an empty literal its annotation's type, so it is not
	// uninferable.
	for _, src := range []string{
		"const Empty: list<int> = []\n",
		"const Empty: map<string, int> = []\n",
	} {
		if _, diags := analyze(src); len(diags) != 0 {
			t.Errorf("%q: unexpected diagnostics %v", src, diags)
		}
	}
}

func TestMethodResultTypeReachesLiteral(t *testing.T) {
	// The method's declared result type checks a returned literal, so its
	// lambda parameters infer.
	src := "pub type T = int8 impl {\n  pub f(): fn(x: int): int {\n    return fn(x) { return x }\n  }\n}\n"
	if _, diags := analyze(src); len(diags) != 0 {
		t.Errorf("unexpected diagnostics: %v", diags)
	}
	// And a literal that does not satisfy it is reported.
	bad := "pub type T = int8 impl {\n  pub f(): fn(x: int): int {\n    return fn(x, y) { return x }\n  }\n}\n"
	if _, diags := analyze(bad); !hasCode(diags, CodeLambdaArityMismatch) {
		t.Errorf("want lambda_arity_mismatch, got %v", codes(diags))
	}
}

func TestFuncLitResultInference(t *testing.T) {
	// An omitted result type is synthesized from the body's returns; declared
	// parameter types carry into the body scope.
	m, diags := analyze("const F = fn(x: int) { return x * 2 }\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if got := m.Consts[0].Type.String(); got != "fn(int): int" {
		t.Errorf("F type = %s, want fn(int): int", got)
	}
}

func TestFuncLitBodyDiagnostics(t *testing.T) {
	cases := []struct {
		src  string
		code diagnostic.Code
	}{
		// An operator error inside the body is reported now that the checking
		// walk descends into the literal.
		{"const F = fn(x: int): int { return x && x }\n", CodeInvalidOperation},
		// A return that does not satisfy the declared result type.
		{"const F = fn(x: int): bool { return x }\n", CodeTypeMismatch},
		// Conflicting unannotated returns cannot unify.
		{"const F = fn(x: int) { return 1\n  return true }\n", CodeTypeMismatch},
		// No result annotation and no return to infer one from.
		{"const F = fn() {}\n", CodeUninferableResult},
		// A division by zero inside the body.
		{"const F = fn(x: int): int { return x / 0 }\n", CodeDivisionByZero},
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
		"const F = fn(x: int): int { return x * 2 }\n",
		"const F = fn(x: int) { return x % 2 == 0 }\n",
		"const F = fn(): int {}\n", // the signature is complete without a return
	} {
		if _, diags := analyze(src); len(diags) != 0 {
			t.Errorf("%q: unexpected diagnostics %v", src, diags)
		}
	}
}

func TestDivisionByZero(t *testing.T) {
	for _, src := range []string{
		"const x = 1 / 0\n",
		"const x = 1 % 0\n",
		"const z = 0\nconst x = 1 / z\n", // zero through a reference
	} {
		_, diags := analyze(src)
		if !hasCode(diags, CodeDivisionByZero) {
			t.Errorf("%q: want division_by_zero, got %v", src, codes(diags))
		}
	}
	if _, diags := analyze("const x = 1 / 2\n"); hasCode(diags, CodeDivisionByZero) {
		t.Error("1 / 2 should not be division by zero")
	}
}

// TestDivisionByZeroInTernary checks that checkDivByZero descends into a
// ternary the same way eval does: the condition is always walked, and only the
// statically-selected branch — so a div-by-zero on the guaranteed-taken path is
// reported, while one on the provably-untaken path stays silent. Before
// checkDivByZero handled TernaryExpr, none of these were reported.
func TestDivisionByZeroInTernary(t *testing.T) {
	reported := []string{
		"const X = true ? 1 / 0 : 5\n",              // taken then-branch
		"const Y = false ? 5 : 1 / 0\n",             // taken else-branch
		"const Z = (1 > 2) ? 5 : 10 / 0\n",          // else taken (1>2 is false)
		"const W = (1 / 0 == 0) ? 1 : 2\n",          // the condition itself
		"const V = true ? (true ? 1 / 0 : 1) : 2\n", // nested, taken
		"assert (true ? 1 / 0 : 5) == 0\n",          // an assert condition
	}
	for _, src := range reported {
		if _, diags := analyze(src); !hasCode(diags, CodeDivisionByZero) {
			t.Errorf("%q: want division_by_zero on the taken branch, got %v", src, codes(diags))
		}
	}
	silent := []string{
		"const X = true ? 1 : 1 / 0\n",     // untaken else
		"const Y = false ? 1 / 0 : 1\n",    // untaken then
		"const Z = (1 < 2) ? 5 : 10 / 0\n", // then taken, else (10/0) untaken
	}
	for _, src := range silent {
		if _, diags := analyze(src); hasCode(diags, CodeDivisionByZero) {
			t.Errorf("%q: a provably-untaken div-by-zero must stay silent, got %v", src, codes(diags))
		}
	}
}

// TestShortCircuitBoolConnectives checks the boolean connectives' short-circuit
// end to end: a false && or a true || folds to a bool without evaluating its
// dead right operand, and checkDivByZero does not flag a div-by-zero in that
// dead operand — it is never evaluated, matching eval and the runtime.
func TestShortCircuitBoolConnectives(t *testing.T) {
	// The dead operand carries a division by zero, which must not be reported.
	for _, tc := range []struct{ src, want string }{
		{"const Y: bool = false && (1 / 0 == 0)\n", "false"},
		{"const Y: bool = true || (1 / 0 == 0)\n", "true"},
	} {
		m, diags := analyze(tc.src)
		if hasCode(diags, CodeDivisionByZero) {
			t.Errorf("%q: a short-circuited div-by-zero must not be reported: %v", tc.src, codes(diags))
		}
		if len(diags) != 0 {
			t.Errorf("%q: unexpected diagnostics: %v", tc.src, codes(diags))
		}
		for _, c := range m.Consts {
			if c.Name == "Y" && (c.Eval == nil || c.Eval.String() != tc.want) {
				t.Errorf("%q: Y = %v, want %s", tc.src, c.Eval, tc.want)
			}
		}
	}

	// The live operand still carries its div-by-zero: a true && (...) needs the
	// right, so a div-by-zero there is real and reported.
	for _, src := range []string{
		"const Y: bool = true && (1 / 0 == 0)\n",
		"const Y: bool = false || (1 / 0 == 0)\n",
	} {
		if _, diags := analyze(src); !hasCode(diags, CodeDivisionByZero) {
			t.Errorf("%q: a live operand's div-by-zero must be reported, got %v", src, codes(diags))
		}
	}
}

func TestAnnotationMismatch(t *testing.T) {
	for _, src := range []string{
		"const x: int8 = true\n",
		"const x: bool = 1\n",
		"const x: bool = 1 + 2\n",
	} {
		_, diags := analyze(src)
		if !hasCode(diags, CodeTypeMismatch) {
			t.Errorf("%q: want type_mismatch, got %v", src, codes(diags))
		}
	}
	for _, src := range []string{
		"const x: int8 = 1 + 2\n",
		"const x: bool = true && false\n",
		"const x: bool = 1 < 2\n",
	} {
		if _, diags := analyze(src); hasCode(diags, CodeTypeMismatch) {
			t.Errorf("%q: unexpected type_mismatch %v", src, codes(diags))
		}
	}
}

func TestDiagnosticMessages(t *testing.T) {
	cases := []struct{ src, code, message string }{
		{"const x = 1 && 2\n", string(CodeInvalidOperation), "cannot apply method anan to int, int"},
		{"const x = 1 / 0\n", string(CodeDivisionByZero), "division by zero"},
		{"const x: int8 = true\n", string(CodeTypeMismatch), "cannot use bool as int8"},
	}
	for _, tc := range cases {
		_, diags := analyze(tc.src)
		var msg string
		for _, d := range diags {
			if string(d.Code) == tc.code {
				msg = d.Message
			}
		}
		if msg != tc.message {
			t.Errorf("%q: message = %q, want %q", tc.src, msg, tc.message)
		}
	}
}

// --- assert declarations ------------------------------------------------------

func TestAssertPasses(t *testing.T) {
	_, diags := analyze("const Max = 100\nconst Min = 0\n" +
		"assert Max > Min\n" +
		"assert Min == 0\n" +
		"assert Max - Min == 100\n" +
		"assert !(Min > Max)\n" +
		"assert true\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
}

func TestAssertFailed(t *testing.T) {
	_, diags := analyze("const Max = 100\nconst Min = 0\nassert Max < Min\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeAssertionFailed {
		t.Fatalf("codes = %v, want [assertion_failed]", got)
	}
	// The message summarizes the condition in surface syntax — rendered back
	// from the desugared AST — and draws the power-assert diagram beneath it,
	// every sub-expression's folded value under the place it appears.
	want := "assertion failed: Max < Min\n" +
		"  Max < Min\n" +
		"  ^   ^ ^\n" +
		"  100 | 0\n" +
		"      false"
	if diags[0].Message != want {
		t.Errorf("message = %q, want %q", diags[0].Message, want)
	}
}

func TestAssertNotBool(t *testing.T) {
	_, diags := analyze("const Max = 100\nassert Max + 1\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeAssertionNotBool {
		t.Fatalf("codes = %v, want [assertion_not_bool]", got)
	}
	if want := "assertion must be a bool; got int"; diags[0].Message != want {
		t.Errorf("message = %q, want %q", diags[0].Message, want)
	}
}

func TestAssertUndefinedName(t *testing.T) {
	// An undefined reference is the existing diagnostic, once — not an extra
	// assertion_* on top of it.
	_, diags := analyze("assert missing > 0\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeUndefinedName {
		t.Fatalf("codes = %v, want [undefined_name]", got)
	}
}

func TestAssertDivisionByZero(t *testing.T) {
	_, diags := analyze("assert 1 / 0 == 0\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeDivisionByZero {
		t.Fatalf("codes = %v, want [division_by_zero]", got)
	}
}

func TestAssertOperatorTypeError(t *testing.T) {
	_, diags := analyze("assert 1 && true\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeInvalidOperation {
		t.Fatalf("codes = %v, want [invalid_operation]", got)
	}
}

func TestAssertNotConstant(t *testing.T) {
	// A user-defined method body is beyond the compile-time evaluator: the
	// condition types as bool but cannot fold, which is itself the error —
	// an assertion the compiler cannot verify must not pass silently.
	src := "type Level = int8 impl {\n  increment(): self {\n    return self + 1\n  }\n}\n" +
		"const L: Level = 50\n" +
		"assert L.increment() == 51\n"
	_, diags := analyze(src)
	if got := codes(diags); len(got) != 1 || got[0] != CodeAssertionNotConstant {
		t.Fatalf("codes = %v, want [assertion_not_constant]", got)
	}
}

func TestAssertSelfNotConstant(t *testing.T) {
	// self has no referent at the top level: each occurrence is reported as
	// self_outside_method, which also explains why the assertion cannot fold —
	// so the generic assertion_not_constant stays suppressed.
	_, diags := analyze("assert self == self\n")
	got := codes(diags)
	if len(got) != 2 || got[0] != CodeSelfOutsideMethod || got[1] != CodeSelfOutsideMethod {
		t.Fatalf("codes = %v, want two self_outside_method", got)
	}
}

func TestAssertMissingExprIsTheParsersProblem(t *testing.T) {
	// A recovered "assert" without an expression already carries a parse
	// diagnostic; the semantic layer adds nothing.
	_, diags := analyze("assert\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
}

func TestAssertOutcomeInModule(t *testing.T) {
	// An assert declares no value — the constants are untouched — but its
	// outcome (the folded condition and its power-assert diagram) is module
	// data, so hover and tooling read the very values it was checked with.
	with, _ := analyze("const A = 1\nassert A > 0\n")
	without, _ := analyze("const A = 1\n")
	if len(with.Consts) != len(without.Consts) || len(with.Types) != len(without.Types) {
		t.Errorf("assert changed the constants or types")
	}
	if len(with.Asserts) != 1 {
		t.Fatalf("got %d asserts in the module, want 1", len(with.Asserts))
	}
	a := with.Asserts[0]
	if a.Cond != "A > 0" || !a.Held() {
		t.Errorf("assert = {Cond: %q, Held: %v}, want A > 0 holding", a.Cond, a.Held())
	}
	want := "A > 0\n" +
		"^ ^\n" +
		"1 true"
	if a.Diagram != want {
		t.Errorf("diagram:\n%s\nwant:\n%s", a.Diagram, want)
	}
}

func TestAssertFailedWithDoc(t *testing.T) {
	// The doc comment is the invariant in the author's words: a failure
	// quotes it above the diagram.
	_, diags := analyze("const Max = 100\nconst Min = 0\n/// the maximum dominates\nassert Max < Min\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeAssertionFailed {
		t.Fatalf("codes = %v, want [assertion_failed]", got)
	}
	want := "assertion failed: Max < Min\n" +
		"  the maximum dominates\n" +
		"  Max < Min\n" +
		"  ^   ^ ^\n" +
		"  100 | 0\n" +
		"      false"
	if diags[0].Message != want {
		t.Errorf("message = %q, want %q", diags[0].Message, want)
	}
}

// --- record literals ----------------------------------------------------------

const pointDecl = "pub type Point = { x: int, y: int }\n"

func TestRecordLiteralTypedForm(t *testing.T) {
	m, diags := analyze(pointDecl + "const O = Point{ x: 1, y: 2 }\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if m.Consts[0].Type.String() != "Point" {
		t.Errorf("O type = %s, want Point", m.Consts[0].Type)
	}
	named, ok := m.Consts[0].Type.(*ir.Named)
	if !ok || named.Def != m.Types[0] {
		t.Errorf("O's Named.Def = %v, want the module's Point definition", m.Consts[0].Type)
	}
	if got := m.Consts[0].Eval.String(); got != "{ x: 1, y: 2 }" {
		t.Errorf("O eval = %s, want { x: 1, y: 2 }", got)
	}
}

func TestRecordLiteralInferredForm(t *testing.T) {
	// The annotation supplies the inferred form's type, exactly as it types an
	// empty collection.
	m, diags := analyze(pointDecl + "const P: Point = { x: 1, y: 2 }\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if m.Consts[0].Type.String() != "Point" {
		t.Errorf("P type = %s, want Point", m.Consts[0].Type)
	}
	if got := m.Consts[0].Eval.String(); got != "{ x: 1, y: 2 }" {
		t.Errorf("P eval = %s, want { x: 1, y: 2 }", got)
	}
}

func TestRecordLiteralEmpty(t *testing.T) {
	m, diags := analyze("pub type Unit = {}\nconst U: Unit = {}\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if got := m.Consts[0].Eval.String(); got != "{}" {
		t.Errorf("U eval = %s, want {}", got)
	}
}

func TestRecordLiteralNormalizesFieldOrder(t *testing.T) {
	// The literal may write fields in any order; the folded constant is
	// canonical, so the two spellings evaluate identically.
	m, diags := analyze(pointDecl + "const A = Point{ y: 2, x: 1 }\nconst B = Point{ x: 1, y: 2 }\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if a, b := m.Consts[0].Eval.String(), m.Consts[1].Eval.String(); a != b {
		t.Errorf("evals differ: %s vs %s", a, b)
	}
}

func TestRecordLiteralNested(t *testing.T) {
	m, diags := analyze(pointDecl +
		"pub type Item = { id: int, name: string, pos: Point }\n" +
		"const Sword = Item{ id: 1, name: \"Sword\", pos: { x: 0, y: 0 } }\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if got := m.Consts[0].Eval.String(); got != "{ id: 1, name: \"Sword\", pos: { x: 0, y: 0 } }" {
		t.Errorf("Sword eval = %s", got)
	}
}

func TestRecordLiteralDatetimeDurationFields(t *testing.T) {
	m, diags := analyze("pub type Event = { at: datetime, cooldown: duration }\n" +
		"const E = Event{ at: D2026-06-05T00:00:00.000Z, cooldown: 90m }\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if got := m.Consts[0].Eval.String(); got != "{ at: D2026-06-05T00:00:00.000Z, cooldown: 1h30m }" {
		t.Errorf("E eval = %s", got)
	}
}

func TestRecordLiteralDiagnostics(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want diagnostic.Code
	}{
		{"missing field", pointDecl + "const A = Point{ x: 1 }\n", CodeMissingField},
		{"unknown field", pointDecl + "const B = Point{ x: 1, y: 2, z: 3 }\n", CodeUnknownField},
		{"missing field through annotation", pointDecl + "const C: Point = { x: 1 }\n", CodeMissingField},
		{"no expectation", "const D = { x: 1, y: 2 }\n", CodeUninferableRecord},
		{"unknown type name", "const E = Bogus{ x: 1 }\n", CodeUnknownType},
		{"not a record", "pub type Coin = int8\nconst F = Coin{ x: 1 }\n", CodeNotARecord},
		{"non-record expectation", "const G: int = { x: 1 }\n", CodeTypeMismatch},
		{"wrong record type", pointDecl + "pub type Unit = {}\nconst H: Unit = Point{ x: 1, y: 2 }\n", CodeTypeMismatch},
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

func TestRecordLiteralMissingFieldsReportedEach(t *testing.T) {
	_, diags := analyze(pointDecl + "const A = Point{}\n")
	got := codes(diags)
	if len(got) != 2 || got[0] != CodeMissingField || got[1] != CodeMissingField {
		t.Fatalf("codes = %v, want two missing_field", got)
	}
}

func TestRecordFieldRangeChecked(t *testing.T) {
	// A field value adapts to the field's declared type and is range-checked
	// against it — with and without an annotation on the constant.
	for _, src := range []string{
		"pub type B = { v: int8 }\nconst X = B{ v: 1000 }\n",
		"pub type B = { v: int8 }\nconst X: B = { v: 1000 }\n",
	} {
		_, diags := analyze(src)
		if got := codes(diags); len(got) != 1 || got[0] != CodeConstantOverflow {
			t.Fatalf("%q: codes = %v, want [constant_overflow]", src, got)
		}
	}
}

func TestRecordFieldTypeMismatch(t *testing.T) {
	_, diags := analyze(pointDecl + "const A = Point{ x: \"a\", y: 2 }\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeTypeMismatch {
		t.Fatalf("codes = %v, want [type_mismatch]", got)
	}
}

func TestRecordExpectationReachesCollectionElements(t *testing.T) {
	// The annotation's element type reaches each element, so an inferred
	// record literal works inside a list.
	m, diags := analyze(pointDecl + "const Path: list<Point> = [{ x: 0, y: 0 }, Point{ x: 1, y: 1 }]\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if got := m.Consts[0].Eval.String(); got != "[{ x: 0, y: 0 }, { x: 1, y: 1 }]" {
		t.Errorf("Path eval = %s", got)
	}
}

func TestRecordAnnotationFailureSuppressesUninferable(t *testing.T) {
	// The broken annotation is the problem; had it resolved it would have
	// typed the literal, so only unknown_type reports.
	_, diags := analyze("const X: Bogus = { x: 1 }\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeUnknownType {
		t.Fatalf("codes = %v, want [unknown_type]", got)
	}
}

func TestRecordLiteralInMethodBodyReturn(t *testing.T) {
	// The method's declared result type reaches an inferred record literal in
	// a return, through the same checking walk the const path uses.
	_, diags := analyze("pub type Point = { x: int, y: int } impl {\n" +
		"  pub origin(): Point {\n    return { x: 0, y: 0 }\n  }\n}\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
}

func TestRecordLiteralStructuralAnnotation(t *testing.T) {
	// A structural record annotation works exactly like a named one.
	m, diags := analyze("const P: { x: int, y: int } = { x: 1, y: 2 }\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if got := m.Consts[0].Eval.String(); got != "{ x: 1, y: 2 }" {
		t.Errorf("P eval = %s", got)
	}
}

// --- top-level functions --------------------------------------------------------

func TestFuncDeclAndCall(t *testing.T) {
	m, diags := analyze("pub fn double(x: int): int -> x * 2\nconst A = double(21)\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if len(m.Funcs) != 1 || m.Funcs[0].Name != "double" || !m.Funcs[0].Public {
		t.Fatalf("funcs = %v, want [pub double]", m.Funcs)
	}
	if m.Funcs[0].Result.String() != "int" || len(m.Funcs[0].Params) != 1 {
		t.Errorf("double signature = %v -> %v, want (x: int): int", m.Funcs[0].Params, m.Funcs[0].Result)
	}
	if m.Consts[0].Type.String() != "int" {
		t.Errorf("A type = %s, want int", m.Consts[0].Type)
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
		{"result mismatch", "fn f(): int { return \"x\" }\n", CodeTypeMismatch},
		{"arity", "fn f(x: int): int -> x\nconst A = f(1, 2)\n", CodeArityMismatch},
		{"undefined", "const X = unknownFn(1)\n", CodeUndefinedName},
		{"missing return", "fn g(x: int): int { }\n", CodeMissingReturn},
		{"unknown param type", "fn f(x: Bogus): int -> 1\n", CodeUnknownType},
		{"duplicate signature", "fn f(): int -> 1\nfn f(): int -> 2\n", CodeDuplicateFuncOverload},
		{"argument mismatch", "fn f(x: int): int -> x\nconst A = f(\"a\")\n", CodeTypeMismatch},
		{"function is not a value", "fn f(): int -> 1\nconst A = f\n", CodeUndefinedName},
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
	_, diags := analyze("fn f(x: int8): int8 -> x\nconst A = f(1000)\n")
	got := codes(diags)
	if len(got) != 2 || got[0] != CodeConstantOverflow || got[1] != CodeConstantOverflow {
		t.Fatalf("codes = %v, want two constant_overflow", got)
	}
}

func TestFuncMissingReturnNotForArrowOrMissingBody(t *testing.T) {
	// An arrow body always returns; a missing body is the parser's report,
	// not a missing return on top.
	_, diags := analyze("fn f(): int -> 1\n")
	if len(diags) != 0 {
		t.Fatalf("arrow body: unexpected diagnostics: %v", diags)
	}
	m, diags := analyze("fn f(): int\n")
	_ = m
	for _, d := range diags {
		if d.Code == CodeMissingReturn {
			t.Fatalf("missing body reported missing_return on top of the parse error: %v", diags)
		}
	}
}

func TestFuncRecursionGuard(t *testing.T) {
	// Infinite recursion folds to nothing — the depth guard, not a stack
	// overflow — and is not a type error (the result type is declared).
	m, diags := analyze("fn loop(x: int): int -> loop(x)\nconst X = loop(1)\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if m.Consts[0].Type.String() != "int" {
		t.Errorf("X type = %s, want int", m.Consts[0].Type)
	}
	if m.Consts[0].Eval != nil {
		t.Errorf("X eval = %v, want unevaluated", m.Consts[0].Eval)
	}
}

func TestFuncCalledFromMethodBody(t *testing.T) {
	src := "fn double(x: int): int -> x * 2\n" +
		"pub type T = int8 impl {\n  pub f(): int {\n    return double(3)\n  }\n}\n"
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
	m, diags := analyze("fn double(x: int): int -> x * 2\nconst D = [1, 2].map(fn(x) -> double(x))\n")
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
	m, diags := analyze("fn f(x: int): int -> x\nconst A = [1].map(fn(f) -> f + 1)\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if got := m.Consts[0].Eval.String(); got != "[2]" {
		t.Errorf("A eval = %s, want [2]", got)
	}
}

func TestFuncMutualRecursionGuard(t *testing.T) {
	// Mutual recursion through two functions bottoms out at the depth guard.
	src := "fn a(x: int): int -> b(x)\nfn b(x: int): int -> a(x)\nconst X = a(1)\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if m.Consts[0].Eval != nil {
		t.Errorf("X eval = %v, want unevaluated", m.Consts[0].Eval)
	}
}

func TestFuncInAssert(t *testing.T) {
	m, diags := analyze("fn area(w: int, h: int): int -> w * h\nassert area(3, 4) == 12\n")
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
		{"function arrow body", "fn f(): int -> self + 1\n"},
		{"function block body", "fn f(): int { return self }\n"},
		{"lambda in function body", "fn f(): list<int> -> [1].map(fn(x) -> self)\n"},
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
	src := "type Port = int32 where self >= 1\n" +
		"pub type L = int8 impl {\n  pub double(): int8 {\n    return self * 2\n  }\n}\n"
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
	src := "fn f(x: int): int -> 1\nfn f(x: string): int -> 2\n" +
		"const A = f(9)\nconst B = f(\"a\")\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if got := m.Consts[0].Eval.String(); got != "1" {
		t.Errorf("A eval = %s, want 1 (the int overload)", got)
	}
	if got := m.Consts[1].Eval.String(); got != "2" {
		t.Errorf("B eval = %s, want 2 (the string overload)", got)
	}
	if len(m.Funcs) != 2 {
		t.Errorf("module funcs = %d, want both overloads", len(m.Funcs))
	}
}

func TestFuncOverloadByArity(t *testing.T) {
	src := "fn f(): int -> 0\nfn f(x: int): int -> x\nconst A = f()\nconst B = f(7)\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if a, b := m.Consts[0].Eval.String(), m.Consts[1].Eval.String(); a != "0" || b != "7" {
		t.Errorf("evals = %s, %s, want 0 and 7", a, b)
	}
}

func TestFuncOverloadDiagnostics(t *testing.T) {
	overloads := "fn f(x: int): int -> 1\nfn f(x: string): int -> 2\n"
	cases := []struct {
		name string
		src  string
		want diagnostic.Code
	}{
		{"no match", overloads + "const A = f(true)\n", CodeNoMatchingFuncOverload},
		{"no match by arity", overloads + "const A = f(1, 2)\n", CodeNoMatchingFuncOverload},
		{"ambiguous", "fn g(x: int8): int -> 1\nfn g(x: int32): int -> 2\nconst A = g(1)\n", CodeAmbiguousFuncOverload},
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
	// A concretely typed argument disambiguates same-kind overloads in
	// typing; the type-blind fold stays conservative and does not pick.
	src := "fn g(x: int8): int -> 1\nfn g(x: int32): int -> 2\n" +
		"const B: int8 = 1\nconst A = g(B)\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if m.Consts[1].Type.String() != "int" {
		t.Errorf("A type = %s, want int", m.Consts[1].Type)
	}
	if m.Consts[1].Eval != nil {
		t.Errorf("A eval = %v, want unevaluated (kind-blind fold stays conservative)", m.Consts[1].Eval)
	}
}

func TestFuncOverloadRecordArgDefers(t *testing.T) {
	// An inferred record literal cannot select an overload; a typed one and
	// the other arguments do, and the winner's parameter reaches into it.
	src := "pub type Point = { x: int, y: int }\n" +
		"fn f(p: Point, tag: int): int -> tag\n" +
		"fn f(p: Point, tag: string): int -> 9\n" +
		"const A = f({ x: 1, y: 2 }, 5)\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if got := m.Consts[0].Eval.String(); got != "5" {
		t.Errorf("A eval = %s, want 5", got)
	}
}

func TestErrorConstruction(t *testing.T) {
	// error("msg") is a conversion: it types as error, folds to an error
	// value, and message() reads the message back — all at compile time.
	m, diags := analyze("const E = error(\"boom\")\nconst M = E.message()\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if got := m.Consts[0].Type.String(); got != "error" {
		t.Errorf("E type = %s, want error", got)
	}
	if got := m.Consts[0].Eval.String(); got != "error(\"boom\")" {
		t.Errorf("E eval = %s, want error(\"boom\")", got)
	}
	if got := m.Consts[1].Type.String(); got != "string" {
		t.Errorf("M type = %s, want string", got)
	}
	if got := m.Consts[1].Eval.String(); got != "\"boom\"" {
		t.Errorf("M eval = %s, want \"boom\"", got)
	}
}

func TestErrorConversionTypeChecks(t *testing.T) {
	// error constructs from exactly one string: a non-string argument is the
	// familiar type_mismatch, a wrong count an arity_mismatch.
	if _, diags := analyze("const E = error(123)\n"); !hasCode(diags, CodeTypeMismatch) {
		t.Errorf("error(123): want type_mismatch, got %v", codes(diags))
	}
	if _, diags := analyze("const E = error()\n"); !hasCode(diags, CodeArityMismatch) {
		t.Errorf("error(): want arity_mismatch, got %v", codes(diags))
	}
	if _, diags := analyze("const E = error(\"a\", \"b\")\n"); !hasCode(diags, CodeArityMismatch) {
		t.Errorf("error(\"a\", \"b\"): want arity_mismatch, got %v", codes(diags))
	}
}

func TestErrorFlowsIntoUnion(t *testing.T) {
	// A fallible function returns its failure as a union member, and the
	// union-typed result flows into a matching annotation.
	src := "pub fn parse(s: string): int8 | error -> error(s)\n" +
		"const P: int8 | error = parse(\"no\")\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if got := m.Consts[0].Eval.String(); got != "error(\"no\")" {
		t.Errorf("P eval = %s, want error(\"no\")", got)
	}
	// A non-member initializer still mismatches.
	if _, diags := analyze("const X: int8 | error = \"no\"\n"); !hasCode(diags, CodeTypeMismatch) {
		t.Errorf("string into int8 | error: want type_mismatch, got %v", codes(diags))
	}
}

func TestEffectDeclarationsPropagate(t *testing.T) {
	// A function calling an effectful one must declare the effects itself;
	// declaring them silences the check, and awaiting consumes async.
	src := "extern fn io async fetch(url: string): string\n" +
		"pub fn io async page(url: string): string {\n" +
		"  return await fetch(url)\n" +
		"}\n"
	if _, diags := analyze(src); len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
}

func TestMissingEffect(t *testing.T) {
	// Each undeclared-but-used effect is reported once, at the first site.
	src := "extern fn io async fetch(url: string): string\n" +
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
	src = "extern fn nondet roll(): int\n" +
		"pub fn nondet f(): int {\n" +
		"  return await roll()\n" +
		"}\n"
	if _, diags := analyze(src); !hasCode(diags, CodeMissingEffect) {
		t.Errorf("await without async: want missing_effect, got %v", codes(diags))
	}
}

func TestMissingEffectOnMethod(t *testing.T) {
	src := "extern fn io async fetch(url: string): string\n" +
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

func TestUnusedEffect(t *testing.T) {
	// A declared effect the body never uses is a warning; an extern's
	// effects are roots and never flagged.
	_, diags := analyze("pub fn io f(): int -> 1\n")
	if !hasCode(diags, CodeUnusedEffect) {
		t.Fatalf("want unused_effect, got %v", codes(diags))
	}
	if _, diags := analyze("extern fn io async fetch(url: string): string\n"); len(diags) != 0 {
		t.Errorf("extern roots should not be checked, got %v", codes(diags))
	}
}

func TestEffectPropagatesThroughLambda(t *testing.T) {
	// A literal's body executes where it is applied, so its effect uses
	// count toward the enclosing declaration.
	src := "extern fn nondet roll(): int\n" +
		"pub fn f(): int {\n" +
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
	roots := "extern fn nondet roll(): int\nextern fn io async fetch(url: string): string\n"
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
	if _, diags := analyze("fn one(): int -> 1\nconst A = one()\n"); hasCode(diags, CodeEffectInPureContext) {
		t.Errorf("pure call flagged: %v", codes(diags))
	}
}

func TestEffectInTernaryBranch(t *testing.T) {
	// A ternary's branches are part of the body the effect walker must pierce:
	// an effectful call in the then or else branch counts toward the enclosing
	// declaration's effects, exactly as one in a return value does. Before
	// collectEffectUses handled TernaryExpr, the branch was never visited, so
	// the effect slipped past both completeness checks.
	roots := "extern fn nondet roll(): int\n"

	// missing_effect: an undeclared effect in a ternary branch of a function body.
	for _, body := range []string{
		"return flag ? roll() : 0",
		"return flag ? 0 : roll()",
		"return (roll() == 1) ? 0 : 0", // the condition counts too
	} {
		src := roots + "pub fn f(flag: bool): int {\n  " + body + "\n}\n"
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
	src := "pub fn nondet f(): int -> 1\nconst A = f()\n"
	m, diags := analyze(src)
	if !hasCode(diags, CodeEffectInPureContext) {
		t.Fatalf("want effect_in_pure_context, got %v", codes(diags))
	}
	if m.Consts[0].Eval != nil {
		t.Errorf("A eval = %s, want unevaluated", m.Consts[0].Eval)
	}
	// The pure twin folds as ever.
	m, _ = analyze("pub fn g(): int -> 1\nconst A = g()\n")
	if m.Consts[0].Eval.String() != "1" {
		t.Errorf("pure call eval = %s, want 1", m.Consts[0].Eval)
	}
}

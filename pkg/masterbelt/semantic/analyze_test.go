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

func TestUnknownTypeInDeclaration(t *testing.T) {
	// A reference to an undeclared type in a type declaration is reported.
	for _, src := range []string{
		"pub type Coin = Nope\n",            // unknown body
		"pub type Pair = int8 | Nope\n",     // unknown union member
		"pub type Rec = {\n  id: Nope\n}\n", // unknown field type
		"pub type Box<T: Nope> = T\n",       // unknown constraint
		"pub type Lvl = int8 impl {\n  pub m(): Nope {\n    return self\n  }\n}\n", // unknown result
	} {
		if _, diags := analyze(src); !hasCode(diags, CodeUnknownType) {
			t.Errorf("%q: want unknown_type, got %v", src, codes(diags))
		}
	}
	// A well-formed declaration referencing only known types has no such error.
	if _, diags := analyze("pub type Coin = int8\npub type GameValue = Coin | int8\n"); hasCode(diags, CodeUnknownType) {
		t.Errorf("known types should not be reported unknown: %v", codes(diags))
	}
}

func TestDuplicateTypeDeclaration(t *testing.T) {
	_, diags := analyze("pub type Coin = int8\npub type Coin = int16\n")
	if !hasCode(diags, CodeDuplicateDeclaration) {
		t.Errorf("want duplicate_declaration for redeclared type, got %v", codes(diags))
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

func TestDuplicateOverload(t *testing.T) {
	// The same name with the same parameter types is a true redeclaration:
	// the first wins, the repeat is reported.
	src := `pub type Score = int32 impl {
  pub fn merge(points: self): self {
    return self + points
  }
  pub fn merge(other: self): self {
    return self
  }
}
const Base: Score = 100
const X = Base.merge(5)
`
	m, diags := analyze(src)
	if got := codes(diags); len(got) != 1 || got[0] != CodeDuplicateOverload {
		t.Fatalf("codes = %v, want [duplicate_overload]", got)
	}
	if len(m.Types[0].Methods) != 1 {
		t.Errorf("Score has %d methods, want 1 (the repeat dropped)", len(m.Types[0].Methods))
	}
	// The call still resolves through the surviving first declaration.
	if got := m.Consts[1].Type.String(); got != "Score" {
		t.Errorf("X type = %s, want Score", got)
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
	// self has no referent at the top level; no other pass reports it, so the
	// assertion is reported as unverifiable rather than passing silently.
	_, diags := analyze("assert self == self\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeAssertionNotConstant {
		t.Fatalf("codes = %v, want [assertion_not_constant]", got)
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

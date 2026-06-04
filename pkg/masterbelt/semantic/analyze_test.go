package semantic

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ir"
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

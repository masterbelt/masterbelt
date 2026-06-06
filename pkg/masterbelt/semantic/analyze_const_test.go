// This file tests constant folding and the value half of analysis: literal and
// reference value evaluation, string/collection/error/datetime/duration folding,
// overflow, and the short-circuit and division-by-zero rules.
package semantic

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

func TestAnnotatedAndInferred(t *testing.T) {
	m, diags := analyze("const A: int = 1\nconst B = 0\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if m.Consts[0].Type.String() != "int" {
		t.Errorf("A type = %s, want int", m.Consts[0].Type)
	}
	if m.Consts[1].Type.String() != "nint" {
		t.Errorf("B type = %s, want nint", m.Consts[1].Type)
	}
}

func TestValueEvaluation(t *testing.T) {
	m, diags := analyze("const A = 100\nconst B = A\nconst C: long = B\n")
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
	m, diags := analyze("const L: list<nint> = [1, 2, 3]\nconst M: map<string, nint> = [\"k\": 1]\nconst I = [10, 20]\nconst E: list<nint> = []\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if m.Consts[0].Type.String() != "list<nint>" {
		t.Errorf("L type = %s, want list<nint>", m.Consts[0].Type)
	}
	// The list folds to a collection constant of its elements.
	if ev := m.Consts[0].Eval; ev == nil || ev.Kind != ir.ConstCollection || len(ev.Coll) != 3 ||
		ev.Coll[0].Key != nil || ev.Coll[0].Value.Int.Int64() != 1 {
		t.Errorf("L eval = %v, want collection [1 2 3]", m.Consts[0].Eval)
	}
	if m.Consts[1].Type.String() != "map<string, nint>" {
		t.Errorf("M type = %s, want map<string, nint>", m.Consts[1].Type)
	}
	if ev := m.Consts[1].Eval; ev == nil || ev.Kind != ir.ConstCollection || len(ev.Coll) != 1 ||
		ev.Coll[0].Key == nil || ev.Coll[0].Key.Str != "k" || ev.Coll[0].Value.Int.Int64() != 1 {
		t.Errorf("M eval = %v, want collection [k: 1]", m.Consts[1].Eval)
	}
	// An un-annotated list infers its element type.
	if m.Consts[2].Type.String() != "list<nint>" {
		t.Errorf("I type = %s, want list<nint>", m.Consts[2].Type)
	}
	// An empty list takes its type from the annotation and folds to [].
	if m.Consts[3].Type.String() != "list<nint>" {
		t.Errorf("E type = %s, want list<nint>", m.Consts[3].Type)
	}
	if ev := m.Consts[3].Eval; ev == nil || ev.Kind != ir.ConstCollection || len(ev.Coll) != 0 {
		t.Errorf("E eval = %v, want empty collection", m.Consts[3].Eval)
	}
}

func TestCollectionElementAdaptsAndChecks(t *testing.T) {
	// Integer elements adapt to the annotation's element type, range-checked.
	if _, diags := analyze("const X: list<sbyte> = [1, 2, 3]\n"); len(diags) != 0 {
		t.Errorf("list<sbyte> = [1,2,3] should be fine, got %v", codes(diags))
	}
	if m, diags := analyze("const X: map<string, nint> = [\"a\": 1, \"b\": 2]\n"); len(diags) != 0 {
		t.Errorf("map literal should be fine, got %v", codes(diags))
	} else if m.Consts[0].Type.String() != "map<string, nint>" {
		t.Errorf("type = %s, want map<string, nint>", m.Consts[0].Type)
	}
}

func TestCollectionDiagnostics(t *testing.T) {
	cases := []struct {
		src  string
		code diagnostic.Code
	}{
		{"const X: list<sbyte> = [1, 999]\n", CodeConstantOverflow}, // element out of range
		{"const X: list<nint> = [\"a\"]\n", CodeTypeMismatch},       // wrong element type
		{"const X = []\n", CodeUninferableCollection},               // empty, no annotation
		{"const X = [1, \"a\"]\n", CodeUninferableCollection},       // heterogeneous, no annotation
		{"const X: nint = [1]\n", CodeTypeMismatch},                 // collection under scalar annotation
		{"const X: list<nint> = [\"k\": 1]\n", CodeTypeMismatch},    // map literal, list annotation
		{"const X: map<string, nint> = [1, 2]\n", CodeTypeMismatch}, // list literal, map annotation
	}
	for _, tc := range cases {
		_, diags := analyze(tc.src)
		if !hasCode(diags, tc.code) {
			t.Errorf("%q: want %s, got %v", tc.src, tc.code, codes(diags))
		}
	}
}

func TestConstantOverflow(t *testing.T) {
	_, diags := analyze("const X: sbyte = 1000\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeConstantOverflow {
		t.Fatalf("codes = %v, want [constant_overflow]", got)
	}
}

func TestIntLiteralDoesNotOverflow(t *testing.T) {
	// An un-annotated integer literal is the arbitrary-precision int; only a
	// sized concrete type triggers the range check.
	_, diags := analyze("const X = 99999999999999999999999999\n")
	if len(diags) != 0 {
		t.Fatalf("an nint literal should not overflow: %v", diags)
	}
}

func TestOverflowThroughReference(t *testing.T) {
	_, diags := analyze("const A = 1000\nconst B: sbyte = A\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeConstantOverflow {
		t.Fatalf("codes = %v, want [constant_overflow]", got)
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

func TestAnnotatedEmptyCollection(t *testing.T) {
	// Checking mode gives an empty literal its annotation's type, so it is not
	// uninferable.
	for _, src := range []string{
		"const Empty: list<nint> = []\n",
		"const Empty: map<string, nint> = []\n",
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
	src := "pub fn parse(s: string): sbyte | error -> error(s)\n" +
		"const P: sbyte | error = parse(\"no\")\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if got := m.Consts[0].Eval.String(); got != "error(\"no\")" {
		t.Errorf("P eval = %s, want error(\"no\")", got)
	}
	// A non-member initializer still mismatches.
	if _, diags := analyze("const X: sbyte | error = \"no\"\n"); !hasCode(diags, CodeTypeMismatch) {
		t.Errorf("string into sbyte | error: want type_mismatch, got %v", codes(diags))
	}
}

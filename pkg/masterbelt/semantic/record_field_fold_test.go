package semantic

import "testing"

// These tests pin stream C of the folding-completeness work: a method call whose
// receiver is a record field access (p.lv.increment()) folds. The field's static
// type is read from the base record type's resolved field annotation — a
// syntactic channel, never the type query — so the method on the field's nominal
// type resolves and its body folds.

const levelPoint = "pub type Level = sbyte impl {\n" +
	"  pub increment(): self {\n    return self + 1\n  }\n" +
	"}\n" +
	"pub type Point = { lv: Level }\n"

// TestRecordFieldMethodFolds is the central case: a const of a record type whose
// field is a nominal type folds a method call on that field.
func TestRecordFieldMethodFolds(t *testing.T) {
	src := levelPoint +
		"const P: Point = {lv: Level(5)}\n" +
		"const Six = P.lv.increment()\n"
	if got := evalOf(t, src, "Six").Int.Int64(); got != 6 {
		t.Errorf("P.lv.increment() = %d, want 6", got)
	}
}

// TestRecordFieldMethodInAssert checks the fold reaches an assertion.
func TestRecordFieldMethodInAssert(t *testing.T) {
	src := levelPoint +
		"const P: Point = {lv: Level(5)}\n" +
		"assert P.lv.increment() == 6\n"
	if got := codes(diagsOf(t, src)); len(got) != 0 {
		t.Fatalf("codes = %v, want no diagnostics (the assertion folds to true)", got)
	}
}

// TestNestedRecordFieldMethodFolds covers a nested field path a.b.c.method():
// each field access resolves the next record's field annotation.
func TestNestedRecordFieldMethodFolds(t *testing.T) {
	src := "pub type Level = sbyte impl {\n" +
		"  pub increment(): self {\n    return self + 1\n  }\n" +
		"}\n" +
		"pub type Inner = { lv: Level }\n" +
		"pub type Outer = { inner: Inner }\n" +
		"const O: Outer = {inner: {lv: Level(9)}}\n" +
		"const Ten = O.inner.lv.increment()\n"
	if got := evalOf(t, src, "Ten").Int.Int64(); got != 10 {
		t.Errorf("O.inner.lv.increment() = %d, want 10", got)
	}
}

// TestRecordFieldMethodChain covers a method chain off a record field:
// p.lv.increment().increment() resolves the result def from the method's self
// result, the way a non-field chain does.
func TestRecordFieldMethodChain(t *testing.T) {
	src := levelPoint +
		"const P: Point = {lv: Level(5)}\n" +
		"const Seven = P.lv.increment().increment()\n"
	if got := evalOf(t, src, "Seven").Int.Int64(); got != 7 {
		t.Errorf("P.lv.increment().increment() = %d, want 7", got)
	}
}

// TestRecordFieldAnonymousType covers an inline (anonymous) record annotation on
// the const itself: the field's type still resolves from the annotation.
func TestRecordFieldAnonymousType(t *testing.T) {
	src := "pub type Level = sbyte impl {\n" +
		"  pub increment(): self {\n    return self + 1\n  }\n" +
		"}\n" +
		"const P: { lv: Level } = {lv: Level(5)}\n" +
		"const Six = P.lv.increment()\n"
	if got := evalOf(t, src, "Six").Int.Int64(); got != 6 {
		t.Errorf("anonymous-record P.lv.increment() = %d, want 6", got)
	}
}

// TestRecordFieldPrimitiveNoFold checks a field whose type is a bare primitive
// (no user method): a method that does not exist there does not fold, but the
// primitive's own operators still do — and a missing method is a type error, not
// a silent wrong fold. Here the field is a plain int, and add (an operator) folds.
func TestRecordFieldPrimitiveOperatorFolds(t *testing.T) {
	src := "pub type Box = { n: nint }\n" +
		"const B: Box = {n: 5}\n" +
		"const Six = B.n + 1\n"
	if got := evalOf(t, src, "Six").Int.Int64(); got != 6 {
		t.Errorf("B.n + 1 = %d, want 6", got)
	}
}

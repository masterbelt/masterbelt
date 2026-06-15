package semantic

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// the master the query-algebra tests build columns over.
const queryCardsMaster = "master Cards {\n" +
	"  record { id: int, cost: int, name: string }\n" +
	"  primary id\n" +
	"}\n"

// funcNamed returns the resolved top-level function of the given name, or nil.
func funcNamed(m *ir.Module, name string) *ir.Function {
	if m == nil {
		return nil
	}
	for _, f := range m.Funcs {
		if f.Name == name {
			return f
		}
	}
	return nil
}

// returnedType is the resolved type of the value a single-return function body
// yields — the type the query algebra settled the body expression to.
func returnedType(fn *ir.Function) ir.Type {
	if fn == nil {
		return nil
	}
	for _, s := range fn.Body {
		if r, ok := s.(*ir.Return); ok && r.Value != nil {
			return ir.TypeOf(r.Value)
		}
	}
	return nil
}

// TestColumnComparisonYieldsPredicate pins the core of the query algebra: a
// comparison of a column<M,T> against a value or another column yields a
// predicate<M>, and the logical operators compose predicates into a predicate —
// never a bool. A whole where condition therefore settles to predicate<Cards>,
// the type that guarantees it is SQL-expressible.
func TestColumnComparisonYieldsPredicate(t *testing.T) {
	src := queryCardsMaster +
		"fn probe(c: column<Cards, int>, d: column<Cards, int>): predicate<Cards> {\n" +
		"  return (c > 100 && c < 200) || c >= d\n" +
		"}\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	if got := returnedType(funcNamed(m, "probe")); got == nil || got.String() != "predicate<Cards>" {
		t.Errorf("return type = %v, want predicate<Cards>", got)
	}
}

// TestColumnEqualityYieldsPredicate pins equality (the == operator) on a column
// of a non-numeric type produces a predicate too — the algebra is not limited to
// the ordered comparisons.
func TestColumnEqualityYieldsPredicate(t *testing.T) {
	src := queryCardsMaster +
		"fn probe(c: column<Cards, string>): predicate<Cards> {\n" +
		"  return c == \"fire\"\n" +
		"}\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	if got := returnedType(funcNamed(m, "probe")); got == nil || got.String() != "predicate<Cards>" {
		t.Errorf("return type = %v, want predicate<Cards>", got)
	}
}

// TestPredicateNegationYieldsPredicate pins the unary ! on a predicate is itself a
// predicate, so a negated condition stays in the algebra.
func TestPredicateNegationYieldsPredicate(t *testing.T) {
	src := queryCardsMaster +
		"fn probe(c: column<Cards, int>): predicate<Cards> {\n" +
		"  return !(c > 100)\n" +
		"}\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	if got := returnedType(funcNamed(m, "probe")); got == nil || got.String() != "predicate<Cards>" {
		t.Errorf("return type = %v, want predicate<Cards>", got)
	}
}

// TestColumnsBindingFieldIsColumn pins that a field access off a query binding
// (c.cost, where c : columns<Cards>) reads that field as a column<Cards, T> rather
// than a value — so the comparison that follows produces a predicate<Cards>, and a
// whole where condition written over the binding settles to predicate<Cards>. This
// is how a query names a column: through the binding, never self.
func TestColumnsBindingFieldIsColumn(t *testing.T) {
	src := queryCardsMaster +
		"fn probe(c: columns<Cards>): predicate<Cards> {\n" +
		"  return c.cost > 100 && c.name == \"fire\"\n" +
		"}\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	if got := returnedType(funcNamed(m, "probe")); got == nil || got.String() != "predicate<Cards>" {
		t.Errorf("return type = %v, want predicate<Cards>", got)
	}
}

// TestColumnsFieldType pins the field-access resolution itself: c.cost reads the
// int column of Cards as column<Cards, int> — the row field's value type lifted
// into query mode.
func TestColumnsFieldType(t *testing.T) {
	src := queryCardsMaster +
		"fn probe(c: columns<Cards>): column<Cards, int> {\n" +
		"  return c.cost\n" +
		"}\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	if got := returnedType(funcNamed(m, "probe")); got == nil || got.String() != "column<Cards, int>" {
		t.Errorf("return type = %v, want column<Cards, int>", got)
	}
}

// TestColumnsUnknownFieldNotAColumn pins that a name the master's row does not have
// does not resolve to a column: c.missing is not a column<Cards, _>, so it cannot
// stand where a column is needed. (It resolves to no readable member, exactly as a
// record's unknown field does — the value-position read is left to the type that
// consumes it; the query lowering rejects a non-column field access.)
func TestColumnsUnknownFieldNotAColumn(t *testing.T) {
	src := queryCardsMaster +
		"fn probe(c: columns<Cards>): column<Cards, int> {\n" +
		"  return c.missing\n" +
		"}\n"
	m, _ := analyze(src)
	if got := returnedType(funcNamed(m, "probe")); got != nil && got.String() == "column<Cards, int>" {
		t.Errorf("c.missing must not resolve to a column, got %v", got)
	}
}

// TestBareBoolIsNotPredicate pins the guarantee the typed algebra exists for: a
// plain bool is not a predicate, so a where condition that is "just true" — or any
// non-SQL-expressible bool — is a type error, caught at compile time rather than
// at query lowering.
func TestBareBoolIsNotPredicate(t *testing.T) {
	src := queryCardsMaster +
		"fn probe(c: column<Cards, int>): predicate<Cards> {\n" +
		"  return true\n" +
		"}\n"
	_, diags := analyze(src)
	if len(diags) == 0 {
		t.Fatal("want a type error: bool is not predicate<Cards>")
	}
}

// TestBareColumnIsNotPredicate pins that a column read that was never compared is
// not a predicate either — a column<M,T> must be turned into a predicate by a
// comparison before it can be a where condition.
func TestBareColumnIsNotPredicate(t *testing.T) {
	src := queryCardsMaster +
		"fn probe(c: column<Cards, int>): predicate<Cards> {\n" +
		"  return c\n" +
		"}\n"
	_, diags := analyze(src)
	if len(diags) == 0 {
		t.Fatal("want a type error: column<Cards, int> is not predicate<Cards>")
	}
}

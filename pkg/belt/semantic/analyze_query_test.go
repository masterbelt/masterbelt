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

// probeReturnType is the resolved type of the value the probe function's single
// return yields — the type the query algebra settled the body expression to.
func probeReturnType(m *ir.Module) ir.Type {
	if m == nil {
		return nil
	}
	for _, f := range m.Funcs {
		if f.Name != "probe" {
			continue
		}
		for _, s := range f.Body {
			if r, ok := s.(*ir.Return); ok && r.Value != nil {
				return ir.TypeOf(r.Value)
			}
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
	if got := probeReturnType(m); got == nil || got.String() != "predicate<Cards>" {
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
	if got := probeReturnType(m); got == nil || got.String() != "predicate<Cards>" {
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
	if got := probeReturnType(m); got == nil || got.String() != "predicate<Cards>" {
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
	if got := probeReturnType(m); got == nil || got.String() != "predicate<Cards>" {
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
	if got := probeReturnType(m); got == nil || got.String() != "column<Cards, int>" {
		t.Errorf("return type = %v, want column<Cards, int>", got)
	}
}

// TestColumnsBareEnumMemberResolves pins that a bare enum member compares against
// an enum column: c.rarity == legend resolves to predicate<Cards> through the
// column's element type, the same way value-mode rarity == legend resolves through
// the row field — not only the qualified Rarity.legend.
func TestColumnsBareEnumMemberResolves(t *testing.T) {
	src := "enum Rarity { common; rare; legend }\n" +
		"master Cards {\n  record { id: int, rarity: Rarity }\n  primary id\n}\n" +
		"fn probe(c: columns<Cards>): predicate<Cards> {\n" +
		"  return c.rarity == legend\n" +
		"}\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	if got := probeReturnType(m); got == nil || got.String() != "predicate<Cards>" {
		t.Errorf("return type = %v, want predicate<Cards>", got)
	}
}

// TestColumnsTypeShadowedNotHijacked pins that a user type named columns shadows the
// query binding: a field access off the user's columns<T> reads the user's type,
// not master M's columns. The query-binding path matches the prelude type by
// identity, not by name.
func TestColumnsTypeShadowedNotHijacked(t *testing.T) {
	src := "master Cards {\n  record { id: int, cost: int }\n  primary id\n}\n" +
		"type columns<T> = int\n" +
		"fn probe(c: columns<Cards>): column<Cards, int> {\n" +
		"  return c.cost\n" +
		"}\n"
	m, _ := analyze(src)
	if got := probeReturnType(m); got != nil && got.String() == "column<Cards, int>" {
		t.Errorf("a shadowed columns type must not resolve c.cost to a query column, got %v", got)
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
	if got := probeReturnType(m); got != nil && got.String() == "column<Cards, int>" {
		t.Errorf("c.missing must not resolve to a column, got %v", got)
	}
}

// TestColumnOrderingRequiresOrderable pins that a column's ordering comparison is
// available only for an orderable element type, mirroring value mode: a bool column
// has no >, < (bool carries only equality), while an int, string, or enum column —
// and equality on any comparable column — does. The query algebra must not be looser
// than the value comparison it stands for.
func TestColumnOrderingRequiresOrderable(t *testing.T) {
	const m = "enum Rarity { common; rare; legend }\n" +
		"master Cards {\n  record { id: int, cost: int, name: string, rarity: Rarity, active: bool }\n  primary id\n}\n"
	cases := []struct {
		name, cond string
		wantErr    bool
	}{
		{"bool ordering rejected", "c.active > false", true},
		{"bool equality allowed", "c.active == true", false},
		{"int ordering allowed", "c.cost >= 0", false},
		{"string ordering allowed", "c.name < \"z\"", false},
		{"enum ordering allowed", "c.rarity <= legend", false},
		{"column-to-column ordering allowed", "c.cost > c.id", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := m + "fn probe(c: columns<Cards>): predicate<Cards> {\n  return " + tc.cond + "\n}\n"
			_, diags := analyze(src)
			if gotErr := len(diags) != 0; gotErr != tc.wantErr {
				t.Errorf("%q: gotErr=%v (diags %v), wantErr=%v", tc.cond, gotErr, codes(diags), tc.wantErr)
			}
		})
	}
}

// TestColumnEqualityRequiresComparable pins the equality half of the same rule: a
// column whose element type is not comparable has no == either, just as the value
// comparison would be invalid — so the comparison guard covers eql/neq, not only the
// orderings.
func TestColumnEqualityRequiresComparable(t *testing.T) {
	src := "type Blob = { x: int }\n" +
		"master M {\n  record { id: int, b: Blob }\n  primary id\n}\n" +
		"fn probe(c: columns<M>): predicate<M> {\n  return c.b == c.b\n}\n"
	_, diags := analyze(src)
	if len(diags) == 0 {
		t.Fatal("want a type error: a non-comparable column element has no == in a query")
	}
}

// TestNullableColumnComparisonResolves pins that a comparison on a nullable column
// type-checks: c.opt == null / != null (the null checks) and c.opt < 5 (against the
// non-null value) all resolve to predicate<M>. The comparison guard judges a
// nullable column T | null by T, so a union element does not spuriously reject it.
func TestNullableColumnComparisonResolves(t *testing.T) {
	const m = "master M {\n  record { id: int, opt: int | null }\n  primary id\n}\n"
	for _, cond := range []string{"c.opt == null", "c.opt != null", "c.opt < 5"} {
		src := m + "fn probe(c: columns<M>): predicate<M> {\n  return " + cond + "\n}\n"
		if _, diags := analyze(src); len(diags) != 0 {
			t.Errorf("%q: unexpected diagnostics %v", cond, codes(diags))
		}
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

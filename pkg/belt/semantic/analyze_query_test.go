package semantic

import (
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/internal/belttest"
	"github.com/masterbelt/masterbelt/pkg/belt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/project"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// the master the query-algebra tests build columns over.
const queryCardsMaster = "master Cards {\n" +
	"  record { id: int, cost: int, name: string }\n" +
	"  primary id\n" +
	"}\n"

// probeReturn is the resolved value the probe function's single return yields.
func probeReturn(m *ir.Module) ir.Value {
	if m == nil {
		return nil
	}
	for _, f := range m.Funcs {
		if f.Name != "probe" {
			continue
		}
		for _, s := range f.Body {
			if r, ok := s.(*ir.Return); ok && r.Value != nil {
				return r.Value
			}
		}
	}
	return nil
}

// probeReturnType is the resolved type of the value the probe function's single
// return yields — the type the query algebra settled the body expression to.
func probeReturnType(m *ir.Module) ir.Type {
	if v := probeReturn(m); v != nil {
		return ir.TypeOf(v)
	}
	return nil
}

// TestRelationCountInValidateAll pins that a relation count over a master name is
// not mistaken for a type-member access in a validate all assertion: the reference
// walk treats Item.where/Item.count as relation methods, so no unknown_associated_const
// is reported for a checker-accepted relation query. (The data-layer evaluation of
// such a check is a following slice; this pins the reference/type agreement.)
func TestRelationCountInValidateAll(t *testing.T) {
	src := "master Item {\n  record { id: int, power: int }\n  primary id\n" +
		"  validate {\n    all {\n      assert Item.where(fn(c) -> c.power > 10).count() < 50\n    }\n  }\n}\n"
	_, diags := analyze(src)
	if hasCode(diags, "belt.semantic.unknown_associated_const") {
		t.Fatalf("a relation method must not be reported as a type member: %v", codes(diags))
	}
}

// TestConstShadowsMaster pins that a top-level constant of a master's name shadows
// the master in value position — matching the lowering, which resolves the constant
// reference, not the relation. The checker must agree: were it to type the name as
// relation<Cards>, that would mismatch the declared nint and report a diagnostic the
// lowering's constant reference contradicts.
func TestConstShadowsMaster(t *testing.T) {
	src := "master Cards {\n  record { id: int }\n  primary id\n}\n" +
		"const Cards = 5\n" +
		"fn probe(): nint {\n  return Cards\n}\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics (the constant should shadow the master): %v", codes(diags))
	}
	r := probeReturn(m)
	if r == nil {
		t.Fatal("probe has no resolved return value")
	}
	if _, ok := r.(*ir.MasterRelation); ok {
		t.Fatalf("Cards resolved to the relation; the shadowing constant must win")
	}
	if _, ok := r.(*ir.Reference); !ok {
		t.Errorf("Cards resolved to %T, want the constant *ir.Reference", r)
	}
}

// TestConstShadowedMasterCallNotSuppressed pins that a method call over a
// const-shadowed master name is not silently accepted: the static-call path defers
// to the relation method path only when the name reads as the relation, so a const
// shadowing the master (the name is then the constant, not the relation) does not
// suppress the call. Cards.count() with a shadowing const reports unknown_static
// rather than leaving an untyped call with a nil result type.
func TestConstShadowedMasterCallNotSuppressed(t *testing.T) {
	src := "master Cards {\n  record { id: int }\n  primary id\n}\n" +
		"const Cards = 5\n" +
		"fn probe(): nint {\n  return Cards.count()\n}\n"
	_, diags := analyze(src)
	if len(diags) == 0 {
		t.Fatal("a method call over a const-shadowed master must not be silently accepted")
	}
	if !hasCode(diags, "belt.semantic.unknown_static") {
		t.Fatalf("want unknown_static for the shadowed-master call, got %v", codes(diags))
	}
}

// TestMasterNameResolvesToRelation pins that a master in value position is its
// relation: Cards.where(fn(c) -> ...).count() and Cards.count() resolve to nint,
// the query operations resolving as methods on relation<Cards>. A name that is one
// of the master's static fns still resolves as a static call (it is not shadowed by
// the relation methods).
func TestMasterNameResolvesToRelation(t *testing.T) {
	const m = "master Cards {\n  record { id: int, cost: int } impl {\n    pub static fn zero(): nint {\n      return 0\n    }\n  }\n  primary id\n}\n"
	cases := []struct{ name, body string }{
		{"filtered count", "Cards.where(fn(c) -> c.cost < 0).count()"},
		{"unfiltered count", "Cards.count()"},
		{"chained where", "Cards.where(fn(c) -> c.cost < 0).where(fn(c) -> c.id > 0).count()"},
		{"static fn still resolves", "Cards.zero()"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := m + "fn probe(): nint {\n  return " + tc.body + "\n}\n"
			out, diags := analyze(src)
			if len(diags) != 0 {
				t.Fatalf("unexpected diagnostics: %v", codes(diags))
			}
			if got := probeReturnType(out); got == nil || got.String() != "nint" {
				t.Errorf("return type = %v, want nint", got)
			}
		})
	}
}

// TestQualifiedMasterResolvesToRelation pins that a master reached through a
// namespace import is its relation the same way a local master name is: an imported
// deck.Cards.where(...).count() (and the unfiltered deck.Cards.count()) type-checks,
// resolving the query operations as methods on relation<Cards> rather than reporting
// a method on the metatype, and the reference walk does not mistake the relation
// method for a type-member access. Without the qualified twin of the bare-name
// relation reading, deck.Cards.count() reports "cannot apply method count to type".
func TestQualifiedMasterResolvesToRelation(t *testing.T) {
	diags := analyzeProject(t, map[string]string{
		"cards.belt": "pub master Cards {\n  record { id: int, cost: int }\n  primary id\n}\n",
		"main.belt": "use deck from \"cards.belt\"\n" +
			"fn filtered(): nint {\n  return deck.Cards.where(fn(r) -> r.cost < 10).count()\n}\n" +
			"fn total(): nint {\n  return deck.Cards.count()\n}\n",
	})
	if len(diags) != 0 {
		t.Fatalf("a qualified master relation query must type-check: %v", codes(diags))
	}
}

// findMasterRelation returns the first MasterRelation reachable from v by
// descending a method-call chain's receivers and arguments — the leaf a relation
// query lowers to, under any number of where/count calls.
func findMasterRelation(v ir.Value) *ir.MasterRelation {
	switch n := v.(type) {
	case *ir.MasterRelation:
		return n
	case *ir.Call:
		if r := findMasterRelation(n.Receiver); r != nil {
			return r
		}
		for _, a := range n.Args {
			if r := findMasterRelation(a); r != nil {
				return r
			}
		}
	}
	return nil
}

// TestQualifiedMasterLowersToRelation pins that the lowering — not only the checker
// — reads an imported master in value position as its relation: the resolved IR of
// deck.Cards.where(...).count() carries a MasterRelation over the imported Cards
// master at the foot of the call chain, not a reified type value. Were the lowering
// to keep the type-value reading, the checker (which types it as relation) and the
// lowering would disagree, and the query driver would never recognize the chain.
func TestQualifiedMasterLowersToRelation(t *testing.T) {
	files := map[string]string{
		"masterbelt.toml": "entry = \"main.belt\"\n",
		"cards.belt":      "pub master Cards {\n  record { id: int, cost: int }\n  primary id\n}\n",
		"main.belt": "use deck from \"cards.belt\"\n" +
			"fn probe(): nint {\n  return deck.Cards.where(fn(r) -> r.cost < 10).count()\n}\n",
	}
	root := belttest.WriteFiles(t, files)
	proj, pdiags := project.Open(root)
	if pdiags.Len() > 0 {
		t.Fatalf("project diagnostics: %v", pdiags.Items())
	}
	docs := map[FileID]*abstract.Document{}
	uses := map[FileID]map[*ast.UseDecl]FileID{}
	var mainID FileID
	for _, f := range proj.Files() {
		docs[FileID(f.ID)] = f.AST
		uses[FileID(f.ID)] = UsesOf(f.Uses)
		if strings.HasSuffix(string(f.ID), "main.belt") {
			mainID = FileID(f.ID)
		}
	}
	modules, diags := AnalyzeProgram(docs, uses)
	if ds := diags[mainID]; len(ds) != 0 {
		t.Fatalf("main.belt did not type-check: %v", codes(ds))
	}
	var rel *ir.MasterRelation
	for _, f := range modules[mainID].Funcs {
		if f.Name == "probe" {
			for _, s := range f.Body {
				if r, ok := s.(*ir.Return); ok && r.Value != nil {
					rel = findMasterRelation(r.Value)
				}
			}
		}
	}
	if rel == nil {
		t.Fatal("the query chain lowered no MasterRelation; the imported master was not read as a relation")
	}
	if rel.Master == nil || rel.Master.Name != "Cards" {
		t.Fatalf("MasterRelation master = %v, want the imported Cards", rel.Master)
	}
}

// TestQualifiedRelationCountInAssert pins that the reference walk treats a relation
// method on an imported master as a relation method, not a type-member access: a
// comptime assert over deck.Cards.where(...).count() reports no
// unknown_associated_const, the qualified twin of the bare-name validate-all case.
// (The reference walk only runs in comptime contexts — an assert, a validate clause,
// a constant — not a plain function body, so the assert is what exercises it.)
func TestQualifiedRelationCountInAssert(t *testing.T) {
	diags := analyzeProject(t, map[string]string{
		"cards.belt": "pub master Cards {\n  record { id: int, cost: int }\n  primary id\n}\n",
		"main.belt": "use deck from \"cards.belt\"\n" +
			"assert deck.Cards.where(fn(r) -> r.cost > 10).count() < 50\n",
	})
	if hasCode(diags, CodeUnknownAssociatedConst) {
		t.Fatalf("a qualified relation method must not be reported as a type member: %v", codes(diags))
	}
}

// TestRelationWhereCountResolves pins the relation type algebra: where narrows a
// relation<M> by a predicate (its lambda binds the columns of M and returns a
// predicate<M> — a column comparison, not a bool) and itself returns a relation<M>,
// so the operations chain; count consumes a relation to nint. A whole query
// expression therefore settles to nint at the type level.
func TestRelationWhereCountResolves(t *testing.T) {
	src := queryCardsMaster +
		"fn probe(r: relation<Cards>): nint {\n" +
		"  return r.where(fn(c) -> c.cost < 0).count()\n" +
		"}\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	if got := probeReturnType(m); got == nil || got.String() != "nint" {
		t.Errorf("return type = %v, want nint", got)
	}
}

// TestRelationWhereChains pins that where returns a relation<M>, so the narrowings
// compose: two where calls in a row each apply, and count consumes the result.
func TestRelationWhereChains(t *testing.T) {
	src := queryCardsMaster +
		"fn probe(r: relation<Cards>): nint {\n" +
		"  return r.where(fn(c) -> c.cost < 0).where(fn(c) -> c.name == \"x\").count()\n" +
		"}\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	if got := probeReturnType(m); got == nil || got.String() != "nint" {
		t.Errorf("return type = %v, want nint", got)
	}
}

// TestRelationCountResolves pins the unfiltered aggregate: count on a relation<M>
// directly (no where) is nint.
func TestRelationCountResolves(t *testing.T) {
	src := queryCardsMaster +
		"fn probe(r: relation<Cards>): nint {\n" +
		"  return r.count()\n" +
		"}\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	if got := probeReturnType(m); got == nil || got.String() != "nint" {
		t.Errorf("return type = %v, want nint", got)
	}
}

// TestRelationWhereRejectsBoolLambda pins that where's lambda must return a
// predicate<M>, not a bool — the same guarantee the column algebra gives, now at
// the relation boundary: a plain bool condition is a type error.
func TestRelationWhereRejectsBoolLambda(t *testing.T) {
	src := queryCardsMaster +
		"fn probe(r: relation<Cards>): nint {\n" +
		"  return r.where(fn(c) -> true).count()\n" +
		"}\n"
	_, diags := analyze(src)
	if len(diags) == 0 {
		t.Fatal("want a type error: where's lambda must return predicate<Cards>, not bool")
	}
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

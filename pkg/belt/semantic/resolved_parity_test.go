// These tests pin the contract between the type-blind value query and the
// assembler's resolution-armed late re-fold (resolved.go): where both fold
// they agree, and the resolutions only ever widen the foldable set — the
// monotonicity that keeps the incremental value query independent of typing
// without the published Eval losing values to that independence.
package semantic

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/belt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// analyzeWithQueries runs the reference analysis and returns the module
// alongside the raw value-query results per const declaration, so a test can
// compare the published Eval against the type-blind fold.
func analyzeWithQueries(t *testing.T, src string) (*ir.Module, map[string]*ir.Constant) {
	t.Helper()
	doc := abstract.NewDocument([]byte(src))
	file := doc.File()
	files := map[FileID]*ast.File{soleFileID: file}
	q := newDirectQueries(files, nil, universe())
	module, _ := assemble(soleFileID, file, positionsOf(doc.Concrete().Tree()), q, constShells(files), q.fnShells)
	raw := map[string]*ir.Constant{}
	for _, decl := range file.Decls {
		raw[decl.Name] = q.valueOf(decl)
	}
	return module, raw
}

// TestResolvedFoldParity checks the monotone split: a method call on a
// nominal-typed receiver is conservative in the type-blind value query (the
// blind graph carries no receiver type, so the overload set is unreachable)
// and folds in the published Eval, through the annotated graph's settled
// receiver type and the checker's selection. Where the raw query does fold, it
// agrees with the published value.
func TestResolvedFoldParity(t *testing.T) {
	src := "pub type Score = int impl {\n" +
		"  pub fn merge(points: self): self {\n    return self + points\n  }\n" +
		"  pub fn merge(active: bool): bool {\n    return active && self > 0\n  }\n" +
		"}\n" +
		"const Base: Score = 100\n" +
		"const Bumped = Base.merge(50)\n" +
		"const Counted = Base.merge(true)\n"
	module, raw := analyzeWithQueries(t, src)
	for _, c := range module.Consts {
		if c.Eval == nil {
			t.Fatalf("const %s did not fold", c.Name)
		}
		// Monotone: every raw fold survives into the published Eval unchanged.
		if raw[c.Name] != nil && !ir.ConstantsEqual(raw[c.Name], c.Eval) {
			t.Errorf("const %s: value query %v != published Eval %v", c.Name, raw[c.Name], c.Eval)
		}
	}
}

// TestResolvedFoldMonotone checks the widening case — gap (d): an overload
// split by a named type the kind rule cannot see stays unfolded in the
// type-blind value query and folds in the published Eval, through exactly the
// overload the checker selected. The resolutions never narrow: a call the
// kind rule can split (the record-argument direction, whose record kind the
// nint parameter refuses) folds identically through both.
func TestResolvedFoldMonotone(t *testing.T) {
	src := "pub type Celsius = { deg: nint }\n" +
		"pub fn describe(c: Celsius): string {\n  return \"record\"\n}\n" +
		"pub fn describe(n: nint): string {\n  return \"int\"\n}\n" +
		"const A = describe(3)\n" +
		"const B = describe(Celsius{ deg: 1 })\n"
	module, raw := analyzeWithQueries(t, src)
	// A: an integer fits both candidates under the kind rule (a named type is
	// undecidable), so the raw query stays conservative — and the resolutions
	// widen it to the checker's pick. If the raw query learns to split this,
	// the pin should move to the parity case above.
	if raw["A"] != nil {
		t.Errorf("A: the raw value query folded %v; expected the conservative nil", raw["A"])
	}
	for _, c := range module.Consts {
		want := map[string]string{"A": `"int"`, "B": `"record"`}[c.Name]
		if c.Eval == nil || c.Eval.String() != want {
			t.Errorf("const %s: published Eval = %v, want %s", c.Name, c.Eval, want)
		}
		// Monotone: every raw fold survives into the published Eval unchanged.
		if raw[c.Name] != nil && !ir.ConstantsEqual(raw[c.Name], c.Eval) {
			t.Errorf("const %s: value query %v != published Eval %v", c.Name, raw[c.Name], c.Eval)
		}
	}
}

// TestFuncCallTargetCorrected pins the FuncCall.Target story: the type-blind
// lowering picks among same-arity overloads by declaration order (the set's
// first), so for gap (d)'s shape it guesses wrong — and the write-back
// corrects Target to the checker's selection, recording it on Resolved too.
func TestFuncCallTargetCorrected(t *testing.T) {
	src := "pub type Celsius = { deg: nint }\n" +
		"pub fn describe(c: Celsius): string {\n  return \"record\"\n}\n" +
		"pub fn describe(n: nint): string {\n  return \"int\"\n}\n" +
		"const A = describe(3)\n"
	module, _ := analyzeWithQueries(t, src)
	call, ok := module.Consts[0].Value.(*ir.FuncCall)
	if !ok {
		t.Fatalf("A's value = %T, want *ir.FuncCall", module.Consts[0].Value)
	}
	if call.Resolved == nil {
		t.Fatal("A's call carries no Resolved overload")
	}
	if call.Target != call.Resolved {
		t.Errorf("Target %p != Resolved %p: the write-back must correct the lowering's guess", call.Target, call.Resolved)
	}
	// The selected individual is the nint overload — the second declaration,
	// which the arity-blind lowering could not have picked on its own.
	if got := call.Resolved.Syntax.Params[0].Name; got != "n" {
		t.Errorf("resolved overload's parameter = %q, want n (the nint overload)", got)
	}
}

// TestSubstWriteBack checks the checker's solved type-variable substitution is
// written back onto the call nodes: a generic function call records
// what the arguments pinned, a method call on a generic receiver records the
// receiver's bindings combined with its own solved variables, and a call that
// pins nothing stays nil — so the IR carries the monomorphization input
// instead of discarding it after the result type is computed.
func TestSubstWriteBack(t *testing.T) {
	src := "pub fn identity<T>(x: T): T {\n  return x\n}\n" +
		"const N = identity(42)\n" +
		"const Doubled = [1, 2, 3].map(fn(x) -> x * 2)\n" +
		"const Plain = 1 + 2\n"
	module, _ := analyzeWithQueries(t, src)

	fc, ok := module.Consts[0].Value.(*ir.FuncCall)
	if !ok {
		t.Fatalf("N's value = %T, want *ir.FuncCall", module.Consts[0].Value)
	}
	if got := typeName(fc.Subst["T"]); got != "nint" {
		t.Errorf("identity(42) Subst[T] = %s, want nint (full subst: %v)", got, fc.Subst)
	}

	call, ok := module.Consts[1].Value.(*ir.Call)
	if !ok {
		t.Fatalf("Doubled's value = %T, want *ir.Call", module.Consts[1].Value)
	}
	if got := typeName(call.Subst["T"]); got != "nint" {
		t.Errorf("map Subst[T] = %s, want nint (the receiver's element binding; full subst: %v)", got, call.Subst)
	}
	if got := typeName(call.Subst["R"]); got != "nint" {
		t.Errorf("map Subst[R] = %s, want nint (the literal-solved result variable; full subst: %v)", got, call.Subst)
	}

	plain, ok := module.Consts[2].Value.(*ir.Call)
	if !ok {
		t.Fatalf("Plain's value = %T, want *ir.Call", module.Consts[2].Value)
	}
	if plain.Subst != nil {
		t.Errorf("1 + 2 Subst = %v, want nil (no type variable to pin)", plain.Subst)
	}
}

// typeName renders a substitution entry for the assertions above; a missing
// entry reads as "<none>".
func typeName(t ir.Type) string {
	if t == nil {
		return "<none>"
	}
	return t.String()
}

// TestResolvedStaticAndMethodWriteBack checks the other two call forms carry
// their selections in the IR: an overloaded static call and an overloaded
// method call both get Resolved bound after the checking walk.
func TestResolvedStaticAndMethodWriteBack(t *testing.T) {
	src := "pub type Celsius = { deg: nint } impl {\n" +
		"  pub static fn fromF(v: nint): Celsius {\n    return Celsius{ deg: (v - 32) * 5 / 9 }\n  }\n" +
		"  pub static fn fromF(c: Celsius): Celsius {\n    return c\n  }\n" +
		"}\n" +
		"pub type Score = int impl {\n" +
		"  pub fn merge(points: self): self {\n    return self + points\n  }\n" +
		"  pub fn merge(active: bool): bool {\n    return active && self > 0\n  }\n" +
		"}\n" +
		"const FromBoiling = Celsius.fromF(212)\n" +
		"const Base: Score = 100\n" +
		"const Counted = Base.merge(true)\n"
	module, _ := analyzeWithQueries(t, src)
	static, ok := module.Consts[0].Value.(*ir.StaticCall)
	if !ok {
		t.Fatalf("FromBoiling's value = %T, want *ir.StaticCall", module.Consts[0].Value)
	}
	if static.Resolved == nil || len(static.Resolved.Params) != 1 || static.Resolved.Params[0].Name != "v" {
		t.Errorf("static Resolved = %+v, want the fromF(v: nint) overload", static.Resolved)
	}
	method, ok := module.Consts[2].Value.(*ir.Call)
	if !ok {
		t.Fatalf("Counted's value = %T, want *ir.Call", module.Consts[2].Value)
	}
	if method.Resolved == nil || len(method.Resolved.Params) != 1 || method.Resolved.Params[0].Name != "active" {
		t.Errorf("method Resolved = %+v, want the merge(active: bool) overload", method.Resolved)
	}
}

package lint

import (
	"reflect"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// TestRefEdgeCoverage is the §8 guard: unused-declaration soundness assumes the
// marker follows every reference edge, so a value form that carries a
// tree:"ref" the marker forgets would let a live declaration read as dead. This
// pins the set of ref-carrying value forms against what visit handles; a new one
// fails here until visit (and this list) learn it.
func TestRefEdgeCoverage(t *testing.T) {
	followed := map[string]bool{
		"Reference":       true,
		"FuncCall":        true,
		"Call":            true,
		"StaticCall":      true,
		"EnumMemberValue": true,
		"AssocConstValue": true,
	}
	for _, v := range ir.ValueKinds() {
		ty := reflect.TypeOf(v).Elem()
		hasRef := false
		for i := range ty.NumField() {
			if ty.Field(i).Tag.Get("tree") == "ref" {
				hasRef = true
				break
			}
		}
		name := ty.Name()
		switch {
		case hasRef && !followed[name]:
			t.Errorf("value %s carries a tree:%q edge the marker does not follow — teach visit() and this list", name, "ref")
		case !hasRef && followed[name]:
			t.Errorf("value %s is listed as ref-carrying but has no tree:%q field — stale entry", name, "ref")
		}
	}
}

// TestUnusedMarkAndSweep: a private constant a live one references is kept; one
// nobody reaches is reported.
func TestUnusedMarkAndSweep(t *testing.T) {
	used := &ir.Const{Name: "Used", Syntax: &ast.ConstDecl{}}
	dead := &ir.Const{Name: "Dead", Syntax: &ast.ConstDecl{}}
	api := &ir.Const{Name: "Api", Public: true, Value: &ir.Reference{Target: used}, Syntax: &ast.ConstDecl{}}
	m := &ir.Module{Consts: []*ir.Const{api, used, dead}}
	span := fakeSpan(map[ast.Node][2]int{api.Syntax: {0, 10}, used.Syntax: {11, 10}, dead.Syntax: {22, 10}})

	l := &linter{span: span}
	l.unusedDeclarations(m)
	if len(l.diags) != 1 {
		t.Fatalf("got %d diagnostics, want 1 (only Dead): %+v", len(l.diags), l.diags)
	}
	if l.diags[0].Offset != 22 {
		t.Errorf("offset = %d, want 22 (Dead)", l.diags[0].Offset)
	}
	if l.diags[0].Code != "belt.lint.unused_declaration" {
		t.Errorf("code = %q", l.diags[0].Code)
	}
}

// TestUnusedDeadCycle: two private constants that name only each other are both
// dead — the mark-and-sweep reaches neither from a root, and the visited set
// makes the cycle terminate (a reference count would call both live).
func TestUnusedDeadCycle(t *testing.T) {
	a := &ir.Const{Name: "A", Syntax: &ast.ConstDecl{}}
	b := &ir.Const{Name: "B", Syntax: &ast.ConstDecl{}}
	a.Value = &ir.Reference{Target: b}
	b.Value = &ir.Reference{Target: a}
	m := &ir.Module{Consts: []*ir.Const{a, b}}
	span := fakeSpan(map[ast.Node][2]int{a.Syntax: {0, 5}, b.Syntax: {6, 5}})

	l := &linter{span: span}
	l.unusedDeclarations(m)
	if len(l.diags) != 2 {
		t.Errorf("got %d diagnostics, want 2 (both dead): %+v", len(l.diags), l.diags)
	}
}

// TestUnusedKeptByAssert: a private constant only an assert reads is live — the
// assert is a root and its condition graph carries the reference.
func TestUnusedKeptByAssert(t *testing.T) {
	c := &ir.Const{Name: "Max", Syntax: &ast.ConstDecl{}}
	m := &ir.Module{
		Consts:  []*ir.Const{c},
		Asserts: []*ir.Assert{{CondGraph: &ir.Reference{Target: c}}},
	}
	span := fakeSpan(map[ast.Node][2]int{c.Syntax: {0, 5}})

	l := &linter{span: span}
	l.unusedDeclarations(m)
	if len(l.diags) != 0 {
		t.Errorf("got %d diagnostics, want 0 (the assert keeps Max live): %+v", len(l.diags), l.diags)
	}
}

// TestUnusedSuppressedByError: a constant the analyzer already reported an error
// for is not also called dead.
func TestUnusedSuppressedByError(t *testing.T) {
	dead := &ir.Const{Name: "Dead", Syntax: &ast.ConstDecl{}}
	m := &ir.Module{Consts: []*ir.Const{dead}}
	span := fakeSpan(map[ast.Node][2]int{dead.Syntax: {10, 8}})
	prior := []diagnostic.Diagnostic{{Severity: diagnostic.Error, Offset: 12, Width: 2}}

	l := &linter{span: span, errors: errorRanges(prior)}
	l.unusedDeclarations(m)
	if len(l.diags) != 0 {
		t.Errorf("got %d diagnostics, want 0 (suppressed by the error): %+v", len(l.diags), l.diags)
	}
}

// TestUnusedPrivateMaster pins that a private master nothing reaches is reported
// as dead code, like a private type/enum/interface. A master keeps Body nil and
// anchors through MasterSyntax, so the reporter must read that backpointer; a
// public master is a root and is never reported.
func TestUnusedPrivateMaster(t *testing.T) {
	dead := &ir.TypeDef{Name: "Dead", Master: &ir.MasterDef{}, MasterSyntax: &ast.MasterDecl{}}
	pub := &ir.TypeDef{Name: "Api", Public: true, Master: &ir.MasterDef{}, MasterSyntax: &ast.MasterDecl{}}
	m := &ir.Module{Types: []*ir.TypeDef{pub, dead}}
	span := fakeSpan(map[ast.Node][2]int{pub.MasterSyntax: {0, 10}, dead.MasterSyntax: {11, 10}})

	l := &linter{span: span}
	l.unusedDeclarations(m)
	if len(l.diags) != 1 {
		t.Fatalf("got %d diagnostics, want 1 (only the private Dead master): %+v", len(l.diags), l.diags)
	}
	if l.diags[0].Offset != 11 {
		t.Errorf("offset = %d, want 11 (Dead)", l.diags[0].Offset)
	}
	if l.diags[0].Code != "belt.lint.unused_declaration" {
		t.Errorf("code = %q", l.diags[0].Code)
	}
}

// TestUnusedRowAliasKeptByMaster pins that a private named record used as a
// master's row is live: the master references it through its row type, so the
// liveness marker reaches it and it is not reported unused.
func TestUnusedRowAliasKeptByMaster(t *testing.T) {
	row := &ir.TypeDef{
		Name:   "Row",
		Syntax: &ast.TypeDecl{},
		Body:   &ir.Record{Fields: []ir.Field{{Name: "id", Type: &ir.Builtin{Name: "int"}}}},
	}
	master := &ir.TypeDef{
		Name:         "M",
		Public:       true,
		MasterSyntax: &ast.MasterDecl{},
		Master:       &ir.MasterDef{Row: &ir.Named{Def: row}, Primary: []string{"id"}},
	}
	m := &ir.Module{Types: []*ir.TypeDef{row, master}}
	span := fakeSpan(map[ast.Node][2]int{row.Syntax: {0, 10}, master.MasterSyntax: {11, 10}})

	l := &linter{span: span}
	l.unusedDeclarations(m)
	if len(l.diags) != 0 {
		t.Fatalf("Row is the master's row type, so it is used; want no diagnostics, got %+v", l.diags)
	}
}

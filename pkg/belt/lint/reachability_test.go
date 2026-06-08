package lint

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

func ret() ir.Stmt  { return &ir.Return{} }
func expr() ir.Stmt { return &ir.ExprStmt{} }

// TestDiverges pins the structural divergence rules: only a path with no
// fall-through diverges, and an if/switch/match diverges only when every one of
// its paths does.
func TestDiverges(t *testing.T) {
	cases := []struct {
		name string
		stmt ir.Stmt
		want bool
	}{
		{"return", &ir.Return{}, true},
		{"expr", &ir.ExprStmt{}, false},
		{"let", &ir.Let{}, false},
		{"assign", &ir.Assign{}, false},
		{"for-with-return", &ir.For{Body: []ir.Stmt{ret()}}, false},
		{"if-no-else", &ir.If{Then: []ir.Stmt{ret()}}, false},
		{"if-then-and-else-return", &ir.If{Then: []ir.Stmt{ret()}, Else: []ir.Stmt{ret()}}, true},
		{"if-then-return-else-falls", &ir.If{Then: []ir.Stmt{ret()}, Else: []ir.Stmt{expr()}}, false},
		{"if-then-falls-else-return", &ir.If{Then: []ir.Stmt{expr()}, Else: []ir.Stmt{ret()}}, false},
		{"if-elseif-chain-all-return", &ir.If{Then: []ir.Stmt{ret()}, ElseIf: &ir.If{Then: []ir.Stmt{ret()}, Else: []ir.Stmt{ret()}}}, true},
		{"if-elseif-chain-no-final-else", &ir.If{Then: []ir.Stmt{ret()}, ElseIf: &ir.If{Then: []ir.Stmt{ret()}}}, false},
		{"switch-no-wildcard", &ir.Switch{Arms: []ir.SwitchArm{{Body: []ir.Stmt{ret()}}}}, false},
		{"switch-all-arms-and-else", &ir.Switch{Arms: []ir.SwitchArm{{Body: []ir.Stmt{ret()}}}, Else: []ir.Stmt{ret()}}, true},
		{"switch-one-arm-falls", &ir.Switch{Arms: []ir.SwitchArm{{Body: []ir.Stmt{ret()}}, {Body: []ir.Stmt{expr()}}}, Else: []ir.Stmt{ret()}}, false},
		{"switch-else-falls", &ir.Switch{Arms: []ir.SwitchArm{{Body: []ir.Stmt{ret()}}}, Else: []ir.Stmt{expr()}}, false},
		{"match-all-arms-and-else", &ir.Match{Arms: []ir.MatchArm{{Body: []ir.Stmt{ret()}}}, Else: []ir.Stmt{ret()}}, true},
		{"match-no-wildcard", &ir.Match{Arms: []ir.MatchArm{{Body: []ir.Stmt{ret()}}}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := diverges(c.stmt); got != c.want {
				t.Errorf("diverges = %v, want %v", got, c.want)
			}
		})
	}
}

// fakeSpan resolves each registered ast node to a fixed offset/width, the seam
// the real assembler fills from its position table.
func fakeSpan(spans map[ast.Node][2]int) Span {
	return func(n ast.Node) (int, int) {
		s := spans[n]
		return s[0], s[1]
	}
}

// TestCheckReportsUnreachableTail reports the run after a return as one faded
// span, from the first dead statement to the end of the last.
func TestCheckReportsUnreachableTail(t *testing.T) {
	live, dead1, dead2 := &ast.ReturnStmt{}, &ast.LetStmt{}, &ast.ReturnStmt{}
	fn := &ast.FuncDecl{}
	mod := &ir.Module{Funcs: []*ir.Function{{
		Name:   "f",
		Public: true, Syntax: fn,
		Body: []ir.Stmt{
			&ir.Return{Syntax: live},
			&ir.Let{Syntax: dead1},
			&ir.Return{Syntax: dead2},
		},
	}}}
	span := fakeSpan(map[ast.Node][2]int{fn: {0, 40}, live: {10, 8}, dead1: {20, 9}, dead2: {30, 8}})

	diags := Check(mod, span, nil)
	if len(diags) != 1 {
		t.Fatalf("got %d diagnostics, want 1: %+v", len(diags), diags)
	}
	d := diags[0]
	if d.Code != "belt.lint.unreachable_code" {
		t.Errorf("code = %q", d.Code)
	}
	// The span runs from dead1's start (20) to dead2's end (38).
	if d.Offset != 20 || d.End() != 38 {
		t.Errorf("span = [%d,%d), want [20,38)", d.Offset, d.End())
	}
	if len(d.Tags) != 1 || d.Tags[0] != diagnostic.TagUnnecessary {
		t.Errorf("tags = %v, want [unnecessary]", d.Tags)
	}
}

// TestCheckSuppressedByError leaves a declaration alone when an error already
// covers it — a broken body is reported wrong, not also dead.
func TestCheckSuppressedByError(t *testing.T) {
	live, dead := &ast.ReturnStmt{}, &ast.ReturnStmt{}
	fn := &ast.FuncDecl{}
	mod := &ir.Module{Funcs: []*ir.Function{{
		Name: "f", Public: true, Syntax: fn,
		Body: []ir.Stmt{&ir.Return{Syntax: live}, &ir.Return{Syntax: dead}},
	}}}
	span := fakeSpan(map[ast.Node][2]int{fn: {0, 40}, live: {10, 8}, dead: {20, 8}})
	prior := []diagnostic.Diagnostic{{Severity: diagnostic.Error, Offset: 15, Width: 2}}

	if diags := Check(mod, span, prior); len(diags) != 0 {
		t.Errorf("got %d diagnostics, want 0 (suppressed by the error): %+v", len(diags), diags)
	}
	// A warning over the same range does not suppress: only errors dominate.
	warn := []diagnostic.Diagnostic{{Severity: diagnostic.Warning, Offset: 15, Width: 2}}
	if diags := Check(mod, span, warn); len(diags) != 1 {
		t.Errorf("got %d diagnostics, want 1 (a warning does not suppress): %+v", len(diags), diags)
	}
}

// TestCheckNestedDeadCode finds dead code inside a reachable branch even when
// the enclosing block falls through.
func TestCheckNestedDeadCode(t *testing.T) {
	inner, dead := &ast.ReturnStmt{}, &ast.ExprStmt{}
	fn := &ast.FuncDecl{}
	mod := &ir.Module{Funcs: []*ir.Function{{
		Name: "f", Public: true, Syntax: fn,
		Body: []ir.Stmt{
			&ir.If{Then: []ir.Stmt{
				&ir.Return{Syntax: inner},
				&ir.ExprStmt{Syntax: dead},
			}},
			&ir.ExprStmt{Syntax: &ast.ExprStmt{}}, // reachable: the if has no else
		},
	}}}
	span := fakeSpan(map[ast.Node][2]int{fn: {0, 50}, inner: {10, 8}, dead: {20, 6}})

	diags := Check(mod, span, nil)
	if len(diags) != 1 {
		t.Fatalf("got %d diagnostics, want 1: %+v", len(diags), diags)
	}
	if diags[0].Offset != 20 {
		t.Errorf("offset = %d, want 20 (the dead statement in the then-branch)", diags[0].Offset)
	}
}

// TestCheckClean reports nothing for a body whose every path is reachable.
func TestCheckClean(t *testing.T) {
	a, b := &ast.LetStmt{}, &ast.ReturnStmt{}
	fn := &ast.FuncDecl{}
	mod := &ir.Module{Funcs: []*ir.Function{{
		Name: "f", Public: true, Syntax: fn,
		Body: []ir.Stmt{&ir.Let{Syntax: a}, &ir.Return{Syntax: b}},
	}}}
	span := fakeSpan(map[ast.Node][2]int{fn: {0, 40}, a: {10, 9}, b: {20, 8}})

	if diags := Check(mod, span, nil); len(diags) != 0 {
		t.Errorf("got %d diagnostics, want 0: %+v", len(diags), diags)
	}
}

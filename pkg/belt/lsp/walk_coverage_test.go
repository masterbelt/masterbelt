package lsp

import (
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source/ast"
)

// These tests pin the coverage of the LSP's body walks — the shared statement
// and expression traversals every AST-driven editor feature funnels through.
// Each regression targets a body location an earlier walk did not reach: an
// enum or interface method body, a let initializer or assignment, a ternary
// branch, a body-nested reference (for rename/references), and a let-bound
// record literal.

// TestForEachExprEnumMethodBody pins that an expression inside an enum impl
// method's body is walked: hovering an enum member access (Element.Fire) inside
// it resolves, where before the walk never descended into file.Enums methods.
func TestForEachExprEnumMethodBody(t *testing.T) {
	src := "pub enum Element {\n  Fire, Water\n} impl {\n  pub isFire(): bool {\n    return self == Element.Fire\n  }\n}\n"
	doc := testView(src)

	// Hover on "Fire" in "Element.Fire" inside the method body.
	offset := strings.Index(src, "Element.Fire") + len("Element.")
	h := hover(doc, offset)
	if h == nil {
		t.Fatal("no hover on the enum member access inside the enum method body")
	}
	if !strings.Contains(h.Contents.Value, "Element.Fire") {
		t.Errorf("hover = %q, want it to show Element.Fire", h.Contents.Value)
	}
}

// TestForEachExprInterfaceDefaultBody pins that the provided-method default body
// of an interface is walked. A top-level constant referenced from inside the
// default body is reached by the occurrence engines only if the walk descends
// file.Interfaces members' bodies; before the fix the body was inert.
func TestForEachExprInterfaceDefaultBody(t *testing.T) {
	src := "const Zero = 0\npub interface foldable<K, V> {\n  fold<A>(init: A, step: fn(acc: A, key: K, value: V): A): A\n  pub count(): nint {\n    return fold(Zero, fn(acc, key, value) -> acc + 1)\n  }\n}\n"
	doc := testView(src)

	// From the declaration: decl + the reference inside the interface default.
	if got := references(doc, strings.Index(src, "const Zero")+6, true); len(got) != 2 {
		t.Fatalf("references(Zero decl) = %d, want 2 (decl + interface-default body)", len(got))
	}

	// And the document's expression walk reaches the FuncLit nested in the
	// default body.
	var sawLit bool
	forEachExpr(doc.AST().File(), func(e ast.Expr) {
		if _, ok := e.(*ast.FuncLit); ok {
			sawLit = true
		}
	})
	if !sawLit {
		t.Error("forEachExpr did not visit the FuncLit inside the interface default body")
	}
}

// TestForEachExprLetInitializer pins that an expression inside a let
// initializer is walked: walkStmts had no LetStmt case, so any expression in a
// let's value was invisible. A lambda parameter inside a let-bound lambda
// hovers, and the walk reaches the FuncLit nested in the let RHS.
func TestForEachExprLetInitializer(t *testing.T) {
	src := "pub fn g(): nint {\n  let h = fn(x: nint): nint { return x.add(1) }\n  return h(2)\n}\n"
	doc := testView(src)

	off := strings.Index(src, "return x.add") + len("return ")
	h := hover(doc, off)
	if h == nil {
		t.Fatal("no hover on the parameter use inside a let-bound lambda")
	}
	if !strings.Contains(h.Contents.Value, "x: nint") {
		t.Errorf("hover = %q, want x: nint", h.Contents.Value)
	}

	var sawLitInLet bool
	forEachExpr(doc.AST().File(), func(e ast.Expr) {
		if _, ok := e.(*ast.FuncLit); ok {
			sawLitInLet = true
		}
	})
	if !sawLitInLet {
		t.Error("forEachExpr did not visit the FuncLit nested in the let RHS")
	}
}

// TestForEachExprAssignRHS pins that the RHS (and target) of an assignment is
// walked: the AssignStmt case for walkStmts was missing.
func TestForEachExprAssignRHS(t *testing.T) {
	src := "pub fn f(): nint {\n  let acc = 0\n  acc = fn(x: nint): nint { return x }(3)\n  return acc\n}\n"
	doc := testView(src)

	var sawLitInAssign bool
	forEachExpr(doc.AST().File(), func(e ast.Expr) {
		if _, ok := e.(*ast.FuncLit); ok {
			sawLitInAssign = true
		}
	})
	if !sawLitInAssign {
		t.Error("forEachExpr did not visit the FuncLit on the assignment RHS")
	}
}

// TestForEachExprTernaryBranch pins that an operand inside a ternary's branch is
// walked: walkExpr had no TernaryExpr case, so a member access or lambda in a
// branch was skipped. Here an enum member access in the "then" branch hovers
// only if the ternary's branches are descended.
func TestForEachExprTernaryBranch(t *testing.T) {
	src := "pub enum Element {\n  Fire, Water\n}\npub fn f(flag: bool): Element {\n  return flag ? Element.Fire : Element.Water\n}\n"
	doc := testView(src)

	// Hover on "Fire" in the "then" branch's "Element.Fire".
	off := strings.Index(src, "? Element.Fire") + len("? Element.")
	h := hover(doc, off)
	if h == nil {
		t.Fatal("no hover on the enum member access inside the ternary branch")
	}
	if !strings.Contains(h.Contents.Value, "Element.Fire") {
		t.Errorf("hover = %q, want Element.Fire", h.Contents.Value)
	}

	// A FuncLit nested in a ternary branch is reached by the expression walk.
	fsrc := "pub fn g(flag: bool): nint {\n  return flag ? fn(x: nint): nint { return x }(1) : 0\n}\n"
	fdoc := testView(fsrc)
	var sawLitInTernary bool
	forEachExpr(fdoc.AST().File(), func(e ast.Expr) {
		if _, ok := e.(*ast.FuncLit); ok {
			sawLitInTernary = true
		}
	})
	if !sawLitInTernary {
		t.Error("forEachExpr did not visit the FuncLit inside the ternary branch")
	}
}

// TestReferencesInFunctionBody pins that a top-level constant referenced from a
// function body is found by references and rewritten by rename — the occurrence
// engines walked only const initializers and assert conditions before.
func TestReferencesInFunctionBody(t *testing.T) {
	src := "const Max = 10\npub fn f(): nint {\n  return Max + 1\n}\n"
	doc := testView(src)

	// From the declaration: decl + the body reference.
	if got := references(doc, strings.Index(src, "const Max")+6, true); len(got) != 2 {
		t.Fatalf("references(Max decl) = %d, want 2 (decl + body)", len(got))
	}
	// From the body reference, it still finds both.
	if got := references(doc, strings.Index(src, "return Max")+len("return "), true); len(got) != 2 {
		t.Fatalf("references(Max body ref) = %d, want 2", len(got))
	}

	// Rename rewrites the body reference too (no dangling reference left).
	we := rename(doc, strings.Index(src, "const Max")+6, "Cap")
	if we == nil {
		t.Fatal("rename returned nil")
	}
	if edits := we.Changes[doc.uri]; len(edits) != 2 {
		t.Fatalf("rename produced %d edits, want 2 (decl + body reference)", len(edits))
	}
}

// TestReferencesInMethodBody pins the same for a reference inside a method body
// (under file.Types), including one nested in a let initializer.
func TestReferencesInMethodBody(t *testing.T) {
	src := "const Step = 2\ntype Lvl = sbyte impl {\n  bump(): sbyte {\n    let s = Step\n    return s\n  }\n}\n"
	doc := testView(src)

	if got := references(doc, strings.Index(src, "const Step")+6, true); len(got) != 2 {
		t.Fatalf("references(Step decl) = %d, want 2 (decl + method-body let)", len(got))
	}
}

// TestRecordFieldCompletionInLetBinding pins that an inferred record literal
// bound by an annotated let gets field-name completion inside its braces:
// recordTypes pushed the type only through a top-level ReturnStmt before, so a
// let-bound literal received no expected type.
func TestRecordFieldCompletionInLetBinding(t *testing.T) {
	src := "type Point = { x: nint, y: nint }\npub fn f(): nint {\n  let p: Point = {  }\n  return p.x\n}\n"
	doc := testView(src)

	// Inside the empty braces of the let-bound record literal.
	offset := strings.Index(src, "= {  }") + len("= { ")
	got := byLabel(completion(doc, offset).Items)
	for _, name := range []string{"x", "y"} {
		if _, ok := got[name]; !ok {
			t.Errorf("record field completion in a let binding missing %q; got %v", name, got)
		}
	}
}

// TestRecordFieldCompletionInBranchReturn pins that an inferred record literal
// returned from inside an if branch gets field completion: recordTypes scanned
// only the top-level statements of a body for a ReturnStmt before.
func TestRecordFieldCompletionInBranchReturn(t *testing.T) {
	src := "type Point = { x: nint, y: nint }\npub fn f(flag: bool): Point {\n  if flag {\n    return {  }\n  }\n  return { x: 0, y: 0 }\n}\n"
	doc := testView(src)

	offset := strings.Index(src, "return {  }") + len("return { ")
	got := byLabel(completion(doc, offset).Items)
	for _, name := range []string{"x", "y"} {
		if _, ok := got[name]; !ok {
			t.Errorf("record field completion in a branch return missing %q; got %v", name, got)
		}
	}
}

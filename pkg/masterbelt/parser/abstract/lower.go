// Package abstract lowers the concrete syntax tree (package source/cst) into the
// abstract syntax tree (package source/ast).
//
// Lowering drops trivia and resolves identifiers and literals to plain strings,
// producing position-independent AST nodes. That independence is what makes the
// result incremental: Document memoizes each lowered declaration on the identity
// of its backing CST node, so when an edit leaves a declaration's green node
// untouched (which the concrete parser guarantees for everything it does not
// reparse), its AST node is reused verbatim instead of being rebuilt.
//
// The package is organised as:
//
//	lower.go     the one-shot, stateless CST -> AST lowering
//	document.go  the incremental Document (memoized re-lowering on edit)
package abstract

import (
	"strings"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/parser/concrete"
	"github.com/masterbelt/masterbelt/pkg/source"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/token"
)

// Lower is the one-shot entry point: it lexes, parses, and lowers src into a
// File and the parse diagnostics, retaining no incremental state. Use Document
// when the source will be edited.
func Lower(src []byte) (*ast.File, []diagnostic.Diagnostic) {
	root, diags := concrete.Parse(src)
	return lowerFile(cst.Root(root), source.NewFile("", src)), diags
}

// lowerFile lowers a positioned File CST node into an ast.File, lowering every
// top-level declaration from scratch. Document uses the same traversal
// (foreachDecl) but lowers through its cache instead.
func lowerFile(root cst.Tree, buf source.Buffer) *ast.File {
	rootNode, _ := root.Node()
	var uses []*ast.UseDecl
	var decls []*ast.ConstDecl
	var types []*ast.TypeDecl
	var enums []*ast.EnumDecl
	var funcs []*ast.FuncDecl
	var asserts []*ast.AssertDecl
	foreachDecl(root, func(child cst.Tree, green *cst.Node) {
		switch green.Kind() {
		case cst.UseDecl:
			uses = append(uses, lowerUseDecl(child, buf))
		case cst.ConstDecl:
			decls = append(decls, lowerConstDecl(child, buf))
		case cst.TypeDecl:
			types = append(types, lowerTypeDecl(child, buf))
		case cst.EnumDecl:
			enums = append(enums, lowerEnumDecl(child, buf))
		case cst.FuncDecl:
			funcs = append(funcs, lowerFuncDecl(child, buf))
		case cst.AssertDecl:
			asserts = append(asserts, lowerAssertDecl(child, buf))
		}
	})
	return ast.NewFile(uses, decls, types, enums, funcs, asserts, rootNode)
}

// foreachDecl calls fn for each top-level declaration child of root (a
// UseDecl, ConstDecl, TypeDecl, FuncDecl, or AssertDecl), in source order,
// passing the positioned child and its green node. Trivia tokens, the EOF
// leaf, and unparsable Error regions are skipped: they have no place in the
// abstract tree.
func foreachDecl(root cst.Tree, fn func(child cst.Tree, green *cst.Node)) {
	for _, child := range root.Children() {
		node, ok := child.Node()
		if !ok {
			continue
		}
		switch node.Kind() {
		case cst.UseDecl, cst.ConstDecl, cst.TypeDecl, cst.EnumDecl, cst.FuncDecl, cst.AssertDecl:
			fn(child, node)
		}
	}
}

// lowerParamList lowers a ParamList node to its parameters.
func lowerParamList(t cst.Tree, buf source.Buffer) []*ast.ParamDef {
	var params []*ast.ParamDef
	for _, child := range t.Children() {
		if n, ok := child.Node(); ok && n.Kind() == cst.Param {
			params = append(params, lowerParam(child, buf))
		}
	}
	return params
}

// lowerParam lowers one parameter: its name and type.
func lowerParam(t cst.Tree, buf source.Buffer) *ast.ParamDef {
	green, _ := t.Node()
	var name string
	var typ ast.TypeExpr
	for _, child := range t.Children() {
		if tok, ok := child.Token(); ok {
			if tok.Kind() == token.Ident && name == "" {
				name = child.Text(buf)
			}
			continue
		}
		if n, ok := child.Node(); ok && isTypeExprKind(n.Kind()) {
			typ = lowerTypeExpr(child, buf)
		}
	}
	return ast.NewParamDef(name, typ, green)
}

// lowerBlock lowers a Block node to its statements.
func lowerBlock(t cst.Tree, buf source.Buffer) []ast.Stmt {
	var stmts []ast.Stmt
	for _, child := range t.Children() {
		if node, ok := child.Node(); ok {
			if s := lowerStmt(child, buf, node); s != nil {
				stmts = append(stmts, s)
			}
		}
	}
	return stmts
}

// lowerStmt lowers a statement node: a return statement, or a bare expression
// statement.
func lowerStmt(t cst.Tree, buf source.Buffer, node *cst.Node) ast.Stmt {
	if node.Kind() == cst.ReturnStmt {
		var value ast.Expr
		for _, child := range t.Children() {
			if n, ok := child.Node(); ok && isExprKind(n.Kind()) {
				value = lowerExpr(child, buf)
			}
		}
		return ast.NewReturnStmt(value, node)
	}
	if isExprKind(node.Kind()) {
		return ast.NewExprStmt(lowerExpr(t, buf), node)
	}
	return nil
}

// firstOperand lowers the single operand node of a UnaryExpr (nil if absent).
func firstOperand(t cst.Tree, buf source.Buffer) ast.Expr {
	for _, c := range t.Children() {
		if _, ok := c.Node(); ok {
			return lowerExpr(c, buf)
		}
	}
	return nil
}

// twoOperands lowers the left and right operand nodes of a BinaryExpr. Either is
// nil when the source omitted it (a recovered "1 +").
func twoOperands(t cst.Tree, buf source.Buffer) (x, y ast.Expr) {
	var operands []ast.Expr
	for _, c := range t.Children() {
		if _, ok := c.Node(); ok {
			operands = append(operands, lowerExpr(c, buf))
		}
	}
	if len(operands) > 0 {
		x = operands[0]
	}
	if len(operands) > 1 {
		y = operands[1]
	}
	return x, y
}

// isTrivia reports whether k is a trivia token kind (interleaved in an
// expression node between operands and operators).
func isTrivia(k token.Kind) bool {
	switch k {
	case token.Whitespace, token.Newline, token.LineComment, token.DocComment, token.BlockComment:
		return true
	default:
		return false
	}
}

// docText strips the "///" marker (and surrounding space) from a doc comment,
// leaving the comment's content.
func docText(s string) string {
	return strings.TrimSpace(strings.TrimPrefix(s, "///"))
}

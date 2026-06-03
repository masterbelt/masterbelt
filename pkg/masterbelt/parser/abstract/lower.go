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

	"github.com/masterbelt/masterbelt/pkg/masterbelt/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/parser/concrete"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ast"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/cst"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/token"
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
	var decls []*ast.ConstDecl
	foreachDecl(root, func(child cst.Tree, _ *cst.Node) {
		decls = append(decls, lowerConstDecl(child, buf))
	})
	return ast.NewFile(decls, rootNode)
}

// foreachDecl calls fn for each top-level ConstDecl child of root, in source
// order, passing the positioned child and its green node. Trivia tokens, the
// EOF leaf, and unparsable Error regions are skipped: they have no place in the
// abstract tree.
func foreachDecl(root cst.Tree, fn func(child cst.Tree, green *cst.Node)) {
	for _, child := range root.Children() {
		node, ok := child.Node()
		if !ok || node.Kind() != cst.ConstDecl {
			continue
		}
		fn(child, node)
	}
}

// lowerConstDecl lowers a positioned ConstDecl CST node into an ast.ConstDecl.
// It reads identifier and literal text from buf at the node's resolved offsets;
// the resulting strings are baked into the AST node, so the node no longer
// depends on the buffer or on where the declaration sits.
func lowerConstDecl(t cst.Tree, buf source.Buffer) *ast.ConstDecl {
	green, _ := t.Node()

	var (
		doc    []string
		public bool
		name   string
		typ    *ast.TypeRef
		value  ast.Expr
	)

	for _, child := range t.Children() {
		if tok, ok := child.Token(); ok {
			switch tok.Kind() {
			case token.Pub:
				public = true
			case token.DocComment:
				doc = append(doc, docText(child.Text(buf)))
			case token.Ident:
				// The only direct Ident child of a ConstDecl is the declared
				// name; the type and value identifiers are nested in TypeClause
				// and Initializer nodes.
				name = child.Text(buf)
			}
			continue
		}

		node, _ := child.Node()
		switch node.Kind() {
		case cst.TypeClause:
			typ = lowerTypeClause(child, buf)
		case cst.Initializer:
			value = lowerInitializer(child, buf)
		}
	}

	return ast.NewConstDecl(doc, public, name, typ, value, green)
}

// lowerTypeClause lowers a ": Type" clause to its TypeRef, or nil when the type
// is missing (a recovered "const x: = 1").
func lowerTypeClause(t cst.Tree, buf source.Buffer) *ast.TypeRef {
	for _, child := range t.Children() {
		if node, ok := child.Node(); ok && node.Kind() == cst.TypeRef {
			return ast.NewTypeRef(child.Text(buf), node)
		}
	}
	return nil
}

// lowerInitializer lowers an "= Expr" clause to its expression, or nil when the
// expression is missing (a recovered "const x =").
func lowerInitializer(t cst.Tree, buf source.Buffer) ast.Expr {
	for _, child := range t.Children() {
		node, ok := child.Node()
		if !ok {
			continue
		}
		switch node.Kind() {
		case cst.Literal:
			return ast.NewIntLit(child.Text(buf), node)
		case cst.NameRef:
			return ast.NewNameRef(child.Text(buf), node)
		}
	}
	return nil
}

// docText strips the "///" marker (and surrounding space) from a doc comment,
// leaving the comment's content.
func docText(s string) string {
	return strings.TrimSpace(strings.TrimPrefix(s, "///"))
}

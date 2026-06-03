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
		if node, ok := child.Node(); ok && isExprKind(node.Kind()) {
			return lowerExpr(child, buf)
		}
	}
	return nil
}

// isExprKind reports whether a CST node kind is an expression node.
func isExprKind(k cst.Kind) bool {
	switch k {
	case cst.Literal, cst.NameRef, cst.UnaryExpr, cst.BinaryExpr:
		return true
	default:
		return false
	}
}

// lowerExpr lowers a positioned expression CST node to its ast.Expr, recursing
// into operands. An Error node (or any non-expression) lowers to nil, so a
// malformed initializer simply yields a missing operand rather than a panic.
func lowerExpr(t cst.Tree, buf source.Buffer) ast.Expr {
	node, ok := t.Node()
	if !ok {
		return nil
	}
	switch node.Kind() {
	case cst.Literal:
		return lowerLiteral(t, buf, node)
	case cst.NameRef:
		return ast.NewIdentifier(t.Text(buf), node)
	case cst.UnaryExpr:
		// -x desugars to x.neg(): the operand is the receiver, no arguments.
		return desugarCall(firstOperand(t, buf), unaryMethod(operatorKind(t)), nil, node)
	case cst.BinaryExpr:
		// 1 + 2 desugars to 1.add(2): the left operand is the receiver, the
		// right operand the single argument (absent when recovered away).
		x, y := twoOperands(t, buf)
		var args []ast.Expr
		if y != nil {
			args = append(args, y)
		}
		return desugarCall(x, binaryMethod(operatorKind(t)), args, node)
	default:
		return nil
	}
}

// desugarCall builds the "receiver.method(args)" form an operator lowers to: a
// CallExpr whose callee is a MemberExpr. All three synthetic nodes share the
// operator's CST node, since the surface syntax has no separate member or call.
func desugarCall(receiver ast.Expr, method string, args []ast.Expr, node *cst.Node) ast.Expr {
	member := ast.NewMemberExpr(receiver, ast.NewIdentifier(method, node), node)
	return ast.NewCallExpr(member, args, node)
}

// lowerLiteral lowers a Literal node (its single Int/True/False leaf) to the
// matching literal expression.
func lowerLiteral(t cst.Tree, buf source.Buffer, node *cst.Node) ast.Expr {
	switch literalKind(t) {
	case token.Int:
		return ast.NewIntLit(t.Text(buf), node)
	case token.True:
		return ast.NewBoolLit(true, node)
	case token.False:
		return ast.NewBoolLit(false, node)
	default:
		return nil
	}
}

// literalKind returns the kind of a Literal node's single value token.
func literalKind(t cst.Tree) token.Kind {
	for _, c := range t.Children() {
		if k, ok := c.TokenKind(); ok {
			return k
		}
	}
	return token.Illegal
}

// operatorKind returns the kind of the operator token of a UnaryExpr/BinaryExpr,
// skipping the operand nodes and trivia.
func operatorKind(t cst.Tree) token.Kind {
	for _, c := range t.Children() {
		if k, ok := c.TokenKind(); ok && !isTrivia(k) {
			return k
		}
	}
	return token.Illegal
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

// binaryMethod maps a binary operator token to the method its expression
// desugars to. The method names are the language's operator labels, taken from
// the examples: + is add, % is rem, == is eql, <= is lteq, && is anan, ...
func binaryMethod(k token.Kind) string {
	switch k {
	case token.Plus:
		return "add"
	case token.Minus:
		return "sub"
	case token.Star:
		return "mul"
	case token.Slash:
		return "div"
	case token.Percent:
		return "rem"
	case token.EqEq:
		return "eql"
	case token.BangEq:
		return "neq"
	case token.Lt:
		return "lt"
	case token.LtEq:
		return "lteq"
	case token.Gt:
		return "gt"
	case token.GtEq:
		return "gteq"
	case token.AmpAmp:
		return "anan"
	case token.PipePipe:
		return "oror"
	default:
		return ""
	}
}

// unaryMethod maps a prefix operator token to the method its expression
// desugars to: +x is x.pos(), -x is x.neg(), !x is x.not().
func unaryMethod(k token.Kind) string {
	switch k {
	case token.Plus:
		return "pos"
	case token.Minus:
		return "neg"
	case token.Bang:
		return "not"
	default:
		return ""
	}
}

// docText strips the "///" marker (and surrounding space) from a doc comment,
// leaving the comment's content.
func docText(s string) string {
	return strings.TrimSpace(strings.TrimPrefix(s, "///"))
}

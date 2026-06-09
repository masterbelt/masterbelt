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

	"github.com/masterbelt/masterbelt/pkg/belt/parser/concrete"
	"github.com/masterbelt/masterbelt/pkg/diagnostic"
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
	var interfaces []*ast.InterfaceDecl
	var funcs []*ast.FuncDecl
	var asserts []*ast.AssertDecl
	var masters []*ast.MasterDecl
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
		case cst.InterfaceDecl:
			interfaces = append(interfaces, lowerInterfaceDecl(child, buf))
		case cst.FuncDecl:
			funcs = append(funcs, lowerFuncDecl(child, buf))
		case cst.AssertDecl:
			asserts = append(asserts, lowerAssertDecl(child, buf))
		case cst.MasterDecl:
			masters = append(masters, lowerMasterDecl(child, buf))
		default:
			// Any other kind is not a top-level declaration: it is skipped and
			// contributes nothing to the lowered File.
		}
	})
	return ast.NewFile(uses, decls, types, enums, interfaces, funcs, asserts, masters, rootNode)
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
		case cst.UseDecl, cst.ConstDecl, cst.TypeDecl, cst.EnumDecl, cst.InterfaceDecl, cst.FuncDecl, cst.AssertDecl, cst.MasterDecl:
			fn(child, node)
		default:
			// Any other kind (trivia, the EOF leaf, an Error region) is not a
			// top-level declaration and has no place in the abstract tree: skip it.
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
			if isNameToken(tok.Kind()) && name == "" {
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

// lowerStmt lowers a statement node: a let binding, a return statement, an
// assignment, a switch statement, a match statement, an if statement, a for
// statement, or a bare expression statement.
func lowerStmt(t cst.Tree, buf source.Buffer, node *cst.Node) ast.Stmt {
	switch {
	case node.Kind() == cst.ReturnStmt:
		var value ast.Expr
		for _, child := range t.Children() {
			if n, ok := child.Node(); ok && isExprKind(n.Kind()) {
				value = lowerExpr(child, buf)
			}
		}
		return ast.NewReturnStmt(value, node)
	case node.Kind() == cst.LetStmt:
		return lowerLetStmt(t, buf, node)
	case node.Kind() == cst.AssignStmt:
		return lowerAssignStmt(t, buf, node)
	case node.Kind() == cst.SwitchStmt:
		return lowerSwitchStmt(t, buf, node)
	case node.Kind() == cst.MatchStmt:
		return lowerMatchStmt(t, buf, node)
	case node.Kind() == cst.IfStmt:
		return lowerIfStmt(t, buf, node)
	case node.Kind() == cst.ForStmt:
		return lowerForStmt(t, buf, node)
	case isExprKind(node.Kind()):
		return ast.NewExprStmt(lowerExpr(t, buf), node)
	default:
		return nil
	}
}

// lowerLetStmt lowers a LetStmt node: its bound name (the bare Ident leaf), its
// optional type annotation (a TypeClause holding the type expression), and its
// initializer value (the Initializer's expression). A missing name lowers to the
// empty string and a missing value to a nil expr — the semantic layer reports
// either, mirroring how a malformed constant declaration lowers.
func lowerLetStmt(t cst.Tree, buf source.Buffer, node *cst.Node) ast.Stmt {
	var name string
	var typ ast.TypeExpr
	var value ast.Expr
	for _, child := range t.Children() {
		if tok, ok := child.Token(); ok {
			if tok.Kind() == token.Ident && name == "" {
				name = child.Text(buf)
			}
			continue
		}
		n, ok := child.Node()
		if !ok {
			continue
		}
		switch n.Kind() {
		case cst.TypeClause:
			typ = lowerTypeClause(child, buf)
		case cst.Initializer:
			value = lowerInitializer(child, buf)
		default:
			// Any other child node is neither the annotation nor the
			// initializer of the let binding: it contributes nothing here.
		}
	}
	return ast.NewLetStmt(name, typ, value, node)
}

// lowerAssignStmt lowers an AssignStmt node: its target expression (the first
// expression child, before the "=") and its value (the second). Either is nil
// when the source omitted it, which the semantic layer reports.
//
// An index target, coll[i] = v, is desugared here to a rebind of the collection:
// coll = coll.set(i, v). set returns a new collection (self), so the assignment
// stays a plain rebind of the let local — the same shape an ordinary bare-name
// assignment already checks and folds — and data stays immutable (a new
// collection, not an in-place write). The receiver of the rebind is the index's
// own receiver
// (coll), which the checker validates as a let local exactly as a bare name
// target.
func lowerAssignStmt(t cst.Tree, buf source.Buffer, node *cst.Node) ast.Stmt {
	var targetTree cst.Tree
	var target, value ast.Expr
	for _, child := range t.Children() {
		n, ok := child.Node()
		if !ok || !isExprKind(n.Kind()) {
			continue
		}
		if target == nil {
			targetTree = child
			target = lowerExpr(child, buf)
		} else {
			value = lowerExpr(child, buf)
		}
	}
	if tn, ok := targetTree.Node(); ok && tn.Kind() == cst.IndexExpr {
		return lowerIndexAssign(targetTree, buf, value, node)
	}
	return ast.NewAssignStmt(target, value, node)
}

// lowerIndexAssign lowers an index assignment coll[i] = v to a rebind of the
// collection, coll = coll.set(i, v). The target tree is the IndexExpr (coll[i]);
// its receiver and index are the set call's receiver and first argument, and the
// assigned value is its second. The receiver expression is the AssignStmt's new
// target, so the checker sees the same let-local rebind a bare-name assignment
// produces. A missing receiver, index, or value lowers to a nil hole the
// semantic layer reports, mirroring a recovered ordinary assignment.
func lowerIndexAssign(target cst.Tree, buf source.Buffer, value ast.Expr, node *cst.Node) ast.Stmt {
	recv, index := twoOperands(target, buf)
	var args []ast.Expr
	if index != nil {
		args = append(args, index)
	}
	if value != nil {
		args = append(args, value)
	}
	set := desugarCall(recv, "set", args, node)
	return ast.NewAssignStmt(recv, set, node)
}

// lowerIfStmt lowers an IfStmt node: its condition (the first expression child),
// its then-block (the first Block), and its optional else branch. The else
// branch follows the "else" token: a nested IfStmt is the else-if chain (lowered
// recursively into ElseIf), and a second Block is the plain else body. The two
// block children are distinguished by order — the first Block is always the
// then-block — so an else block is the one that appears after the condition's
// then-block.
func lowerIfStmt(t cst.Tree, buf source.Buffer, node *cst.Node) ast.Stmt {
	var cond ast.Expr
	var then, els []ast.Stmt
	var elseIf *ast.IfStmt
	seenThen := false
	for _, child := range t.Children() {
		n, ok := child.Node()
		if !ok {
			continue
		}
		switch {
		case cond == nil && isExprKind(n.Kind()):
			cond = lowerExpr(child, buf)
		case n.Kind() == cst.Block && !seenThen:
			then = lowerBlock(child, buf)
			seenThen = true
		case n.Kind() == cst.Block:
			els = lowerBlock(child, buf)
		case n.Kind() == cst.IfStmt:
			// The else-if branch: an if after the then-block is the chain, lowered
			// to its own IfStmt so the else-if ladder nests faithfully.
			if s, ok := lowerIfStmt(child, buf, n).(*ast.IfStmt); ok {
				elseIf = s
			}
		}
	}
	return ast.NewIfStmt(cond, then, elseIf, els, node)
}

// lowerForStmt lowers a ForStmt node: its loop variable (the Ident token after
// "for"), its iteration kind (the of/in keyword token — ForIn for "in", ForOf
// otherwise), the iterated expression (the first expression child), and its loop
// body (the Block). A recovered-away for leaves the missing pieces nil/empty.
func lowerForStmt(t cst.Tree, buf source.Buffer, node *cst.Node) ast.Stmt {
	var name string
	kind := ast.ForOf
	var iter ast.Expr
	var body []ast.Stmt
	for _, child := range t.Children() {
		if tok, ok := child.Token(); ok {
			switch {
			case tok.Kind() == token.Ident && name == "":
				name = child.Text(buf)
			case tok.Kind() == token.In:
				kind = ast.ForIn
			}
			continue
		}
		n, ok := child.Node()
		if !ok {
			continue
		}
		switch {
		case iter == nil && isExprKind(n.Kind()):
			iter = lowerExpr(child, buf)
		case n.Kind() == cst.Block:
			body = lowerBlock(child, buf)
		}
	}
	return ast.NewForStmt(name, kind, iter, body, node)
}

// lowerSwitchStmt lowers a SwitchStmt node: its scrutinee (the first expression
// child), its value-pattern arms, and the wildcard "_" arm lifted out into the
// Else body. An arm whose sole value pattern is the bare identifier "_" is the
// wildcard; the rest carry their value patterns. A second wildcard (malformed)
// keeps the first as Else and drops the rest, which the semantic layer would
// already flag as unreachable.
func lowerSwitchStmt(t cst.Tree, buf source.Buffer, node *cst.Node) ast.Stmt {
	var scrutinee ast.Expr
	var arms, afterElse []*ast.SwitchArm
	var els []ast.Stmt
	seenWildcard := false
	for _, child := range t.Children() {
		n, ok := child.Node()
		if !ok {
			continue
		}
		switch {
		case n.Kind() == cst.SwitchArm:
			values, body := lowerSwitchArm(child, buf)
			if isWildcardArm(values) {
				if !seenWildcard {
					seenWildcard = true
					els = body
					if els == nil {
						// A wildcard with an empty body still marks the switch as
						// having a catch-all; an empty slice distinguishes it from
						// "no wildcard at all".
						els = []ast.Stmt{}
					}
				}
				continue
			}
			arm := ast.NewSwitchArm(values, body, n)
			if seenWildcard {
				// The wildcard already matches every remaining value, so an arm
				// after it is unreachable: kept apart from the live arms.
				afterElse = append(afterElse, arm)
			} else {
				arms = append(arms, arm)
			}
		case scrutinee == nil && isExprKind(n.Kind()):
			scrutinee = lowerExpr(child, buf)
		}
	}
	return ast.NewSwitchStmt(scrutinee, arms, els, afterElse, node)
}

// lowerSwitchArm lowers a SwitchArm node to its value patterns and its body. The
// "->" token splits the arm: every expression child before it is a value
// pattern, and what follows is the body — a Block (lowered to its statements) or
// a single inline statement (a return, a nested switch, or a bare expression).
func lowerSwitchArm(t cst.Tree, buf source.Buffer) (values []ast.Expr, body []ast.Stmt) {
	seenArrow := false
	for _, child := range t.Children() {
		if tok, ok := child.Token(); ok {
			if tok.Kind() == token.Arrow {
				seenArrow = true
			}
			continue
		}
		n, ok := child.Node()
		if !ok {
			continue
		}
		if !seenArrow {
			if isExprKind(n.Kind()) {
				values = append(values, lowerExpr(child, buf))
			}
			continue
		}
		switch {
		case n.Kind() == cst.Block:
			body = lowerBlock(child, buf)
		case isExprKind(n.Kind()):
			body = []ast.Stmt{ast.NewExprStmt(lowerExpr(child, buf), n)}
		default:
			if s := lowerStmt(child, buf, n); s != nil {
				body = []ast.Stmt{s}
			}
		}
	}
	return values, body
}

// isWildcardArm reports whether an arm's value patterns are the single bare
// identifier "_", the switch's catch-all.
func isWildcardArm(values []ast.Expr) bool {
	if len(values) != 1 {
		return false
	}
	id, ok := values[0].(*ast.Identifier)
	return ok && id.Name == "_"
}

// lowerMatchStmt lowers a MatchStmt node: its scrutinee (the first expression
// child), its type-pattern arms, and the wildcard "_" arm lifted out into the
// Else body. An arm whose pattern is the bare type name "_" with no binding is
// the wildcard; the rest carry their member type and optional binding. A second
// wildcard (malformed) keeps the first as Else and drops the rest, which the
// semantic layer would already flag as unreachable. The structure mirrors
// lowerSwitchStmt — the only difference is the arm grammar (a type pattern).
func lowerMatchStmt(t cst.Tree, buf source.Buffer, node *cst.Node) ast.Stmt {
	var scrutinee ast.Expr
	var arms, afterElse []*ast.MatchArm
	var els []ast.Stmt
	seenWildcard := false
	for _, child := range t.Children() {
		n, ok := child.Node()
		if !ok {
			continue
		}
		switch {
		case n.Kind() == cst.MatchArm:
			arm := lowerMatchArm(child, buf, n)
			if isWildcardPattern(arm) {
				if !seenWildcard {
					seenWildcard = true
					els = arm.Body
					if els == nil {
						// A wildcard with an empty body still marks the match as
						// having a catch-all; an empty slice distinguishes it from
						// "no wildcard at all".
						els = []ast.Stmt{}
					}
				}
				continue
			}
			if seenWildcard {
				// The wildcard already matches every remaining type, so an arm
				// after it is unreachable: kept apart from the live arms.
				afterElse = append(afterElse, arm)
			} else {
				arms = append(arms, arm)
			}
		case scrutinee == nil && isExprKind(n.Kind()):
			scrutinee = lowerExpr(child, buf)
		}
	}
	return ast.NewMatchStmt(scrutinee, arms, els, afterElse, node)
}

// lowerMatchArm lowers a MatchArm node to its type pattern (the member type and
// optional binding name) and its body. The "->" token splits the arm: the
// MatchPattern child before it carries the type and binding, and what follows is
// the body — a Block (lowered to its statements) or a single inline statement.
func lowerMatchArm(t cst.Tree, buf source.Buffer, node *cst.Node) *ast.MatchArm {
	var typ ast.TypeExpr
	var bind string
	var body []ast.Stmt
	seenArrow := false
	for _, child := range t.Children() {
		if tok, ok := child.Token(); ok {
			if tok.Kind() == token.Arrow {
				seenArrow = true
			}
			continue
		}
		n, ok := child.Node()
		if !ok {
			continue
		}
		switch {
		case n.Kind() == cst.MatchPattern:
			typ, bind = lowerMatchPattern(child, buf)
		case n.Kind() == cst.Block:
			body = lowerBlock(child, buf)
		case isExprKind(n.Kind()) && seenArrow:
			body = []ast.Stmt{ast.NewExprStmt(lowerExpr(child, buf), n)}
		case seenArrow:
			if s := lowerStmt(child, buf, n); s != nil {
				body = []ast.Stmt{s}
			}
		}
	}
	return ast.NewMatchArm(typ, bind, body, node)
}

// lowerMatchPattern lowers a MatchPattern node to its member type and optional
// binding name. The pattern is a primary type followed by an optional binding
// Ident; the binding is the trailing identifier, while a generic type name's own
// identifiers are nested inside the type node, so the binding is read as a token
// child of the pattern itself, never one inside the type.
func lowerMatchPattern(t cst.Tree, buf source.Buffer) (ast.TypeExpr, string) {
	var typ ast.TypeExpr
	var bind string
	for _, child := range t.Children() {
		if tok, ok := child.Token(); ok {
			// The pattern's own Ident child (not one nested in the type) is the
			// binding name.
			if tok.Kind() == token.Ident && bind == "" {
				bind = child.Text(buf)
			}
			continue
		}
		if n, ok := child.Node(); ok && isTypeExprKind(n.Kind()) {
			typ = lowerTypeExpr(child, buf)
		}
	}
	return typ, bind
}

// isWildcardPattern reports whether a match arm is the catch-all "_": its pattern
// is the bare type name "_" with no binding. The wildcard is written as an
// otherwise-ordinary type name, so it lowers to a NamedType whose name is "_";
// the semantic layer treats that as the catch-all rather than a type to resolve.
func isWildcardPattern(arm *ast.MatchArm) bool {
	if arm.Bind != "" {
		return false
	}
	named, ok := arm.Type.(*ast.NamedType)
	return ok && named.Namespace == "" && named.Name == "_"
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

// twoOperands lowers the first two operand nodes of a node, in source order: a
// BinaryExpr's left and right, or an IndexExpr's receiver and index. Either is
// nil when the source omitted it (a recovered "1 +" or "xs[").
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

// threeOperands lowers the condition, then-branch, and else-branch nodes of a
// TernaryExpr, in source order. Any is nil when the source omitted it (a
// recovered "a ? b").
func threeOperands(t cst.Tree, buf source.Buffer) (cond, then, els ast.Expr) {
	var operands []ast.Expr
	for _, c := range t.Children() {
		if _, ok := c.Node(); ok {
			operands = append(operands, lowerExpr(c, buf))
		}
	}
	if len(operands) > 0 {
		cond = operands[0]
	}
	if len(operands) > 1 {
		then = operands[1]
	}
	if len(operands) > 2 {
		els = operands[2]
	}
	return cond, then, els
}

// isNameToken reports whether a token kind names an identifier at a position the
// grammar admits a reserved word as one — a member name after ".", a record
// field name, a function parameter name. It mirrors the concrete parser's
// nameLike, so the keyword leaf the parser accepts there lowers to its text as
// the name. The excluded positions (let/loop/match bindings, generic parameter
// and declaration names) keep matching token.Ident alone.
func isNameToken(k token.Kind) bool {
	return k == token.Ident || k.Keyword()
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

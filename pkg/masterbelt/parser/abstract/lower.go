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
	"strconv"
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
		case cst.FuncDecl:
			funcs = append(funcs, lowerFuncDecl(child, buf))
		case cst.AssertDecl:
			asserts = append(asserts, lowerAssertDecl(child, buf))
		}
	})
	return ast.NewFile(uses, decls, types, funcs, asserts, rootNode)
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
		case cst.UseDecl, cst.ConstDecl, cst.TypeDecl, cst.FuncDecl, cst.AssertDecl:
			fn(child, node)
		}
	}
}

// lowerUseDecl lowers a positioned UseDecl CST node into an ast.UseDecl. The
// target's shape decides which field is set: a direct Ident is a namespace
// import, a UseList child carries the selective names, and a Star leaf marks
// the wildcard. The path is decoded from its string literal.
func lowerUseDecl(t cst.Tree, buf source.Buffer) *ast.UseDecl {
	green, _ := t.Node()

	var (
		public    bool
		namespace string
		names     []string
		star      bool
		path      string
	)

	for _, child := range t.Children() {
		if tok, ok := child.Token(); ok {
			switch tok.Kind() {
			case token.Pub:
				public = true
			case token.Ident:
				// The only direct Ident child is the namespace name; the
				// selective names are nested in the UseList node.
				namespace = child.Text(buf)
			case token.Star:
				star = true
			case token.String:
				path = decodeString(child.Text(buf))
			}
			continue
		}

		if node, _ := child.Node(); node.Kind() == cst.UseList {
			names = lowerUseList(child, buf)
		}
	}

	return ast.NewUseDecl(public, namespace, names, star, path, green)
}

// lowerUseList lowers a selective-import list to its names, in source order.
func lowerUseList(t cst.Tree, buf source.Buffer) []string {
	var names []string
	for _, child := range t.Children() {
		if tok, ok := child.Token(); ok && tok.Kind() == token.Ident {
			names = append(names, child.Text(buf))
		}
	}
	return names
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
		typ    ast.TypeExpr
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

// lowerAssertDecl lowers a positioned AssertDecl CST node into an
// ast.AssertDecl: its doc-comment lines and the asserted expression, nil when
// the expression is missing (a recovered "assert").
func lowerAssertDecl(t cst.Tree, buf source.Buffer) *ast.AssertDecl {
	green, _ := t.Node()

	var doc []string
	var cond ast.Expr
	for _, child := range t.Children() {
		if tok, ok := child.Token(); ok {
			if tok.Kind() == token.DocComment {
				doc = append(doc, docText(child.Text(buf)))
			}
			continue
		}
		if node, _ := child.Node(); isExprKind(node.Kind()) {
			cond = lowerExpr(child, buf)
		}
	}
	return ast.NewAssertDecl(doc, cond, green)
}

// lowerFuncDecl lowers a positioned FuncDecl CST node into an ast.FuncDecl:
// its modifiers, name, parameters, result type, and body. The two body forms
// normalize here — and only here — exactly as a function literal's do: an
// arrow body ("->" Expr) becomes a single implicit return, so inference,
// lowering, and evaluation see one body shape. The kinds keep the children
// apart: the result type is a type-expression node, the arrow body an
// expression node.
func lowerFuncDecl(t cst.Tree, buf source.Buffer) *ast.FuncDecl {
	green, _ := t.Node()
	var (
		doc    []string
		public bool
		name   string
		params []*ast.ParamDef
		result ast.TypeExpr
		body   []ast.Stmt
	)
	for _, child := range t.Children() {
		if tok, ok := child.Token(); ok {
			switch tok.Kind() {
			case token.Pub:
				public = true
			case token.DocComment:
				doc = append(doc, docText(child.Text(buf)))
			case token.Ident:
				// The only direct Ident child is the declared name; the
				// parameter and type names are nested in their own nodes.
				name = child.Text(buf)
			}
			continue
		}
		node, _ := child.Node()
		switch {
		case node.Kind() == cst.ParamList:
			params = lowerParamList(child, buf)
		case node.Kind() == cst.Block:
			body = lowerBlock(child, buf)
		case isTypeExprKind(node.Kind()):
			result = lowerTypeExpr(child, buf)
		case isExprKind(node.Kind()):
			body = []ast.Stmt{ast.NewReturnStmt(lowerExpr(child, buf), node)}
		}
	}
	return ast.NewFuncDecl(doc, public, name, params, result, body, green)
}

// lowerTypeClause lowers a ": Type" clause to its type expression, or nil when
// the type is missing (a recovered "const x: = 1"). The annotation is a full
// type expression, lowered the same way a type declaration's is.
func lowerTypeClause(t cst.Tree, buf source.Buffer) ast.TypeExpr {
	for _, child := range t.Children() {
		if node, ok := child.Node(); ok && isTypeExprKind(node.Kind()) {
			return lowerTypeExpr(child, buf)
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
	case cst.Literal, cst.NameRef, cst.SelfExpr, cst.UnaryExpr, cst.BinaryExpr, cst.CallExpr, cst.MemberExpr, cst.CollectionLit, cst.RecordLit, cst.FuncLit, cst.ParenExpr:
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
	case cst.CollectionLit:
		return lowerCollectionLit(t, buf, node)
	case cst.RecordLit:
		return lowerRecordLit(t, buf, node)
	case cst.NameRef:
		return ast.NewIdentifier(t.Text(buf), node)
	case cst.SelfExpr:
		return ast.NewSelfExpr(node)
	case cst.MemberExpr:
		return lowerMemberExpr(t, buf, node)
	case cst.CallExpr:
		return lowerCallExpr(t, buf, node)
	case cst.FuncLit:
		return lowerFuncLit(t, buf, node)
	case cst.ParenExpr:
		// The parentheses exist only to override precedence, which the tree
		// shape already encodes; the grouping unwraps to its inner expression.
		return firstOperand(t, buf)
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

// lowerLiteral lowers a Literal node (its single Int/String/Datetime/Duration/
// True/False/Null leaf) to the matching literal expression.
func lowerLiteral(t cst.Tree, buf source.Buffer, node *cst.Node) ast.Expr {
	switch literalKind(t) {
	case token.Int:
		return ast.NewIntLit(t.Text(buf), node)
	case token.String:
		return ast.NewStringLit(decodeString(t.Text(buf)), node)
	case token.DatetimeLit:
		return ast.NewDatetimeLit(t.Text(buf), node)
	case token.DurationLit:
		return ast.NewDurationLit(t.Text(buf), node)
	case token.True:
		return ast.NewBoolLit(true, node)
	case token.False:
		return ast.NewBoolLit(false, node)
	case token.Null:
		return ast.NewNullLit(node)
	default:
		return nil
	}
}

// decodeString turns a string literal's raw source text (with the surrounding
// quotes) into its value, interpreting the escapes the lexer recognizes: the
// simple ones (\n \r \t \0 \\ \") and the unicode escape \u{...}. The lexer has
// already reported any malformed escape, so a bad one here is decoded
// best-effort (the backslash is dropped) rather than reported a second time; raw
// multi-byte UTF-8 is copied through verbatim.
func decodeString(raw string) string {
	// Drop the surrounding quotes. An unterminated literal may lack the closing
	// one, so strip each end only when present.
	body := raw
	if len(body) > 0 && body[0] == '"' {
		body = body[1:]
	}
	if len(body) > 0 && body[len(body)-1] == '"' {
		body = body[:len(body)-1]
	}

	var b strings.Builder
	for i := 0; i < len(body); {
		c := body[i]
		if c != '\\' {
			b.WriteByte(c) // ordinary byte, including a UTF-8 continuation byte
			i++
			continue
		}
		i++ // consume the backslash
		if i >= len(body) {
			break // a trailing backslash (only reachable for an unterminated literal)
		}
		switch body[i] {
		case 'n':
			b.WriteByte('\n')
			i++
		case 'r':
			b.WriteByte('\r')
			i++
		case 't':
			b.WriteByte('\t')
			i++
		case '0':
			b.WriteByte(0)
			i++
		case '\\':
			b.WriteByte('\\')
			i++
		case '"':
			b.WriteByte('"')
			i++
		case 'u':
			i++ // consume the "u"
			if r, n, ok := decodeUnicodeEscape(body[i:]); ok {
				b.WriteRune(r)
				i += n
			}
			// A malformed \u{...} was lexer-reported; drop it best-effort.
		default:
			// An unknown escape was lexer-reported; keep the escaped byte.
			b.WriteByte(body[i])
			i++
		}
	}
	return b.String()
}

// decodeUnicodeEscape decodes the body of a \u{...} escape from the start of s
// (which begins just after the "u", i.e. at the "{"). It returns the code point,
// the number of bytes consumed (through the closing "}"), and whether s held a
// well-formed escape.
func decodeUnicodeEscape(s string) (r rune, n int, ok bool) {
	if len(s) == 0 || s[0] != '{' {
		return 0, 0, false
	}
	end := strings.IndexByte(s, '}')
	if end < 0 {
		return 0, 0, false
	}
	hex := s[1:end]
	if len(hex) == 0 {
		return 0, 0, false
	}
	v, err := strconv.ParseInt(hex, 16, 32)
	if err != nil {
		return 0, 0, false
	}
	return rune(v), end + 1, true
}

// lowerCollectionLit lowers a CollectionLit node to its entries: a bare element
// child becomes a value-only entry (a list element), and a MapEntry child
// becomes a key/value entry. An empty literal yields no entries.
func lowerCollectionLit(t cst.Tree, buf source.Buffer, node *cst.Node) ast.Expr {
	var entries []*ast.CollectionEntry
	for _, child := range t.Children() {
		n, ok := child.Node()
		if !ok {
			continue
		}
		switch {
		case n.Kind() == cst.MapEntry:
			entries = append(entries, lowerMapEntry(child, buf))
		case isExprKind(n.Kind()):
			entries = append(entries, &ast.CollectionEntry{Value: lowerExpr(child, buf)})
		}
	}
	return ast.NewCollectionLit(entries, node)
}

// lowerMapEntry lowers a MapEntry node (key ":" value) to a collection entry.
// The two expression children are the key and the value, in order; either is nil
// when the source omitted it.
func lowerMapEntry(t cst.Tree, buf source.Buffer) *ast.CollectionEntry {
	var exprs []ast.Expr
	for _, child := range t.Children() {
		if n, ok := child.Node(); ok && isExprKind(n.Kind()) {
			exprs = append(exprs, lowerExpr(child, buf))
		}
	}
	entry := &ast.CollectionEntry{}
	if len(exprs) > 0 {
		entry.Key = exprs[0]
	}
	if len(exprs) > 1 {
		entry.Value = exprs[1]
	}
	return entry
}

// lowerRecordLit lowers a RecordLit node to an ast.RecordLit: the optional
// leading type name (the typed form Point{...}; "" for the inferred form
// {...}) and the field initializers. The only direct Ident token child is the
// type name — the field names are nested in the RecordField nodes.
func lowerRecordLit(t cst.Tree, buf source.Buffer, node *cst.Node) ast.Expr {
	var typeName string
	var fields []*ast.FieldInit
	for _, child := range t.Children() {
		if tok, ok := child.Token(); ok {
			if tok.Kind() == token.Ident {
				typeName = child.Text(buf)
			}
			continue
		}
		if n, ok := child.Node(); ok && n.Kind() == cst.RecordField {
			fields = append(fields, lowerRecordField(child, buf))
		}
	}
	return ast.NewRecordLit(typeName, fields, node)
}

// lowerRecordField lowers one field initializer: its name and value, the value
// nil when the source omitted it (a recovered "x:").
func lowerRecordField(t cst.Tree, buf source.Buffer) *ast.FieldInit {
	green, _ := t.Node()
	var name string
	var value ast.Expr
	for _, child := range t.Children() {
		if tok, ok := child.Token(); ok {
			if tok.Kind() == token.Ident && name == "" {
				name = child.Text(buf)
			}
			continue
		}
		if n, ok := child.Node(); ok && isExprKind(n.Kind()) {
			value = lowerExpr(child, buf)
		}
	}
	return ast.NewFieldInit(name, value, green)
}

// lowerMemberExpr lowers an explicit member access, receiver.member, to an
// ast.MemberExpr. The member name has no CST node of its own, so its synthetic
// Identifier shares the MemberExpr's node.
func lowerMemberExpr(t cst.Tree, buf source.Buffer, node *cst.Node) ast.Expr {
	var receiver ast.Expr
	var member string
	for _, child := range t.Children() {
		if n, ok := child.Node(); ok && isExprKind(n.Kind()) && receiver == nil {
			receiver = lowerExpr(child, buf)
			continue
		}
		if tok, ok := child.Token(); ok && tok.Kind() == token.Ident {
			member = child.Text(buf)
		}
	}
	return ast.NewMemberExpr(receiver, ast.NewIdentifier(member, node), node)
}

// lowerCallExpr lowers an explicit call, callee(args), to an ast.CallExpr.
func lowerCallExpr(t cst.Tree, buf source.Buffer, node *cst.Node) ast.Expr {
	var callee ast.Expr
	var args []ast.Expr
	for _, child := range t.Children() {
		n, ok := child.Node()
		if !ok || !isExprKind(n.Kind()) {
			continue
		}
		if callee == nil {
			callee = lowerExpr(child, buf)
		} else {
			args = append(args, lowerExpr(child, buf))
		}
	}
	return ast.NewCallExpr(callee, args, node)
}

// lowerFuncLit lowers a function-literal expression — fn(Params): Result with
// either a block body or an arrow body — to an ast.FuncLit. Its parameter list,
// result type, and block body lower the same way a method declaration's do. An
// arrow body, "->" Expr, is normalized here — and only here — to a single
// implicit return, so inference, lowering, and evaluation see the same FuncLit
// shape for both body forms. The kinds keep the children apart: a result type
// is a type-expression node, the arrow body an expression node.
func lowerFuncLit(t cst.Tree, buf source.Buffer, node *cst.Node) ast.Expr {
	var params []*ast.ParamDef
	var result ast.TypeExpr
	var body []ast.Stmt
	for _, child := range t.Children() {
		n, ok := child.Node()
		if !ok {
			continue
		}
		switch {
		case n.Kind() == cst.ParamList:
			params = lowerParamList(child, buf)
		case n.Kind() == cst.Block:
			body = lowerBlock(child, buf)
		case isTypeExprKind(n.Kind()):
			result = lowerTypeExpr(child, buf)
		case isExprKind(n.Kind()):
			body = []ast.Stmt{ast.NewReturnStmt(lowerExpr(child, buf), n)}
		}
	}
	return ast.NewFuncLit(params, result, body, node)
}

// --- type declarations ------------------------------------------------------

// isTypeExprKind reports whether a CST node kind is a type-expression node.
func isTypeExprKind(k cst.Kind) bool {
	switch k {
	case cst.TypeName, cst.UnionType, cst.RecordType, cst.FuncType, cst.BuiltinType:
		return true
	default:
		return false
	}
}

// lowerTypeDecl lowers a positioned TypeDecl CST node into an ast.TypeDecl.
func lowerTypeDecl(t cst.Tree, buf source.Buffer) *ast.TypeDecl {
	green, _ := t.Node()

	var (
		doc     []string
		public  bool
		name    string
		params  []*ast.TypeParam
		body    ast.TypeExpr
		where   ast.Expr
		methods []*ast.MethodDecl
	)
	for _, child := range t.Children() {
		if tok, ok := child.Token(); ok {
			switch tok.Kind() {
			case token.Pub:
				public = true
			case token.DocComment:
				doc = append(doc, docText(child.Text(buf)))
			case token.Ident, token.Null:
				// The declared name: an identifier, or the null keyword (null is
				// a builtin type, declarable as `type null = builtin`). Generic
				// parameters and the body's names are nested in their own nodes.
				name = child.Text(buf)
			}
			continue
		}
		node, _ := child.Node()
		switch {
		case node.Kind() == cst.GenericParams:
			params = lowerGenericParams(child, buf)
		case node.Kind() == cst.WhereClause:
			where = lowerWhereClause(child, buf)
		case node.Kind() == cst.ImplBlock:
			methods = lowerImpl(child, buf)
		case isTypeExprKind(node.Kind()):
			body = lowerTypeExpr(child, buf)
		}
	}
	return ast.NewTypeDecl(doc, public, name, params, body, where, methods, green)
}

// lowerWhereClause lowers a "where Expr" clause to its predicate expression, or
// nil when the predicate is missing (a recovered "type T = int8 where").
func lowerWhereClause(t cst.Tree, buf source.Buffer) ast.Expr {
	for _, child := range t.Children() {
		if node, ok := child.Node(); ok && isExprKind(node.Kind()) {
			return lowerExpr(child, buf)
		}
	}
	return nil
}

// lowerGenericParams lowers a GenericParams node to its type parameters.
func lowerGenericParams(t cst.Tree, buf source.Buffer) []*ast.TypeParam {
	var params []*ast.TypeParam
	for _, child := range t.Children() {
		if node, ok := child.Node(); ok && node.Kind() == cst.GenericParam {
			params = append(params, lowerGenericParam(child, buf))
		}
	}
	return params
}

// lowerGenericParam lowers one type parameter: its name and optional constraint.
func lowerGenericParam(t cst.Tree, buf source.Buffer) *ast.TypeParam {
	green, _ := t.Node()
	var name string
	var constraint ast.TypeExpr
	for _, child := range t.Children() {
		if tok, ok := child.Token(); ok {
			if tok.Kind() == token.Ident && name == "" {
				name = child.Text(buf)
			}
			continue
		}
		if node, ok := child.Node(); ok && isTypeExprKind(node.Kind()) {
			constraint = lowerTypeExpr(child, buf)
		}
	}
	return ast.NewTypeParam(name, constraint, green)
}

// lowerTypeExpr lowers a positioned type-expression CST node to its ast.TypeExpr.
func lowerTypeExpr(t cst.Tree, buf source.Buffer) ast.TypeExpr {
	node, ok := t.Node()
	if !ok {
		return nil
	}
	switch node.Kind() {
	case cst.TypeName:
		return lowerTypeName(t, buf, node)
	case cst.UnionType:
		return lowerUnionType(t, buf, node)
	case cst.RecordType:
		return lowerRecordType(t, buf, node)
	case cst.FuncType:
		return lowerFuncType(t, buf, node)
	case cst.BuiltinType:
		return lowerBuiltinType(t, buf, node)
	default:
		return nil
	}
}

// lowerBuiltinType lowers a BuiltinType node (its optional generic arguments).
func lowerBuiltinType(t cst.Tree, buf source.Buffer, node *cst.Node) ast.TypeExpr {
	var args []ast.TypeExpr
	for _, child := range t.Children() {
		if n, ok := child.Node(); ok && n.Kind() == cst.GenericArgs {
			args = lowerGenericArgs(child, buf)
		}
	}
	return ast.NewBuiltinType(args, node)
}

// lowerTypeName lowers a TypeName node: an identifier — qualified by a
// namespace import as in geo.Point — with optional generic arguments, or the
// self/null type keyword. A dangling qualifier (geo.) keeps its namespace and
// leaves the name empty, as elsewhere for recovered parts.
func lowerTypeName(t cst.Tree, buf source.Buffer, node *cst.Node) ast.TypeExpr {
	var idents []string
	var namespace, name string
	var dotted bool
	var args []ast.TypeExpr
	for _, child := range t.Children() {
		if tok, ok := child.Token(); ok {
			switch tok.Kind() {
			case token.Ident:
				idents = append(idents, child.Text(buf))
			case token.Dot:
				dotted = true
			case token.Self:
				name = "self"
			case token.Null:
				name = "null"
			}
			continue
		}
		if n, ok := child.Node(); ok && n.Kind() == cst.GenericArgs {
			args = lowerGenericArgs(child, buf)
		}
	}
	switch {
	case len(idents) >= 2:
		namespace, name = idents[0], idents[1]
	case len(idents) == 1 && dotted:
		namespace = idents[0] // geo. — the qualified name is missing
	case len(idents) == 1:
		name = idents[0]
	}
	return ast.NewNamedType(namespace, name, args, node)
}

// lowerGenericArgs lowers a GenericArgs node to its type arguments.
func lowerGenericArgs(t cst.Tree, buf source.Buffer) []ast.TypeExpr {
	var args []ast.TypeExpr
	for _, child := range t.Children() {
		if node, ok := child.Node(); ok && isTypeExprKind(node.Kind()) {
			args = append(args, lowerTypeExpr(child, buf))
		}
	}
	return args
}

// lowerUnionType lowers a UnionType node to its member types.
func lowerUnionType(t cst.Tree, buf source.Buffer, node *cst.Node) ast.TypeExpr {
	var members []ast.TypeExpr
	for _, child := range t.Children() {
		if n, ok := child.Node(); ok && isTypeExprKind(n.Kind()) {
			members = append(members, lowerTypeExpr(child, buf))
		}
	}
	return ast.NewUnionType(members, node)
}

// lowerRecordType lowers a RecordType node to its fields.
func lowerRecordType(t cst.Tree, buf source.Buffer, node *cst.Node) ast.TypeExpr {
	var fields []*ast.FieldDef
	for _, child := range t.Children() {
		if n, ok := child.Node(); ok && n.Kind() == cst.Field {
			fields = append(fields, lowerField(child, buf))
		}
	}
	return ast.NewRecordType(fields, node)
}

// lowerField lowers one record field: its name and type.
func lowerField(t cst.Tree, buf source.Buffer) *ast.FieldDef {
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
	return ast.NewFieldDef(name, typ, green)
}

// lowerFuncType lowers a FuncType node: its parameter list and result type.
func lowerFuncType(t cst.Tree, buf source.Buffer, node *cst.Node) ast.TypeExpr {
	var params []*ast.ParamDef
	var result ast.TypeExpr
	for _, child := range t.Children() {
		n, ok := child.Node()
		if !ok {
			continue
		}
		switch {
		case n.Kind() == cst.ParamList:
			params = lowerParamList(child, buf)
		case isTypeExprKind(n.Kind()):
			result = lowerTypeExpr(child, buf)
		}
	}
	return ast.NewFuncType(params, result, node)
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

// lowerImpl lowers an ImplBlock node to its method declarations.
func lowerImpl(t cst.Tree, buf source.Buffer) []*ast.MethodDecl {
	var methods []*ast.MethodDecl
	for _, child := range t.Children() {
		if n, ok := child.Node(); ok && n.Kind() == cst.MethodDecl {
			methods = append(methods, lowerMethod(child, buf))
		}
	}
	return methods
}

// lowerMethod lowers a MethodDecl node: its modifiers, name, parameters, result
// type, and statement body.
func lowerMethod(t cst.Tree, buf source.Buffer) *ast.MethodDecl {
	green, _ := t.Node()
	var (
		doc    []string
		public bool
		extern bool
		name   string
		params []*ast.ParamDef
		result ast.TypeExpr
		body   []ast.Stmt
	)
	for _, child := range t.Children() {
		if tok, ok := child.Token(); ok {
			switch tok.Kind() {
			case token.Pub:
				public = true
			case token.Extern:
				extern = true
			case token.DocComment:
				doc = append(doc, docText(child.Text(buf)))
			case token.Ident:
				if name == "" {
					name = child.Text(buf)
				}
			}
			continue
		}
		node, _ := child.Node()
		switch {
		case node.Kind() == cst.ParamList:
			params = lowerParamList(child, buf)
		case node.Kind() == cst.Block:
			body = lowerBlock(child, buf)
		case isTypeExprKind(node.Kind()):
			result = lowerTypeExpr(child, buf)
		}
	}
	return ast.NewMethodDecl(doc, public, extern, name, params, result, body, green)
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

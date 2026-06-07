// This file lowers expression CST nodes into ast expressions, desugaring the
// operator and literal surface syntax: unary and binary operators become
// receiver.method(args) calls, string literals are decoded to their values, and
// collection, record, member, call, and function-literal forms are flattened.
package abstract

import (
	"strconv"
	"strings"

	"github.com/masterbelt/masterbelt/pkg/source"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/token"
)

// isExprKind reports whether a CST node kind is an expression node.
func isExprKind(k cst.Kind) bool {
	switch k {
	case cst.Literal, cst.NameRef, cst.SelfExpr, cst.UnaryExpr, cst.BinaryExpr, cst.RangeExpr, cst.TernaryExpr, cst.CallExpr, cst.MemberExpr, cst.IndexExpr, cst.CollectionLit, cst.RecordLit, cst.FuncLit, cst.ParenExpr, cst.AwaitExpr:
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
	case cst.IndexExpr:
		// coll[i] desugars to coll.get(i): the receiver is the collection, the
		// index the single argument. A read may miss (out of range, key absent),
		// so get's result is a union (V | error) — but that is the type rule's
		// concern; here it is just a method call, the same shape an operator takes.
		recv, index := twoOperands(t, buf)
		var args []ast.Expr
		if index != nil {
			args = append(args, index)
		}
		return desugarCall(recv, "get", args, node)
	case cst.FuncLit:
		return lowerFuncLit(t, buf, node)
	case cst.ParenExpr:
		// The parentheses exist only to override precedence, which the tree
		// shape already encodes; the grouping unwraps to its inner expression.
		return firstOperand(t, buf)
	case cst.AwaitExpr:
		// await marks the suspension point; it wraps its operand rather than
		// desugaring to a method call.
		return ast.NewAwaitExpr(firstOperand(t, buf), node)
	case cst.TernaryExpr:
		// cond ? then : else keeps its own node rather than desugaring: only the
		// taken branch is evaluated, which a uniform call could not express. The
		// three expression children are the condition, then-branch, and
		// else-branch in order; any is nil when the source omitted it.
		cond, then, els := threeOperands(t, buf)
		return ast.NewTernaryExpr(cond, then, els, node)
	case cst.RangeExpr:
		// 0..9 keeps its own node rather than desugaring to range(0, 9): the
		// direction and the half-open trim depend on the bound values, which only
		// the evaluator knows, so the desugaring to range(start, end, step) is
		// deferred to the fold. The two expression children are the lower and upper
		// bounds in order; the operator token distinguishes the half-open "..." form.
		lower, upper := twoOperands(t, buf)
		return ast.NewRangeExpr(lower, upper, operatorKind(t) == token.DotDotDot, node)
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

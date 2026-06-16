package sql

import (
	"math/big"

	"github.com/masterbelt/masterbelt/pkg/belt/builtin"
	"github.com/masterbelt/masterbelt/pkg/belt/eval"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// Lower lowers a query condition — a predicate<M> over a master's columns — to a
// backend-neutral Predicate and its bind values. The predicate is the typed query
// algebra's construction tree: a comparison of columns (a column<M,T> field access
// read off the query binding) against a value or another column, composed by the
// logical operators. A condition the core cannot express yields one or more
// Unsupported entries; a caller treats any Unsupported as a rejection (it does not
// render or run a partial predicate).
//
// The lowering is type-driven: a column is a field access whose type is the query
// algebra's column<M,T>, not a read off self — value-mode reads (self.field in a
// per-row validate) are folded by the evaluator, never lowered to SQL, so they do
// not reach here.
func Lower(pred ir.Value) (Predicate, []Unsupported) {
	l := &lowering{}
	root := l.expr(pred)
	return Predicate{root: root, binds: l.binds}, l.unsupported
}

// lowering accumulates the bind values and the unsupported nodes as it walks.
type lowering struct {
	binds       []Bind
	unsupported []Unsupported
}

func (l *lowering) reject(v ir.Value, reason string) sqlExpr {
	l.unsupported = append(l.unsupported, Unsupported{Node: v, Reason: reason})
	return nil
}

// expr lowers one value-graph node to a SQL expression node, recording a bind or
// an Unsupported as it goes. It returns nil for a node it rejects.
func (l *lowering) expr(v ir.Value) sqlExpr {
	switch n := v.(type) {
	case *ir.Call:
		return l.call(n)
	case *ir.FieldAccess:
		return l.column(n)
	case *ir.Adapt:
		// A literal flowing into a sized/typed column is wrapped in an Adapt; the
		// SQL value is the literal beneath it, bound by its own kind.
		return l.expr(n.Value)
	case *ir.IntLiteral:
		return l.intLiteral(n)
	case *ir.StringLiteral:
		l.binds = append(l.binds, Bind{Kind: BindText, Text: n.Value})
		return placeholder{}
	case *ir.BoolLiteral:
		l.binds = append(l.binds, Bind{Kind: BindBool, Bool: n.Value})
		return placeholder{}
	case *ir.EnumMemberValue:
		return l.enumMember(n)
	default:
		return l.reject(v, "expression")
	}
}

// The comparison operators whose null form is special (= NULL is never true, so
// equality and inequality against null become IS [NOT] NULL).
const (
	opEql = "eql"
	opNeq = "neq"
)

// comparisonOps maps each comparison operator's method name to its SQL operator.
var comparisonOps = map[string]string{
	opEql: "=", opNeq: "<>", "lt": "<", "lteq": "<=", "gt": ">", "gteq": ">=",
}

// call lowers a method call — the desugared form of every operator. The column
// comparisons and the predicate's logical operators map to SQL, and a unary sign
// over an integer literal folds to a signed bind; anything else (an arithmetic
// operator, a row method) is outside the core.
func (l *lowering) call(c *ir.Call) sqlExpr {
	// A unary sign over a literal is the literal's value, signed — the form a
	// negative threshold (c.id >= -1) takes, where -1 is neg over the literal 1.
	if c.Method == "neg" || c.Method == "pos" {
		return l.signedLiteral(c)
	}
	// A comparison over a column whose element type overrides the operator does not
	// carry the builtin's SQL semantics — SQL's = would apply different semantics
	// than the column value's own — so reject it rather than mis-lower it.
	if l.overridden(c) {
		return l.reject(c, "overridden operator "+c.Method)
	}
	switch c.Method {
	case "anan", "oror":
		return l.logical(c)
	case "not":
		if x := l.expr(c.Receiver); x != nil {
			return unary{x}
		}
		return nil
	}
	if _, ok := comparisonOps[c.Method]; ok {
		return l.compare(c)
	}
	return l.reject(c, "operator "+c.Method)
}

// signedLiteral folds a unary sign over an integer literal to a signed bind. A
// sign over anything else (a column, an expression) is arithmetic, outside the
// core, and rejected.
func (l *lowering) signedLiteral(c *ir.Call) sqlExpr {
	lit, ok := intLiteralOf(c.Receiver)
	if !ok {
		return l.reject(c, "operator "+c.Method)
	}
	v, ok := eval.ParseIntLiteral(lit.Text)
	if !ok {
		return l.reject(lit, "integer literal")
	}
	if c.Method == "neg" {
		v = new(big.Int).Neg(v)
	}
	l.binds = append(l.binds, Bind{Kind: BindInt, Int: v})
	return placeholder{}
}

// overridden reports whether any operand of an operator call is a column whose
// element type overrides the operator method — so the call would not carry the
// builtin operator's SQL semantics.
func (l *lowering) overridden(c *ir.Call) bool {
	if l.overrides(c.Receiver, c.Method) {
		return true
	}
	for _, a := range c.Args {
		if l.overrides(a, c.Method) {
			return true
		}
	}
	return false
}

// logical lowers && / || to AND / OR over its two operands.
func (l *lowering) logical(c *ir.Call) sqlExpr {
	if len(c.Args) != 1 {
		return l.reject(c, "operator "+c.Method)
	}
	left := l.expr(c.Receiver)
	right := l.expr(c.Args[0])
	if left == nil || right == nil {
		return nil
	}
	op := "AND"
	if c.Method == "oror" {
		op = "OR"
	}
	return binary{op: op, l: left, r: right}
}

// compare lowers a comparison (its operator already confirmed not overridden by
// call). Equality and inequality against null — on either side, since SQL's
// = NULL is never true — become IS NULL / IS NOT NULL over the other operand;
// every other comparison is the plain operator over its two operands.
func (l *lowering) compare(c *ir.Call) sqlExpr {
	if len(c.Args) != 1 {
		return l.reject(c, "operator "+c.Method)
	}
	switch {
	case isNull(c.Args[0]):
		return l.nullCompare(c, c.Receiver)
	case isNull(c.Receiver):
		return l.nullCompare(c, c.Args[0])
	}
	left := l.expr(c.Receiver)
	right := l.expr(c.Args[0])
	if left == nil || right == nil {
		return nil
	}
	return binary{op: comparisonOps[c.Method], l: left, r: right}
}

// nullCompare lowers a comparison against the null literal: only equality and
// inequality have a null form (IS [NOT] NULL); an ordering against null is not a
// query condition the core expresses.
func (l *lowering) nullCompare(c *ir.Call, operand ir.Value) sqlExpr {
	if c.Method != opEql && c.Method != opNeq {
		return l.reject(c, "null comparison")
	}
	if x := l.expr(operand); x != nil {
		return nullTest{x: x, not: c.Method == opNeq}
	}
	return nil
}

// column lowers a column reference: a field access whose type is the query
// algebra's column<M,T>, read off the query binding. Its SQL is the column named by
// the field. A field access of any other type is not a column of this table (a
// value read, a getter) and is rejected rather than emitted as a column that does
// not exist.
func (l *lowering) column(f *ir.FieldAccess) sqlExpr {
	if _, ok := columnElem(f); !ok {
		return l.reject(f, "non-column field access")
	}
	return column{name: f.Field}
}

func (l *lowering) intLiteral(n *ir.IntLiteral) sqlExpr {
	v, ok := eval.ParseIntLiteral(n.Text)
	if !ok {
		return l.reject(n, "integer literal")
	}
	l.binds = append(l.binds, Bind{Kind: BindInt, Int: v})
	return placeholder{}
}

// enumMember binds an enum member used as a comparison value by its underlying
// base value — the integer or string the member stands for — so a column compared
// against an enum compares against the value stored for it.
func (l *lowering) enumMember(m *ir.EnumMemberValue) sqlExpr {
	if m.Def == nil || m.Def.Enum == nil || m.Index < 0 || m.Index >= len(m.Def.Enum.Members) {
		return l.reject(m, "enum member")
	}
	v := m.Def.Enum.Members[m.Index].Value
	if v == nil {
		return l.reject(m, "enum member value")
	}
	switch v.Kind {
	case ir.ConstInt:
		l.binds = append(l.binds, Bind{Kind: BindInt, Int: v.Int})
		return placeholder{}
	case ir.ConstString:
		l.binds = append(l.binds, Bind{Kind: BindText, Text: v.Str})
		return placeholder{}
	default:
		// An enum's base is an integer or a string, so no other kind is a member
		// value; reject anything else rather than emit a bind for it.
		return l.reject(m, "enum member value")
	}
}

// intLiteralOf returns the integer literal beneath a value, seen through the
// Adapt a typed literal is wrapped in, or false for anything else.
func intLiteralOf(v ir.Value) (*ir.IntLiteral, bool) {
	if a, ok := v.(*ir.Adapt); ok {
		v = a.Value
	}
	lit, ok := v.(*ir.IntLiteral)
	return lit, ok
}

// columnElem returns the element type T of a column<M, T> field access — the type
// the column's value has — and whether f is a column reference at all. The lowering
// keys a column on its type rather than its receiver: a field access typed
// column<M, T> names a table column (by its field name) wherever the binding it
// reads off came from.
func columnElem(f *ir.FieldAccess) (ir.Type, bool) {
	app, ok := f.Type.(*ir.App)
	if !ok || app.Def == nil || app.Def.Name != builtin.NameColumn || len(app.Args) != 2 {
		return nil, false
	}
	return app.Args[1], true
}

// overrides reports whether a value is a column whose element type declares the
// operator method itself — a user-defined operator that does not carry the
// builtin's SQL semantics. A column of a builtin scalar, or of an alias/refinement
// that inherits the operator rather than declaring it, does not override.
func (l *lowering) overrides(v ir.Value, method string) bool {
	f, ok := v.(*ir.FieldAccess)
	if !ok {
		return false
	}
	elem, ok := columnElem(f)
	if !ok {
		return false
	}
	// A nullable column (Weird | null) carries its custom comparison on the non-null
	// member, so unwrap the null before the nominal check — otherwise the union is
	// not nominal, declaresMethod returns false, and a custom-comparison column would
	// lower to plain SQL equality.
	return declaresMethod(nonNullType(elem), method)
}

// nonNullType strips a null member from a union, yielding the single remaining
// member — so a nullable type T | null is examined as T. A non-union, or a union
// with more than one non-null member, is returned unchanged.
func nonNullType(t ir.Type) ir.Type {
	u, ok := t.(*ir.Union)
	if !ok {
		return t
	}
	var nonNull []ir.Type
	for _, m := range u.Members {
		if b, ok := m.(*ir.Builtin); ok && b.Name == builtin.NameNull {
			continue
		}
		nonNull = append(nonNull, m)
	}
	if len(nonNull) == 1 {
		return nonNull[0]
	}
	return t
}

// declaresMethod reports whether a nominal type, or one in its underlying chain,
// declares its own implementation of a method of the given name — the test for an
// overridden operator. Only a method with a body counts: an extern method is a
// builtin's or an enum's synthesized operator, which carries the SQL-standard
// semantics (an enum compares by its base value, which SQL reproduces), whereas a
// method with a body is user-defined logic SQL cannot stand in for. A generic
// application (Weird<string>) carries the same definition as the bare nominal, so
// it is unwrapped the same way.
func declaresMethod(t ir.Type, method string) bool {
	seen := map[*ir.TypeDef]bool{}
	for {
		var def *ir.TypeDef
		switch x := t.(type) {
		case *ir.Named:
			def = x.Def
		case *ir.App:
			def = x.Def
		default:
			return false
		}
		if def == nil || seen[def] {
			return false
		}
		seen[def] = true
		for i := range def.Methods {
			if def.Methods[i].Name == method && !def.Methods[i].Extern {
				return true
			}
		}
		t = def.Body
	}
}

// isNull reports whether a value is the null literal, seen through the Adapt a
// typed-null position wraps it in.
func isNull(v ir.Value) bool {
	if a, ok := v.(*ir.Adapt); ok {
		v = a.Value
	}
	_, ok := v.(*ir.NullValue)
	return ok
}

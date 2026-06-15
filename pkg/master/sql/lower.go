package sql

import (
	"math/big"

	"github.com/masterbelt/masterbelt/pkg/belt/eval"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// Lower lowers a row predicate to a backend-neutral Predicate and its bind
// values. fields are the row's stored columns (name and type), which the
// lowering needs to tell a real column from a getter read — both surface as a
// field access — and to tell a builtin operator from one a column's nominal type
// overrides. A predicate the core cannot express yields one or more Unsupported
// entries; a caller treats any Unsupported as a rejection (it does not render or
// run a partial predicate).
func Lower(pred ir.Value, fields []ir.Field) (Predicate, []Unsupported) {
	cols := make(map[string]ir.Type, len(fields))
	for _, f := range fields {
		cols[f.Name] = f.Type
	}
	l := &lowering{cols: cols}
	root := l.expr(pred)
	return Predicate{root: root, binds: l.binds}, l.unsupported
}

// lowering accumulates the bind values and the unsupported nodes as it walks. cols
// is the row's stored columns by name, the truth for what is a column.
type lowering struct {
	cols        map[string]ir.Type
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

// call lowers a method call — the desugared form of every operator. The
// comparison, logical, and negation operators map to SQL, and a unary sign over
// an integer literal folds to a signed bind; anything else (an arithmetic
// operator over columns, a column or row method) is outside the core.
func (l *lowering) call(c *ir.Call) sqlExpr {
	// A unary sign over a literal is the literal's value, signed — the form a
	// negative threshold (self.id >= -1) takes, where -1 is neg over the literal 1.
	if c.Method == "neg" || c.Method == "pos" {
		return l.signedLiteral(c)
	}
	// The operator forms below must be the builtin operator, not one a column's
	// type overrides — SQL's builtin =/AND/NOT would otherwise apply different
	// semantics than the column's own. Reject an overridden operator on any operand.
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
// type overrides the operator method — so the call is not the builtin operator.
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
// row predicate the core expresses.
func (l *lowering) nullCompare(c *ir.Call, operand ir.Value) sqlExpr {
	if c.Method != opEql && c.Method != opNeq {
		return l.reject(c, "null comparison")
	}
	if x := l.expr(operand); x != nil {
		return nullTest{x: x, not: c.Method == opNeq}
	}
	return nil
}

// column lowers a column reference: a read of a stored row column off self (an
// implicit-self read, lowered to self.field). A read off anything other than self
// is not a single column of this table; a read whose name is not a stored column
// is a getter (which also surfaces as a field access) computing a value no table
// column holds. Both are outside the core and rejected rather than emitted as a
// column that does not exist.
func (l *lowering) column(f *ir.FieldAccess) sqlExpr {
	if _, ok := f.Receiver.(*ir.SelfValue); !ok {
		return l.reject(f, "non-self field access")
	}
	if _, ok := l.cols[f.Field]; !ok {
		return l.reject(f, "non-column read "+f.Field)
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

// intLiteralOf returns the integer literal beneath a value, seen through the
// Adapt a typed literal is wrapped in, or false for anything else.
func intLiteralOf(v ir.Value) (*ir.IntLiteral, bool) {
	if a, ok := v.(*ir.Adapt); ok {
		v = a.Value
	}
	lit, ok := v.(*ir.IntLiteral)
	return lit, ok
}

// overrides reports whether a value is a column whose nominal type declares the
// operator method itself — a user-defined operator that does not carry the
// builtin's SQL semantics. A builtin scalar, or an alias/refinement that inherits
// the operator rather than declaring it, does not override.
func (l *lowering) overrides(v ir.Value, method string) bool {
	f, ok := v.(*ir.FieldAccess)
	if !ok {
		return false
	}
	return declaresMethod(l.cols[f.Field], method)
}

// declaresMethod reports whether a nominal type, or one in its underlying chain,
// declares a method of the given name — the test for an overridden operator. A
// generic application (Weird<string>) carries the same definition as the bare
// nominal, so it is unwrapped the same way.
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
			if def.Methods[i].Name == method {
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

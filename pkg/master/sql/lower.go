package sql

import (
	"math/big"

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
// comparison, logical, and negation operators map to SQL; anything else (an
// arithmetic operator, a column or row method) is outside the core.
func (l *lowering) call(c *ir.Call) sqlExpr {
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

// compare lowers a comparison. A column whose nominal type overrides the operator
// is not the builtin comparison and is rejected, so SQL never silently ignores
// custom semantics. Equality and inequality against null — on either side, since
// SQL's = NULL is never true — become IS NULL / IS NOT NULL over the other
// operand; every other comparison is the plain operator over its two operands.
func (l *lowering) compare(c *ir.Call) sqlExpr {
	if len(c.Args) != 1 {
		return l.reject(c, "operator "+c.Method)
	}
	if l.overrides(c.Receiver, c.Method) || l.overrides(c.Args[0], c.Method) {
		return l.reject(c, "overridden operator "+c.Method)
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
	v, ok := new(big.Int).SetString(n.Text, 0)
	if !ok {
		return l.reject(n, "integer literal")
	}
	l.binds = append(l.binds, Bind{Kind: BindInt, Int: v})
	return placeholder{}
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
// declares a method of the given name — the test for an overridden operator.
func declaresMethod(t ir.Type, method string) bool {
	seen := map[*ir.TypeDef]bool{}
	for {
		n, ok := t.(*ir.Named)
		if !ok || n.Def == nil || seen[n.Def] {
			return false
		}
		seen[n.Def] = true
		for i := range n.Def.Methods {
			if n.Def.Methods[i].Name == method {
				return true
			}
		}
		t = n.Def.Body
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

package sql

import (
	"github.com/masterbelt/masterbelt/pkg/belt/eval"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// CountRelation recognizes a relation count query — count() over a chain of where
// narrowings over a master relation, the shape Cards.where(...).count() and
// Cards.count() lower to — and returns the Relation to count together with the
// master it is over and any where predicates lowered to SQL. ok is false when the
// value is not such a chain (a different aggregate, a non-relation expression), so
// the caller leaves it to the ordinary fold. A predicate the core cannot express
// yields Unsupported entries, which the caller treats as a rejection.
//
// env folds a where predicate's data-independent operands to constants before
// lowering — a named constant (c.cost > MIN) or an arithmetic expression
// (c.id > 1 + 2) — since Lower binds only literals. It resolves constants, not
// master rows: the recognition and lowering still need no row data, which the
// query driver supplies by pairing the returned Relation with the master's loaded
// engine and running engine.Count.
func CountRelation(chain ir.Value, env eval.GraphEnv) (rel Relation, master *ir.TypeDef, unsupported []Unsupported, ok bool) {
	count, ok := asCall(chain, "count")
	if !ok || len(count.Args) != 0 {
		return Relation{}, nil, nil, false
	}
	rel = All()
	recv := count.Receiver
	for {
		switch r := unwrap(recv).(type) {
		case *ir.MasterRelation:
			return rel, r.Master, unsupported, true
		case *ir.Call:
			if r.Method != "where" || len(r.Args) != 1 {
				return Relation{}, nil, nil, false
			}
			pred, ok := whereBody(r.Args[0])
			if !ok {
				return Relation{}, nil, nil, false
			}
			p, u := Lower(foldOperands(pred, env))
			unsupported = append(unsupported, u...)
			rel = rel.Where(p)
			recv = r.Receiver
		default:
			return Relation{}, nil, nil, false
		}
	}
}

// foldOperands replaces every data-independent subexpression of a predicate with
// the constant it evaluates to, so a named-constant or arithmetic operand reaches
// Lower as a literal it can bind. A subexpression that reads a column (or the query
// binding) is data-dependent: eval.Graph leaves it unevaluable (nil), so it is kept
// and its children are folded instead — the column comparison stays, only the value
// side collapses. The fold never reduces the column, the relation, or the count
// (all unevaluable without row data); it only resolves the constants the predicate
// compares against, which Lower otherwise rejects as non-literal expressions.
func foldOperands(v ir.Value, env eval.GraphEnv) ir.Value {
	if v == nil {
		return nil
	}
	if lit := constantToValue(eval.Graph(v, env)); lit != nil {
		return lit
	}
	switch n := v.(type) {
	case *ir.Call:
		folded := *n
		folded.Receiver = foldOperands(n.Receiver, env)
		folded.Args = make([]ir.Value, len(n.Args))
		for i, a := range n.Args {
			folded.Args[i] = foldOperands(a, env)
		}
		return &folded
	case *ir.Adapt:
		folded := *n
		folded.Value = foldOperands(n.Value, env)
		return &folded
	default:
		return v
	}
}

// constantToValue renders a folded constant as the literal node Lower binds: an
// integer, string, or boolean. It is nil for a constant Lower does not bind (a
// collection, a record, an error, an enum base) and for a nil constant (an
// unevaluable subexpression), leaving the original node in place.
func constantToValue(c *ir.Constant) ir.Value {
	if c == nil {
		return nil
	}
	switch c.Kind {
	case ir.ConstInt:
		return &ir.IntLiteral{Text: c.Int.String()}
	case ir.ConstString:
		return &ir.StringLiteral{Value: c.Str}
	case ir.ConstBool:
		return &ir.BoolLiteral{Value: c.Bool}
	default:
		return nil
	}
}

// asCall returns v as a method call of the given name, seen through an Adapt.
func asCall(v ir.Value, method string) (*ir.Call, bool) {
	c, ok := unwrap(v).(*ir.Call)
	if !ok || c.Method != method {
		return nil, false
	}
	return c, true
}

// whereBody returns the predicate a where lambda yields: the value of its single
// return. It requires exactly that shape — one return of a value, the form the
// arrow lambda fn(c) -> pred takes — and is false for anything else, so a lambda
// with block control flow (multiple statements, a conditional return) is left
// unrecognized rather than silently lowered to one branch's predicate.
func whereBody(v ir.Value) (ir.Value, bool) {
	lit, ok := unwrap(v).(*ir.FuncLiteral)
	if !ok || len(lit.Body) != 1 {
		return nil, false
	}
	r, ok := lit.Body[0].(*ir.Return)
	if !ok || r.Value == nil {
		return nil, false
	}
	return r.Value, true
}

// unwrap sees a value through the Adapt wrappers an accepted conversion adds. It
// peels every layer: a value adapted to a type that needs nested coercions — a
// count widened to short and then tagged into short | error — carries more than one
// Adapt, and a single strip would leave an inner Adapt that hides the call chain.
func unwrap(v ir.Value) ir.Value {
	for {
		a, ok := v.(*ir.Adapt)
		if !ok {
			return v
		}
		v = a.Value
	}
}

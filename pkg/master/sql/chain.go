package sql

import (
	"math"
	"math/big"

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
	// A count does not respect a limit (its SQL counts every matching row), so a
	// limited count is not recognized here — it is left unfoldable and the check
	// fails safe rather than reporting the unlimited count as if it were the limited
	// one.
	return relationChain(count.Receiver, env, false)
}

// RowsRelation recognizes a relation materialization — to_list() over a chain of
// where narrowings and limits over a master relation, the shape
// Cards.where(...).limit(n).to_list() lowers to — and returns the Relation whose
// rows to read together with the master it is over and any where predicates lowered
// to SQL. ok is false when the value is not such a chain. Unlike count and sum, the
// chain may carry a limit, which caps the rows read; env folds the where operands as
// in CountRelation.
func RowsRelation(chain ir.Value, env eval.GraphEnv) (rel Relation, master *ir.TypeDef, unsupported []Unsupported, ok bool) {
	tl, ok := asCall(chain, "to_list")
	if !ok || len(tl.Args) != 0 {
		return Relation{}, nil, nil, false
	}
	return relationChain(tl.Receiver, env, true)
}

// SumRelation recognizes a relation sum query — sum(fn(c) -> c.col) over a chain of
// where narrowings over a master relation, the shape Cards.sum(fn(c) -> c.cost)
// lowers to — and returns the Relation to sum, the column to add, the master it is
// over, and any where predicates lowered to SQL. ok is false when the value is not
// such a chain or the selector does not name a column. The numeric-ness of the
// column is the checker's guarantee (the sum selector's T: numeric bound), so the
// driver lowers the column unconditionally; env folds the where operands as in
// CountRelation.
func SumRelation(chain ir.Value, env eval.GraphEnv) (rel Relation, column string, master *ir.TypeDef, unsupported []Unsupported, ok bool) {
	sum, ok := asCall(chain, "sum")
	if !ok || len(sum.Args) != 1 {
		return Relation{}, "", nil, nil, false
	}
	col, ok := selectorColumn(sum.Args[0])
	if !ok {
		return Relation{}, "", nil, nil, false
	}
	// A sum, like a count, does not respect a limit, so a limited sum is not
	// recognized and is left to fail safe.
	rel, master, unsupported, ok = relationChain(sum.Receiver, env, false)
	if !ok {
		return Relation{}, "", nil, nil, false
	}
	// The column's numeric-ness is the checker's bound; its summability by plain SQL
	// is not. An arbitrary-precision column (the sum may exceed int64) or one whose
	// type carries a custom add is rejected here, the aggregate twin of the
	// comparison lowering's custom-operator guard, so the engine never sums it with
	// the wrong arithmetic or a truncated total.
	elem, _ := columnElem(col)
	if !sqlSummable(elem) {
		unsupported = append(unsupported, Unsupported{Node: col, Reason: "sum of this column type"})
	}
	return rel, col.Field, master, unsupported, ok
}

// relationChain walks the where-narrowing chain over a master relation that an
// aggregate sits on — [where(fn(c)->pred)]* over MasterRelation — accumulating the
// lowered filter. It is shared by every aggregate (count, sum), so they recognize
// the same relation shape and lower the same where predicates.
func relationChain(recv ir.Value, env eval.GraphEnv, allowLimit bool) (rel Relation, master *ir.TypeDef, unsupported []Unsupported, ok bool) {
	rel = All()
	// The walk runs outermost call first, and the flat render applies WHERE, then
	// ORDER BY, then LIMIT — so a limit must be the outermost (last applied) operation.
	// A where or order seen before a limit is applied after it (filtering or sorting the
	// already-capped relation), which the flat shape cannot express; such a limit is
	// declined, so a limit-before-filter or limit-before-order chain is left unfoldable
	// and fails safe rather than reading rows from outside the cap. A where or order
	// after a limit in source (limit outer) is the supported filter/sort-then-cap order.
	// Filtering and ordering commute, so their relative order needs no such guard.
	sawFilterOrOrder := false
	for {
		switch r := unwrap(recv).(type) {
		case *ir.MasterRelation:
			return rel, r.Master, unsupported, true
		case *ir.Call:
			switch {
			case r.Method == "where" && len(r.Args) == 1:
				pred, ok := whereBody(r.Args[0])
				if !ok {
					return Relation{}, nil, nil, false
				}
				p, u := Lower(foldOperands(pred, env))
				unsupported = append(unsupported, u...)
				rel = rel.Where(p)
				recv = r.Receiver
				sawFilterOrOrder = true
			case r.Method == "order" && len(r.Args) == 1:
				col, desc, elem, ok := orderKey(r.Args[0])
				if !ok {
					return Relation{}, nil, nil, false
				}
				// A column whose type carries a custom order cannot be sorted by plain
				// SQL — it would sort by the stored scalar, not the type's order — so the
				// key is recorded but reported unsupported, and the consumer fails safe.
				if !sqlOrderable(elem) {
					unsupported = append(unsupported, Unsupported{Node: r.Args[0], Reason: "order by a column with a custom order"})
				}
				rel = rel.OrderBy(col, desc)
				recv = r.Receiver
				sawFilterOrOrder = true
			case allowLimit && r.Method == "limit" && len(r.Args) == 1 && !sawFilterOrOrder:
				n, ok := limitValue(r.Args[0])
				if !ok {
					return Relation{}, nil, nil, false
				}
				rel = rel.Limit(n)
				recv = r.Receiver
			default:
				return Relation{}, nil, nil, false
			}
		default:
			return Relation{}, nil, nil, false
		}
	}
}

// orderKey reads the column and direction an order selector names: fn(c) -> c.col.asc()
// or fn(c) -> c.col.desc(). The body is a direction method (asc or desc, no arguments)
// on a column field access, so a selector that does not name a column ordering is not
// recognized and the chain is left unfoldable. The field access carries the column's
// name; the method name carries the direction.
func orderKey(v ir.Value) (column string, desc bool, elem ir.Type, ok bool) {
	body, ok := whereBody(v)
	if !ok {
		return "", false, nil, false
	}
	call, ok := unwrap(body).(*ir.Call)
	if !ok || len(call.Args) != 0 {
		return "", false, nil, false
	}
	switch call.Method {
	case "asc":
		desc = false
	case "desc":
		desc = true
	default:
		return "", false, nil, false
	}
	fa, ok := unwrap(call.Receiver).(*ir.FieldAccess)
	if !ok {
		return "", false, nil, false
	}
	el, ok := columnElem(fa)
	if !ok {
		return "", false, nil, false
	}
	return fa.Field, desc, el, true
}

// limitValue reads the row cap a limit(n) carries: the evaluator folds the argument
// to an integer literal before the chain reaches here, so a non-literal (an
// unevaluable cap) is not recognized and the whole materialization is left to fail
// safe. A cap wider than int64 is a valid nint the SQL literal cannot hold, but a cap
// that large is no effective cap (no table holds that many rows), so it clamps to the
// widest representable cap rather than failing to render; a value below zero floors to
// zero, the floor Relation.Limit keeps. An unparseable literal is not recognized.
func limitValue(arg ir.Value) (int64, bool) {
	lit, ok := unwrap(arg).(*ir.IntLiteral)
	if !ok {
		return 0, false
	}
	n, ok := new(big.Int).SetString(lit.Text, 0)
	if !ok {
		return 0, false
	}
	if !n.IsInt64() {
		if n.Sign() < 0 {
			return 0, true
		}
		return math.MaxInt64, true
	}
	return n.Int64(), true
}

// selectorColumn returns the single column reference a sum selector names (the c.cost
// of fn(c) -> c.cost). It requires the arrow-lambda shape whereBody requires and a
// body that is a column<M, T> field access, so a selector that computes a value
// rather than naming a column is not recognized. The field access carries the
// column's name and its element type, both of which the caller reads.
func selectorColumn(v ir.Value) (*ir.FieldAccess, bool) {
	body, ok := whereBody(v)
	if !ok {
		return nil, false
	}
	fa, ok := unwrap(body).(*ir.FieldAccess)
	if !ok {
		return nil, false
	}
	if _, ok := columnElem(fa); !ok {
		return nil, false
	}
	return fa, true
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

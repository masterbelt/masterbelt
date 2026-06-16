package sql

import "github.com/masterbelt/masterbelt/pkg/source/ir"

// CountRelation recognizes a relation count query — count() over a chain of where
// narrowings over a master relation, the shape Cards.where(...).count() and
// Cards.count() lower to — and returns the Relation to count together with the
// master it is over and any where predicates lowered to SQL. ok is false when the
// value is not such a chain (a different aggregate, a non-relation expression), so
// the caller leaves it to the ordinary fold. A predicate the core cannot express
// yields Unsupported entries, which the caller treats as a rejection.
//
// The query driver pairs the returned Relation with the master's loaded engine and
// runs engine.Count; the recognition and the predicate lowering live here, apart
// from the engine, so they need no data.
func CountRelation(chain ir.Value) (rel Relation, master *ir.TypeDef, unsupported []Unsupported, ok bool) {
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
			p, u := Lower(pred)
			unsupported = append(unsupported, u...)
			rel = rel.Where(p)
			recv = r.Receiver
		default:
			return Relation{}, nil, nil, false
		}
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

// This file is the evaluator's seam to the master query driver. The evaluator
// interprets the body a relation query sits in — a static fn's lets and
// arithmetic, a helper call, a conditional — but cannot run the query itself: it
// has no rows, and the one-way layer rule keeps it from the master driver. So when
// it reaches an aggregate over a master relation (count/sum), it inlines any
// let-bound relation back into the chain and hands the self-contained chain to the
// RelationFolder the environment carries (a data-aware env supplies one; a pure
// compile-time fold does not, leaving the aggregate unfoldable). A let that binds a
// relation records its chain here rather than folding to a non-existent constant,
// so a query reached through the local is reconstructed before the folder runs it.

package eval

import "github.com/masterbelt/masterbelt/pkg/source/ir"

// graphRelationAggregate folds a relation aggregate (a count()/sum() over a master
// relation, possibly let-bound) to the constant it yields over the loaded rows, or
// ok=false when v is not such an aggregate or the environment carries no folder.
func graphRelationAggregate(v *ir.Call, ctx graphCtx) (*ir.Constant, bool) {
	rf, ok := ctx.env.(RelationFolder)
	if !ok {
		return nil, false
	}
	chain := inlineRelation(v, ctx.relationLocals)
	if !rootsAtRelation(chain) {
		return nil, false
	}
	return rf.FoldRelationAggregate(chain)
}

// inlineRelation rebuilds a relation chain's receiver spine with each let-bound
// relation inlined — a LocalRef the chain narrows through (matching in
// matching.count()) is replaced by the relation it binds (self.where(...)) — so the
// driver, which walks only where over the master relation and does not follow a
// local, sees the whole chain. Only the spine is rebuilt; the where predicates and
// the sum selector ride along untouched. A value not part of a chain is unchanged.
func inlineRelation(v ir.Value, locals map[string]ir.Value) ir.Value {
	switch v := v.(type) {
	case *ir.Call:
		return &ir.Call{Receiver: inlineRelation(v.Receiver, locals), Method: v.Method, Args: v.Args, Setter: v.Setter, Resolved: v.Resolved, Subst: v.Subst, Type: v.Type, Syntax: v.Syntax}
	case *ir.LocalRef:
		if rv, ok := locals[v.Name]; ok {
			return inlineRelation(rv, locals)
		}
		return v
	case *ir.Adapt:
		return inlineRelation(v.Value, locals)
	default:
		return v
	}
}

// rootsAtRelation reports whether a value's receiver spine bottoms at a master
// relation — through where narrowings and Adapt wrappers — so it is a query over
// the relation rather than an ordinary value. The local inlining has already run,
// so a bare LocalRef here is not a relation.
func rootsAtRelation(v ir.Value) bool {
	for {
		switch r := v.(type) {
		case *ir.MasterRelation:
			return true
		case *ir.Call:
			v = r.Receiver
		case *ir.Adapt:
			v = r.Value
		default:
			return false
		}
	}
}

// isRelationExpr reports whether v is the master relation itself — a where narrowing
// over it, or a let that binds one (followed through locals) — and not an aggregate
// (count/sum) over it. A let binding such a value records the chain in relationLocals
// so a use of it can be inlined; a let binding an aggregate (a scalar count) or any
// other value folds the ordinary way.
func isRelationExpr(v ir.Value, locals map[string]ir.Value) bool {
	switch v := v.(type) {
	case *ir.MasterRelation:
		return true
	case *ir.Call:
		return v.Method == "where" && isRelationExpr(v.Receiver, locals)
	case *ir.LocalRef:
		if rv, ok := locals[v.Name]; ok {
			return isRelationExpr(rv, locals)
		}
	case *ir.Adapt:
		return isRelationExpr(v.Value, locals)
	}
	return false
}

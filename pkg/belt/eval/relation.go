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
	// A where predicate may compare a column against a captured scalar — a static fn
	// parameter (c.rarity == r) or a let in the same body (c.cost > min) — which the
	// driver, having only the chain and no body locals, cannot resolve. Fold those
	// scalars to their constants here, where the locals are in scope, so the chain the
	// driver lowers carries literals it can bind.
	chain = substituteChainScalars(chain, ctx)
	return rf.FoldRelationAggregate(chain)
}

// graphConstantToValue renders a folded constant as the literal node the driver's
// lowering binds — an integer, string, boolean, or enum member. It is nil for a
// constant the lowering does not bind (a collection, record, function) and for a nil
// constant, leaving the original node in place. A captured scalar carries the
// column's own scalar type, since a comparison against a nullable or otherwise wider
// capture does not type, so no null or union member reaches a column comparison here.
func graphConstantToValue(c *ir.Constant) ir.Value {
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
	case ir.ConstEnum:
		return &ir.EnumMemberValue{Def: c.EnumDef, Index: c.EnumIndex}
	default:
		return nil
	}
}

// substituteChainScalars folds the captured scalars a relation chain's where
// predicates compare columns against — a static fn parameter or a same-body let — to
// the constants they hold under the current locals, so the driver, which binds only
// literals, can lower them. Only the value side of a comparison collapses; the column
// reads stay unevaluable and ride along.
func substituteChainScalars(chain ir.Value, ctx graphCtx) ir.Value {
	switch v := chain.(type) {
	case *ir.Call:
		out := *v
		out.Receiver = substituteChainScalars(v.Receiver, ctx)
		if v.Method == "where" && len(v.Args) == 1 {
			out.Args = []ir.Value{substituteWhereScalars(v.Args[0], ctx)}
		}
		return &out
	case *ir.Adapt:
		out := *v
		out.Value = substituteChainScalars(v.Value, ctx)
		return &out
	default:
		return chain
	}
}

// substituteWhereScalars rewrites a where lambda's predicate, folding the captured
// scalars its column comparisons read against. The fold runs with the lambda's own
// parameters removed from scope, so the column binding (the c of fn(c) -> ...) stays
// unevaluable — only an outer parameter or let resolves — and a column read is never
// mistaken for a value to collapse.
func substituteWhereScalars(arg ir.Value, ctx graphCtx) ir.Value {
	rewrap := func(v ir.Value) ir.Value { return v }
	inner := arg
	if a, ok := arg.(*ir.Adapt); ok {
		base := *a
		inner = a.Value
		rewrap = func(v ir.Value) ir.Value { b := base; b.Value = v; return &b }
	}
	lit, ok := inner.(*ir.FuncLiteral)
	if !ok || len(lit.Body) != 1 {
		return arg
	}
	ret, ok := lit.Body[0].(*ir.Return)
	if !ok || ret.Value == nil {
		return arg
	}
	foldCtx := ctx
	foldCtx.locals = localsExcluding(ctx.locals, lit.Params)
	newRet := *ret
	newRet.Value = substituteScalarRefs(ret.Value, foldCtx)
	newLit := *lit
	newLit.Body = []ir.Stmt{&newRet}
	return rewrap(&newLit)
}

// substituteScalarRefs folds only a captured scalar reference a predicate names — a
// parameter or a let — to the constant it holds, leaving every other node in place:
// a column read, an arithmetic expression, and crucially a nested relation aggregate
// over the rows. Folding a bare reference and nothing more keeps a row-dependent
// subexpression — a correlated aggregate that reads the outer query binding — from
// collapsing to a constant under the relation folder this fold carries; such a query
// rides along for the driver to decline, failing safe. The data-independent
// arithmetic a substituted literal leaves behind (min + 1) is still folded by the
// driver's own foldOperands, which runs without a relation folder.
func substituteScalarRefs(v ir.Value, ctx graphCtx) ir.Value {
	switch n := v.(type) {
	case *ir.ParamRef, *ir.LocalRef:
		if lit := graphConstantToValue(graphValue(v, ctx)); lit != nil {
			return lit
		}
		return v
	case *ir.Call:
		out := *n
		out.Receiver = substituteScalarRefs(n.Receiver, ctx)
		out.Args = make([]ir.Value, len(n.Args))
		for i, a := range n.Args {
			out.Args[i] = substituteScalarRefs(a, ctx)
		}
		return &out
	case *ir.FieldAccess:
		out := *n
		out.Receiver = substituteScalarRefs(n.Receiver, ctx)
		return &out
	case *ir.Conversion:
		out := *n
		out.Args = make([]ir.Value, len(n.Args))
		for i, a := range n.Args {
			out.Args[i] = substituteScalarRefs(a, ctx)
		}
		return &out
	case *ir.Ternary:
		out := *n
		out.Cond = substituteScalarRefs(n.Cond, ctx)
		out.Then = substituteScalarRefs(n.Then, ctx)
		out.Else = substituteScalarRefs(n.Else, ctx)
		return &out
	case *ir.Adapt:
		out := *n
		out.Value = substituteScalarRefs(n.Value, ctx)
		return &out
	default:
		return v
	}
}

// localsExcluding returns a copy of locals without the given names — a where lambda's
// own parameters, removed so the column binding they name stays unevaluable while an
// outer scalar resolves. The map is returned unchanged when there is nothing to copy.
func localsExcluding(locals map[string]*ir.Constant, names []string) map[string]*ir.Constant {
	if len(locals) == 0 || len(names) == 0 {
		return locals
	}
	out := make(map[string]*ir.Constant, len(locals))
	for k, v := range locals {
		out[k] = v
	}
	for _, n := range names {
		delete(out, n)
	}
	return out
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

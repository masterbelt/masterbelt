// This file is the evaluator's seam to the master query driver. A master relation is
// a first-class value (ir.ConstRelation carrying its query chain), so it flows through
// the evaluator's ordinary plumbing — a let, a parameter, a closure capture, a
// reassignment. A where narrows that value to a new relation; an aggregate (count/sum)
// hands the chain to the RelationFolder the environment carries (a data-aware env
// supplies one; a pure compile-time fold does not, leaving the aggregate unfoldable),
// which runs it against the loaded rows. The evaluator itself has no rows, and the
// one-way layer rule keeps it from the master driver.

package eval

import "github.com/masterbelt/masterbelt/pkg/source/ir"

// graphRelationMethod folds a method whose receiver is a relation value. A where
// narrows the chain to a new relation, folding the captured scalars its predicate
// compares against now — at the where, where the locals are in scope — so a later
// reassignment of a captured let does not change this relation. A count or sum hands
// the chain to the data layer's folder, which runs it against the loaded rows; the
// folder declines (nil) when it cannot run the query — a different master, an
// unsupported predicate, no rows loaded — so the aggregate is left unfolded and a
// check over it fails safe. A method the fold does not recognize, or an aggregate with
// no folder in scope, yields nil.
func graphRelationMethod(v *ir.Call, recv *ir.Constant, ctx graphCtx) *ir.Constant {
	switch v.Method {
	case "where":
		if len(v.Args) != 1 {
			return nil
		}
		out := *v
		out.Receiver = recv.Relation
		out.Args = []ir.Value{substituteWhereScalars(v.Args[0], ctx)}
		return ir.RelationConstant(&out)
	case "count", "sum":
		rf, ok := ctx.env.(RelationFolder)
		if !ok {
			return nil
		}
		out := *v
		out.Receiver = recv.Relation
		c, _ := rf.FoldRelationAggregate(&out)
		return c
	default:
		return nil
	}
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

// noFolderEnv carries the driver's environment and stays a RelationFolder — so the
// data-aware range and refinement checks still run while folding a predicate's
// captured operands — but its fold declines every relation aggregate. A fold run under
// it resolves an ordinary constant (a captured scalar, a helper, a method on a nominal
// scalar, a refined conversion that the admission check still vets) while a relation
// aggregate among the operands stays unevaluable, so a predicate's value side collapses
// without a correlated aggregate over the rows collapsing with it.
type noFolderEnv struct{ GraphEnv }

// FoldRelationAggregate declines every aggregate, so a nested relation query in a
// predicate is left for the driver rather than folded without the outer query binding.
// The type still satisfies RelationFolder, which keeps the data-aware admission checks
// active during the predicate fold.
func (noFolderEnv) FoldRelationAggregate(ir.Value) (*ir.Constant, bool) { return nil, false }

// substituteWhereScalars rewrites a where lambda's predicate, folding the captured
// scalars its column comparisons read against to the constants they hold. The fold
// runs with the lambda's own parameters out of scope (so a column binding stays
// unevaluable) and without the relation folder (so a nested relation aggregate does
// not collapse), the two conditions that keep only the value side of a comparison from
// folding while a column read or a correlated aggregate rides along.
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
	foldCtx.env = noFolderEnv{ctx.env}
	foldCtx.locals = localsExcluding(ctx.locals, lit.Params)
	newRet := *ret
	newRet.Value = substituteScalarRefs(ret.Value, foldCtx)
	newLit := *lit
	newLit.Body = []ir.Stmt{&newRet}
	return rewrap(&newLit)
}

// substituteScalarRefs replaces each subexpression of a predicate that folds to a
// renderable constant under the body's locals — a captured parameter or let, and any
// data-independent expression over one (min + 1, inc(min), int(min), min.bump(),
// useHigh ? a : b) — with that literal, but only when the value inhabits the
// expression's own type. The admission check (graphMemberAdmits) refuses a refined
// conversion the rows violate (Positive(min) for min = -1), which graphConvert folds
// without checking, so an out-of-range or predicate-violating capture is left whole
// for the driver to decline rather than counted. The fold carries a relation folder
// that declines every aggregate (noFolderEnv), so a nested relation aggregate over the
// rows folds to nothing and is left whole too, never collapsing to a value computed
// without the outer query binding, while the data-aware admission still runs. A column
// read folds to nothing, so a comparison is recursed into and only its value side
// collapses; anything else is left as is.
func substituteScalarRefs(v ir.Value, ctx graphCtx) ir.Value {
	if c := graphValue(v, ctx); c != nil {
		if t := ir.TypeOf(v); t == nil || t == ir.Invalid || graphMemberAdmits(ctx, t, c) {
			if lit := graphConstantToValue(c); lit != nil {
				return lit
			}
		}
	}
	switch n := v.(type) {
	case *ir.Call:
		out := *n
		out.Receiver = substituteScalarRefs(n.Receiver, ctx)
		out.Args = make([]ir.Value, len(n.Args))
		for i, a := range n.Args {
			out.Args[i] = substituteScalarRefs(a, ctx)
		}
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

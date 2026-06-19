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

// relationOwnsUserMethod reports whether the relation value's static type declares its
// own body-bearing method of the call's name — a relation alias (type CardRel =
// relation<M> impl {...}) that overrides a built-in name (count, sum, where) or adds a
// new one. The checker resolves such a call to the override, so the built-in relation
// method must step aside and let ordinary user-method dispatch run it; a plain relation,
// whose static type owns no such method, keeps the built-in. The relation receiver does
// not carry a resolved method on the call node, so the override is found by its name on
// the receiver's definition rather than read off the call.
func relationOwnsUserMethod(ctx graphCtx, v *ir.Call, recv *ir.Constant) bool {
	def := graphReceiverDef(ctx, v.Receiver, recv)
	if def == nil {
		return false
	}
	return len(bodyMethods(ctx.env.Registry(), def, v.Method)) > 0
}

// graphRelationMethod folds a built-in method whose receiver is a relation value,
// reporting handled=false for one it does not own (a user method on a relation alias,
// which the caller dispatches the ordinary way). A where narrows the chain to a new
// relation, folding the captured scalars its predicate compares against now — at the
// where, where the locals are in scope — so a later reassignment of a captured let
// does not change this relation. A limit caps it to a new relation likewise, folding
// its row-count argument now. A count, sum, or to_list hands the chain to the data
// layer's folder, which runs it against the loaded rows (an aggregate value, or the
// materialized rows for to_list); the folder declines (nil) when it cannot run the
// query — a different master, an unsupported predicate, no rows loaded — so the result
// is left unfolded and a check over it fails safe.
func graphRelationMethod(v *ir.Call, recv *ir.Constant, ctx graphCtx) (*ir.Constant, bool) {
	switch v.Method {
	case "where":
		if len(v.Args) != 1 {
			return nil, true
		}
		out := *v
		out.Receiver = recv.Relation
		out.Args = []ir.Value{substituteWhereScalars(v.Args[0], ctx)}
		return ir.RelationConstant(&out), true
	case "limit":
		if len(v.Args) != 1 {
			return nil, true
		}
		out := *v
		out.Receiver = recv.Relation
		out.Args = []ir.Value{foldScalarArg(v.Args[0], ctx)}
		return ir.RelationConstant(&out), true
	case "count", "sum", "to_list":
		rf, ok := ctx.env.(RelationFolder)
		if !ok {
			return nil, true
		}
		out := *v
		out.Receiver = recv.Relation
		c, _ := rf.FoldRelationAggregate(&out)
		return c, true
	default:
		return nil, false
	}
}

// foldScalarArg folds a relation method's scalar argument — a limit's row cap — to the
// literal the driver binds, so the chain the folder reads is self-contained: a captured
// cap (limit(maxRows)) resolves to its value here, where the locals are in scope. The
// fold is admitted only when the value inhabits the argument's own type, the same guard
// substituteScalarRefs uses: a data-dependent refined conversion the rows violate
// (limit(Positive(n)) for n = 0) folds without checking through graphConvert, so without
// this it would render a literal a refused conversion produced. An argument that does
// not fold to an admissible renderable scalar is left whole, so the driver declines the
// query and a check over it fails safe.
func foldScalarArg(arg ir.Value, ctx graphCtx) ir.Value {
	if c := graphValue(arg, ctx); c != nil {
		if t := ir.TypeOf(arg); t == nil || t == ir.Invalid || graphMemberAdmits(ctx, t, c) {
			if lit := graphConstantToValue(c); lit != nil {
				return lit
			}
		}
	}
	return arg
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

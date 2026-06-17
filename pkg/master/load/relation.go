// This file folds a master's relation queries against its loaded rows — the
// data-aware step belt-core eval cannot take, since it has no rows and may not
// import the query driver (the layer boundary, §1). A per-table validate check
// may compose a master's static fn whose body queries the relation
// (average_cost: a where-narrowed sum over a count), so before the check folds,
// driveRelations rewrites it: every relation query and master static-fn call in
// it is replaced by the constant it folds to against the rows, leaving the
// arithmetic and comparisons around them for the evaluator. The query driver
// (pkg/master/sql) recognizes a count/sum chain and the SQLite engine
// (pkg/master/sqlite) runs it, so this file is only the glue — the recursion
// that finds the relation pieces, inlines a let-bound relation back into the
// chain, and hands each piece to the driver.

package load

import (
	"math/big"

	"github.com/masterbelt/masterbelt/pkg/belt/eval"
	mastersql "github.com/masterbelt/masterbelt/pkg/master/sql"
	"github.com/masterbelt/masterbelt/pkg/master/sqlite"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// driveRelations rewrites v, replacing every relation query (a count()/sum() over
// the master's relation) and every master static-fn call in it with the constant
// it folds to against the loaded rows, so the surrounding arithmetic and
// comparisons fold through the ordinary evaluator afterward. locals carries the
// relation a let in scope binds (matching = self.where(...)), so a query reached
// through that local (matching.count()) is inlined back to the full chain before
// the driver sees it. unsupported collects a query the driver cannot express; the
// caller leaves such a check unfolded (its fail-safe), never a wrong fold.
func driveRelations(v ir.Value, locals map[string]ir.Value, eng *sqlite.Engine, env eval.GraphEnv) (ir.Value, []mastersql.Unsupported) {
	if val, unsupported, ok := foldRelationChain(v, locals, eng, env); ok {
		return val, unsupported
	}
	if sc, ok := v.(*ir.StaticCall); ok && sc.Def != nil && sc.Def.Master != nil {
		return foldMasterStatic(sc, eng, env)
	}
	switch v := v.(type) {
	case *ir.Call:
		recv, u1 := driveRelations(v.Receiver, locals, eng, env)
		args, u2 := driveArgs(v.Args, locals, eng, env)
		return &ir.Call{Receiver: recv, Method: v.Method, Args: args, Setter: v.Setter, Resolved: v.Resolved, Subst: v.Subst, Type: v.Type, Syntax: v.Syntax}, append(u1, u2...)
	case *ir.Conversion:
		args, u := driveArgs(v.Args, locals, eng, env)
		return &ir.Conversion{Type: v.Type, Args: args, Syntax: v.Syntax}, u
	case *ir.Adapt:
		inner, u := driveRelations(v.Value, locals, eng, env)
		return &ir.Adapt{Value: inner, To: v.To}, u
	default:
		// A leaf (a literal, a reference, a bare relation count): nothing to drive,
		// folded by the evaluator as before.
		return v, nil
	}
}

// driveArgs drives each value in an argument list, collecting the unsupported
// entries.
func driveArgs(args []ir.Value, locals map[string]ir.Value, eng *sqlite.Engine, env eval.GraphEnv) ([]ir.Value, []mastersql.Unsupported) {
	if args == nil {
		return nil, nil
	}
	out := make([]ir.Value, len(args))
	var unsupported []mastersql.Unsupported
	for i, a := range args {
		var u []mastersql.Unsupported
		out[i], u = driveRelations(a, locals, eng, env)
		unsupported = append(unsupported, u...)
	}
	return out, unsupported
}

// foldRelationChain folds a count()/sum() relation query to the constant it
// yields over the loaded rows: it inlines any let-bound relation back into the
// chain (so matching.count() reads as self.where(...).count()), hands the
// self-contained chain to the driver, and runs the result against the engine. ok
// is false when v is not such a query, so the caller takes its other arms.
func foldRelationChain(v ir.Value, locals map[string]ir.Value, eng *sqlite.Engine, env eval.GraphEnv) (ir.Value, []mastersql.Unsupported, bool) {
	chain := inlineSpine(v, locals)
	if rel, _, unsupported, ok := mastersql.CountRelation(chain, env); ok {
		if len(unsupported) > 0 {
			return nil, unsupported, true
		}
		n, err := eng.Count(rel)
		if err != nil {
			return nil, nil, false
		}
		return intResult(big.NewInt(n)), nil, true
	}
	if rel, col, _, unsupported, ok := mastersql.SumRelation(chain, env); ok {
		if len(unsupported) > 0 {
			return nil, unsupported, true
		}
		total, err := eng.Sum(rel, col)
		if err != nil {
			return nil, nil, false
		}
		return intResult(total), nil, true
	}
	return nil, nil, false
}

// intResult renders a driven relation result (a count or sum, always an integer)
// as the literal value node the evaluator folds back to it, so it flows into the
// surrounding arithmetic as a plain constant. The decimal text round-trips through
// the literal parser, negative totals included.
func intResult(n *big.Int) ir.Value {
	return &ir.IntLiteral{Text: n.String()}
}

// inlineSpine rebuilds a relation chain's receiver spine with each let-bound
// relation inlined — a LocalRef the chain narrows through (matching in
// matching.count()) is replaced by the relation it binds (self.where(...)), so the
// driver, which walks only where over the master relation and does not follow a
// local, sees the whole chain. Only the spine is rebuilt: the where predicates and
// the sum selector ride along untouched (the driver folds their operands). A value
// that is not part of a chain is returned unchanged.
func inlineSpine(v ir.Value, locals map[string]ir.Value) ir.Value {
	switch v := v.(type) {
	case *ir.Call:
		return &ir.Call{Receiver: inlineSpine(v.Receiver, locals), Method: v.Method, Args: v.Args, Setter: v.Setter, Resolved: v.Resolved, Subst: v.Subst, Type: v.Type, Syntax: v.Syntax}
	case *ir.LocalRef:
		if rv, ok := locals[v.Name]; ok {
			return inlineSpine(rv, locals)
		}
		return v
	case *ir.Adapt:
		return inlineSpine(v.Value, locals)
	default:
		return v
	}
}

// foldMasterStatic folds a master static-fn call (average_cost()) by driving the
// relation queries in its body against the rows: its let-bound relations become
// the local scope a use of one inlines through, and its returned expression is
// driven and handed back for the evaluator to fold (the arithmetic over the driven
// counts and sums). A body shaped other than lets-then-return is left undriven
// (returned as the call), so the check folds it the ordinary way (and fails safe).
func foldMasterStatic(sc *ir.StaticCall, eng *sqlite.Engine, env eval.GraphEnv) (ir.Value, []mastersql.Unsupported) {
	m := staticMethod(sc)
	if m == nil {
		return sc, nil
	}
	locals := map[string]ir.Value{}
	var ret ir.Value
	for _, stmt := range m.Body {
		switch s := stmt.(type) {
		case *ir.Let:
			locals[s.Name] = s.Value
		case *ir.Return:
			ret = s.Value
		default:
			return sc, nil // an unsupported body shape: leave it to the ordinary fold
		}
	}
	if ret == nil {
		return sc, nil
	}
	return driveRelations(ret, locals, eng, env)
}

// staticMethod returns the static fn a master static call resolves to: the
// checker's selection when one was recorded, else the master's static fn of the
// call's name and arity.
func staticMethod(sc *ir.StaticCall) *ir.Method {
	if sc.Resolved != nil {
		return sc.Resolved
	}
	for _, m := range sc.Def.Methods {
		if m.Kind == ir.MethodStatic && m.Name == sc.Name && len(m.Params) == len(sc.Args) {
			return m
		}
	}
	return nil
}

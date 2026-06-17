// This file folds a master's relation queries against its loaded rows — the
// data-aware step the belt evaluator cannot take, since it has no rows and the
// one-way layer rule keeps it from reaching the query driver. A per-table
// validate check may compose a master static fn whose body queries the relation
// (average_cost: a where-narrowed sum over a count), so before the check folds,
// driveRelations rewrites it: every relation query and master static-fn call in
// it is replaced by the constant it folds to against the rows, leaving the
// arithmetic and comparisons around them for the evaluator. The query driver
// (pkg/master/sql) recognizes a count/sum chain and the SQLite engine
// (pkg/master/sqlite) runs it, so this file is only the glue — the recursion
// that finds the relation pieces, inlines a let-bound relation back into the
// chain, and hands each piece to the driver, while leaving anything it cannot
// drive untouched (the check then folds it the ordinary way, failing safe).

package load

import (
	"math/big"

	"github.com/masterbelt/masterbelt/pkg/belt/eval"
	"github.com/masterbelt/masterbelt/pkg/belt/types"
	mastersql "github.com/masterbelt/masterbelt/pkg/master/sql"
	"github.com/masterbelt/masterbelt/pkg/master/sqlite"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// relationFold drives the relation queries of one master's per-table checks: the
// engine its rows are loaded into (nil when the table failed to load, so only an
// unfiltered count — which needs no cells — still folds), the loaded row count for
// that unfiltered case, the master the check is on (a query over another master is
// not run against this engine), and the fold environment that resolves a where
// predicate's named constants.
type relationFold struct {
	eng    *sqlite.Engine
	rows   int64
	master *ir.TypeDef
	env    eval.GraphEnv
}

// drive rewrites v, replacing every relation query (a count()/sum() over the
// master's relation) and every master static-fn call in it with the constant it
// folds to against the loaded rows, so the surrounding arithmetic and comparison
// fold through the ordinary evaluator afterward. locals carries the relation a let
// in scope binds (matching = self.where(...)), so a query reached through that
// local is inlined back to the full chain before the driver sees it. A node it
// cannot drive is returned with its children driven (or unchanged for a leaf), so
// the evaluator still sees the rest; unsupported collects a query the driver cannot
// express, which the caller treats as a rejection (the check stays undriven).
func (f relationFold) drive(v ir.Value, locals map[string]ir.Value) (ir.Value, []mastersql.Unsupported) {
	if val, unsupported, ok := f.chain(v, locals); ok {
		return val, unsupported
	}
	if sc, ok := v.(*ir.StaticCall); ok && sc.Def != nil && sc.Def.Master != nil {
		return f.static(sc)
	}
	switch v := v.(type) {
	case *ir.Call:
		recv, u1 := f.drive(v.Receiver, locals)
		args, u2 := f.driveArgs(v.Args, locals)
		return &ir.Call{Receiver: recv, Method: v.Method, Args: args, Setter: v.Setter, Resolved: v.Resolved, Subst: v.Subst, Type: v.Type, Syntax: v.Syntax}, append(u1, u2...)
	case *ir.FuncCall:
		args, u := f.driveArgs(v.Args, locals)
		return &ir.FuncCall{Target: v.Target, Args: args, Resolved: v.Resolved, Subst: v.Subst, Type: v.Type, Syntax: v.Syntax}, u
	case *ir.Apply:
		callee, u1 := f.drive(v.Callee, locals)
		args, u2 := f.driveArgs(v.Args, locals)
		return &ir.Apply{Callee: callee, Args: args, Type: v.Type, Syntax: v.Syntax}, append(u1, u2...)
	case *ir.Conversion:
		args, u := f.driveArgs(v.Args, locals)
		return &ir.Conversion{Type: v.Type, Args: args, Syntax: v.Syntax}, u
	case *ir.Adapt:
		inner, u := f.drive(v.Value, locals)
		return &ir.Adapt{Value: inner, To: v.To}, u
	default:
		// A leaf (a literal, a reference, a bare relation count): nothing to drive,
		// folded by the evaluator as before.
		return v, nil
	}
}

// driveArgs drives each value in an argument list, collecting the unsupported
// entries.
func (f relationFold) driveArgs(args []ir.Value, locals map[string]ir.Value) ([]ir.Value, []mastersql.Unsupported) {
	if args == nil {
		return nil, nil
	}
	out := make([]ir.Value, len(args))
	var unsupported []mastersql.Unsupported
	for i, a := range args {
		var u []mastersql.Unsupported
		out[i], u = f.drive(a, locals)
		unsupported = append(unsupported, u...)
	}
	return out, unsupported
}

// chain folds a count()/sum() relation query to the constant it yields over the
// loaded rows: it inlines any let-bound relation back into the chain, hands the
// self-contained chain to the driver, and runs the result against the engine. A
// query over a different master than this check's is left undriven (ok stays false,
// so the bare relation reaches the evaluator and the check fails safe) — the engine
// holds only this master's rows. An unfiltered count needs no cells, so it reads the
// row count directly, the way the bare count check does; a filtered query needs the
// engine, so it is undriven when the table did not load. ok is false when v is not
// such a query.
func (f relationFold) chain(v ir.Value, locals map[string]ir.Value) (ir.Value, []mastersql.Unsupported, bool) {
	chain := inlineSpine(v, locals)
	if rel, m, unsupported, ok := mastersql.CountRelation(chain, f.env); ok {
		return f.count(rel, m, chain, unsupported)
	}
	if rel, col, m, unsupported, ok := mastersql.SumRelation(chain, f.env); ok {
		return f.sum(rel, col, m, unsupported)
	}
	return nil, nil, false
}

// count runs a recognized count query: a query over another master is not driven
// against this engine; an unsupported predicate is a rejection; an unfiltered count
// reads the row count (no cells); a filtered count needs the engine.
func (f relationFold) count(rel mastersql.Relation, m *ir.TypeDef, chain ir.Value, unsupported []mastersql.Unsupported) (ir.Value, []mastersql.Unsupported, bool) {
	if m != f.master {
		return nil, nil, false
	}
	if len(unsupported) > 0 {
		return nil, unsupported, true
	}
	if isBareCount(chain) {
		return intResult(big.NewInt(f.rows)), nil, true
	}
	if f.eng == nil {
		return nil, nil, false
	}
	n, err := f.eng.Count(rel)
	if err != nil {
		return nil, nil, false
	}
	return intResult(big.NewInt(n)), nil, true
}

// sum runs a recognized sum query against the engine, with the same cross-master,
// unsupported, and unloaded-table guards count uses.
func (f relationFold) sum(rel mastersql.Relation, col string, m *ir.TypeDef, unsupported []mastersql.Unsupported) (ir.Value, []mastersql.Unsupported, bool) {
	if m != f.master {
		return nil, nil, false
	}
	if len(unsupported) > 0 {
		return nil, unsupported, true
	}
	if f.eng == nil {
		return nil, nil, false
	}
	total, err := f.eng.Sum(rel, col)
	if err != nil {
		return nil, nil, false
	}
	return intResult(total), nil, true
}

// static folds a master static-fn call (average_cost()) by driving the relation
// queries in its body against the rows. It is conservative: only a parameterless fn
// whose body is relation-bound lets then a single return is driven, and only when
// the driven result inhabits the fn's declared result type — anything else is left
// as the call for the evaluator to fold (or fail) the ordinary way, so a scalar fn,
// a parameter substitution (a later slice), or an overflowing narrowed result is
// never turned into a wrong constant.
func (f relationFold) static(sc *ir.StaticCall) (ir.Value, []mastersql.Unsupported) {
	if len(sc.Args) > 0 {
		return sc, nil // a parameterized static fn: parameter substitution is a later slice
	}
	m := staticMethod(sc)
	if m == nil || !bodyQueriesRelation(m.Body) {
		return sc, nil // nothing relational to drive: the evaluator folds it as before
	}
	locals := map[string]ir.Value{}
	var ret ir.Value
	for _, stmt := range m.Body {
		switch s := stmt.(type) {
		case *ir.Let:
			if !isRelationValue(s.Value, locals) {
				return sc, nil // a scalar let needs the evaluator's binding; leave the whole call to it
			}
			locals[s.Name] = s.Value
		case *ir.Return:
			ret = s.Value
		default:
			return sc, nil
		}
		if ret != nil {
			break // the first return decides the result; later statements never run
		}
	}
	if ret == nil {
		return sc, nil
	}
	driven, unsupported := f.drive(ret, locals)
	if len(unsupported) > 0 {
		return sc, unsupported
	}
	if !f.resultFits(driven, m, sc) {
		return sc, nil // the count/sum overflows the declared result: leave it to fail safe
	}
	return driven, nil
}

// resultFits reports whether the driven result inhabits the static fn's declared
// result type — a count or sum that exceeds a narrowed result (short, a refined
// type) makes the call ill, so it must not be passed off as an in-range value. The
// arbitrary-precision result (nint) holds any count, so it always fits; a result
// the evaluator cannot fold here is treated as not fitting (left to the ordinary
// fold).
func (f relationFold) resultFits(driven ir.Value, m *ir.Method, sc *ir.StaticCall) bool {
	want := m.Result
	if len(sc.Subst) > 0 {
		want = types.Substitute(want, sc.Subst)
	}
	if want == nil || want == ir.Invalid {
		return true
	}
	c := eval.Graph(driven, f.env)
	if c == nil {
		return false
	}
	if c.Kind == ir.ConstInt {
		return types.Fits(f.env.Registry(), want, c.Int)
	}
	return true
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

// isBareCount reports whether a (spine-inlined) chain is count() directly over the
// master relation, with no where narrowing — the count of every row, which the row
// count answers without reading a cell. A narrowed count (where(...).count()) is not
// bare and needs the engine.
func isBareCount(chain ir.Value) bool {
	count, ok := chain.(*ir.Call)
	if !ok || count.Method != "count" {
		return false
	}
	_, ok = stripAdapt(count.Receiver).(*ir.MasterRelation)
	return ok
}

// isRelationValue reports whether v is the master's relation — a where narrowing
// over it, or a let that binds one (followed through locals) — so a let binding it
// can be inlined into a chain. A scalar value (a plain int, a non-relation call) is
// not, so a let binding one is left to the evaluator.
func isRelationValue(v ir.Value, locals map[string]ir.Value) bool {
	switch v := stripAdapt(v).(type) {
	case *ir.MasterRelation:
		return true
	case *ir.Call:
		return v.Method == "where" && isRelationValue(v.Receiver, locals)
	case *ir.LocalRef:
		if rv, ok := locals[v.Name]; ok {
			return isRelationValue(rv, locals)
		}
	}
	return false
}

// bodyQueriesRelation reports whether a static fn body holds a master relation
// anywhere — the signal that it is worth driving. A body with none is folded by the
// evaluator unchanged, so a plain static fn keeps its ordinary fold.
func bodyQueriesRelation(body []ir.Stmt) bool {
	found := false
	ir.WalkBody(body, func(v ir.Value) bool {
		if _, ok := v.(*ir.MasterRelation); ok {
			found = true
		}
		return true
	})
	return found
}

// stripAdapt peels the implicit-adaption wrappers off a value, the way the driver's
// own walk does, so a chain or relation reads through them.
func stripAdapt(v ir.Value) ir.Value {
	for {
		a, ok := v.(*ir.Adapt)
		if !ok {
			return v
		}
		v = a.Value
	}
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

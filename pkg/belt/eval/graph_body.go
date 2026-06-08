// This file runs a lowered statement body ([]ir.Stmt) to its returned value —
// the IR interpreter's execution half, the graph twin of evalBody. A let binds
// a block-scoped mutable local (its resolved Type the initializer's
// expectation channel), an assignment rebinds one (a property write through
// its setter), a switch dispatches on folded equality, a match on the
// scrutinee's member, an if on its condition, a for over a folded collection
// or range. The body folds only when its dispatch is fully determined, the
// soundness-over-completeness rule both folders share.

package eval

import (
	"fmt"
	"math/big"

	"github.com/masterbelt/masterbelt/pkg/belt/types"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// graphApply folds a function-value constant against the given arguments over
// its lowered IR body — the application path of every closure: the parameters
// bind over the captured environment, and the body runs to its return.
func graphApply(ctx graphCtx, fn *ir.Constant, args []*ir.Constant) *ir.Constant {
	if fn.Fn == nil || len(args) != len(fn.Fn.Params) {
		return nil
	}
	if ctx.depth >= maxApplyDepth {
		ctx.noteBudget()
		return nil
	}
	locals := make(map[string]*ir.Constant, len(fn.Captured)+len(args))
	for k, v := range fn.Captured {
		locals[k] = v
	}
	for i, p := range fn.Fn.Params {
		locals[p] = args[i]
	}
	// A literal's solved signature is its parameter/result channel when the
	// graph was annotated; the body otherwise folds without one.
	var resultType ir.Type
	if sig, ok := fn.Fn.Type.(*ir.Func); ok {
		resultType = sig.Result
		for i, p := range fn.Fn.Params {
			if i < len(sig.Params) && sig.Params[i] != nil {
				if tag := graphUnionTagValue(ctx, locals[p], sig.Params[i]); tag != nil {
					locals[p] = ir.Tagged(locals[p], tag)
				}
			}
		}
	}
	return graphBody(fn.Fn.Body, graphCtx{
		env: ctx.env, locals: locals, depth: ctx.depth + 1, budgetHit: ctx.budgetHit,
		resultColl: CollKindOf(resultType), resultType: resultType,
		// A closure captures its defining environment's type parameters, so a
		// match in its body folds under the same substitution as the routine the
		// literal was written in (the T of the enclosing generic function).
		subst: ctx.subst,
	})
}

// graphBody runs a statement body to its returned value, or nil when no path
// reaches a return. The local environment is mutated in place so a nested
// block's assignment reaches an outer local; block scoping is restored on
// return (graphBlockScope), so a shadowing let does not leak.
func graphBody(body []ir.Stmt, ctx graphCtx) *ir.Constant {
	scope := newGraphScope(ctx.locals)
	defer scope.restore()
	v, out := graphStmts(body, ctx, scope)
	if out == graphReturned {
		return v
	}
	return nil
}

// graphOutcome classifies how a statement (or a block) ended, the shared
// three-way verdict the AST folder's if/switch/match outcomes all reduce to.
type graphOutcome int

const (
	graphUnknown     graphOutcome = iota // could not be folded: stop the enclosing fold
	graphReturned                        // returned a folded value
	graphFellThrough                     // ran to the end without returning
)

// graphBranch runs a branch body in its own block scope and classifies how it
// ended — the graph twin of branchOutcome.
func graphBranch(body []ir.Stmt, ctx graphCtx) (*ir.Constant, graphOutcome) {
	scope := newGraphScope(ctx.locals)
	defer scope.restore()
	return graphStmts(body, ctx, scope)
}

// graphStmts runs a statement sequence within the given block scope.
func graphStmts(body []ir.Stmt, ctx graphCtx, scope *graphScope) (*ir.Constant, graphOutcome) {
	for _, stmt := range body {
		switch s := stmt.(type) {
		case *ir.Return:
			return graphReturn(s, ctx)
		case *ir.ExprStmt:
			// A bare expression yields no binding and cannot return.
			continue
		case *ir.Let:
			if !graphLet(s, ctx, scope) {
				return nil, graphUnknown
			}
		case *ir.Assign:
			if !graphAssign(s, ctx) {
				return nil, graphUnknown
			}
		case *ir.Switch:
			v, out := graphSwitch(s, ctx)
			if out == graphFellThrough {
				continue
			}
			return v, out
		case *ir.Match:
			v, out := graphMatch(s, ctx)
			if out == graphFellThrough {
				continue
			}
			return v, out
		case *ir.If:
			v, out := graphIf(s, ctx)
			if out == graphFellThrough {
				continue
			}
			return v, out
		case *ir.For:
			v, out := graphFor(s, ctx)
			if out == graphFellThrough {
				continue
			}
			return v, out
		default:
			panic(unhandledGraphStmt(stmt))
		}
	}
	return nil, graphFellThrough
}

// graphReturn folds a return statement's value under the body's result
// expectation channels.
func graphReturn(s *ir.Return, ctx graphCtx) (*ir.Constant, graphOutcome) {
	if s.Value == nil {
		return nil, graphUnknown
	}
	retCtx := ctx
	retCtx.expectedColl = ctx.resultColl
	retCtx.expectedType = ctx.resultType
	if v := graphValue(s.Value, retCtx); v != nil {
		return v, graphReturned
	}
	return nil, graphUnknown
}

// graphLet folds a let binding under its annotation's expectation channel and
// binds the local into the block scope; false means the body cannot fold.
func graphLet(s *ir.Let, ctx graphCtx, scope *graphScope) bool {
	if s.Value == nil {
		return false
	}
	letCtx := graphExpectingType(ctx, s.Type)
	v := graphValue(s.Value, letCtx)
	return v != nil && s.Name != "" && scope.bind(s.Name, v)
}

// graphAssign folds an assignment: a property write (the synthetic setter
// call) applies the setter with self bound to the local's current value and
// rebinds the local to the result; a plain reassignment folds the value (the
// local's settled type its expectation channel, read from the introducing Let
// — the graph carries it on the LocalRef nodes, but the assignment's own
// channel is its value node's Adapt when annotated, so the bare fold
// suffices) and rebinds in place.
func graphAssign(s *ir.Assign, ctx graphCtx) bool {
	if ctx.locals == nil || s.Name == "" || s.Value == nil {
		return false
	}
	cur, inScope := ctx.locals[s.Name]
	if !inScope {
		return false
	}
	if call, ok := s.Value.(*ir.Call); ok && call.Setter {
		return graphSetterAssign(s.Name, call, cur, ctx)
	}
	v := graphValue(s.Value, ctx)
	if v == nil {
		return false
	}
	ctx.locals[s.Name] = v
	return true
}

// graphSetterAssign folds a property write p.name = v through its setter: the
// new value folds, the setter's body applies with self bound to the local's
// current value, and the local rebinds to the result.
func graphSetterAssign(name string, call *ir.Call, cur *ir.Constant, ctx graphCtx) bool {
	if cur == nil || len(call.Args) != 1 {
		return false
	}
	def := graphReceiverDef(ctx, call.Receiver, cur)
	if def == nil {
		return false
	}
	sel := bodyAccessor(ctx.env.Registry(), def, call.Method, ir.MethodSetter)
	if sel == nil {
		return false
	}
	if ctx.depth >= maxApplyDepth {
		ctx.noteBudget()
		return false
	}
	v := graphValue(call.Args[0], ctx)
	if v == nil {
		return false
	}
	next := graphApplyBody(graphMethodCallable(sel, def), cur, []*ir.Constant{v}, call.Subst, ctx)
	if next == nil {
		return false
	}
	ctx.locals[name] = next
	return true
}

// graphIf folds an if statement: the condition decides the taken branch, a
// false guard with no else falls through.
func graphIf(s *ir.If, ctx graphCtx) (*ir.Constant, graphOutcome) {
	cond := graphValue(s.Cond, ctx)
	if cond == nil || cond.Kind != ir.ConstBool {
		return nil, graphUnknown
	}
	if cond.Bool {
		return graphBranch(s.Then, ctx)
	}
	switch {
	case s.ElseIf != nil:
		return graphIf(s.ElseIf, ctx)
	case s.Else != nil:
		return graphBranch(s.Else, ctx)
	default:
		return nil, graphFellThrough
	}
}

// graphSwitch selects and runs the matching arm of a switch by folded
// equality, the wildcard last; an unfoldable scrutinee or pattern, or no
// matching arm, leaves the dispatch undetermined.
func graphSwitch(s *ir.Switch, ctx graphCtx) (*ir.Constant, graphOutcome) {
	scrut := graphValue(s.Scrutinee, ctx)
	if scrut == nil {
		return nil, graphUnknown
	}
	for _, arm := range s.Arms {
		for _, pat := range arm.Values {
			cv := graphValue(pat, ctx)
			if cv == nil {
				return nil, graphUnknown
			}
			if ir.ConstantsEqual(scrut, cv) {
				return graphBranch(arm.Body, ctx)
			}
		}
	}
	if s.Else != nil {
		return graphBranch(s.Else, ctx)
	}
	return nil, graphUnknown
}

// substArmType resolves a match arm's type through the substitution the body
// folds under — the checker-settled T = nint of the enclosing generic call — so
// a type-variable arm (the T arm of an optional<T> scrutinee) becomes the
// concrete type the dispatch can decide. Without a substitution (a non-generic
// body) the arm type is returned unchanged, so nothing else's folding shifts.
func (ctx graphCtx) substArmType(t ir.Type) ir.Type {
	if len(ctx.subst) == 0 {
		return t
	}
	return types.Substitute(t, ctx.subst)
}

// graphMatch selects and runs the matching arm of a match: a tagged scrutinee
// dispatches confidently on its member tag; an untagged one folds only when
// exactly one arm can hold the value — the soundness rule evalMatch keeps. The
// arm types are resolved on the arms themselves (ir.MatchArm.Type), so no
// syntactic channel is needed.
func graphMatch(m *ir.Match, ctx graphCtx) (*ir.Constant, graphOutcome) {
	scrut := graphValue(m.Scrutinee, ctx)
	if scrut == nil {
		return nil, graphUnknown
	}
	if scrut.UnionTag != nil {
		for _, arm := range m.Arms {
			armType := ctx.substArmType(arm.Type)
			if armType == nil || armType == ir.Invalid || types.HasTypeVar(armType) {
				// An unresolved arm, or one still generic after the body's
				// substitution (a free variable no call pinned): which values it
				// matches depends on the instantiation, so the dispatch order is
				// undecidable — running the wildcard instead would fold the wrong
				// arm.
				return nil, graphUnknown
			}
			if tagMatchesType(scrut.UnionTag, normalizeBuiltin(armType)) {
				return graphBranch(arm.Body, narrowGraphBinding(ctx, arm.Name, ir.Untagged(scrut)))
			}
		}
		if m.Else != nil {
			return graphBranch(m.Else, ctx)
		}
		return nil, graphUnknown
	}
	selected := -1
	for i, arm := range m.Arms {
		matched, certain := graphMatchesArm(ctx, scrut, ctx.substArmType(arm.Type))
		if !certain {
			return nil, graphUnknown
		}
		if matched {
			if selected != -1 {
				return nil, graphUnknown
			}
			selected = i
		}
	}
	if selected != -1 {
		arm := m.Arms[selected]
		return graphBranch(arm.Body, narrowGraphBinding(ctx, arm.Name, scrut))
	}
	if m.Else != nil {
		return graphBranch(m.Else, ctx)
	}
	return nil, graphUnknown
}

// graphMatchesArm reports whether a folded scrutinee is of a match arm's
// resolved member type, and whether that could be decided — constMatchesArm
// over the arm's resolved ir.Type.
func graphMatchesArm(ctx graphCtx, scrut *ir.Constant, armType ir.Type) (matched, certain bool) {
	switch t := armType.(type) {
	case *ir.Builtin:
		return scalarMatchesBuiltin(ctx.env.Registry(), scrut, t.Name), true
	case *ir.Named:
		if t.Def == nil {
			return false, false
		}
		if t.Def.Builtin {
			return scalarMatchesBuiltin(ctx.env.Registry(), scrut, t.Def.Name), true
		}
		if t.Def.Enum != nil {
			return scrut.Kind == ir.ConstEnum && scrut.EnumDef == t.Def, true
		}
		if underlyingPrimitive(ctx.env.Registry(), t.Def, map[*ir.TypeDef]bool{}) != nil {
			return defBacksKind(ctx.env.Registry(), t.Def, scrut.Kind), true
		}
		return false, false
	default:
		return false, false
	}
}

// narrowGraphBinding binds a match arm's binding name to the scrutinee value
// for the arm body, in a copied environment so the binding reaches only this
// arm.
func narrowGraphBinding(ctx graphCtx, name string, scrut *ir.Constant) graphCtx {
	if name == "" {
		return ctx
	}
	locals := make(map[string]*ir.Constant, len(ctx.locals)+1)
	for k, v := range ctx.locals {
		locals[k] = v
	}
	locals[name] = scrut
	ctx.locals = locals
	return ctx
}

// graphFor folds a for statement over a folded collection or range, binding
// the loop variable per iteration — evalFor's graph twin.
func graphFor(s *ir.For, ctx graphCtx) (*ir.Constant, graphOutcome) {
	if s.Iter == nil {
		return nil, graphUnknown
	}
	iter := graphValue(s.Iter, ctx)
	if iter == nil {
		return nil, graphUnknown
	}
	switch iter.Kind {
	case ir.ConstCollection:
		return graphForCollection(s, iter, ctx)
	case ir.ConstRange:
		return graphForRange(s, iter, ctx)
	default:
		return nil, graphUnknown
	}
}

// graphForCollection runs the loop body over a folded collection's entries:
// the value for an of-loop, the key (a list's 0-based index) for an in-loop.
func graphForCollection(s *ir.For, iter *ir.Constant, ctx graphCtx) (*ir.Constant, graphOutcome) {
	for i, entry := range iter.Coll {
		elem := entry.Value
		if !s.Of {
			elem = entry.Key
			if elem == nil {
				elem = ir.IntConstant(big.NewInt(int64(i)))
			}
		}
		v, out := graphIteration(s, elem, ctx)
		switch out {
		case graphFellThrough:
			continue
		case graphReturned:
			return v, graphReturned
		default:
			return nil, graphUnknown
		}
	}
	return nil, graphFellThrough
}

// graphForRange runs the loop body over a folded range's elements — the
// element for an of-loop, the 0-based position for an in-loop — under the
// compile-time iteration bound.
func graphForRange(s *ir.For, iter *ir.Constant, ctx graphCtx) (*ir.Constant, graphOutcome) {
	count, ok := rangeCount(iter)
	if !ok {
		return nil, graphUnknown
	}
	if count.Sign() <= 0 {
		return nil, graphFellThrough
	}
	if count.Cmp(big.NewInt(maxRangeIterations)) > 0 {
		ctx.noteBudget()
		return nil, graphUnknown
	}
	for i := range count.Int64() {
		elem := ir.IntConstant(rangeElement(iter, i))
		if !s.Of {
			elem = ir.IntConstant(big.NewInt(i))
		}
		v, out := graphIteration(s, elem, ctx)
		switch out {
		case graphFellThrough:
			continue
		case graphReturned:
			return v, graphReturned
		default:
			return nil, graphUnknown
		}
	}
	return nil, graphFellThrough
}

// graphIteration runs one for-iteration with the loop variable bound in a
// fresh block scope, so it does not leak past the iteration while an
// assignment to an outer local persists.
func graphIteration(s *ir.For, elem *ir.Constant, ctx graphCtx) (*ir.Constant, graphOutcome) {
	scope := newGraphScope(ctx.locals)
	defer scope.restore()
	if s.Var != "" && elem != nil {
		scope.bind(s.Var, elem)
	}
	return graphStmts(s.Body, ctx, scope)
}

// graphScope records the let bindings a block introduces so they can be undone
// when the block ends — blockScope without the AST folder's static-def
// tracking (the graph carries its types on the nodes).
type graphScope struct {
	locals  map[string]*ir.Constant
	shadows map[string]*ir.Constant
	added   map[string]bool
}

func newGraphScope(locals map[string]*ir.Constant) *graphScope {
	return &graphScope{locals: locals}
}

func (s *graphScope) bind(name string, v *ir.Constant) bool {
	if s.locals == nil {
		return false
	}
	if !s.recorded(name) {
		s.record(name)
	}
	s.locals[name] = v
	return true
}

// record notes how the scope's restore must treat name: a shadowed outer
// binding is saved for restoration, a fresh one is marked for deletion.
func (s *graphScope) record(name string) {
	if prior, ok := s.locals[name]; ok {
		if s.shadows == nil {
			s.shadows = map[string]*ir.Constant{}
		}
		s.shadows[name] = prior
		return
	}
	if s.added == nil {
		s.added = map[string]bool{}
	}
	s.added[name] = true
}

func (s *graphScope) recorded(name string) bool {
	if _, ok := s.shadows[name]; ok {
		return true
	}
	return s.added[name]
}

func (s *graphScope) restore() {
	for name, prior := range s.shadows {
		s.locals[name] = prior
	}
	for name := range s.added {
		delete(s.locals, name)
	}
}

// unhandledGraphStmt panics for a statement kind the interpreter has no case
// for — a new lowered form must be executed, never silently skipped.
func unhandledGraphStmt(s ir.Stmt) string {
	return fmt.Sprintf("eval: unhandled ir.Stmt kind %T", s)
}

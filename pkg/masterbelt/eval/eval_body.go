// This file is the compile-time execution model for a statement body: evalBody
// runs a body to its returned value, and apply folds a function-value constant by
// folding its body. The block scoping (blockScope), the let/assign/return
// folders, and the control-flow folders (if/for/switch/match) with their
// fall-through/returned/unknown outcomes live here — the same first-match
// dispatch the runtime makes, kept finite by the depth and range guards.
package eval

import (
	"maps"
	"math/big"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// apply folds a function-value constant against the given arguments: it binds the
// parameters to the arguments over the closure's captured environment and folds
// the body's return statement. A body with no return, a wrong argument count, an
// unfoldable return, or an application past the recursion guard yields nil.
func apply(ctx evalCtx, fn *ir.Constant, args []*ir.Constant) *ir.Constant {
	if fn.Fn == nil || len(args) != len(fn.Fn.Params) || ctx.depth >= maxApplyDepth {
		return nil
	}
	locals := make(map[string]*ir.Constant, len(fn.Captured)+len(args))
	maps.Copy(locals, fn.Captured)
	for i, p := range fn.Fn.Params {
		locals[p.Name] = args[i]
	}
	// A function body sees its parameters and captures, never an outer self: a
	// literal has no receiver.
	return evalBody(fn.Fn.Body, evalCtx{env: ctx.env, locals: locals, depth: ctx.depth + 1})
}

// evalBody runs a statement body to its returned value, or nil when no path
// reaches a return. It executes a switch by folding the scrutinee and running
// the first arm whose value patterns it equals (the wildcard last), and an if by
// folding the condition and running the taken branch — a guard whose condition
// is false falls through to the next statement. A let introduces a mutable
// block-local and an assignment updates one, both folding in the body's local
// environment; a bare expression statement has no value and is skipped. It is
// the compile-time execution model the const folder shares with a function
// application, kept in step with the runtime's first-match dispatch.
//
// The local environment (ctx.locals) is mutated in place, so a nested block's
// assignment reaches an outer local. Block scoping is restored on return: a
// shadowing let in this block is undone (blockScope), so its binding does not
// leak to the caller — while an assignment to an outer local persists.
func evalBody(body []ast.Stmt, ctx evalCtx) *ir.Constant {
	scope := newBlockScope(ctx.locals, ctx.localDefs)
	ctx.localDefs = scope.localDefs // share the scope's def map (it may allocate one)
	defer scope.restore()
	for _, stmt := range body {
		switch stmt := stmt.(type) {
		case *ast.ReturnStmt:
			if stmt.Value == nil {
				return nil
			}
			return evalReturn(stmt.Value, ctx)
		case *ast.LetStmt:
			if !evalLet(stmt, ctx, scope) {
				return nil // an unfoldable initializer: the body cannot fold past it
			}
		case *ast.AssignStmt:
			if !evalAssign(stmt, ctx) {
				return nil // an unfoldable value (or invalid target): cannot fold on
			}
		case *ast.SwitchStmt:
			v, out := evalSwitch(stmt, ctx)
			if out == switchFellThrough {
				continue // the selected arm ran without returning; carry on
			}
			// switchReturned yields the arm's value; switchUnknown (an
			// unfoldable scrutinee/pattern, or no arm matched) leaves v nil,
			// which stops folding here.
			return v
		case *ast.MatchStmt:
			v, out := evalMatch(stmt, ctx)
			if out == matchFellThrough {
				continue // the selected arm ran without returning; carry on
			}
			// matchReturned yields the arm's value; matchUnknown (an unfoldable
			// scrutinee, an undecidable arm, or no arm matched) leaves v nil,
			// which stops folding here.
			return v
		case *ast.IfStmt:
			v, out := evalIf(stmt, ctx)
			if out == ifFellThrough {
				continue // the taken branch (or no branch) ran without returning
			}
			// ifReturned yields the branch's value; ifUnknown (an unfoldable
			// condition or branch) leaves v nil, which stops folding here.
			return v
		case *ast.ForStmt:
			v, out := evalFor(stmt, ctx)
			if out == ifFellThrough {
				continue // the loop ran every element without returning; carry on
			}
			// ifReturned yields an early return's value; ifUnknown (an unfoldable
			// iter or body) leaves v nil, which stops folding here.
			return v
		case *ast.ExprStmt:
			// A bare expression yields no binding and cannot return, so folding
			// the body steps over it. Listed so a new statement kind hits the
			// default rather than being silently skipped here too.
			continue
		default:
			panic(ast.UnhandledStmt(stmt))
		}
	}
	return nil
}

// blockScope records the let bindings a block introduces so they can be undone
// when the block ends, restoring any outer binding they shadowed. Assignments to
// an outer local are not recorded — they mutate the shared environment and
// persist past the block, exactly as at runtime.
type blockScope struct {
	locals    map[string]*ir.Constant
	localDefs map[string]*ir.TypeDef
	shadows   map[string]*ir.Constant // the prior value of each name a let shadows
	defShadow map[string]*ir.TypeDef  // the prior static def of each shadowed name
	added     map[string]bool         // names this block's lets introduced fresh
}

// newBlockScope begins tracking a block's let bindings over the body's locals
// and their static defs. A nil locals (a body with no environment) still yields
// a usable scope whose restore is a no-op, since no let can run without an
// environment to bind into. When locals is non-nil the def map is allocated
// here if absent — so it is shared with the body ctx in place, the way the
// locals map is, and a let's def written by bind is visible to the statements
// that follow.
func newBlockScope(locals map[string]*ir.Constant, localDefs map[string]*ir.TypeDef) *blockScope {
	if locals != nil && localDefs == nil {
		localDefs = map[string]*ir.TypeDef{}
	}
	return &blockScope{locals: locals, localDefs: localDefs}
}

// bind records a let of name and writes its value (and, when its annotation
// names a nominal type, its static def) into the environment, saving what it
// shadows so restore can put it back. The environment must be non-nil (a
// function or method body always has one); a nil one means a let appeared where
// it cannot bind, which bind reports by returning false.
//
// Only the first let of a name in this block records what it shadows: a later
// rebind (two lets of the same name, illegal but tolerated) overwrites the
// value, and restore still returns the binding the block inherited.
func (s *blockScope) bind(name string, v *ir.Constant, def *ir.TypeDef) bool {
	if s.locals == nil {
		return false
	}
	if !s.recorded(name) {
		if prior, ok := s.locals[name]; ok {
			if s.shadows == nil {
				s.shadows = map[string]*ir.Constant{}
			}
			s.shadows[name] = prior
			if s.defShadow == nil {
				s.defShadow = map[string]*ir.TypeDef{}
			}
			s.defShadow[name] = s.localDefs[name] // nil when the shadowed name had no def
		} else {
			if s.added == nil {
				s.added = map[string]bool{}
			}
			s.added[name] = true
		}
	}
	s.locals[name] = v
	s.setDef(name, def)
	return true
}

// setDef records a let's static def into the local-def map, allocating it on
// first use. A nil def clears any inherited def for the name, so a binding whose
// annotation names no nominal type does not read an outer name's def.
func (s *blockScope) setDef(name string, def *ir.TypeDef) {
	if def == nil {
		delete(s.localDefs, name)
		return
	}
	if s.localDefs == nil {
		s.localDefs = map[string]*ir.TypeDef{}
	}
	s.localDefs[name] = def
}

// recorded reports whether this block already saved what a let of name shadows
// (or noted it as freshly added), so a rebind does not overwrite that record.
func (s *blockScope) recorded(name string) bool {
	if _, ok := s.shadows[name]; ok {
		return true
	}
	return s.added[name]
}

// restore undoes this block's let bindings: a shadowed outer binding (value and
// static def) is put back, and a freshly added one is removed, leaving the
// environment as the caller had it (save for assignments to outer locals, which
// persist).
func (s *blockScope) restore() {
	for name, prior := range s.shadows {
		s.locals[name] = prior
		if def := s.defShadow[name]; def != nil {
			s.localDefs[name] = def
		} else {
			delete(s.localDefs, name)
		}
	}
	for name := range s.added {
		delete(s.locals, name)
		delete(s.localDefs, name)
	}
}

// evalLet folds a let's initializer and binds the local, recording its static
// def when the let's annotation names a nominal type (so a method call on the
// let folds). It returns false when the initializer cannot be folded (so the
// body cannot fold past the let) or when there is no environment to bind into.
// evalReturn folds a return's value, threading the body's result-type collection
// channel to the immediate expression so a `return []` in a map<K,V>-returning
// routine folds to an empty map. The channel is carried on ctx.resultColl (set
// when the body began) and handed to the expression's expectedColl, which
// evalExpr consumes for an empty literal exactly as the const/let channels are.
func evalReturn(value ast.Expr, ctx evalCtx) *ir.Constant {
	ctx.expectedColl = ctx.resultColl
	return evalExpr(value, ctx)
}

func evalLet(s *ast.LetStmt, ctx evalCtx, scope *blockScope) bool {
	if s.Value == nil {
		return false
	}
	// A let annotation is the collection-mapness channel for the initializer, so
	// let m: map<K,V> = [] folds to an empty map exactly as the const form does,
	// and the expected-enum channel, so let r: Rarity = Legend folds the bare
	// member — the body twin of a const initializer's rule. Both are read from the
	// annotation (annotationType, never the type query). Set on a copy of ctx:
	// evalExpr consumes (and clears) them for the immediate value, and ctx's other
	// fields are unaffected for the bind below.
	letCtx := ctx
	annType := annotationType(ctx.env, s.Type)
	letCtx.expectedColl = CollKindOf(annType)
	letCtx.expected = expectedEnum(annType)
	v := evalExpr(s.Value, letCtx)
	if v == nil {
		return false
	}
	return scope.bind(s.Name, v, annotationDef(ctx.env, s.Type))
}

// evalAssign folds an assignment's value and updates the target local in place,
// so a later read (and an outer block) sees the new value. It returns false when
// the target is not a plain local name (an immutable-data error the checker
// already reported), the local is not in scope, or the value cannot be folded.
func evalAssign(s *ast.AssignStmt, ctx evalCtx) bool {
	id, ok := s.Target.(*ast.Identifier)
	if !ok || ctx.locals == nil {
		return false
	}
	if _, inScope := ctx.locals[id.Name]; !inScope {
		return false
	}
	if s.Value == nil {
		return false
	}
	// A bare member on the right folds through the target local's static enum (r =
	// Common, where r is a Rarity let), read syntactically from the local's
	// annotation (recvType, never the type query) — the assignment twin of the
	// let-initializer rule. The expectation reaches only the immediate value.
	assignCtx := ctx
	assignCtx.expected = expectedEnum(recvType(ctx, s.Target))
	v := evalExpr(s.Value, assignCtx)
	if v == nil {
		return false
	}
	ctx.locals[id.Name] = v
	return true
}

// ifOutcome is the result of folding an if statement at compile time.
type ifOutcome int

const (
	ifUnknown     ifOutcome = iota // the condition or the taken branch could not be folded
	ifReturned                     // the taken branch returned a value
	ifFellThrough                  // no branch returned; execution continues after the if
)

// evalIf folds an if statement: it evaluates the condition, runs the matching
// branch (the then body when the condition is true, otherwise the else-if chain
// or the else body), and reports whether that branch returned a value, fell
// through, or could not be determined. A branch with no return falls through to
// the statement after the if, exactly as it does at runtime.
func evalIf(s *ast.IfStmt, ctx evalCtx) (*ir.Constant, ifOutcome) {
	cond := evalExpr(s.Cond, ctx)
	if cond == nil || cond.Kind != ir.ConstBool {
		return nil, ifUnknown // an unfoldable (or non-bool) condition: cannot dispatch
	}
	if cond.Bool {
		return branchOutcome(s.Then, ctx)
	}
	switch {
	case s.ElseIf != nil:
		return evalIf(s.ElseIf, ctx)
	case s.Else != nil:
		return branchOutcome(s.Else, ctx)
	default:
		return nil, ifFellThrough // a false guard with no else: continue past it
	}
}

// evalFor folds a for statement: it folds the iterated expression and runs the
// body once per element in fold order, binding the loop variable to each element
// (the value for an of-loop, the key — a map's entry key, a list's or a range's
// index — for an in-loop) as a fresh per-iteration local. It iterates a folded
// collection or a folded range; the walk is bounded by the element count (a
// range's is capped by maxRangeIterations), so it always terminates — the same
// finite walks collectionFold and rangeFold make. An iteration whose body
// returns ends the for with that value (ifReturned); a body that runs to its end
// falls through to the next element, and once every element is visited the for
// falls through to the statement after it (ifFellThrough). An unfoldable iter, an
// unfoldable body, or a value of no iterable kind leaves the for undecided
// (ifUnknown), which stops the enclosing fold.
//
// The loop variable is block-scoped to each iteration (bound and undone per
// element), so it does not leak past the loop, while an assignment the body makes
// to an outer let local persists across iterations — which is what lets a for
// accumulate into a let, exactly as it does at runtime.
func evalFor(s *ast.ForStmt, ctx evalCtx) (*ir.Constant, ifOutcome) {
	if s.Iter == nil {
		return nil, ifUnknown
	}
	iter := evalExpr(s.Iter, ctx)
	if iter == nil {
		return nil, ifUnknown // an unfoldable iter: cannot iterate
	}
	of := s.Kind == ast.ForOf
	switch iter.Kind {
	case ir.ConstCollection:
		return evalForCollection(s, iter, of, ctx)
	case ir.ConstRange:
		return evalForRange(s, iter, of, ctx)
	default:
		return nil, ifUnknown // a value of no iterable kind
	}
}

// evalForCollection runs a for over a folded list/map: each entry binds the loop
// variable (the value for of, the key — a list's index, a map's entry key — for
// in) and runs the body. It is the collection arm of evalFor; see it for the
// outcome semantics.
func evalForCollection(s *ast.ForStmt, coll *ir.Constant, of bool, ctx evalCtx) (*ir.Constant, ifOutcome) {
	for i, entry := range coll.Coll {
		// The loop variable: the value for of, the key for in. A list entry has no
		// key, so its key is the element index — the same rule collectionFold uses.
		elem := entry.Value
		if !of {
			elem = entry.Key
			if elem == nil {
				elem = ir.IntConstant(big.NewInt(int64(i)))
			}
		}
		v, out := iterationOutcome(s, elem, ctx)
		switch out {
		case ifFellThrough:
			continue // the body ran without returning; on to the next element
		case ifReturned:
			return v, ifReturned // an early return ends the whole loop
		default:
			return nil, ifUnknown // an unfoldable body stops the fold
		}
	}
	return nil, ifFellThrough // every element visited without returning
}

// evalForRange runs a for over a folded range: each element of the half-open
// sequence start..end-1 binds the loop variable (the element for of, its 0-based
// position for in — the same key rangeFold threads) and runs the body. The walk
// is bounded by maxRangeIterations: a range wider than the cap leaves the for
// undecided (ifUnknown) rather than iterating, so a wide range never hangs the
// folder — the same verdict rangeFold gives. An empty range (end at or below
// start) falls through without running the body. The outcome semantics match the
// collection arm.
func evalForRange(s *ast.ForStmt, rng *ir.Constant, of bool, ctx evalCtx) (*ir.Constant, ifOutcome) {
	if rng.Start == nil || rng.End == nil {
		return nil, ifUnknown
	}
	count := new(big.Int).Sub(rng.End, rng.Start)
	if count.Sign() <= 0 {
		return nil, ifFellThrough // the empty range: the body never runs
	}
	if count.Cmp(big.NewInt(maxRangeIterations)) > 0 {
		return nil, ifUnknown // wider than the compile-time iteration bound
	}
	cur := new(big.Int).Set(rng.Start)
	one := big.NewInt(1)
	for i := int64(0); cur.Cmp(rng.End) < 0; i++ {
		// The loop variable: the element for of, its 0-based position for in — the
		// same key rangeFold threads.
		elem := ir.IntConstant(new(big.Int).Set(cur))
		if !of {
			elem = ir.IntConstant(big.NewInt(i))
		}
		v, out := iterationOutcome(s, elem, ctx)
		switch out {
		case ifFellThrough:
			cur.Add(cur, one)
			continue // the body ran without returning; on to the next element
		case ifReturned:
			return v, ifReturned // an early return ends the whole loop
		default:
			return nil, ifUnknown // an unfoldable body stops the fold
		}
	}
	return nil, ifFellThrough // every element visited without returning
}

// iterationOutcome runs one for-iteration: it binds the loop variable for this
// element in a fresh block scope and runs the body through branchOutcome, the
// shared body executor. The binding is block-scoped to the iteration (restored on
// return), so it does not leak to the next element or past the loop, while an
// assignment the body makes to an outer local persists through ctx.locals. A
// loop with no variable name (recovered away) or no environment to bind into
// runs the body unbound.
func iterationOutcome(s *ast.ForStmt, elem *ir.Constant, ctx evalCtx) (*ir.Constant, ifOutcome) {
	scope := newBlockScope(ctx.locals, ctx.localDefs)
	ctx.localDefs = scope.localDefs
	defer scope.restore()
	if s.Var != "" && elem != nil {
		scope.bind(s.Var, elem, nil)
	}
	return branchOutcome(s.Body, ctx)
}

// branchOutcome runs a taken branch body and classifies how it ended: a return
// of a folded value (ifReturned), a fall-through to after the if (ifFellThrough
// when no statement returned), or an unfoldable return (ifUnknown). It mirrors
// evalBody but distinguishes "ran to the end without returning" from "could not
// fold", which the if needs to decide whether to continue the outer body. A let
// in the branch is block-scoped to it (and undone on exit); an assignment to an
// outer local persists, so a guarded reassignment is visible after the if.
func branchOutcome(body []ast.Stmt, ctx evalCtx) (*ir.Constant, ifOutcome) {
	scope := newBlockScope(ctx.locals, ctx.localDefs)
	ctx.localDefs = scope.localDefs // share the scope's def map (it may allocate one)
	defer scope.restore()
	for _, stmt := range body {
		switch stmt := stmt.(type) {
		case *ast.ReturnStmt:
			if stmt.Value == nil {
				return nil, ifUnknown
			}
			if v := evalReturn(stmt.Value, ctx); v != nil {
				return v, ifReturned
			}
			return nil, ifUnknown
		case *ast.LetStmt:
			if !evalLet(stmt, ctx, scope) {
				return nil, ifUnknown
			}
		case *ast.AssignStmt:
			if !evalAssign(stmt, ctx) {
				return nil, ifUnknown
			}
		case *ast.SwitchStmt:
			v, sout := evalSwitch(stmt, ctx)
			switch sout {
			case switchFellThrough:
				continue
			case switchReturned:
				return v, ifReturned
			default:
				return nil, ifUnknown
			}
		case *ast.MatchStmt:
			v, mout := evalMatch(stmt, ctx)
			switch mout {
			case matchFellThrough:
				continue
			case matchReturned:
				return v, ifReturned
			default:
				return nil, ifUnknown
			}
		case *ast.IfStmt:
			v, out := evalIf(stmt, ctx)
			if out == ifFellThrough {
				continue
			}
			if out == ifReturned {
				return v, ifReturned
			}
			return nil, ifUnknown
		case *ast.ForStmt:
			v, out := evalFor(stmt, ctx)
			if out == ifFellThrough {
				continue
			}
			if out == ifReturned {
				return v, ifReturned
			}
			return nil, ifUnknown
		case *ast.ExprStmt:
			// As in evalBody: a bare expression neither binds nor returns, so the
			// branch steps over it. Listed so a new kind hits the default.
			continue
		default:
			panic(ast.UnhandledStmt(stmt))
		}
	}
	return nil, ifFellThrough // the branch ran to its end without returning
}

// switchOutcome mirrors ifOutcome for a switch: the same three cases the body
// walk threads to decide whether to continue past the statement or stop.
type switchOutcome int

const (
	switchUnknown     switchOutcome = iota // scrutinee/pattern unfoldable, or no arm matched
	switchReturned                         // the selected arm returned a value
	switchFellThrough                      // the selected arm ran to its end without returning
)

// evalSwitch selects and runs the matching arm of a switch: it folds the
// scrutinee, compares it for equality against each arm's folded value patterns
// in order, and runs the first matching arm's body — the wildcard arm last. It
// classifies how the selected arm ended (switchReturned / switchFellThrough)
// like an if, so a fall-through arm continues the outer body carrying any
// assignment it made to an outer local. It is switchUnknown when the scrutinee
// or a needed pattern cannot be folded, or when no arm (and no wildcard)
// matches, so a switch only folds when its dispatch is fully determined.
func evalSwitch(sw *ast.SwitchStmt, ctx evalCtx) (*ir.Constant, switchOutcome) {
	scrut := evalExpr(sw.Scrutinee, ctx)
	if scrut == nil {
		return nil, switchUnknown
	}
	for _, arm := range sw.Arms {
		for _, v := range arm.Values {
			cv := evalExpr(v, expectingScrutinee(ctx, scrut))
			if cv == nil {
				return nil, switchUnknown // an unfoldable pattern: undetermined
			}
			if constEqual(scrut, cv) {
				return switchArmOutcome(branchOutcome(arm.Body, ctx))
			}
		}
	}
	if sw.Else != nil {
		return switchArmOutcome(branchOutcome(sw.Else, ctx))
	}
	return nil, switchUnknown
}

// switchArmOutcome translates an arm body's ifOutcome — branchOutcome is the
// shared block runner — into the switch's own outcome: a return yields its
// value, a fall-through continues the outer body, and an unfoldable branch is
// undetermined.
func switchArmOutcome(v *ir.Constant, out ifOutcome) (*ir.Constant, switchOutcome) {
	switch out {
	case ifReturned:
		return v, switchReturned
	case ifFellThrough:
		return nil, switchFellThrough
	default:
		return nil, switchUnknown
	}
}

// expectingScrutinee folds an arm value with the scrutinee's enum in scope, so
// a bare member (Common) folds to that member — the value rule a switch shares
// with a const initializer's bare member.
func expectingScrutinee(ctx evalCtx, scrut *ir.Constant) evalCtx {
	if scrut.Kind == ir.ConstEnum {
		ctx.expected = scrut.EnumDef
	}
	return ctx
}

// matchOutcome mirrors switchOutcome for a match: the same three cases the body
// walk threads to decide whether to continue past the statement or stop.
type matchOutcome int

const (
	matchUnknown     matchOutcome = iota // scrutinee unfoldable, arm undecidable, or no arm matched
	matchReturned                        // the selected arm returned a value
	matchFellThrough                     // the selected arm ran to its end without returning
)

// evalMatch selects and runs the matching arm of a match: it folds the
// scrutinee, finds the arm whose member type the scrutinee's value is, and runs
// that arm's body with the arm's binding bound to the scrutinee value (the
// narrowing). It classifies how the selected arm ended (matchReturned /
// matchFellThrough) like a switch.
//
// Soundness over completeness: the dispatch folds only when exactly one arm can
// hold the value. A union value carries no member tag, so when two arms could
// back the value's kind — two nominal-over-int members (Small | Big), or two
// int-family builtins (int8 | int16) over a folded integer — the fold cannot tell
// which arm the runtime would run, and leaves the result matchUnknown rather than
// guessing. A scrutinee that does not fold, or an arm whose match cannot be
// decided syntactically (a nominal record value, which carries no member tag), is
// undetermined the same way — the discipline the switch and index folders use.
// The arm types are read through the Env's ReceiverTyper (a universe lookup), so
// the value query stays independent of the type query.
func evalMatch(m *ast.MatchStmt, ctx evalCtx) (*ir.Constant, matchOutcome) {
	scrut := evalExpr(m.Scrutinee, ctx)
	if scrut == nil {
		return nil, matchUnknown
	}
	// Scan every arm first: a fold is sound only when exactly one arm can hold the
	// value (a union value has no tag to break a tie). An undecidable arm (a
	// record member, an unresolvable type) makes the whole dispatch undetermined,
	// since it might be the runtime's chosen arm.
	selected := -1
	for i, arm := range m.Arms {
		matched, certain := constMatchesArm(ctx, scrut, arm.Type)
		if !certain {
			return nil, matchUnknown
		}
		if matched {
			if selected != -1 {
				return nil, matchUnknown // two arms back this value's kind: ambiguous
			}
			selected = i
		}
	}
	if selected != -1 {
		arm := m.Arms[selected]
		return matchArmOutcome(branchOutcome(arm.Body, narrowMatchBinding(ctx, arm.Bind, scrut)))
	}
	if m.Else != nil {
		return matchArmOutcome(branchOutcome(m.Else, ctx))
	}
	return nil, matchUnknown
}

// matchArmOutcome translates an arm body's ifOutcome — branchOutcome is the
// shared block runner — into the match's own outcome, exactly as
// switchArmOutcome does for a switch.
func matchArmOutcome(v *ir.Constant, out ifOutcome) (*ir.Constant, matchOutcome) {
	switch out {
	case ifReturned:
		return v, matchReturned
	case ifFellThrough:
		return nil, matchFellThrough
	default:
		return nil, matchUnknown
	}
}

// narrowMatchBinding binds a match arm's binding name to the scrutinee value for
// the arm body, so a reference to it folds. The narrowed value is the scrutinee
// itself (narrowing changes the static type, not the runtime value). A nameless
// arm binds nothing. The locals map is copied so the binding reaches only this
// arm body, not a sibling arm or the enclosing block.
func narrowMatchBinding(ctx evalCtx, name string, scrut *ir.Constant) evalCtx {
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

// constMatchesArm reports whether a folded scrutinee value is of a match arm's
// member type, and whether that could be decided at all. The arm type is
// resolved through the Env's ReceiverTyper (a universe lookup, never the type
// query); the value's kind is then tested against it:
//
//   - an enum value matches the arm's enum definition by identity;
//   - a scalar value (int/bool/string/datetime/duration/error) matches a builtin
//     or nominal-over-primitive arm type whose underlying primitive backs that
//     kind; and
//   - a nominal record value carries no member tag, so a record arm type is
//     undecidable — the second result is false and the match does not fold.
//
// A nil or unresolvable arm type is undecidable too. Returning (false, false)
// keeps the fold from ever choosing the wrong arm.
func constMatchesArm(ctx evalCtx, scrut *ir.Constant, armType ast.TypeExpr) (matched, certain bool) {
	t := annotationType(ctx.env, armType)
	if t == nil {
		return false, false // no type channel, or an unresolvable arm type
	}
	switch t := t.(type) {
	case *ir.Builtin:
		return scalarMatchesBuiltin(ctx.env.Registry(), scrut, t.Name), true
	case *ir.Named:
		if t.Def == nil {
			return false, false
		}
		if t.Def.Enum != nil {
			return scrut.Kind == ir.ConstEnum && scrut.EnumDef == t.Def, true
		}
		// A nominal type over a primitive (a refinement type) matches by the
		// underlying kind; a nominal record carries no tag, so it is undecidable.
		if underlyingPrimitive(ctx.env.Registry(), t.Def, map[*ir.TypeDef]bool{}) != nil {
			return defBacksKind(ctx.env.Registry(), t.Def, scrut.Kind), true
		}
		return false, false
	default:
		// A record, function, union, or collection arm type carries no value tag
		// the fold can test (a record value's nominal identity is unknown), so the
		// dispatch is left undetermined.
		return false, false
	}
}

// scalarMatchesBuiltin reports whether a folded value is of the builtin type
// named name — the scalar kinds keyed on the registry's native classification,
// so a new primitive added to the registry is matched without a hardcoded list.
func scalarMatchesBuiltin(reg *builtin.Registry, scrut *ir.Constant, name string) bool {
	n, ok := reg.Native(name)
	if !ok {
		return false
	}
	switch scrut.Kind {
	case ir.ConstInt:
		return n.IsInteger()
	case ir.ConstBool:
		return n.Bool
	case ir.ConstString:
		return n.Str
	case ir.ConstDatetime:
		return n.Datetime
	case ir.ConstDuration:
		return n.Duration
	case ir.ConstError:
		return n.Err
	case ir.ConstNull:
		return n.Null
	default:
		return false
	}
}

// constEqual reports whether two folded constants are structurally equal — the
// equality a switch dispatches on and a map keys by. It is ir.ConstantsEqual,
// the single shared definition the semantic engine's early cutoff also uses;
// see its doc for the per-kind rules. A nil constant never reaches the map-key
// and switch-scrutinee comparisons that call this, but ConstantsEqual handles
// it (two nil constants are equal) so the contract is one consistent equality.
func constEqual(a, b *ir.Constant) bool {
	return ir.ConstantsEqual(a, b)
}

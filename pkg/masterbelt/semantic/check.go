package semantic

import (
	"strconv"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/eval"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/types/infer"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// --- expression diagnostics -------------------------------------------------

// checkDivByZero reports each div/rem whose divisor folds to zero. It descends
// into a function literal's body — a divisor folds (or doesn't) the same way
// there, since parameters never fold to a constant.
func checkDivByZero(e ast.Expr, env evalEnv, report func(node ast.Node)) {
	if lit, ok := e.(*ast.FuncLit); ok {
		forEachBodyExpr(lit.Body, func(x ast.Expr) {
			checkDivByZero(x, env, report)
		})
		return
	}
	if tern, ok := e.(*ast.TernaryExpr); ok {
		// A ternary folds and runs only the selected branch, so the div-by-zero
		// catch follows the same dispatch: the condition is always evaluated
		// (and walked), and only the statically-taken branch is descended into —
		// a div-by-zero on the guaranteed-taken path is reported, one on a
		// provably-untaken path stays silent, matching eval's short-circuit. An
		// unfoldable condition walks both branches conservatively (either could
		// run), so a definite div-by-zero on every path is still caught.
		checkDivByZero(tern.Cond, env, report)
		cond := eval.Expr(tern.Cond, env)
		switch {
		case cond != nil && cond.Kind == ir.ConstBool && cond.Bool:
			checkDivByZero(tern.Then, env, report)
		case cond != nil && cond.Kind == ir.ConstBool && !cond.Bool:
			checkDivByZero(tern.Else, env, report)
		default:
			checkDivByZero(tern.Then, env, report)
			checkDivByZero(tern.Else, env, report)
		}
		return
	}
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return
	}
	member, ok := call.Callee.(*ast.MemberExpr)
	if !ok {
		return
	}
	if (member.Member.Name == "div" || member.Member.Name == "rem") && len(call.Arguments) == 1 {
		if d := eval.Expr(call.Arguments[0], env); d != nil && d.Kind == ir.ConstInt && d.Int.Sign() == 0 {
			report(call)
		}
	}
	checkDivByZero(member.Receiver, env, report)
	if shortCircuits(member, call.Arguments, env) {
		// A short-circuited && / || never evaluates its right operand, so a
		// div-by-zero there is unreachable — matching eval, which folds the
		// connective without touching the dead side. The receiver is still
		// walked above; the argument is skipped.
		return
	}
	for _, a := range call.Arguments {
		checkDivByZero(a, env, report)
	}
}

// checkRangeStepZero reports each range(start, end, step) call whose step folds
// to a constant zero — a range with no sequence (it would neither advance nor
// terminate). It descends the same way checkDivByZero does: into a function
// literal's body, and through a ternary's condition and statically-taken branch
// (an unfoldable condition walks both), so a zero-step range on a guaranteed path
// is caught and one on a provably-dead path stays silent. A non-constant step is
// left to the runtime — exactly the non-constant divisor's treatment — so only a
// step that folds to zero is reported. The receiver and the other arguments are
// walked too, so a nested range(...) anywhere in the expression is reached.
func checkRangeStepZero(e ast.Expr, env evalEnv, report func(node ast.Node)) {
	if lit, ok := e.(*ast.FuncLit); ok {
		forEachBodyExpr(lit.Body, func(x ast.Expr) {
			checkRangeStepZero(x, env, report)
		})
		return
	}
	if tern, ok := e.(*ast.TernaryExpr); ok {
		checkRangeStepZero(tern.Cond, env, report)
		cond := eval.Expr(tern.Cond, env)
		switch {
		case cond != nil && cond.Kind == ir.ConstBool && cond.Bool:
			checkRangeStepZero(tern.Then, env, report)
		case cond != nil && cond.Kind == ir.ConstBool && !cond.Bool:
			checkRangeStepZero(tern.Else, env, report)
		default:
			checkRangeStepZero(tern.Then, env, report)
			checkRangeStepZero(tern.Else, env, report)
		}
		return
	}
	// A range literal carries no step argument (its step is the implicit ±1), so
	// it never has a zero step; only the three-argument constructor does. Walk a
	// literal's bounds for a nested range(..., 0) all the same.
	if rng, ok := e.(*ast.RangeExpr); ok {
		checkRangeStepZero(rng.Lower, env, report)
		checkRangeStepZero(rng.Upper, env, report)
		return
	}
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return
	}
	// range(start, end, step) is a conversion call: the callee names the type
	// directly (an identifier "range"), not a member. A three-argument call whose
	// step folds to a constant zero is the zero-step range.
	if id, ok := call.Callee.(*ast.Identifier); ok && id.Name == "range" && len(call.Arguments) == 3 {
		if step := eval.Expr(call.Arguments[2], env); step != nil && step.Kind == ir.ConstInt && step.Int.Sign() == 0 {
			report(call)
		}
	}
	// Walk the callee's receiver (for a method call) and every argument, so a
	// nested range(..., 0) inside an operand is reached.
	if member, ok := call.Callee.(*ast.MemberExpr); ok {
		checkRangeStepZero(member.Receiver, env, report)
	}
	for _, a := range call.Arguments {
		checkRangeStepZero(a, env, report)
	}
}

// shortCircuits reports whether a boolean connective call (&& desugared to anan,
// || to oror) short-circuits — its receiver folds to a bool that already decides
// the result (false && _, true || _) — so its right operand is dead. A receiver
// that does not fold, or the non-deciding bool, returns false: the argument is
// live and must be walked.
func shortCircuits(member *ast.MemberExpr, args []ast.Expr, env evalEnv) bool {
	if len(args) != 1 {
		return false
	}
	recv := eval.Expr(member.Receiver, env)
	if recv == nil || recv.Kind != ir.ConstBool {
		return false
	}
	switch member.Member.Name {
	case "anan":
		return !recv.Bool
	case "oror":
		return recv.Bool
	}
	return false
}

// checkIndexWrites reports a list index write past the end (index_out_of_range).
// A write coll[i] = v desugared to coll = coll.set(i, v); when the collection
// folds to a list of known length and the index folds to a constant out of that
// range, the write is a compile-time bug — a list write cannot grow the list, so
// it has nowhere to land (a map write upserts and is never out of range). The
// folding tracks the body's let locals so a write to a let-bound list is reached;
// a receiver or index that does not fold (a parameter, a dynamic index) is left
// to the runtime, exactly as the plan scopes it.
//
// The walk mirrors eval's body execution for the bindings it needs: a let adds a
// local, an assignment updates one, and a nested block (an if/switch branch) gets
// a copy so its bindings do not leak out. Only foldable bindings are tracked;
// the check is a best-effort static catch, not a guarantee.
func checkIndexWrites(body []ast.Stmt, env eval.Env, at func(ast.Node) span, diags *diagnostic.List) {
	locals := map[string]*ir.Constant{}
	checkIndexWritesIn(body, locals, env, at, diags)
}

// checkIndexWritesIn walks a statement body with the running local environment
// locals, reporting out-of-range list writes and threading the bindings each
// statement introduces. A nested block is walked with a copy of locals so its
// lets stay block-scoped, while an assignment to an outer local persists.
func checkIndexWritesIn(body []ast.Stmt, locals map[string]*ir.Constant, env eval.Env, at func(ast.Node) span, diags *diagnostic.List) {
	for _, stmt := range body {
		switch s := stmt.(type) {
		case *ast.LetStmt:
			if s.Name != "" && s.Value != nil {
				// The let annotation is the value folder's expectation channel: an
				// empty map/list literal folds to the settled empty collection the
				// write check needs to tell an upsert from a list write, and a member
				// value is tagged with its union member.
				if v := eval.ExprInExpecting(s.Value, locals, annotationTypeOf(env, s.Type), env); v != nil {
					locals[s.Name] = v
				} else {
					delete(locals, s.Name) // an unfoldable rebind: stop tracking it
				}
			}
		case *ast.AssignStmt:
			reportIndexWrite(s, locals, env, at, diags)
			if id, ok := s.Target.(*ast.Identifier); ok && s.Value != nil {
				if v := eval.ExprIn(s.Value, locals, env); v != nil {
					locals[id.Name] = v
				} else {
					delete(locals, id.Name)
				}
			}
		case *ast.IfStmt:
			checkIndexWritesIn(s.Then, copyLocals(locals), env, at, diags)
			if s.ElseIf != nil {
				checkIndexWritesIn([]ast.Stmt{s.ElseIf}, copyLocals(locals), env, at, diags)
			}
			checkIndexWritesIn(s.Else, copyLocals(locals), env, at, diags)
		case *ast.SwitchStmt:
			for _, arm := range s.Arms {
				checkIndexWritesIn(arm.Body, copyLocals(locals), env, at, diags)
			}
			checkIndexWritesIn(s.Else, copyLocals(locals), env, at, diags)
			for _, arm := range s.AfterElse {
				checkIndexWritesIn(arm.Body, copyLocals(locals), env, at, diags)
			}
		case *ast.MatchStmt:
			// A match arm's narrowed binding is not a foldable list constant the
			// write check tracks, so the arm bodies are walked with a copy of the
			// locals exactly as a switch's arms are.
			for _, arm := range s.Arms {
				checkIndexWritesIn(arm.Body, copyLocals(locals), env, at, diags)
			}
			checkIndexWritesIn(s.Else, copyLocals(locals), env, at, diags)
			for _, arm := range s.AfterElse {
				checkIndexWritesIn(arm.Body, copyLocals(locals), env, at, diags)
			}
		case *ast.ForStmt:
			// The loop variable is bound per iteration, not to a foldable list
			// constant the write check tracks, so the body is walked with a copy of
			// the locals exactly as a switch's arms are.
			checkIndexWritesIn(s.Body, copyLocals(locals), env, at, diags)
		case *ast.ReturnStmt, *ast.ExprStmt:
			// Neither binds or reassigns a local, so neither can carry an index
			// write or change which list a later write targets: nothing to do.
			// Listed explicitly so a new statement kind hits the default instead.
		default:
			panic(ast.UnhandledStmt(stmt))
		}
	}
}

// reportIndexWrite reports an out-of-range list write for an assignment whose
// value is a set call on a foldable list. The set call carries the index and the
// new value (a desugared coll[i] = v, or a hand-written coll = coll.set(i, v));
// when the receiver folds to a settled list and the index to a constant outside
// it, the write is reported at the index expression. A map receiver is an upsert
// and never out of range, and an unknown empty collection (its mapness unsettled)
// is ambiguous, so neither is reported — only a settled list is.
func reportIndexWrite(s *ast.AssignStmt, locals map[string]*ir.Constant, env eval.Env, at func(ast.Node) span, diags *diagnostic.List) {
	call, ok := s.Value.(*ast.CallExpr)
	if !ok {
		return
	}
	member, ok := call.Callee.(*ast.MemberExpr)
	if !ok || member.Member.Name != "set" || len(call.Arguments) != 2 {
		return
	}
	recv := eval.ExprIn(member.Receiver, locals, env)
	if recv == nil || recv.Kind != ir.ConstCollection || !recv.IsList() {
		return
	}
	idx := eval.ExprIn(call.Arguments[0], locals, env)
	if idx == nil || idx.Kind != ir.ConstInt {
		return
	}
	n := len(recv.Coll)
	if idx.Int.IsInt64() && idx.Int.Int64() >= 0 && idx.Int.Int64() < int64(n) {
		return // in range
	}
	c := at(call.Arguments[0])
	diags.Add(newIndexOutOfRangeDiagnostic(c.offset, c.width, idx.Int.String(), strconv.Itoa(n)))
}

// annotationTypeOf resolves a let's type annotation to its full type through a
// pure universe lookup, or nil for no annotation (or an Env that supplies no type
// resolution). It is the channel the write check threads so an empty collection
// let folds to a settled value and a member let is tagged with its union member.
func annotationTypeOf(env eval.Env, t ast.TypeExpr) ir.Type {
	rt, ok := env.(eval.ReceiverTyper)
	if !ok || t == nil {
		return nil
	}
	return rt.TypeExprType(t)
}

// copyLocals returns a shallow copy of a local environment, so a nested block's
// bindings do not leak back to the enclosing one.
func copyLocals(locals map[string]*ir.Constant) map[string]*ir.Constant {
	out := make(map[string]*ir.Constant, len(locals))
	for k, v := range locals {
		out[k] = v
	}
	return out
}

// checkBareEnumArgs reports a bare member in an operator/method argument whose
// receiver is an enum but which names no member of it (rarity == Bogus, desugared
// to rarity.eql(Bogus)) as unknown_enum_member — the argument twin of the const
// path's finding for a bare member of a const annotation. It is a best-effort
// guardrail, fired only where the receiver's static type names an enum and the
// argument is a bare name resolving to nothing else; the bare members it leaves
// alone (a member, a local, a parameter, a constant) all resolve in the lowering.
//
// The walk threads bs through a block's lets exactly as the checking walk does
// (a fresh scope per let, a copy per nested block), so a comparison against a
// let-bound enum local resolves its receiver from the local in scope.
func checkBareEnumArgs(body []ast.Stmt, bs infer.BodyScope, env eval.Env, at func(ast.Node) span, diags *diagnostic.List) {
	for _, stmt := range body {
		switch s := stmt.(type) {
		case *ast.LetStmt:
			reportBareEnumArgsIn(s.Value, bs, env, at, diags)
			bs = bindReturnLocal(s, bs)
		case *ast.AssignStmt:
			reportBareEnumArgsIn(s.Value, bs, env, at, diags)
		case *ast.ExprStmt:
			reportBareEnumArgsIn(s.X, bs, env, at, diags)
		case *ast.ReturnStmt:
			reportBareEnumArgsIn(s.Value, bs, env, at, diags)
		case *ast.IfStmt:
			reportBareEnumArgsIn(s.Cond, bs, env, at, diags)
			checkBareEnumArgs(s.Then, bs, env, at, diags)
			if s.ElseIf != nil {
				checkBareEnumArgs([]ast.Stmt{s.ElseIf}, bs, env, at, diags)
			}
			checkBareEnumArgs(s.Else, bs, env, at, diags)
		case *ast.SwitchStmt:
			reportBareEnumArgsIn(s.Scrutinee, bs, env, at, diags)
			checkBareEnumArgsArms(s.Arms, bs, env, at, diags)
			checkBareEnumArgs(s.Else, bs, env, at, diags)
			checkBareEnumArgsArms(s.AfterElse, bs, env, at, diags)
		case *ast.MatchStmt:
			reportBareEnumArgsIn(s.Scrutinee, bs, env, at, diags)
			for _, arm := range s.Arms {
				checkBareEnumArgs(arm.Body, armNarrowedScope(bs, arm), env, at, diags)
			}
			checkBareEnumArgs(s.Else, bs, env, at, diags)
			for _, arm := range s.AfterElse {
				checkBareEnumArgs(arm.Body, armNarrowedScope(bs, arm), env, at, diags)
			}
		case *ast.ForStmt:
			reportBareEnumArgsIn(s.Iter, bs, env, at, diags)
			checkBareEnumArgs(s.Body, forNarrowedScope(bs, s), env, at, diags)
		default:
			panic(ast.UnhandledStmt(stmt))
		}
	}
}

// checkBareEnumArgsArms walks each switch arm's value patterns and body.
func checkBareEnumArgsArms(arms []*ast.SwitchArm, bs infer.BodyScope, env eval.Env, at func(ast.Node) span, diags *diagnostic.List) {
	for _, arm := range arms {
		for _, v := range arm.Values {
			reportBareEnumArgsIn(v, bs, env, at, diags)
		}
		checkBareEnumArgs(arm.Body, bs, env, at, diags)
	}
}

// reportBareEnumArgsIn walks an expression and reports a bare non-member
// argument of every method call whose receiver's static type names an enum. The
// receiver type is read through the type query (infer.Body) — this is the
// reporting pass, which the value query never feeds, so the early-cutoff
// invariant (value-blind folding) is untouched.
func reportBareEnumArgsIn(e ast.Expr, bs infer.BodyScope, env eval.Env, at func(ast.Node) span, diags *diagnostic.List) {
	if e == nil {
		return
	}
	ast.WalkExprs(e, func(x ast.Expr) bool {
		call, ok := x.(*ast.CallExpr)
		if !ok {
			return true
		}
		member, ok := call.Callee.(*ast.MemberExpr)
		if !ok {
			return true
		}
		if enumDef := enumDefOf(infer.Body(member.Receiver, bs)); enumDef != nil {
			for _, a := range call.Arguments {
				reportBareEnumMember(a, enumDef, bs, env, at, diags)
			}
		}
		return true
	})
}

// checkNoSelf reports each self expression in e — descending into nested
// function-literal bodies, which inherit the enclosing context's receiver (or
// its absence). A constant initializer, an assert condition, and a function
// body have no receiver, so self has no meaning anywhere inside them.
func checkNoSelf(e ast.Expr, report func(node ast.Node)) {
	ast.WalkExprs(e, func(x ast.Expr) bool {
		switch x := x.(type) {
		case *ast.SelfExpr:
			report(x)
		case *ast.FuncLit:
			forEachBodyExpr(x.Body, func(inner ast.Expr) {
				checkNoSelf(inner, report)
			})
		}
		return true
	})
}

// forEachBodyExpr calls fn for every top expression of a statement body —
// delegating to the shared ast.WalkBodyExprs so the editor and the semantic
// layer walk a body's statements (return, expression, let, assign, switch, if)
// identically and cannot drift as new statement forms are added.
func forEachBodyExpr(body []ast.Stmt, fn func(ast.Expr)) {
	ast.WalkBodyExprs(body, fn)
}

// --- method bodies ----------------------------------------------------------

// methodTScope is the generic type-parameter scope in effect in a method body:
// the enclosing type's parameters (each with its resolved bound) plus the
// method's own explicit type parameters (fold<A>). A body type annotation naming
// one of these (a let, a match/switch arm) then resolves to a TypeVar rather than
// an unknown type. The enclosing parameters' bounds are carried through; a method
// type parameter is left unbounded here (a body annotation only needs its name in
// scope, and the signature already carries the bound). A method-introduced
// inference variable (the implicit R in map(func: fn(T): R)) is deliberately not
// added: it is an inference hole, not a name a body annotation may pin, and
// adding it would risk shadowing a same-named real type.
func methodTScope(def *ir.TypeDef, m *ast.MethodDecl) infer.TypeScope {
	if len(def.Params) == 0 && len(m.TypeParams) == 0 {
		return nil
	}
	scope := make(infer.TypeScope, len(def.Params)+len(m.TypeParams))
	for _, p := range def.Params {
		scope[p.Name] = p.Bound
	}
	for _, tp := range m.TypeParams {
		if _, ok := scope[tp.Name]; !ok {
			scope[tp.Name] = nil
		}
	}
	return scope
}

// checkMethodBodies type-checks each method body's returned value against the
// method's declared result type, reporting through sink. It runs after
// resolveTypes; each resolved method carries the declaration it came from
// (ir.Method.Syntax), which is the pairing — a dropped duplicate overload
// never shifts a neighbour onto the wrong signature. The walk is the same
// checking walk the const path uses (infer.CheckBody), so the declared result
// type reaches into a returned function or collection literal. universe is
// the file's annotation universe — its own definitions shadowing its imported
// ones — and qualified its namespace-qualified lookup, so a type in a body
// resolves exactly as an annotation does. env folds switch arm values for the
// exhaustiveness and duplicate checks.
func checkMethodBodies(reg *builtin.Registry, defs []*ir.TypeDef, universe map[string]*ir.TypeDef, qualified func(namespace, name string) *ir.TypeDef, funcs map[string][]*ast.FuncDecl, qualifiedFuncs func(namespace, name string) []*ast.FuncDecl, env eval.Env, sink *infer.Sink, at func(ast.Node) span, diags *diagnostic.List) {
	var noSelf func(node ast.Node)
	if diags != nil {
		noSelf = func(node ast.Node) {
			s := at(node)
			diags.Add(newSelfOutsideMethodDiagnostic(s.offset, s.width))
		}
	}
	for _, def := range defs {
		self := &ir.Named{Def: def}
		for _, irm := range def.Methods {
			m := irm.Syntax
			if m == nil || len(m.Body) == 0 {
				continue // an extern or empty body has nothing to check
			}
			// A static fn has no receiver: its body is checked with self unbound
			// (Self ir.Invalid) and a self reference reported (self_outside_method),
			// exactly as a top-level function body is. An instance method, getter, or
			// setter binds self to the receiver and passes a nil noSelf.
			selfT := ir.Type(self)
			var bodyNoSelf func(ast.Node)
			if irm.Kind == ir.MethodStatic {
				selfT = ir.Invalid
				bodyNoSelf = noSelf
			}
			params := make(map[string]ir.Type, len(irm.Params))
			for _, p := range irm.Params {
				params[p.Name] = substSelf(p.Type, self)
			}
			want := substSelf(irm.Result, self)
			bs := infer.BodyScope{Reg: reg, Universe: universe, Qualified: qualified, Self: selfT, Params: params, Funcs: funcs, QualifiedFuncs: qualifiedFuncs, TScope: methodTScope(def, m)}
			checkStmts(m.Body, want, bs, env, bodyNoSelf, sink, at, diags)
			checkIndexWrites(m.Body, env, at, diags)
			checkBareEnumArgs(m.Body, bs, env, at, diags)
		}
	}
}

// checkFuncBodies type-checks each function body's returned value against the
// declared result type — the same checking walk a method body uses, with no
// receiver (self is ir.Invalid) — and reports a body that never returns a
// value (missing_return). A function produces its declared result when it
// returns on every path: a trailing return, or an exhaustive switch all of
// whose arms return. A declaration whose body is missing altogether is a parse
// error, not a missing return.
//
// sink receives the body's typing facts (operator errors, mismatches, and each
// function literal's solved signature); diags receives the func-body
// diagnostics. A nil diagnostic list runs the walk for the sink alone — the
// func-literal-types path settles the signatures of the lambdas inside a
// function body without reporting (the self and missing-return diagnostics, and
// the index-write check, are suppressed) — mirroring checkMethodBodies.
func checkFuncBodies(reg *builtin.Registry, file *ast.File, universe map[string]*ir.TypeDef, qualified func(namespace, name string) *ir.TypeDef, funcs map[string][]*ast.FuncDecl, qualifiedFuncs func(namespace, name string) []*ast.FuncDecl, env eval.Env, sink *infer.Sink, at func(ast.Node) span, diags *diagnostic.List) {
	r := &infer.TypeResolver{Defs: universe, Qualified: qualified}
	var noSelf func(node ast.Node)
	if diags != nil {
		noSelf = func(node ast.Node) {
			s := at(node)
			diags.Add(newSelfOutsideMethodDiagnostic(s.offset, s.width))
		}
	}
	for _, fd := range file.Funcs {
		// The function's generic type parameters are in scope for its parameter
		// and result annotations, so a `c: T` where `T: foldable<int>` resolves
		// to a TypeVar. ResolveFuncTypeParams back-fills each resolved bound into
		// tscope, so resolving the parameter and result types already yields a
		// TypeVar carrying its bound — the body may then call the bound interface's
		// methods on the parameter, and nothing else. tscope is also the body's
		// type-param scope (TScope), so a type annotation in the body (a let, a
		// match/switch arm) may name a type parameter.
		tscope := infer.FuncTypeParamScope(fd.TypeParams)
		infer.ResolveFuncTypeParams(r, fd.TypeParams, tscope)
		params := make(map[string]ir.Type, len(fd.Params))
		for _, p := range fd.Params {
			params[p.Name] = r.ResolveType(p.Type, tscope)
		}
		want := r.ResolveType(fd.Result, tscope)
		bs := infer.BodyScope{Reg: reg, Universe: universe, Qualified: qualified, Self: ir.Invalid, Params: params, Funcs: funcs, QualifiedFuncs: qualifiedFuncs, TScope: tscope}
		checkStmts(fd.Body, want, bs, env, noSelf, sink, at, diags)
		if diags == nil {
			continue // the sink-only walk wants no further diagnostics
		}
		checkIndexWrites(fd.Body, env, at, diags)
		checkBareEnumArgs(fd.Body, bs, env, at, diags)
		if hasBlockBody(fd) && !bodyReturns(fd.Body, bs) {
			s := at(fd)
			diags.Add(newMissingReturnDiagnostic(s.offset, s.width, fd.Name))
		}
	}
}

// checkStmts walks a statement body, checking each return value against the
// declared result type want and validating each switch. It is the shared body
// walk for a method and a function body, recursing through a switch's arm
// bodies (and its wildcard) so a nested switch is checked the same way. noSelf,
// when non-nil (a function body), reports a self expression in any statement;
// a method body passes nil, since self is bound there.
func checkStmts(stmts []ast.Stmt, want ir.Type, bs infer.BodyScope, env eval.Env, noSelf func(ast.Node), sink *infer.Sink, at func(ast.Node) span, diags *diagnostic.List) {
	// A let introduces a block-local that the statements after it (within this
	// block) see, so bs is rebound with the new local as the walk descends — a
	// fresh BodyScope per let, leaving the caller's scope untouched. A nested
	// if/switch is checked with the scope in force at its block, so an inner let
	// shadows an outer one and a block-local does not leak out.
	for _, stmt := range stmts {
		switch stmt := stmt.(type) {
		case *ast.ReturnStmt:
			if stmt.Value == nil {
				continue
			}
			if noSelf != nil {
				checkNoSelf(stmt.Value, noSelf)
			}
			infer.CheckBody(stmt.Value, want, bs, sink)
		case *ast.LetStmt:
			bs = checkLet(stmt, bs, env, noSelf, sink, at, diags)
		case *ast.AssignStmt:
			checkAssign(stmt, bs, env, noSelf, sink, at, diags)
		case *ast.ExprStmt:
			if noSelf != nil {
				checkNoSelf(stmt.X, noSelf)
			}
			// An expression statement discards its value, so it is checked with
			// no expected type — synthesis alone, surfacing the operator and
			// method-call errors a discarded call would otherwise hide (an
			// undefined method, a type-mismatched argument). This mirrors the
			// lambda-body walk (infer.walkBody), which checks its expression
			// statements the same way.
			if stmt.X != nil {
				infer.CheckPredicate(stmt.X, bs, sink)
			}
		case *ast.SwitchStmt:
			if noSelf != nil && stmt.Scrutinee != nil {
				checkNoSelf(stmt.Scrutinee, noSelf)
			}
			// A nil diagnostic list suppresses the switch diagnostics (the
			// func-literal-types walk wants only the checking sink); the body
			// walk still reaches every nested statement.
			if diags != nil {
				checkSwitch(stmt, bs, env, at, diags)
			}
			for _, arm := range stmt.Arms {
				checkStmts(arm.Body, want, bs, env, noSelf, sink, at, diags)
			}
			checkStmts(stmt.Else, want, bs, env, noSelf, sink, at, diags)
			// Unreachable arms still type-check, so their own errors surface even
			// though they can never run.
			for _, arm := range stmt.AfterElse {
				checkStmts(arm.Body, want, bs, env, noSelf, sink, at, diags)
			}
		case *ast.MatchStmt:
			if noSelf != nil && stmt.Scrutinee != nil {
				checkNoSelf(stmt.Scrutinee, noSelf)
			}
			// A nil diagnostic list suppresses the match diagnostics (the
			// func-literal-types walk wants only the checking sink); the body
			// walk still reaches every nested statement.
			if diags != nil {
				checkMatch(stmt, bs, at, diags)
			}
			// Each arm body is checked in the scope where its binding is narrowed
			// to the arm's member type, so a reference to the binding resolves at
			// the narrowed type.
			for _, arm := range stmt.Arms {
				checkStmts(arm.Body, want, armNarrowedScope(bs, arm), env, noSelf, sink, at, diags)
			}
			checkStmts(stmt.Else, want, bs, env, noSelf, sink, at, diags)
			// Unreachable arms still type-check, so their own errors surface even
			// though they can never run.
			for _, arm := range stmt.AfterElse {
				checkStmts(arm.Body, want, armNarrowedScope(bs, arm), env, noSelf, sink, at, diags)
			}
		case *ast.IfStmt:
			checkIf(stmt, want, bs, env, noSelf, sink, at, diags)
		case *ast.ForStmt:
			if noSelf != nil && stmt.Iter != nil {
				checkNoSelf(stmt.Iter, noSelf)
			}
			// A nil diagnostic list suppresses the for diagnostics (the
			// func-literal-types walk wants only the checking sink); the body walk
			// still reaches every nested statement.
			if diags != nil {
				checkFor(stmt, bs, at, diags)
			}
			// The body is checked in the scope where the loop variable is bound to
			// its element type, so a reference to it resolves at that type.
			checkStmts(stmt.Body, want, forNarrowedScope(bs, stmt), env, noSelf, sink, at, diags)
		default:
			panic(ast.UnhandledStmt(stmt))
		}
	}
}

// scrutEnumOf builds the scrutinee-enum resolver return analysis uses: it reads
// the enum a scrutinee's static type names from the body scope — a let-bound
// local (which shadows a same-named parameter), a parameter's type, or the
// receiver's type for self — without the type query, exactly as checkSwitch and
// the body binder resolve the scrutinee. The walk grows bs.Locals as it descends
// a block's lets, so the local in scope at the switch is the one read here.
func scrutEnumOf(bs infer.BodyScope) func(ast.Expr) *ir.TypeDef {
	return func(scrutinee ast.Expr) *ir.TypeDef {
		switch e := scrutinee.(type) {
		case *ast.Identifier:
			if t, ok := bs.Locals[e.Name]; ok {
				return enumDefOf(t)
			}
			if t, ok := bs.Params[e.Name]; ok {
				return enumDefOf(t)
			}
		case *ast.SelfExpr:
			return enumDefOf(bs.Self)
		}
		return nil
	}
}

// hasBlockBody reports whether the declaration carries a block body in the
// source — the form that can fail to return. An arrow body always returns its
// expression, and a declaration with no body at all was reported by the
// parser; the AST drops an empty block, so the distinction reads off the CST.
func hasBlockBody(fd *ast.FuncDecl) bool {
	for _, c := range fd.Syntax().Children() {
		if n, ok := c.(*cst.Node); ok && n.Kind() == cst.Block {
			return true
		}
	}
	return false
}

// substSelf substitutes the self type for ir.SelfType.
func substSelf(t, self ir.Type) ir.Type {
	if _, ok := t.(*ir.SelfType); ok {
		return self
	}
	return t
}

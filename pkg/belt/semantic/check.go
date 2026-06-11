package semantic

import (
	"github.com/masterbelt/masterbelt/pkg/belt/builtin"
	"github.com/masterbelt/masterbelt/pkg/belt/types/infer"
	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// --- expression diagnostics -------------------------------------------------

// checkDivByZero reports each div/rem whose divisor folds to zero. It descends
// into a function literal's body — a divisor folds (or doesn't) the same way
// there, since parameters never fold to a constant.
func checkDivByZero(e ast.Expr, env exprFolder, report func(node ast.Node)) {
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
		cond := env.fold(tern.Cond)
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
		if d := env.fold(call.Arguments[0]); d != nil && d.Kind == ir.ConstInt && d.Int.Sign() == 0 {
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
func checkRangeStepZero(e ast.Expr, env exprFolder, report func(node ast.Node)) {
	if lit, ok := e.(*ast.FuncLit); ok {
		forEachBodyExpr(lit.Body, func(x ast.Expr) {
			checkRangeStepZero(x, env, report)
		})
		return
	}
	if tern, ok := e.(*ast.TernaryExpr); ok {
		checkRangeStepZero(tern.Cond, env, report)
		cond := env.fold(tern.Cond)
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
	if id, ok := call.Callee.(*ast.Identifier); ok && id.Name == builtin.NameRange && len(call.Arguments) == 3 {
		if step := env.fold(call.Arguments[2]); step != nil && step.Kind == ir.ConstInt && step.Int.Sign() == 0 {
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
func shortCircuits(member *ast.MemberExpr, args []ast.Expr, env exprFolder) bool {
	if len(args) != 1 {
		return false
	}
	recv := env.fold(member.Receiver)
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
func checkBareEnumArgs(body []ast.Stmt, bs infer.BodyScope, env exprFolder, at func(ast.Node) span, diags *diagnostic.List) {
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
func checkBareEnumArgsArms(arms []*ast.SwitchArm, bs infer.BodyScope, env exprFolder, at func(ast.Node) span, diags *diagnostic.List) {
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
func reportBareEnumArgsIn(e ast.Expr, bs infer.BodyScope, env exprFolder, at func(ast.Node) span, diags *diagnostic.List) {
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

// reportTypeParamValueUse reports each value-position use of a generic type
// parameter in a function or method body: a type parameter is a compile-time
// type, not a foldable value, so projecting a member off it (T.x) or consuming
// it bare (T == string) where a value is expected is type_param_in_value_position
// — the value-position counterpart of the type-position projection T.x, which
// resolves through the bound. Without it a bounded parameter projected in value
// position folds to nothing and the consuming expression passes vacuously
// (assert T.x == string and == nint both clean), so the read is rejected at the
// definition site instead.
//
// Only a value-position use is reported. A parameter or result annotation, a
// conversion T(x), and a match/switch arm type are type positions that never
// reach this value walk (the conversion's bare-name callee is skipped, the
// annotations and arm types are TypeExprs the value walk does not visit). A value
// binding of the same name — a parameter, a let, a loop variable, a match
// binding, or a lambda parameter — shadows the type parameter for the statements
// it scopes and takes the value reading, exactly as the body checker scopes those
// bindings, so reusing a type parameter's name as a local does not misfire.
func reportTypeParamValueUse(scope infer.TypeScope, params map[string]ir.Type, valueShadows func(*ast.Identifier) bool, enumArm func(scrutinee ast.Expr, name string) bool, body []ast.Stmt, at func(ast.Node) span, diags *diagnostic.List) {
	if len(scope) == 0 || diags == nil {
		return
	}
	shadowed := make(map[string]bool, len(params))
	for name := range params {
		shadowed[name] = true
	}
	typeParamValueWalk{scope: scope, valueShadows: valueShadows, enumArm: enumArm, at: at, diags: diags}.stmts(body, shadowed)
}

// valueShadowPredicate combines the const-shadowing and namespace-import checks
// into the predicate the value-position walk consults: a name is read as a value,
// not the type parameter, when it is a top-level constant (constShadows) or a
// namespace import whose member the body reads (nsImport). Either is nil-safe.
func valueShadowPredicate(constShadows func(*ast.Identifier) bool, nsImport func(string) bool) func(*ast.Identifier) bool {
	return func(id *ast.Identifier) bool {
		if constShadows != nil && constShadows(id) {
			return true
		}
		return nsImport != nil && nsImport(id.Name)
	}
}

// enumArmPredicate reports whether a switch arm's bare name is a member of the
// enum its scrutinee resolves to, typing the scrutinee against the body scope —
// so a bare enum-member pattern is not mistaken for a value read of a same-named
// type parameter, while a value compared against a non-enum scrutinee still is.
func enumArmPredicate(bs infer.BodyScope) func(ast.Expr, string) bool {
	return func(scrutinee ast.Expr, name string) bool {
		def := enumDefOf(infer.Body(scrutinee, bs))
		return def != nil && enumIndex(def, name) >= 0
	}
}

// typeParamValueWalk walks a body for value-position type-parameter uses,
// threading the value names in scope so a binding that shadows a type parameter
// suppresses the report for the statements it scopes.
type typeParamValueWalk struct {
	scope infer.TypeScope
	// valueShadows reports whether a bare name resolves to a value the body binder
	// reads before reifying a type — a top-level constant, or a namespace import
	// whose member it reads — so the name shadows a same-named type parameter
	// exactly as a local binding does.
	valueShadows func(*ast.Identifier) bool
	// enumArm reports whether a name is a member of the enum the scrutinee resolves
	// to, so a switch arm's bare enum-member pattern is not read as a value use of a
	// same-named type parameter while a non-enum value pattern still is.
	enumArm func(scrutinee ast.Expr, name string) bool
	at      func(ast.Node) span
	diags   *diagnostic.List
}

// stmts walks a statement block with shadowed holding the value names in scope at
// its start. A let extends the scope for the statements after it (mutating the
// block's own set); a nested block — an if/for/switch/match body, or a lambda —
// receives a copy so its bindings do not leak to its siblings. The block's set is
// owned by this call, so a let mutates it in place.
func (w typeParamValueWalk) stmts(stmts []ast.Stmt, shadowed map[string]bool) {
	for _, stmt := range stmts {
		switch s := stmt.(type) {
		case *ast.ReturnStmt:
			w.expr(s.Value, shadowed)
		case *ast.ExprStmt:
			w.expr(s.X, shadowed)
		case *ast.LetStmt:
			w.expr(s.Value, shadowed) // the initializer is checked before the binding is in scope
			if s.Name != "" {
				shadowed[s.Name] = true
			}
		case *ast.AssignStmt:
			w.expr(s.Value, shadowed) // the target is a local name, not a type-parameter read
		case *ast.IfStmt:
			w.ifStmt(s, shadowed)
		case *ast.ForStmt:
			w.expr(s.Iter, shadowed)
			w.stmts(s.Body, withName(shadowed, s.Var)) // the loop variable binds in the body
		case *ast.SwitchStmt:
			w.expr(s.Scrutinee, shadowed)
			// An arm value that is a bare name matching a member of the scrutinee's
			// enum is an enum-member pattern, not a value read of a same-named type
			// parameter, so it is skipped; any other arm value (a value compared
			// against a non-enum scrutinee, a projection) is walked. The after-wildcard
			// arms are unreachable but still type-checked, so a use in one is reported.
			for _, arm := range append(append([]*ast.SwitchArm{}, s.Arms...), s.AfterElse...) {
				for _, v := range arm.Values {
					if id, ok := v.(*ast.Identifier); ok && w.enumArm != nil && w.enumArm(s.Scrutinee, id.Name) {
						continue
					}
					w.expr(v, shadowed)
				}
				w.stmts(arm.Body, withName(shadowed, ""))
			}
			w.stmts(s.Else, withName(shadowed, ""))
		case *ast.MatchStmt:
			w.expr(s.Scrutinee, shadowed)
			for _, arm := range append(append([]*ast.MatchArm{}, s.Arms...), s.AfterElse...) {
				w.stmts(arm.Body, withName(shadowed, arm.Bind)) // the arm binding narrows in the body
			}
			w.stmts(s.Else, withName(shadowed, ""))
		}
	}
}

// ifStmt walks an if's condition in the current scope and each branch in its own,
// recursing through the else-if chain — no branch introduces a binding visible to
// another, so each gets a copy of the scope.
func (w typeParamValueWalk) ifStmt(s *ast.IfStmt, shadowed map[string]bool) {
	w.expr(s.Cond, shadowed)
	w.stmts(s.Then, withName(shadowed, ""))
	if s.ElseIf != nil {
		w.ifStmt(s.ElseIf, shadowed)
	}
	w.stmts(s.Else, withName(shadowed, ""))
}

// expr walks one value expression, reporting a value-position type-parameter use
// that the current scope does not shadow. A bare-name call callee (a conversion
// T(x) or a function call) is a type or call use, not a value read, so it is
// skipped; a member access off a type parameter is the value-position projection
// T.member, reported without descending into the receiver (which would
// double-report the bare parameter); and a lambda body is walked with the
// lambda's parameters added to the scope, since the shared expression walk does
// not enter it.
func (w typeParamValueWalk) expr(e ast.Expr, shadowed map[string]bool) {
	if e == nil {
		return
	}
	callee := map[*ast.Identifier]bool{}
	ast.WalkExprs(e, func(e ast.Expr) bool {
		switch e := e.(type) {
		case *ast.CallExpr:
			if id, ok := e.Callee.(*ast.Identifier); ok {
				callee[id] = true
			}
		case *ast.FuncLit:
			inner := withName(shadowed, "")
			for _, p := range e.Params {
				inner[p.Name] = true
			}
			w.stmts(e.Body, inner)
			return false
		case *ast.MemberExpr:
			if recv, ok := e.Receiver.(*ast.Identifier); ok && w.flagged(recv, shadowed) {
				w.report(e, recv.Name)
				return false
			}
		case *ast.Identifier:
			if !callee[e] && w.flagged(e, shadowed) {
				w.report(e, e.Name)
			}
		}
		return true
	})
}

// flagged reports whether id names a type parameter in scope that no value
// shadows — neither a binding in scope here nor a top-level constant — the
// condition for a value-position use to be reported.
func (w typeParamValueWalk) flagged(id *ast.Identifier, shadowed map[string]bool) bool {
	if _, ok := w.scope[id.Name]; !ok {
		return false
	}
	if shadowed[id.Name] {
		return false
	}
	return w.valueShadows == nil || !w.valueShadows(id)
}

func (w typeParamValueWalk) report(node ast.Node, name string) {
	s := w.at(node)
	w.diags.Add(newTypeParamInValuePositionDiagnostic(s.offset, s.width, name))
}

// withName copies a scope's value-name set, adding name (when non-empty) — the
// copy a nested block receives so its bindings stay local to it.
func withName(shadowed map[string]bool, name string) map[string]bool {
	out := make(map[string]bool, len(shadowed)+1)
	for k := range shadowed {
		out[k] = true
	}
	if name != "" {
		out[name] = true
	}
	return out
}

// --- method bodies ----------------------------------------------------------

// methodTScope is the generic type-parameter scope in effect in a method body:
// the enclosing type's parameters (each with its resolved bound) plus the
// method's own explicit type parameters (fold<A>). A body type annotation naming
// one of these (a let, a match/switch arm) then resolves to a TypeVar rather than
// an unknown type. Each method type parameter's bound is resolved in the full
// scope and back-filled, the way the signature resolves it, so a body annotation
// projecting off a bounded method parameter (let y: T.x where T: HasX) sees the
// bound rather than an unbounded variable that has no members. A method-introduced
// inference variable (the implicit R in map(func: fn(T): R)) is deliberately not
// added: it is an inference hole, not a name a body annotation may pin, and
// adding it would risk shadowing a same-named real type.
func methodTScope(r *infer.TypeResolver, def *ir.TypeDef, m *ast.MethodDecl) infer.TypeScope {
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
	infer.SettleBounds(r, m.TypeParams, scope)
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
func checkMethodBodies(reg *builtin.Registry, defs []*ir.TypeDef, universe map[string]*ir.TypeDef, qualified func(namespace, name string) *ir.TypeDef, funcs map[string][]*ast.FuncDecl, qualifiedFuncs func(namespace, name string) []*ast.FuncDecl, constShadows func(*ast.Identifier) bool, nsImport func(string) bool, env exprFolder, sink *infer.Sink, at func(ast.Node) span, diags *diagnostic.List) {
	// The resolver that builds each method's body type-parameter scope: it resolves
	// a method type parameter's bound (fn g<T: HasX>) so a body annotation
	// projecting off it (let y: T.x) sees the bound, the same registry-backed
	// resolution the signature uses.
	tscopeResolver := &infer.TypeResolver{Defs: universe, Qualified: qualified, Registry: reg}
	valueShadows := valueShadowPredicate(constShadows, nsImport)
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
			bs := infer.BodyScope{Reg: reg, Universe: universe, Qualified: qualified, Self: selfT, Params: params, Funcs: funcs, QualifiedFuncs: qualifiedFuncs, ConstShadows: constShadows, TScope: methodTScope(tscopeResolver, def, m)}
			checkStmts(m.Body, want, bs, env, bodyNoSelf, sink, at, diags)
			checkBareEnumArgs(m.Body, bs, env, at, diags)
			reportTypeParamValueUse(bs.TScope, params, valueShadows, enumArmPredicate(bs), m.Body, at, diags)
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
func checkFuncBodies(reg *builtin.Registry, file *ast.File, universe map[string]*ir.TypeDef, qualified func(namespace, name string) *ir.TypeDef, funcs map[string][]*ast.FuncDecl, qualifiedFuncs func(namespace, name string) []*ast.FuncDecl, constShadows func(*ast.Identifier) bool, nsImport func(string) bool, env exprFolder, sink *infer.Sink, at func(ast.Node) span, diags *diagnostic.List) {
	// The resolver reports a failed field-type projection in a parameter or result
	// annotation (fn f(x: Item.nope)), so an invalid projection there surfaces the
	// same diagnostic it does in a type or const annotation rather than resolving
	// silently. The registry lets a bounded projection (fn f<T: HasX>(): T.x) read
	// the bound's readable member, the same as the top-level type-declaration path.
	// A sink-only walk (diags nil) keeps it silent.
	r := &infer.TypeResolver{Defs: universe, Qualified: qualified, Registry: reg, ProjectionError: projectionErrorReporter(at, diags)}
	valueShadows := valueShadowPredicate(constShadows, nsImport)
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
		bs := infer.BodyScope{Reg: reg, Universe: universe, Qualified: qualified, Self: ir.Invalid, Params: params, Funcs: funcs, QualifiedFuncs: qualifiedFuncs, ConstShadows: constShadows, TScope: tscope}
		checkStmts(fd.Body, want, bs, env, noSelf, sink, at, diags)
		if diags == nil {
			continue // the sink-only walk wants no further diagnostics
		}
		reportTypeParamValueUse(tscope, params, valueShadows, enumArmPredicate(bs), fd.Body, at, diags)
		// A function parameter or result may not be a type value: fn f(t: type) or
		// fn f(): type is type_in_value_position — there are no type-value functions,
		// which is why generics stay type parameters rather than type-value
		// parameters.
		ptypes := make([]ir.Type, len(fd.Params))
		for i, p := range fd.Params {
			ptypes[i] = params[p.Name]
		}
		reportMetatypeSlot(at, diags, fd, &ir.Func{Params: ptypes, Result: want})
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
func checkStmts(stmts []ast.Stmt, want ir.Type, bs infer.BodyScope, env exprFolder, noSelf func(ast.Node), sink *infer.Sink, at func(ast.Node) span, diags *diagnostic.List) {
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
			checkSwitchStmt(stmt, want, bs, env, noSelf, sink, at, diags)
		case *ast.MatchStmt:
			checkMatchStmt(stmt, want, bs, env, noSelf, sink, at, diags)
		case *ast.IfStmt:
			checkIf(stmt, want, bs, env, noSelf, sink, at, diags)
		case *ast.ForStmt:
			checkForStmt(stmt, want, bs, env, noSelf, sink, at, diags)
		default:
			panic(ast.UnhandledStmt(stmt))
		}
	}
}

// checkSwitchStmt is the *ast.SwitchStmt arm of checkStmts: it checks the
// switch's own diagnostics, then walks every arm body, the else, and the
// unreachable after-else arms (which still type-check so their own errors
// surface even though they can never run). A nil diagnostic list suppresses the
// switch diagnostics (the func-literal-types walk wants only the checking sink);
// the body walk still reaches every nested statement.
func checkSwitchStmt(stmt *ast.SwitchStmt, want ir.Type, bs infer.BodyScope, env exprFolder, noSelf func(ast.Node), sink *infer.Sink, at func(ast.Node) span, diags *diagnostic.List) {
	if noSelf != nil && stmt.Scrutinee != nil {
		checkNoSelf(stmt.Scrutinee, noSelf)
	}
	if diags != nil {
		checkSwitch(stmt, bs, env, sink, at, diags)
	}
	for _, arm := range stmt.Arms {
		checkStmts(arm.Body, want, bs, env, noSelf, sink, at, diags)
	}
	checkStmts(stmt.Else, want, bs, env, noSelf, sink, at, diags)
	for _, arm := range stmt.AfterElse {
		checkStmts(arm.Body, want, bs, env, noSelf, sink, at, diags)
	}
}

// checkMatchStmt is the *ast.MatchStmt arm of checkStmts: it checks the match's
// own diagnostics, then walks every arm body in the scope where its binding is
// narrowed to the arm's member type, the else, and the unreachable after-else
// arms (which still type-check so their own errors surface). A nil diagnostic
// list suppresses the match diagnostics (the func-literal-types walk wants only
// the checking sink); the body walk still reaches every nested statement.
func checkMatchStmt(stmt *ast.MatchStmt, want ir.Type, bs infer.BodyScope, env exprFolder, noSelf func(ast.Node), sink *infer.Sink, at func(ast.Node) span, diags *diagnostic.List) {
	if noSelf != nil && stmt.Scrutinee != nil {
		checkNoSelf(stmt.Scrutinee, noSelf)
	}
	if diags != nil {
		checkMatch(stmt, bs, sink, at, diags)
	}
	for _, arm := range stmt.Arms {
		checkStmts(arm.Body, want, armNarrowedScope(bs, arm), env, noSelf, sink, at, diags)
	}
	checkStmts(stmt.Else, want, bs, env, noSelf, sink, at, diags)
	for _, arm := range stmt.AfterElse {
		checkStmts(arm.Body, want, armNarrowedScope(bs, arm), env, noSelf, sink, at, diags)
	}
}

// checkForStmt is the *ast.ForStmt arm of checkStmts: it checks the loop's own
// diagnostics, then walks its body in the scope where the loop variable is bound
// to its element type, so a reference to it resolves at that type. A nil
// diagnostic list suppresses the for diagnostics (the func-literal-types walk
// wants only the checking sink); the body walk still reaches every nested
// statement.
func checkForStmt(stmt *ast.ForStmt, want ir.Type, bs infer.BodyScope, env exprFolder, noSelf func(ast.Node), sink *infer.Sink, at func(ast.Node) span, diags *diagnostic.List) {
	if noSelf != nil && stmt.Iter != nil {
		checkNoSelf(stmt.Iter, noSelf)
	}
	if diags != nil {
		checkFor(stmt, bs, sink, at, diags)
	}
	checkStmts(stmt.Body, want, forNarrowedScope(bs, stmt), env, noSelf, sink, at, diags)
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

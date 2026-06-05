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
	for _, a := range call.Arguments {
		checkDivByZero(a, env, report)
	}
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
				if v := eval.ExprIn(s.Value, locals, env); v != nil {
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
		}
	}
}

// reportIndexWrite reports an out-of-range list write for an assignment whose
// value is a set call on a foldable list. The set call carries the index and the
// new value (a desugared coll[i] = v, or a hand-written coll = coll.set(i, v));
// when the receiver folds to a list and the index to a constant outside it, the
// write is reported at the index expression. A map receiver (keyed entries) is an
// upsert and never out of range.
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
	if recv == nil || recv.Kind != ir.ConstCollection || isMapConst(recv) {
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

// isMapConst reports whether a folded collection constant is a map — its entries
// carry keys. An empty collection has no key, so it reads as a list (the
// conservative default eval uses for the same ambiguity).
func isMapConst(c *ir.Constant) bool {
	for _, e := range c.Coll {
		if e.Key != nil {
			return true
		}
	}
	return false
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

// forEachBodyExpr calls fn for every top expression of a statement body — a
// return value, an expression statement, and a switch's scrutinee, arm value
// patterns, and (recursively) its arm and wildcard bodies — so an expression
// walk over a body reaches into the control flow a switch introduces.
func forEachBodyExpr(body []ast.Stmt, fn func(ast.Expr)) {
	for _, stmt := range body {
		switch stmt := stmt.(type) {
		case *ast.ReturnStmt:
			if stmt.Value != nil {
				fn(stmt.Value)
			}
		case *ast.ExprStmt:
			if stmt.X != nil {
				fn(stmt.X)
			}
		case *ast.LetStmt:
			if stmt.Value != nil {
				fn(stmt.Value)
			}
		case *ast.AssignStmt:
			if stmt.Value != nil {
				fn(stmt.Value)
			}
		case *ast.SwitchStmt:
			if stmt.Scrutinee != nil {
				fn(stmt.Scrutinee)
			}
			for _, arm := range stmt.Arms {
				for _, v := range arm.Values {
					fn(v)
				}
				forEachBodyExpr(arm.Body, fn)
			}
			forEachBodyExpr(stmt.Else, fn)
			for _, arm := range stmt.AfterElse {
				for _, v := range arm.Values {
					fn(v)
				}
				forEachBodyExpr(arm.Body, fn)
			}
		case *ast.IfStmt:
			forEachIfExpr(stmt, fn)
		}
	}
}

// forEachIfExpr calls fn for every top expression of an if statement — its
// condition and (recursively) the top expressions of its then body, its else-if
// chain, and its else body — so an expression walk over a body reaches into the
// control flow an if introduces.
func forEachIfExpr(s *ast.IfStmt, fn func(ast.Expr)) {
	if s.Cond != nil {
		fn(s.Cond)
	}
	forEachBodyExpr(s.Then, fn)
	if s.ElseIf != nil {
		forEachIfExpr(s.ElseIf, fn)
	}
	forEachBodyExpr(s.Else, fn)
}

// --- method bodies ----------------------------------------------------------

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
	for _, def := range defs {
		self := &ir.Named{Def: def}
		for _, irm := range def.Methods {
			m := irm.Syntax
			if m == nil || len(m.Body) == 0 {
				continue // an extern or empty body has nothing to check
			}
			params := make(map[string]ir.Type, len(irm.Params))
			for _, p := range irm.Params {
				params[p.Name] = substSelf(p.Type, self)
			}
			want := substSelf(irm.Result, self)
			bs := infer.BodyScope{Reg: reg, Universe: universe, Qualified: qualified, Self: self, Params: params, Funcs: funcs, QualifiedFuncs: qualifiedFuncs}
			checkStmts(m.Body, want, bs, env, nil, sink, at, diags)
			checkIndexWrites(m.Body, env, at, diags)
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
func checkFuncBodies(reg *builtin.Registry, file *ast.File, universe map[string]*ir.TypeDef, qualified func(namespace, name string) *ir.TypeDef, funcs map[string][]*ast.FuncDecl, qualifiedFuncs func(namespace, name string) []*ast.FuncDecl, env eval.Env, at func(ast.Node) span, diags *diagnostic.List) {
	sink := exprSink(at, diags)
	r := &infer.TypeResolver{Defs: universe, Qualified: qualified}
	noSelf := func(node ast.Node) {
		s := at(node)
		diags.Add(newSelfOutsideMethodDiagnostic(s.offset, s.width))
	}
	for _, fd := range file.Funcs {
		// The function's generic type parameters are in scope for its parameter
		// and result annotations, so a `c: T` where `T: foldable<int>` resolves
		// to a bounded TypeVar — the body may then call the bound interface's
		// methods on it, and nothing else.
		tscope := infer.FuncTypeParamScope(fd.TypeParams)
		params := make(map[string]ir.Type, len(fd.Params))
		for _, p := range fd.Params {
			params[p.Name] = r.ResolveType(p.Type, tscope)
		}
		want := r.ResolveType(fd.Result, tscope)
		bs := infer.BodyScope{Reg: reg, Universe: universe, Qualified: qualified, Self: ir.Invalid, Params: params, Funcs: funcs, QualifiedFuncs: qualifiedFuncs}
		checkStmts(fd.Body, want, bs, env, noSelf, sink, at, diags)
		checkIndexWrites(fd.Body, env, at, diags)
		if hasBlockBody(fd) && !bodyReturns(fd.Body, scrutEnumOf(bs)) {
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
			bs = checkLet(stmt, bs, noSelf, sink, at, diags)
		case *ast.AssignStmt:
			checkAssign(stmt, bs, env, noSelf, sink, at, diags)
		case *ast.ExprStmt:
			if noSelf != nil {
				checkNoSelf(stmt.X, noSelf)
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
		case *ast.IfStmt:
			checkIf(stmt, want, bs, env, noSelf, sink, at, diags)
		}
	}
}

// scrutEnumOf builds the scrutinee-enum resolver return analysis uses: it reads
// the enum a scrutinee's static type names from the body scope — a parameter's
// type, the receiver's type for self — without the type query, exactly as the
// body binder does when lowering a switch's bare-member arms.
func scrutEnumOf(bs infer.BodyScope) func(ast.Expr) *ir.TypeDef {
	return func(scrutinee ast.Expr) *ir.TypeDef {
		switch e := scrutinee.(type) {
		case *ast.Identifier:
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

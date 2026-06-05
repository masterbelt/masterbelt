package semantic

import (
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
		params := make(map[string]ir.Type, len(fd.Params))
		for _, p := range fd.Params {
			params[p.Name] = r.ResolveType(p.Type, nil)
		}
		want := r.ResolveType(fd.Result, nil)
		bs := infer.BodyScope{Reg: reg, Universe: universe, Qualified: qualified, Self: ir.Invalid, Params: params, Funcs: funcs, QualifiedFuncs: qualifiedFuncs}
		checkStmts(fd.Body, want, bs, env, noSelf, sink, at, diags)
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

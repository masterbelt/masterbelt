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
		for _, stmt := range lit.Body {
			switch stmt := stmt.(type) {
			case *ast.ReturnStmt:
				if stmt.Value != nil {
					checkDivByZero(stmt.Value, env, report)
				}
			case *ast.ExprStmt:
				checkDivByZero(stmt.X, env, report)
			}
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
			for _, stmt := range x.Body {
				switch s := stmt.(type) {
				case *ast.ReturnStmt:
					checkNoSelf(s.Value, report)
				case *ast.ExprStmt:
					checkNoSelf(s.X, report)
				}
			}
		}
		return true
	})
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
// resolves exactly as an annotation does.
func checkMethodBodies(reg *builtin.Registry, defs []*ir.TypeDef, universe map[string]*ir.TypeDef, qualified func(namespace, name string) *ir.TypeDef, funcs map[string]*ast.FuncDecl, sink *infer.Sink) {
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
			bs := infer.BodyScope{Reg: reg, Universe: universe, Qualified: qualified, Self: self, Params: params, Funcs: funcs}
			for _, stmt := range m.Body {
				ret, ok := stmt.(*ast.ReturnStmt)
				if !ok || ret.Value == nil {
					continue
				}
				infer.CheckBody(ret.Value, want, bs, sink)
			}
		}
	}
}

// checkFuncBodies type-checks each function body's returned value against the
// declared result type — the same checking walk a method body uses, with no
// receiver (self is ir.Invalid) — and reports a body that never returns a
// value (missing_return): with no control flow yet, a function produces its
// declared result exactly when some return carries a value. A declaration
// whose body is missing altogether is a parse error, not a missing return.
func checkFuncBodies(reg *builtin.Registry, file *ast.File, universe map[string]*ir.TypeDef, qualified func(namespace, name string) *ir.TypeDef, funcs map[string]*ast.FuncDecl, at func(ast.Node) span, diags *diagnostic.List) {
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
		bs := infer.BodyScope{Reg: reg, Universe: universe, Qualified: qualified, Self: ir.Invalid, Params: params, Funcs: funcs}
		returned := false
		for _, stmt := range fd.Body {
			switch stmt := stmt.(type) {
			case *ast.ReturnStmt:
				if stmt.Value == nil {
					continue
				}
				returned = true
				checkNoSelf(stmt.Value, noSelf)
				infer.CheckBody(stmt.Value, want, bs, sink)
			case *ast.ExprStmt:
				checkNoSelf(stmt.X, noSelf)
			}
		}
		if !returned && hasBlockBody(fd) {
			s := at(fd)
			diags.Add(newMissingReturnDiagnostic(s.offset, s.width, fd.Name))
		}
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

package semantic

import (
	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/eval"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/types/infer"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
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

// --- method bodies ----------------------------------------------------------

// checkMethodBodies type-checks each method body's returned value against the
// method's declared result type, reporting through sink. It runs after
// resolveTypes, so defs are in file.Types order and each method lines up with
// its resolved signature. The walk is the same checking walk the const path
// uses (infer.CheckBody), so the declared result type reaches into a returned
// function or collection literal.
func checkMethodBodies(file *ast.File, reg *builtin.Registry, defs []*ir.TypeDef, sink *infer.Sink) {
	universe := make(map[string]*ir.TypeDef, len(defs))
	for _, d := range defs {
		if d.Name != "" {
			universe[d.Name] = d
		}
	}
	for i, td := range file.Types {
		def := defs[i]
		self := &ir.Named{Def: def}
		for j, m := range td.Methods {
			if len(m.Body) == 0 || j >= len(def.Methods) {
				continue // an extern or empty body has nothing to check
			}
			irm := def.Methods[j]
			params := make(map[string]ir.Type, len(irm.Params))
			for _, p := range irm.Params {
				params[p.Name] = substSelf(p.Type, self)
			}
			want := substSelf(irm.Result, self)
			bs := infer.BodyScope{Reg: reg, Universe: universe, Self: self, Params: params}
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

// substSelf substitutes the self type for ir.SelfType.
func substSelf(t, self ir.Type) ir.Type {
	if _, ok := t.(*ir.SelfType); ok {
		return self
	}
	return t
}

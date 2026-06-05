package semantic

import (
	"maps"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/types"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/types/infer"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// Effects are declared, never silently inferred: a function or method writes
// its effect list in its signature, and the call-graph check here verifies
// the declaration is complete. The used effects of a body — an await uses
// async, a call uses the callee's declared effects — must be a subset of the
// declared ones (missing_effect), and a declared effect must be used
// (unused_effect, a warning that keeps signatures canonical). An extern
// declaration is a root: its effects are axiomatic, so it is never checked.

// checkEffects runs the declaration-completeness check over a file's
// functions and its types' methods.
func checkEffects(reg *builtin.Registry, file *ast.File, defs []*ir.TypeDef, universe map[string]*ir.TypeDef, qualified func(namespace, name string) *ir.TypeDef, funcs map[string][]*ast.FuncDecl, qualifiedFuncs func(namespace, name string) []*ast.FuncDecl, at func(ast.Node) span, diags *diagnostic.List) {
	r := &infer.TypeResolver{Defs: universe, Qualified: qualified}
	for _, fd := range file.Funcs {
		if fd.Extern || len(fd.Body) == 0 {
			continue // a root's effects are axiomatic; a missing body was reported
		}
		params := make(map[string]ir.Type, len(fd.Params))
		for _, p := range fd.Params {
			params[p.Name] = r.ResolveType(p.Type, nil)
		}
		bs := infer.BodyScope{Reg: reg, Universe: universe, Qualified: qualified, Self: ir.Invalid, Params: params, Funcs: funcs, QualifiedFuncs: qualifiedFuncs}
		checkDeclEffects(fd.Name, fd.Effects, fd, fd.Body, bs, at, diags)
	}
	for _, def := range defs {
		self := &ir.Named{Def: def}
		for _, irm := range def.Methods {
			m := irm.Syntax
			if m == nil || m.Extern || len(m.Body) == 0 {
				continue
			}
			params := make(map[string]ir.Type, len(irm.Params))
			for _, p := range irm.Params {
				params[p.Name] = substSelf(p.Type, self)
			}
			bs := infer.BodyScope{Reg: reg, Universe: universe, Qualified: qualified, Self: self, Params: params, Funcs: funcs, QualifiedFuncs: qualifiedFuncs}
			checkDeclEffects(def.Name+"."+irm.Name, m.Effects, m, m.Body, bs, at, diags)
		}
	}
}

// checkDeclEffects compares one body's used effects with its declaration's:
// an effect used but not declared is missing_effect, anchored at the first
// site that uses it; one declared but never used is unused_effect, a warning
// at the declaration.
func checkDeclEffects(name string, declared []string, decl ast.Node, body []ast.Stmt, bs infer.BodyScope, at func(ast.Node) span, diags *diagnostic.List) {
	has := make(map[string]bool, len(declared))
	for _, eff := range declared {
		has[eff] = true
	}
	used := map[string]bool{}
	use := func(effect string, node ast.Node) {
		if !has[effect] && !used[effect] {
			s := at(node)
			diags.Add(newMissingEffectDiagnostic(s.offset, s.width, name, effect))
		}
		used[effect] = true
	}
	collectBodyEffectUses(body, bs, use)
	for _, eff := range declared {
		if !used[eff] {
			s := at(decl)
			diags.Add(newUnusedEffectDiagnostic(s.offset, s.width, name, eff))
		}
	}
}

// collectBodyEffectUses walks a statement body collecting its effect uses,
// descending through a switch's scrutinee, arm value patterns, arm bodies, and
// wildcard body so an effectful call anywhere in a switch counts.
func collectBodyEffectUses(body []ast.Stmt, bs infer.BodyScope, use func(effect string, node ast.Node)) {
	for _, stmt := range body {
		switch stmt := stmt.(type) {
		case *ast.ReturnStmt:
			collectEffectUses(stmt.Value, bs, use)
		case *ast.ExprStmt:
			collectEffectUses(stmt.X, bs, use)
		case *ast.SwitchStmt:
			collectEffectUses(stmt.Scrutinee, bs, use)
			for _, arm := range stmt.Arms {
				for _, v := range arm.Values {
					collectEffectUses(v, bs, use)
				}
				collectBodyEffectUses(arm.Body, bs, use)
			}
			collectBodyEffectUses(stmt.Else, bs, use)
			for _, arm := range stmt.AfterElse {
				for _, v := range arm.Values {
					collectEffectUses(v, bs, use)
				}
				collectBodyEffectUses(arm.Body, bs, use)
			}
		case *ast.IfStmt:
			collectIfEffectUses(stmt, bs, use)
		}
	}
}

// collectIfEffectUses descends through an if's condition, then body, else-if
// chain, and else body, so an effectful call anywhere in the control flow counts
// toward the enclosing declaration's used effects.
func collectIfEffectUses(s *ast.IfStmt, bs infer.BodyScope, use func(effect string, node ast.Node)) {
	collectEffectUses(s.Cond, bs, use)
	collectBodyEffectUses(s.Then, bs, use)
	if s.ElseIf != nil {
		collectIfEffectUses(s.ElseIf, bs, use)
	}
	collectBodyEffectUses(s.Else, bs, use)
}

// collectEffectUses walks an expression collecting the effects it uses: an
// await uses async, a call of a top-level function (by name, or through a
// namespace import) uses the callee's declared effects, and a method call
// the resolved method's. A function literal's body counts toward the
// enclosing declaration — effects are monomorphic — with the literal's
// parameters shadowing same-named functions and types, exactly as in typing.
func collectEffectUses(e ast.Expr, bs infer.BodyScope, use func(effect string, node ast.Node)) {
	switch e := e.(type) {
	case nil:
		return
	case *ast.AwaitExpr:
		// The explicit suspension point: awaiting is itself the async effect.
		use("async", e)
		collectEffectUses(e.Value, bs, use)
	case *ast.FuncLit:
		inner := bs
		inner.Params = maps.Clone(bs.Params)
		if inner.Params == nil {
			inner.Params = map[string]ir.Type{}
		}
		for _, p := range e.Params {
			inner.Params[p.Name] = ir.Invalid
		}
		collectBodyEffectUses(e.Body, inner, use)
	case *ast.CollectionLit:
		for _, entry := range e.Entries {
			collectEffectUses(entry.Key, bs, use)
			collectEffectUses(entry.Value, bs, use)
		}
	case *ast.RecordLit:
		for _, f := range e.Fields {
			collectEffectUses(f.Value, bs, use)
		}
	case *ast.MemberExpr:
		collectEffectUses(e.Receiver, bs, use)
	case *ast.CallExpr:
		switch callee := e.Callee.(type) {
		case *ast.Identifier:
			// A parameter shadows a same-named function, and a type name is a
			// conversion (no effects), exactly as the type rules order it.
			if _, isParam := bs.Params[callee.Name]; !isParam {
				if _, isType := bs.Universe[callee.Name]; !isType {
					for _, eff := range declaredEffects(bs.Funcs[callee.Name]) {
						use(eff, e)
					}
				}
			}
		case *ast.MemberExpr:
			// A namespace function call (geo.area(...)) uses the imported
			// function's effects; any other member callee is a method call,
			// resolved by the receiver's type.
			if recv, ok := callee.Receiver.(*ast.Identifier); ok && bs.QualifiedFuncs != nil {
				if _, isParam := bs.Params[recv.Name]; !isParam {
					if fds := bs.QualifiedFuncs(recv.Name, callee.Member.Name); len(fds) > 0 {
						for _, eff := range declaredEffects(fds) {
							use(eff, e)
						}
						break
					}
				}
			}
			recvT := infer.Body(callee.Receiver, bs)
			if ms, _, ok := types.Candidates(bs.Reg, recvT, callee.Member.Name); ok {
				seen := map[string]bool{}
				for _, m := range ms {
					for _, eff := range m.Effects {
						if !seen[eff] {
							seen[eff] = true
							use(eff, e)
						}
					}
				}
			}
			collectEffectUses(callee.Receiver, bs, use)
		}
		for _, a := range e.Arguments {
			collectEffectUses(a, bs, use)
		}
	}
}

// declaredEffects is the union of an overload set's declared effects, in
// first-seen order — the conservative set a call of the name may use.
func declaredEffects(fds []*ast.FuncDecl) []string {
	var out []string
	seen := map[string]bool{}
	for _, fd := range fds {
		for _, eff := range fd.Effects {
			if !seen[eff] {
				seen[eff] = true
				out = append(out, eff)
			}
		}
	}
	return out
}

// checkPureContext reports every effect used in a compile-time-evaluated
// expression. These positions — a constant initializer, an assert condition —
// fold to values, so an effectful call cannot appear in them at all: pure
// folds, effectful cannot even be written. Each effect is reported once, at
// the first site that uses it.
func checkPureContext(e ast.Expr, context string, bs infer.BodyScope, at func(ast.Node) span, diags *diagnostic.List) {
	reported := map[string]bool{}
	collectEffectUses(e, bs, func(effect string, node ast.Node) {
		if reported[effect] {
			return
		}
		reported[effect] = true
		s := at(node)
		diags.Add(newEffectInPureContextDiagnostic(s.offset, s.width, effect, context))
	})
}

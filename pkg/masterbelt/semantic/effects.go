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
	// The body walk is the shared ast.WalkBodyExprs skeleton — the one place a
	// new statement form is wired in — so effect collection cannot drift out of
	// sync with it or silently skip a kind. WalkBodyExprs yields the top
	// expression of every statement (a return value, a let initializer, an
	// assignment's target and value, a switch's scrutinee and arm patterns, an
	// if's condition) across the nested control flow; collectEffectUses gathers
	// the effects of each. (It also yields an assignment's target — an
	// identifier — which collectEffectUses treats as a no-op, exactly right.)
	ast.WalkBodyExprs(body, func(e ast.Expr) {
		collectEffectUses(e, bs, use)
	})
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
	case *ast.TernaryExpr:
		// A ternary's condition and both branches run (one at compile time, but
		// the type and effect surface spans both), so an effect anywhere in it
		// counts — mirroring ast.WalkExprs, which descends all three operands.
		collectEffectUses(e.Cond, bs, use)
		collectEffectUses(e.Then, bs, use)
		collectEffectUses(e.Else, bs, use)
	case *ast.MemberExpr:
		collectEffectUses(e.Receiver, bs, use)
	case *ast.CallExpr:
		collectCallEffectUses(e, bs, use)
	}
}

// collectCallEffectUses gathers the effects of a call: a name callee uses the
// top-level overload set's effects, a member callee a namespace function's, a
// static fn's, or a method's — then the arguments. A static fn call short-
// circuits (it already descended its arguments), so it returns early.
func collectCallEffectUses(e *ast.CallExpr, bs infer.BodyScope, use func(effect string, node ast.Node)) {
	switch callee := e.Callee.(type) {
	case *ast.Identifier:
		collectNameCallEffectUses(e, callee, bs, use)
	case *ast.MemberExpr:
		if collectNamespaceCallEffectUses(e, callee, bs, use) {
			break
		}
		if collectStaticCallEffectUses(e, callee, bs, use) {
			return // a static fn call already descended its arguments
		}
		collectMethodCallEffectUses(e, callee, bs, use)
		collectEffectUses(callee.Receiver, bs, use)
	}
	for _, a := range e.Arguments {
		collectEffectUses(a, bs, use)
	}
}

// collectNameCallEffectUses handles a call of a bare name: a parameter shadows
// a same-named function, and a type name is a conversion (no effects), exactly
// as the type rules order it.
func collectNameCallEffectUses(e *ast.CallExpr, callee *ast.Identifier, bs infer.BodyScope, use func(effect string, node ast.Node)) {
	if _, isParam := bs.Params[callee.Name]; isParam {
		return
	}
	if _, isType := bs.Universe[callee.Name]; isType {
		return
	}
	for _, eff := range declaredEffects(bs.Funcs[callee.Name]) {
		use(eff, e)
	}
}

// collectNamespaceCallEffectUses handles a namespace function call
// (geo.area(...)): it uses the imported function's effects. It reports whether
// the callee resolved to a namespace function (and the member arms are done).
func collectNamespaceCallEffectUses(e *ast.CallExpr, callee *ast.MemberExpr, bs infer.BodyScope, use func(effect string, node ast.Node)) bool {
	recv, ok := callee.Receiver.(*ast.Identifier)
	if !ok || bs.QualifiedFuncs == nil {
		return false
	}
	if _, isParam := bs.Params[recv.Name]; isParam {
		return false
	}
	fds := bs.QualifiedFuncs(recv.Name, callee.Member.Name)
	if len(fds) == 0 {
		return false
	}
	for _, eff := range declaredEffects(fds) {
		use(eff, e)
	}
	return true
}

// collectStaticCallEffectUses handles a static fn call (Type.name(...)): it
// uses the static fn's declared effects, the same rule a top-level function
// call follows; a local or parameter of the type name shadows the type (it is
// a value receiver). It reports whether the call resolved to a static fn, in
// which case it has already descended the arguments.
func collectStaticCallEffectUses(e *ast.CallExpr, callee *ast.MemberExpr, bs infer.BodyScope, use func(effect string, node ast.Node)) bool {
	recv, ok := callee.Receiver.(*ast.Identifier)
	if !ok {
		return false
	}
	if _, isParam := bs.Params[recv.Name]; isParam {
		return false
	}
	def := bs.Universe[recv.Name]
	if def == nil {
		return false
	}
	used := staticEffects(def, callee.Member.Name)
	if len(used) == 0 {
		return false
	}
	for _, eff := range used {
		use(eff, e)
	}
	for _, a := range e.Arguments {
		collectEffectUses(a, bs, use)
	}
	return true
}

// collectMethodCallEffectUses handles a method call: the union of the
// resolved method candidates' declared effects, each reported once.
func collectMethodCallEffectUses(e *ast.CallExpr, callee *ast.MemberExpr, bs infer.BodyScope, use func(effect string, node ast.Node)) {
	recvT := infer.Body(callee.Receiver, bs)
	ms, _, ok := types.Candidates(bs.Reg, recvT, callee.Member.Name)
	if !ok {
		return
	}
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

// staticEffects is the union of a type's static fns of the given name's declared
// effects, in first-seen order — the conservative set a Type.name(...) call may
// use, mirroring declaredEffects for a top-level overload set.
func staticEffects(def *ir.TypeDef, name string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range def.Methods {
		if m.Kind != ir.MethodStatic || m.Name != name {
			continue
		}
		for _, eff := range m.Effects {
			if !seen[eff] {
				seen[eff] = true
				out = append(out, eff)
			}
		}
	}
	return out
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

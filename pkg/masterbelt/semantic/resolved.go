// This file carries the checker-selected overloads from the checking walk
// into the IR and the folder. The IR's doctrine is that every reference is
// bound to its declaration; an overloaded call lowered type-blind carries only
// a name, so after the checking walks run, writeBackResolutions binds each
// call node to the individual the checker selected (ir.Call.Resolved and
// friends — the .ir dump renders the selection, and codegen and the structural
// editors are its future readers). resolvedEnv then arms the same selections
// as an eval.CallResolver for the late re-fold: a call the value-kind rule
// cannot split folds by the checker's choice, monotonically widening the
// foldable set without growing a second type system inside eval.
package semantic

import (
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// callResolutions accumulates the overload selections the checking walk
// streams out (Sink.ResolvedMethod / ResolvedStatic / ResolvedFunc), keyed by
// the call expression. Only calls whose name carries several signatures
// appear; a single-signature call needs no selection. substs accumulates the
// CallSubst stream the same way: the type-variable solution of every call
// that pinned at least one variable, the write-back's source for
// ir.Call.Subst and friends. types accumulates the Typed stream — every
// expression's settled type — the typed-value-graph write-back's source.
type callResolutions struct {
	methods map[*ast.CallExpr]*ir.Method
	statics map[*ast.CallExpr]*ir.Method
	funcs   map[*ast.CallExpr]*ast.FuncDecl
	substs  map[*ast.CallExpr]map[string]ir.Type
	types   map[ast.Expr]ir.Type
}

func newCallResolutions() *callResolutions {
	return &callResolutions{
		methods: map[*ast.CallExpr]*ir.Method{},
		statics: map[*ast.CallExpr]*ir.Method{},
		funcs:   map[*ast.CallExpr]*ast.FuncDecl{},
		substs:  map[*ast.CallExpr]map[string]ir.Type{},
		types:   map[ast.Expr]ir.Type{},
	}
}

// resolvedEnv arms the collected selections on top of the ordinary evaluation
// environment, satisfying eval.CallResolver — the channel the late re-fold
// reads. The embedded evalEnv keeps every other capability (resolution, the
// syntactic ReceiverTyper channels) untouched; ValueOf alone is widened: a
// reference to one of this file's own constants reads through the published
// Eval when the type-blind value query folds nothing, so a reader of a
// constant the late re-fold settled folds too (the caller fixpoints the
// re-fold, so declaration order does not matter). A cross-file constant stays
// the query's verdict — deterministic whatever order the program's files
// assemble in.
type resolvedEnv struct {
	evalEnv
	res *callResolutions
	own map[*ast.ConstDecl]*ir.Const // this file's shells, published Eval included
}

func (e resolvedEnv) ValueOf(decl *ast.ConstDecl) *ir.Constant {
	if v := e.q.valueOf(decl); v != nil {
		return v
	}
	if c := e.own[decl]; c != nil {
		return c.Eval
	}
	return nil
}

func (e resolvedEnv) ResolvedFunc(call *ast.CallExpr) *ast.FuncDecl { return e.res.funcs[call] }

func (e resolvedEnv) ResolvedMethod(call *ast.CallExpr) *ast.MethodDecl {
	if m := e.res.methods[call]; m != nil {
		return m.Syntax
	}
	return nil
}

func (e resolvedEnv) ResolvedStatic(call *ast.CallExpr) *ast.MethodDecl {
	if m := e.res.statics[call]; m != nil {
		return m.Syntax
	}
	return nil
}

// writeBackResolutions binds every call node in the module to its
// checker-selected overload: ir.Call.Resolved and ir.StaticCall.Resolved take
// the selected method, and an overloaded ir.FuncCall takes the selected
// function as both Resolved and Target — correcting the type-blind lowering's
// arity-based guess. Each call form also takes the checker's solved
// type-variable substitution (Subst), the monomorphization input, and every
// value node takes its settled type (the typed value graph, F-3 §2.1): a node
// with a Syntax key reads the checker's Typed stream, and a binding reference
// (a parameter, a local, self) reads its binding's type off the enclosing
// signature or introducing statement — the same fact the checker's scope
// carried, derived instead of streamed because the checker may resolve such a
// reference without re-typing its expression. It walks every value position
// the module carries: the constants' value graphs and the function and method
// bodies (an associated constant carries no value graph — only its folded
// value — so there is nothing to bind there).
func writeBackResolutions(module *ir.Module, res *callResolutions, fnShells map[*ast.FuncDecl]*ir.Function) {
	w := resolutionWriter{res: res, fnShells: fnShells}
	for _, c := range module.Consts {
		if c != nil {
			w.value(c.Value, bindings{})
		}
	}
	for _, fn := range module.Funcs {
		if fn != nil {
			w.stmts(fn.Body, bindings{params: bindParams(fn.Params, nil)})
		}
	}
	for _, def := range module.Types {
		self := ir.Type(&ir.Named{Def: def})
		for _, m := range def.Methods {
			// A static fn has no receiver: its body's self stays untyped (a
			// self there is reported as self_outside_method), exactly as the
			// checker walks it with self unbound.
			mself := self
			if m.Kind == ir.MethodStatic {
				mself = nil
			}
			w.stmts(m.Body, bindings{self: mself, params: bindParams(m.Params, self)})
		}
	}
}

// bindParams maps a signature's parameters to their declared types, with a
// self-typed parameter resolved to the enclosing type — the same reading the
// checker's body scope uses.
func bindParams(params []ir.Param, self ir.Type) map[string]ir.Type {
	if len(params) == 0 {
		return nil
	}
	out := make(map[string]ir.Type, len(params))
	for _, p := range params {
		t := p.Type
		if _, isSelf := t.(*ir.SelfType); isSelf && self != nil {
			t = self
		}
		out[p.Name] = t
	}
	return out
}

// bindings is the binding context the write-back walks a body under: the
// receiver's type, the enclosing signature's parameter types, and the
// let/match/for locals in scope — what a SelfValue, ParamRef, or LocalRef
// node's settled type reads from. The maps extend copy-on-write as the walk
// descends (withLocal), so a block's binding never leaks to its siblings,
// mirroring the scoping the lowering and the checker share.
type bindings struct {
	self   ir.Type
	params map[string]ir.Type
	locals map[string]ir.Type
}

// withLocal returns the context extended with one local binding, copying the
// locals map so the extension reaches only the statements it scopes over.
func (bd bindings) withLocal(name string, typ ir.Type) bindings {
	locals := make(map[string]ir.Type, len(bd.locals)+1)
	for k, v := range bd.locals {
		locals[k] = v
	}
	locals[name] = typ
	bd.locals = locals
	return bd
}

type resolutionWriter struct {
	res      *callResolutions
	fnShells map[*ast.FuncDecl]*ir.Function
}

// value binds one value graph's nodes to the checker's facts — the overload
// selections, the solved substitutions, and the settled types — recursing
// through every composite form. Every assignment is unconditional: a
// method-body node lives on the memoized type definition, so a fact written
// by an earlier assemble must be cleared when the current walk recorded none
// rather than surviving stale. The switch lists every form explicitly so an
// omission stays deliberate.
func (w resolutionWriter) value(v ir.Value, bd bindings) {
	switch v := v.(type) {
	case *ir.Call:
		v.Resolved = w.res.methods[v.Syntax]
		v.Subst = w.res.substs[v.Syntax]
		w.value(v.Receiver, bd)
		for _, a := range v.Args {
			w.value(a, bd)
		}
		if v.Setter && v.Syntax == nil {
			// The synthetic call a property write lowers to has no call
			// expression of its own; it computes the receiver local's next
			// value, so its type is the receiver's.
			v.Type = ir.TypeOf(v.Receiver)
			return
		}
		v.Type = w.res.types[v.Syntax]
	case *ir.FuncCall:
		v.Resolved = nil
		if fd := w.res.funcs[v.Syntax]; fd != nil {
			if fn := w.fnShells[fd]; fn != nil {
				v.Resolved = fn
				v.Target = fn
			}
		}
		v.Subst = w.res.substs[v.Syntax]
		v.Type = w.res.types[v.Syntax]
		for _, a := range v.Args {
			w.value(a, bd)
		}
	case *ir.StaticCall:
		v.Resolved = w.res.statics[v.Syntax]
		v.Subst = w.res.substs[v.Syntax]
		v.Type = w.res.types[v.Syntax]
		for _, a := range v.Args {
			w.value(a, bd)
		}
	case *ir.Apply:
		v.Type = w.res.types[v.Syntax]
		w.value(v.Callee, bd)
		for _, a := range v.Args {
			w.value(a, bd)
		}
	case *ir.CollectionLiteral:
		v.Type = w.res.types[v.Syntax]
		for _, e := range v.Entries {
			w.value(e.Key, bd)
			w.value(e.Value, bd)
		}
	case *ir.RecordValue:
		v.Type = w.res.types[v.Syntax]
		for _, f := range v.Fields {
			w.value(f.Value, bd)
		}
	case *ir.Conversion:
		// Born typed: its target is its type. Only the arguments take facts.
		for _, a := range v.Args {
			w.value(a, bd)
		}
	case *ir.FieldAccess:
		v.Type = w.res.types[v.Syntax]
		w.value(v.Receiver, bd)
	case *ir.Await:
		// await adds nothing to its operand's type.
		w.value(v.Value, bd)
		v.Type = ir.TypeOf(v.Value)
	case *ir.Ternary:
		v.Type = w.res.types[v.Syntax]
		w.value(v.Cond, bd)
		w.value(v.Then, bd)
		w.value(v.Else, bd)
	case *ir.RangeLit:
		// Every range literal is the range builtin, whatever its bounds.
		v.Type = &ir.Builtin{Name: "range"}
		w.value(v.Lower, bd)
		w.value(v.Upper, bd)
	case *ir.FuncLiteral:
		// The literal's own type is the checker-solved signature; its body
		// then walks with the parameters bound at the solved types (the
		// capture of the enclosing scope rides along in bd).
		v.Type = w.res.types[v.Syntax]
		w.stmts(v.Body, lambdaBindings(bd, v))
	case *ir.Reference:
		v.Type = w.res.types[v.Syntax]
	case *ir.SelfValue:
		v.Type = bd.self
	case *ir.ParamRef:
		v.Type = bd.params[v.Name]
	case *ir.LocalRef:
		v.Type = bd.locals[v.Name]
	case *ir.EnumMemberValue:
		// A member's type is its enum, however the reference was written.
		if v.Def != nil {
			v.Type = &ir.Named{Def: v.Def}
		}
	case *ir.AssocConstValue:
		if v.Def != nil && v.Index >= 0 && v.Index < len(v.Def.Consts) {
			v.Type = v.Def.Consts[v.Index].Type
		}
	case *ir.IntLiteral:
		// A literal keeps its synthesized type even where a sized type
		// expects it — the width settle is an explicit adaption, not a
		// retype — so the leaf literals' types are structural facts.
		v.Type = &ir.Builtin{Name: "nint"}
	case *ir.StringLiteral:
		v.Type = &ir.Builtin{Name: "string"}
	case *ir.BoolLiteral:
		v.Type = &ir.Builtin{Name: "bool"}
	case *ir.DatetimeLiteral:
		v.Type = &ir.Builtin{Name: "datetime"}
	case *ir.DurationLiteral:
		v.Type = &ir.Builtin{Name: "duration"}
	case *ir.NullValue:
		v.Type = &ir.Builtin{Name: "null"}
	case nil:
		// A hole in the graph (a recovered expression): nothing to bind.
	}
}

// lambdaBindings extends the binding context for a function literal's body:
// the literal's parameters at the solved signature's types, on top of the
// captured scope. A literal whose signature never solved (its declaration is
// broken) binds its parameter names untyped, so the body's references stay
// visible holes rather than inheriting a same-named outer binding.
func lambdaBindings(bd bindings, lit *ir.FuncLiteral) bindings {
	fn, _ := lit.Type.(*ir.Func)
	params := make(map[string]ir.Type, len(bd.params)+len(lit.Params))
	for k, v := range bd.params {
		params[k] = v
	}
	for i, name := range lit.Params {
		var t ir.Type
		if fn != nil && i < len(fn.Params) {
			t = fn.Params[i]
		}
		params[name] = t
	}
	bd.params = params
	return bd
}

// stmts binds a statement body's value graphs, threading the binding context
// the way the lowering scoped it: a let extends the block for the statements
// after it, and a match arm's binding and a for's loop variable extend their
// own bodies only.
func (w resolutionWriter) stmts(body []ir.Stmt, bd bindings) {
	for _, s := range body {
		switch s := s.(type) {
		case *ir.Return:
			w.value(s.Value, bd)
		case *ir.ExprStmt:
			w.value(s.Value, bd)
		case *ir.Let:
			w.value(s.Value, bd)
			if s.Name != "" {
				bd = bd.withLocal(s.Name, s.Type)
			}
		case *ir.Assign:
			w.value(s.Value, bd)
		case *ir.Switch:
			w.value(s.Scrutinee, bd)
			for _, arm := range s.Arms {
				for _, pat := range arm.Values {
					w.value(pat, bd)
				}
				w.stmts(arm.Body, bd)
			}
			w.stmts(s.Else, bd)
		case *ir.Match:
			w.value(s.Scrutinee, bd)
			for _, arm := range s.Arms {
				abd := bd
				if arm.Name != "" {
					abd = bd.withLocal(arm.Name, arm.Type)
				}
				w.stmts(arm.Body, abd)
			}
			w.stmts(s.Else, bd)
		case *ir.If:
			w.value(s.Cond, bd)
			w.stmts(s.Then, bd)
			if s.ElseIf != nil {
				w.stmts([]ir.Stmt{s.ElseIf}, bd)
			}
			w.stmts(s.Else, bd)
		case *ir.For:
			w.value(s.Iter, bd)
			fbd := bd
			if s.Var != "" {
				fbd = bd.withLocal(s.Var, s.VarType)
			}
			w.stmts(s.Body, fbd)
		}
	}
}

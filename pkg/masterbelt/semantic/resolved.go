// This file carries the checker's settled facts from the checking walk into
// the IR. The IR's doctrine is that every reference is bound to its
// declaration; the lowering is type-blind, so after the checking walks run,
// writeBackResolutions binds each node to what the checker settled — the
// selected overload (ir.Call.Resolved and friends), the solved substitution,
// the node's type, and the explicit adaptions — monotonically widening what
// the late re-fold can fold without growing a second type system inside
// eval.

package semantic

import (
	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/types"
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
	// adapts accumulates the Adapted stream: the positions where the checker
	// accepted a value at a differing expectation, keyed by the expression,
	// with the adapted-to type — the write-back wraps each one in an explicit
	// ir.Adapt (F-3 §2.2).
	adapts map[ast.Expr]ir.Type
}

func newCallResolutions() *callResolutions {
	return &callResolutions{
		methods: map[*ast.CallExpr]*ir.Method{},
		statics: map[*ast.CallExpr]*ir.Method{},
		funcs:   map[*ast.CallExpr]*ast.FuncDecl{},
		substs:  map[*ast.CallExpr]map[string]ir.Type{},
		types:   map[ast.Expr]ir.Type{},
		adapts:  map[ast.Expr]ir.Type{},
	}
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
func writeBackResolutions(module *ir.Module, res *callResolutions, fnShells map[*ast.FuncDecl]*ir.Function, reg *builtin.Registry) {
	w := resolutionWriter{res: res, fnShells: fnShells, reg: reg}
	for _, c := range module.Consts {
		if c != nil {
			c.Value = w.value(c.Value, bindings{})
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
		// The refinement predicate is a value graph over self like any method
		// body — its facts streamed from the declaration's checking walk — so
		// it types and adapts the same way.
		if def.Where != nil {
			def.Where = w.value(def.Where, bindings{self: self})
		}
	}
}

// annotateGraph runs the write-back over one standalone value graph (an assert
// condition's), binding the checker facts res carries onto its nodes — the
// same walk writeBackResolutions runs over the module's graphs.
func annotateGraph(v ir.Value, res *callResolutions, fnShells map[*ast.FuncDecl]*ir.Function, reg *builtin.Registry) ir.Value {
	w := resolutionWriter{res: res, fnShells: fnShells, reg: reg}
	return w.value(v, bindings{})
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
	reg      *builtin.Registry
}

// value binds one value graph's nodes to the checker's facts — the overload
// selections, the solved substitutions, the settled types, and the accepted
// adaptions — recursing through every composite form and returning the node
// (wrapped in an explicit ir.Adapt where the checker accepted it at a
// differing expectation), so the caller stores the adapted graph back into
// its slot. Every assignment is unconditional: a method-body node lives on
// the memoized type definition, so a fact written by an earlier assemble must
// be cleared when the current walk recorded none rather than surviving stale
// — an Adapt wrapper from an earlier assemble is stripped on entry and
// rebuilt fresh for the same reason. The switch lists every form explicitly
// so an omission stays deliberate.
func (w resolutionWriter) value(v ir.Value, bd bindings) ir.Value {
	switch v := v.(type) {
	case *ir.Adapt:
		// A wrapper from an earlier assemble on a memoized body: rebuild from
		// the inner node, so a stale adaption never survives an edit.
		return w.value(v.Value, bd)
	case *ir.Call:
		v.Resolved = w.res.methods[v.Syntax]
		v.Subst = w.res.substs[v.Syntax]
		v.Receiver = w.value(v.Receiver, bd)
		for i, a := range v.Args {
			v.Args[i] = w.value(a, bd)
		}
		if v.Setter && v.Syntax == nil {
			// The synthetic call a property write lowers to has no call
			// expression of its own; it computes the receiver local's next
			// value, so its type is the receiver's.
			v.Type = ir.TypeOf(v.Receiver)
			return v
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
		for i, a := range v.Args {
			v.Args[i] = w.value(a, bd)
		}
	case *ir.StaticCall:
		v.Resolved = w.res.statics[v.Syntax]
		v.Subst = w.res.substs[v.Syntax]
		v.Type = w.res.types[v.Syntax]
		for i, a := range v.Args {
			v.Args[i] = w.value(a, bd)
		}
	case *ir.Apply:
		v.Type = w.res.types[v.Syntax]
		v.Callee = w.value(v.Callee, bd)
		for i, a := range v.Args {
			v.Args[i] = w.value(a, bd)
		}
	case *ir.CollectionLiteral:
		v.Type = w.res.types[v.Syntax]
		for i, e := range v.Entries {
			v.Entries[i].Key = w.value(e.Key, bd)
			v.Entries[i].Value = w.value(e.Value, bd)
		}
	case *ir.RecordValue:
		v.Type = w.res.types[v.Syntax]
		for i, f := range v.Fields {
			v.Fields[i].Value = w.value(f.Value, bd)
		}
	case *ir.Conversion:
		// Born typed: its target is its type. Only the arguments take facts.
		for i, a := range v.Args {
			v.Args[i] = w.value(a, bd)
		}
	case *ir.FieldAccess:
		v.Type = w.res.types[v.Syntax]
		v.Receiver = w.value(v.Receiver, bd)
	case *ir.Await:
		// await adds nothing to its operand's type.
		v.Value = w.value(v.Value, bd)
		v.Type = ir.TypeOf(v.Value)
	case *ir.Ternary:
		v.Type = w.res.types[v.Syntax]
		v.Cond = w.value(v.Cond, bd)
		v.Then = w.value(v.Then, bd)
		v.Else = w.value(v.Else, bd)
	case *ir.RangeLit:
		// Every range literal is the range builtin, whatever its bounds.
		v.Type = &ir.Builtin{Name: builtin.NameRange}
		v.Lower = w.value(v.Lower, bd)
		v.Upper = w.value(v.Upper, bd)
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
		v.Type = &ir.Builtin{Name: builtin.NameNint}
	case *ir.StringLiteral:
		v.Type = &ir.Builtin{Name: builtin.NameString}
	case *ir.BoolLiteral:
		v.Type = &ir.Builtin{Name: builtin.NameBool}
	case *ir.DatetimeLiteral:
		v.Type = &ir.Builtin{Name: "datetime"}
	case *ir.DurationLiteral:
		v.Type = &ir.Builtin{Name: "duration"}
	case *ir.NullValue:
		v.Type = &ir.Builtin{Name: "null"}
	case nil:
		// A hole in the graph (a recovered expression): nothing to bind.
		return nil
	}
	return w.wrapAdapt(v)
}

// wrapAdapt wraps a settled node in the explicit adaption the checker
// accepted at its position, when one was streamed (Adapted, keyed by the
// node's syntax): a value flowing into a union settles its member inside (the
// width/nominal adaption) and tags the union outside — the same member
// selection (types.SelectUnionMember) the checker and the folder use, so the
// three layers cannot disagree on the tag. A node whose type already is the
// expectation, or whose type never settled, wraps nothing.
func (w resolutionWriter) wrapAdapt(v ir.Value) ir.Value {
	key := ir.SyntaxOf(v)
	if key == nil {
		return v
	}
	to := w.res.adapts[key]
	if to == nil {
		return v
	}
	t := ir.TypeOf(v)
	if t == nil || types.Identical(t, to) {
		return v
	}
	out := v
	if sel, member := types.SelectUnionMember(w.reg, t, to); sel == types.UnionUnique {
		// The member the value tags: settle into it first when the value's own
		// type is not already the member (an nint literal into short | error
		// settles to short), then tag the union.
		if !types.Identical(t, member) {
			out = &ir.Adapt{Value: out, To: member}
		}
		return &ir.Adapt{Value: out, To: to}
	}
	return &ir.Adapt{Value: out, To: to}
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
			s.Value = w.value(s.Value, bd)
		case *ir.ExprStmt:
			s.Value = w.value(s.Value, bd)
		case *ir.Let:
			s.Value = w.value(s.Value, bd)
			if s.Name != "" {
				bd = bd.withLocal(s.Name, s.Type)
			}
		case *ir.Assign:
			s.Value = w.value(s.Value, bd)
		case *ir.Switch:
			s.Scrutinee = w.value(s.Scrutinee, bd)
			for _, arm := range s.Arms {
				for i, pat := range arm.Values {
					arm.Values[i] = w.value(pat, bd)
				}
				w.stmts(arm.Body, bd)
			}
			w.stmts(s.Else, bd)
		case *ir.Match:
			s.Scrutinee = w.value(s.Scrutinee, bd)
			for _, arm := range s.Arms {
				abd := bd
				if arm.Name != "" {
					abd = bd.withLocal(arm.Name, arm.Type)
				}
				w.stmts(arm.Body, abd)
			}
			w.stmts(s.Else, bd)
		case *ir.If:
			s.Cond = w.value(s.Cond, bd)
			w.stmts(s.Then, bd)
			if s.ElseIf != nil {
				w.stmts([]ir.Stmt{s.ElseIf}, bd)
			}
			w.stmts(s.Else, bd)
		case *ir.For:
			s.Iter = w.value(s.Iter, bd)
			fbd := bd
			if s.Var != "" {
				fbd = bd.withLocal(s.Var, s.VarType)
			}
			w.stmts(s.Body, fbd)
		}
	}
}

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
// appear; a single-signature call needs no selection.
type callResolutions struct {
	methods map[*ast.CallExpr]*ir.Method
	statics map[*ast.CallExpr]*ir.Method
	funcs   map[*ast.CallExpr]*ast.FuncDecl
}

func newCallResolutions() *callResolutions {
	return &callResolutions{
		methods: map[*ast.CallExpr]*ir.Method{},
		statics: map[*ast.CallExpr]*ir.Method{},
		funcs:   map[*ast.CallExpr]*ast.FuncDecl{},
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
// arity-based guess. It walks every value position the module carries: the
// constants' value graphs and the function and method bodies (an associated
// constant carries no value graph — only its folded value — so there is
// nothing to bind there).
func writeBackResolutions(module *ir.Module, res *callResolutions, fnShells map[*ast.FuncDecl]*ir.Function) {
	w := resolutionWriter{res: res, fnShells: fnShells}
	for _, c := range module.Consts {
		if c != nil {
			w.value(c.Value)
		}
	}
	for _, fn := range module.Funcs {
		if fn != nil {
			w.stmts(fn.Body)
		}
	}
	for _, def := range module.Types {
		for _, m := range def.Methods {
			w.stmts(m.Body)
		}
	}
}

type resolutionWriter struct {
	res      *callResolutions
	fnShells map[*ast.FuncDecl]*ir.Function
}

// value binds the call nodes in one value graph, recursing through every
// composite form. A form added to the IR without a case here simply carries no
// resolution — the conservative default — so the switch lists every form
// explicitly to keep the omission deliberate.
func (w resolutionWriter) value(v ir.Value) {
	switch v := v.(type) {
	case *ir.Call:
		if m := w.res.methods[v.Syntax]; m != nil {
			v.Resolved = m
		}
		w.value(v.Receiver)
		for _, a := range v.Args {
			w.value(a)
		}
	case *ir.FuncCall:
		if fd := w.res.funcs[v.Syntax]; fd != nil {
			if fn := w.fnShells[fd]; fn != nil {
				v.Resolved = fn
				v.Target = fn
			}
		}
		for _, a := range v.Args {
			w.value(a)
		}
	case *ir.StaticCall:
		if m := w.res.statics[v.Syntax]; m != nil {
			v.Resolved = m
		}
		for _, a := range v.Args {
			w.value(a)
		}
	case *ir.CollectionLiteral:
		for _, e := range v.Entries {
			w.value(e.Key)
			w.value(e.Value)
		}
	case *ir.RecordValue:
		for _, f := range v.Fields {
			w.value(f.Value)
		}
	case *ir.Conversion:
		for _, a := range v.Args {
			w.value(a)
		}
	case *ir.FieldAccess:
		w.value(v.Receiver)
	case *ir.Await:
		w.value(v.Value)
	case *ir.Ternary:
		w.value(v.Cond)
		w.value(v.Then)
		w.value(v.Else)
	case *ir.RangeLit:
		w.value(v.Lower)
		w.value(v.Upper)
	case *ir.FuncLiteral:
		w.stmts(v.Body)
	case *ir.IntLiteral, *ir.StringLiteral, *ir.BoolLiteral, *ir.DatetimeLiteral,
		*ir.DurationLiteral, *ir.Reference, *ir.SelfValue, *ir.ParamRef,
		*ir.LocalRef, *ir.NullValue, *ir.EnumMemberValue, *ir.AssocConstValue, nil:
		// Leaves: nothing to bind, nothing to recurse into.
	}
}

// stmts binds the call nodes in a statement body.
func (w resolutionWriter) stmts(body []ir.Stmt) {
	for _, s := range body {
		switch s := s.(type) {
		case *ir.Return:
			w.value(s.Value)
		case *ir.ExprStmt:
			w.value(s.Value)
		case *ir.Let:
			w.value(s.Value)
		case *ir.Assign:
			w.value(s.Value)
		case *ir.Switch:
			w.value(s.Scrutinee)
			for _, arm := range s.Arms {
				for _, pat := range arm.Values {
					w.value(pat)
				}
				w.stmts(arm.Body)
			}
			w.stmts(s.Else)
		case *ir.Match:
			w.value(s.Scrutinee)
			for _, arm := range s.Arms {
				w.stmts(arm.Body)
			}
			w.stmts(s.Else)
		case *ir.If:
			w.value(s.Cond)
			w.stmts(s.Then)
			if s.ElseIf != nil {
				w.stmts([]ir.Stmt{s.ElseIf})
			}
			w.stmts(s.Else)
		case *ir.For:
			w.value(s.Iter)
			w.stmts(s.Body)
		}
	}
}

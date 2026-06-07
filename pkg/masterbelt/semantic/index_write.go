// This file holds the list index-write check over the lowered bodies: a write
// coll[i] = v desugars to coll = coll.set(i, v), and when the collection folds
// to a list of known length while the index folds to a constant outside it,
// the write is a compile-time bug — a list write cannot grow the list, so it
// has nowhere to land (a map write upserts and is never out of range). The
// walk runs over the IR statements post-write-back, so the locals' settled
// types and the graph's annotations are exactly what the folder reads; a
// receiver or index that does not fold (a parameter, a dynamic index) is left
// to the runtime, the check's conservative discipline.
package semantic

import (
	"maps"
	"strconv"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/eval"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// checkIndexWritesIR walks every lowered body the module carries — the
// functions' and the type definitions' methods' — reporting out-of-range list
// writes.
func checkIndexWritesIR(module *ir.Module, genv graphFoldEnv, at func(ast.Node) span, diags *diagnostic.List) {
	for _, fn := range module.Funcs {
		if fn != nil {
			indexWriteStmts(fn.Body, map[string]*ir.Constant{}, genv, at, diags)
		}
	}
	for _, def := range module.Types {
		for _, m := range def.Methods {
			indexWriteStmts(m.Body, map[string]*ir.Constant{}, genv, at, diags)
		}
	}
}

// indexWriteStmts walks a statement body with the running local environment,
// reporting out-of-range list writes and threading the bindings each statement
// introduces. A nested block walks a copy of locals so its lets stay
// block-scoped, while an assignment to an outer local persists — mirroring the
// folder's execution model, since only foldable bindings are tracked.
func indexWriteStmts(body []ir.Stmt, locals map[string]*ir.Constant, genv graphFoldEnv, at func(ast.Node) span, diags *diagnostic.List) {
	for _, stmt := range body {
		switch s := stmt.(type) {
		case *ir.Let:
			if s.Name != "" && s.Value != nil {
				// The let's settled type is the initializer's expectation
				// channel, so an empty literal settles its mapness and the
				// write check can tell an upsert from a list write.
				if v := eval.GraphInExpecting(s.Value, locals, s.Type, genv); v != nil {
					locals[s.Name] = v
				} else {
					delete(locals, s.Name) // an unfoldable rebind: stop tracking it
				}
			}
		case *ir.Assign:
			reportIndexWriteIR(s, locals, genv, at, diags)
			if s.Name != "" && s.Value != nil {
				if v := eval.GraphIn(s.Value, locals, genv); v != nil {
					locals[s.Name] = v
				} else {
					delete(locals, s.Name)
				}
			}
		case *ir.If:
			indexWriteStmts(s.Then, copyLocals(locals), genv, at, diags)
			if s.ElseIf != nil {
				indexWriteStmts([]ir.Stmt{s.ElseIf}, copyLocals(locals), genv, at, diags)
			}
			indexWriteStmts(s.Else, copyLocals(locals), genv, at, diags)
		case *ir.Switch:
			for _, arm := range s.Arms {
				indexWriteStmts(arm.Body, copyLocals(locals), genv, at, diags)
			}
			indexWriteStmts(s.Else, copyLocals(locals), genv, at, diags)
		case *ir.Match:
			// An arm's narrowed binding is not a foldable list constant the
			// write check tracks, so the arm bodies walk with a copy of the
			// locals exactly as a switch's arms do.
			for _, arm := range s.Arms {
				indexWriteStmts(arm.Body, copyLocals(locals), genv, at, diags)
			}
			indexWriteStmts(s.Else, copyLocals(locals), genv, at, diags)
		case *ir.For:
			// The loop variable is bound per iteration, not to a foldable list
			// constant the write check tracks.
			indexWriteStmts(s.Body, copyLocals(locals), genv, at, diags)
		case *ir.Return, *ir.ExprStmt:
			// Neither binds nor reassigns a local: nothing to do. Listed
			// explicitly so a new statement kind is a deliberate decision.
		}
	}
}

// reportIndexWriteIR reports an out-of-range list write for an assignment
// whose value is a set call on a foldable list — coll = coll.set(i, v), the
// desugared coll[i] = v (a property write's synthetic setter call is a
// different shape and never a set). A map receiver upserts and an unknown
// empty collection is ambiguous, so only a settled list is reported, at the
// index expression.
func reportIndexWriteIR(s *ir.Assign, locals map[string]*ir.Constant, genv graphFoldEnv, at func(ast.Node) span, diags *diagnostic.List) {
	call, ok := s.Value.(*ir.Call)
	if !ok || call.Setter || call.Method != "set" || len(call.Args) != 2 {
		return
	}
	recv := eval.GraphIn(call.Receiver, locals, genv)
	if recv == nil || recv.Kind != ir.ConstCollection || !recv.IsList() {
		return
	}
	idx := eval.GraphIn(call.Args[0], locals, genv)
	if idx == nil || idx.Kind != ir.ConstInt {
		return
	}
	n := len(recv.Coll)
	if idx.Int.IsInt64() && idx.Int.Int64() >= 0 && idx.Int.Int64() < int64(n) {
		return // in range
	}
	anchor := ir.SyntaxOf(call.Args[0])
	if anchor == nil {
		return // a synthesized index with no surface position
	}
	c := at(anchor)
	diags.Add(newIndexOutOfRangeDiagnostic(c.offset, c.width, idx.Int.String(), strconv.Itoa(n)))
}

// copyLocals returns a shallow copy of a local environment, so a nested
// block's bindings do not leak back to the enclosing one.
func copyLocals(locals map[string]*ir.Constant) map[string]*ir.Constant {
	return maps.Clone(locals)
}

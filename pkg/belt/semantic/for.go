package semantic

import (
	"github.com/masterbelt/masterbelt/pkg/belt/types"
	"github.com/masterbelt/masterbelt/pkg/belt/types/infer"
	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// A for is a collection-iteration control statement: it visits every element of a
// foldable collection, binding an immutable loop variable to each — the value for
// an of-loop, the key for an in-loop — and running its body once per element.
// Iterability is foldable: the iterated expression must opt into the prelude
// foldable interface. Its analysis layers on top of the per-statement body walk:
//
//   - the iterated expression must be foldable (not_iterable); and
//   - the loop variable is immutable — the body may not reassign it
//     (loop_var_immutable); accumulation goes through a let.
//
// The body is checked by the caller (checkStmts) in the scope where the loop
// variable is bound to its element type, so a reference to it resolves at that
// type. A for yields no value and its body is not guaranteed to run (an empty
// collection skips it), so a for never counts as a return.

// checkFor validates one for statement: its iterated expression's iterability and
// any reassignment of the loop variable. The body itself is walked by the caller
// (checkStmts), which binds the loop variable and threads the result type into
// its returns. The iter types through the body's checking sink, reporting its
// own operator errors and streaming its settled type for the typed value graph.
func checkFor(s *ast.ForStmt, bs infer.BodyScope, sink *infer.Sink, at func(ast.Node) span, diags *diagnostic.List) {
	if s.Iter != nil {
		iterT := infer.CheckPredicate(s.Iter, bs, sink)
		// An unresolved iter already produced its own reference/type diagnostic; only
		// a resolved, non-foldable type is reported as not_iterable here.
		if iterT != ir.Invalid {
			if _, ok := types.ForElement(bs.Reg, iterT, s.Kind == ast.ForOf); !ok {
				c := at(s.Iter)
				diags.Add(newNotIterableDiagnostic(c.offset, c.width, iterT.String()))
			}
		}
	}
	if s.Var != "" {
		reportLoopVarWrites(s.Body, s.Var, at, diags)
	}
}

// reportLoopVarWrites reports a reassignment of the loop variable name anywhere in
// the loop body: the loop variable is an immutable per-iteration binding, so the
// body must accumulate into a let instead. It recurses through the nested control
// statements an assignment can hide in — but stops at a nested binding that
// shadows the name (an inner for or match arm of the same name, or a let), since a
// write there targets that inner binding, not this loop variable.
func reportLoopVarWrites(body []ast.Stmt, name string, at func(ast.Node) span, diags *diagnostic.List) {
	for _, stmt := range body {
		switch s := stmt.(type) {
		case *ast.AssignStmt:
			if id, ok := s.Target.(*ast.Identifier); ok && id.Name == name {
				c := at(s.Target)
				diags.Add(newLoopVarImmutableDiagnostic(c.offset, c.width, name))
			}
		case *ast.LetStmt:
			if s.Name == name {
				return // a let of the same name shadows the loop variable from here on
			}
		case *ast.IfStmt:
			reportLoopVarWrites(s.Then, name, at, diags)
			if s.ElseIf != nil {
				reportLoopVarWrites([]ast.Stmt{s.ElseIf}, name, at, diags)
			}
			reportLoopVarWrites(s.Else, name, at, diags)
		case *ast.SwitchStmt:
			reportLoopVarWritesInSwitch(s, name, at, diags)
		case *ast.MatchStmt:
			reportLoopVarWritesInMatch(s, name, at, diags)
		case *ast.ForStmt:
			if s.Var == name {
				continue // an inner for of the same name shadows the loop variable
			}
			reportLoopVarWrites(s.Body, name, at, diags)
		case *ast.ReturnStmt, *ast.ExprStmt:
			// Neither reassigns a local, so neither can write the loop variable.
			// Listed explicitly so a new statement kind forces a decision here.
		}
	}
}

// reportLoopVarWritesInSwitch recurses reportLoopVarWrites through a switch's
// arm bodies, its wildcard, and its unreachable after-else arms.
func reportLoopVarWritesInSwitch(s *ast.SwitchStmt, name string, at func(ast.Node) span, diags *diagnostic.List) {
	for _, arm := range s.Arms {
		reportLoopVarWrites(arm.Body, name, at, diags)
	}
	reportLoopVarWrites(s.Else, name, at, diags)
	for _, arm := range s.AfterElse {
		reportLoopVarWrites(arm.Body, name, at, diags)
	}
}

// reportLoopVarWritesInMatch recurses reportLoopVarWrites through a match's arm
// bodies, its wildcard, and its unreachable after-else arms — skipping an arm
// whose binding shadows the loop variable in its body.
func reportLoopVarWritesInMatch(s *ast.MatchStmt, name string, at func(ast.Node) span, diags *diagnostic.List) {
	for _, arm := range s.Arms {
		if arm.Bind == name {
			continue // the arm's binding shadows the loop variable in its body
		}
		reportLoopVarWrites(arm.Body, name, at, diags)
	}
	reportLoopVarWrites(s.Else, name, at, diags)
	for _, arm := range s.AfterElse {
		if arm.Bind == name {
			continue
		}
		reportLoopVarWrites(arm.Body, name, at, diags)
	}
}

// forNarrowedScope returns the body scope a for loop is checked in: the loop
// variable bound to the iter's element type (the value type for an of-loop, the
// key type for an in-loop), so a reference to it inside the body resolves at that
// type. A nameless loop variable or a non-foldable iter binds nothing usable. It
// is the checking-walk twin of the IR lowering's ForLocal and infer's forScope.
func forNarrowedScope(bs infer.BodyScope, s *ast.ForStmt) infer.BodyScope {
	if s.Var == "" {
		return bs
	}
	elem, _ := types.ForElement(bs.Reg, infer.Body(s.Iter, bs), s.Kind == ast.ForOf)
	return withLocal(bs, s.Var, elem)
}

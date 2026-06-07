// This file holds the fold-totality side of the analyzer: the language rule is
// that a constant either folds to a value or carries an error — there is no
// silently unfolded const. foldFailure classifies the failure for the
// unfolded_const diagnostic (and the fold gate tests): "depth" when an
// evaluation budget guard refused the fold (the user's computation is too
// deep — fixable), "evaluator gap" for everything else (a missing fold rule —
// a compiler bug, never the user's).
package semantic

import (
	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/eval"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// foldFailure classifies why a constant declaration's fold produced no value,
// re-running the fold with eval's budget channel armed. It is only called on
// the error path (a diagnostic-free const whose Eval is nil), so the re-run
// costs nothing in the green case.
func foldFailure(file FileID, decl *ast.ConstDecl, q queries) string {
	return eval.DeclFailure(decl, annotationResolved(q, file, decl), evalEnv{q: q, file: file})
}

// enforceEvalPublication is the publication rule for compile-time values —
// Eval ⇔ the declaration (and what it depends on) is diagnostic-free — run
// once every diagnostic and the late re-fold have settled. Both directions are
// enforced:
//
// (⇐ soundness) a declaration that carries an error — or whose type is
// poisoned by a dependency (ir.HasInvalid) — publishes no value. The internal
// type-blind value query may well have folded one, but a structural value that
// never passed the type-bound checks (refinement, overflow, union tagging) is
// not "the constant's value", and publishing it would let a downstream assert
// look green over an unverified value.
//
// (⇒ totality) a diagnostic-free constant with no value is an error
// (unfolded_const): a pure constant is computed at compile time or never, so a
// declaration without a value did not happen. The reason classifies the
// failure — depth (the user's computation exceeded the evaluation budget) or
// evaluator gap (a missing fold rule: a compiler bug). A constant whose
// failure traces to an unvalued dependency is not re-reported — the cause
// carries its own diagnostic at its own site, the bad-flag suppression style;
// the check is deliberately that narrow (own-declaration error, HasInvalid
// taint, unvalued dependency) so a genuine evaluator gap cannot hide behind a
// broad exemption.
func enforceEvalPublication(fileID FileID, file *ast.File, module *ir.Module, shells map[*ast.ConstDecl]*ir.Const, q queries, renv resolvedEnv, at func(ast.Node) span, diags *diagnostic.List) {
	errOffsets := errorOffsets(diags)
	within := func(s span) bool {
		for _, off := range errOffsets {
			if off >= s.offset && off < s.offset+s.width {
				return true
			}
		}
		return false
	}
	taintedType := func(t ir.Type) bool { return t != nil && ir.HasInvalid(t) }

	own := make(map[*ast.ConstDecl]*ir.Const, len(file.Decls))
	for _, decl := range file.Decls {
		own[decl] = shells[decl]
	}
	// published reads a top-level constant's published value: this file's
	// shells carry it directly; a cross-file constant reads the value query
	// (deterministic whatever order the program's files assemble in).
	published := func(target *ast.ConstDecl) bool {
		if c, mine := own[target]; mine {
			return c.Eval != nil
		}
		return q.valueOf(target) != nil
	}

	// (⇐) Withhold the values of broken declarations.
	for _, decl := range file.Decls {
		c := shells[decl]
		if c.Eval != nil && (within(at(decl)) || taintedType(c.Type)) {
			c.Eval = nil
		}
	}
	// An associated constant's type is not checker-derived (an unannotated one
	// types from its own folded value), so the taint test reads the written
	// annotation, and the dependency taint is read off the published values
	// directly: an initializer reading a withheld constant is withheld too.
	// The loop runs to a fixpoint so a chain of associated constants settles
	// whatever its declaration order.
	for progress := true; progress; {
		progress = false
		for _, def := range module.Types {
			for _, ac := range def.Consts {
				if ac.Value == nil || ac.Syntax == nil {
					continue
				}
				annTainted := ac.Syntax.Type != nil && taintedType(ac.Type)
				if within(at(ac.Syntax)) || annTainted || dependsOnUnvalued(fileID, ac.Syntax.Value, q, published) {
					ac.Value = nil
					progress = true
				}
			}
		}
	}

	// (⇒) A clean declaration without a value is an error.
	// valuedAnywhere is the totality side's dependency reading: a constant
	// counts as valued when either the type-blind query or the published
	// shells carry a value — so a reader is suppressed only when its
	// dependency's failure is genuinely reported at the dependency.
	valuedAnywhere := func(target *ast.ConstDecl) bool {
		if q.valueOf(target) != nil {
			return true
		}
		if c, mine := own[target]; mine {
			return c.Eval != nil
		}
		return false
	}
	for _, decl := range file.Decls {
		c := shells[decl]
		if decl.Value == nil || c.Eval != nil {
			continue
		}
		s := at(decl)
		if within(s) || taintedType(c.Type) {
			continue // the cause is reported at (or taints through) the declaration
		}
		if dependsOnUnvalued(fileID, decl.Value, q, valuedAnywhere) {
			continue // the cause carries its own diagnostic at the dependency
		}
		reason := eval.DeclFailure(decl, annotationResolved(q, fileID, decl), renv)
		diags.Add(newUnfoldedConstDiagnostic(s.offset, s.width, decl.Name, reason))
	}
	for _, def := range module.Types {
		for _, ac := range def.Consts {
			if ac.Builtin || ac.Syntax == nil || ac.Syntax.Value == nil || ac.Value != nil {
				continue
			}
			s := at(ac.Syntax)
			// The written annotation carries the taint; the Invalid an
			// unannotated, unfolded constant fell back to is the symptom being
			// diagnosed here, not a cause to skip over.
			if within(s) || (ac.Syntax.Type != nil && taintedType(ac.Type)) {
				continue
			}
			if dependsOnUnvalued(fileID, ac.Syntax.Value, q, valuedAnywhere) {
				continue
			}
			reason := eval.DeclFailure(ac.Syntax, ac.Type, renv)
			diags.Add(newUnfoldedConstDiagnostic(s.offset, s.width, def.Name+"."+ac.Name, reason))
		}
	}
}

// errorOffsets collects the anchor offsets of the error-severity diagnostics
// so far — what the publication rule reads "the declaration carries an error"
// from. Warnings do not withhold a value.
func errorOffsets(diags *diagnostic.List) []int {
	var out []int
	for _, d := range diags.Items() {
		if d.Severity == diagnostic.Error {
			out = append(out, d.Offset)
		}
	}
	return out
}

// dependsOnUnvalued reports whether the expression reads a constant that has
// no value: a top-level or imported const topValued answers false for, an
// associated constant that resolved to nothing, or an enum member without a
// value. The publication rule runs it with two readings of topValued — the
// soundness side asks "is the published value withheld?", the totality side
// "is there genuinely no value anywhere?" — over the same reference walk.
func dependsOnUnvalued(fileID FileID, e ast.Expr, q queries, topValued func(*ast.ConstDecl) bool) bool {
	un := false
	valued := func(target *ast.ConstDecl) bool {
		if target == nil || target.Value == nil {
			return true // undefined or empty: not a value dependency (its own diagnostic)
		}
		return topValued(target)
	}
	walkRefsEnum(fileID, e, q,
		func(id *ast.Identifier) {
			if !valued(q.resolve(fileID, id)) {
				un = true
			}
		},
		func(m *ast.MemberExpr) {
			if !valued(q.resolveMember(fileID, m)) {
				un = true
			}
		},
		func(m *ast.MemberExpr) {
			recv, ok := m.Receiver.(*ast.Identifier)
			if !ok {
				return
			}
			def := q.universe(fileID)[recv.Name]
			if def == nil {
				return
			}
			if def.Enum != nil {
				for _, mem := range def.Enum.Members {
					if mem.Name == m.Member.Name && mem.Value == nil {
						un = true
					}
				}
			}
			for _, ac := range def.Consts {
				if ac.Name == m.Member.Name && ac.Value == nil {
					un = true
				}
			}
		})
	return un
}

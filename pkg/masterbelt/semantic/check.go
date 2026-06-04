package semantic

import (
	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/eval"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ast"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ir"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/types"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/types/infer"
)

// --- expression diagnostics -------------------------------------------------

// checkDivByZero reports each div/rem whose divisor folds to zero.
func checkDivByZero(e ast.Expr, q queries, report func(node ast.Node)) {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return
	}
	member, ok := call.Callee.(*ast.MemberExpr)
	if !ok {
		return
	}
	if (member.Member.Name == "div" || member.Member.Name == "rem") && len(call.Arguments) == 1 {
		if d := eval.Expr(call.Arguments[0], evalEnv{q}); d != nil && d.Kind == ir.ConstInt && d.Int.Sign() == 0 {
			report(call)
		}
	}
	checkDivByZero(member.Receiver, q, report)
	for _, a := range call.Arguments {
		checkDivByZero(a, q, report)
	}
}

// --- collection literals ----------------------------------------------------

// collectionChecker type-checks a collection literal against an expected type,
// element by element, reporting element type mismatches and out-of-range
// element values precisely at the offending entry.
type collectionChecker struct {
	env   typeEnv
	q     queries
	reg   *builtin.Registry
	at    func(ast.Node) span
	diags *diagnostic.List
}

// check is the entry point for a collection-valued constant. An annotated
// literal is checked against its annotation; an un-annotated one only needs its
// inferred type to be determinable (a non-empty, homogeneous literal) — an empty
// or heterogeneous one is reported as uninferable.
func (c collectionChecker) check(lit *ast.CollectionLit, annotated bool, t ir.Type) {
	if annotated {
		c.against(lit, t)
		return
	}
	if t == ir.Invalid {
		s := c.at(lit)
		c.diags.Add(newUninferableCollectionDiagnostic(s.offset, s.width))
	}
}

// against checks expression e against the expected type want: a collection
// literal must match want's shape (a list or map of the right constructor) and
// then have each entry checked against the element type; any other expression is
// checked for assignability and integer range.
func (c collectionChecker) against(e ast.Expr, want ir.Type) {
	if e == nil {
		return
	}
	if lit, ok := e.(*ast.CollectionLit); ok {
		app, isColl := collectionApp(want)
		if !isColl {
			c.mismatch(lit, want)
			return
		}
		if len(lit.Entries) > 0 && lit.IsMap() != (len(app.Args) == 2) {
			c.mismatch(lit, want) // a map literal under a list annotation, or vice versa
			return
		}
		c.entries(lit, app)
		return
	}
	if got := infer.Expr(e, c.env); want != ir.Invalid && got != ir.Invalid && !types.Assignable(c.reg, got, want) {
		s := c.at(e)
		c.diags.Add(newTypeMismatchDiagnostic(s.offset, s.width, got.String(), want.String()))
	}
	if v := eval.Expr(e, evalEnv{c.q}); v != nil && v.Kind == ir.ConstInt && !types.Fits(c.reg, want, v.Int) {
		s := c.at(e)
		c.diags.Add(newConstantOverflowDiagnostic(s.offset, s.width, v.String(), want.String()))
	}
}

// entries checks each entry of lit against app's element types: a list's
// elements against its one argument, a map's keys and values against its two.
func (c collectionChecker) entries(lit *ast.CollectionLit, app *ir.App) {
	switch len(app.Args) {
	case 1:
		for _, entry := range lit.Entries {
			c.against(entry.Value, app.Args[0])
		}
	case 2:
		for _, entry := range lit.Entries {
			if entry.Key != nil {
				c.against(entry.Key, app.Args[0])
			}
			c.against(entry.Value, app.Args[1])
		}
	}
}

// mismatch reports that the literal's inferred type cannot be used where want is
// expected (a non-collection annotation, or the wrong collection kind).
func (c collectionChecker) mismatch(lit *ast.CollectionLit, want ir.Type) {
	s := c.at(lit)
	c.diags.Add(newTypeMismatchDiagnostic(s.offset, s.width, infer.Expr(lit, c.env).String(), want.String()))
}

// collectionApp returns t as a list or map application, or false if t is not a
// builtin collection type.
func collectionApp(t ir.Type) (*ir.App, bool) {
	app, ok := t.(*ir.App)
	if !ok || app.Def == nil {
		return nil, false
	}
	if app.Def.Name == "list" || app.Def.Name == "map" {
		return app, true
	}
	return nil, false
}

// --- method bodies ----------------------------------------------------------

// checkMethodBodies type-checks each method body's returned value against the
// method's declared result type, reporting a mismatch through report. It runs
// after resolveTypes, so defs are in file.Types order and each method lines up
// with its resolved signature. The body's expression types come from
// infer.Body, the same inference the const path uses.
func checkMethodBodies(file *ast.File, reg *builtin.Registry, defs []*ir.TypeDef, report func(node ast.Node, got, want ir.Type)) {
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
				if got := infer.Body(ret.Value, bs); !types.Assignable(reg, got, want) {
					report(ret.Value, got, want)
				}
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

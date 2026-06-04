package semantic

import (
	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/diagnostic"
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
		if d := evalExpr(call.Arguments[0], q); d != nil && d.Kind == ir.ConstInt && d.Int.Sign() == 0 {
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
	if v := evalExpr(e, c.q); v != nil && v.Kind == ir.ConstInt && !types.Fits(c.reg, want, v.Int) {
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
// with its resolved signature.
func checkMethodBodies(file *ast.File, reg *builtin.Registry, defs []*ir.TypeDef, report func(node ast.Node, got, want ir.Type)) {
	universe := make(map[string]*ir.TypeDef, len(defs))
	for _, d := range defs {
		if d.Name != "" {
			universe[d.Name] = d
		}
	}
	bc := bodyChecker{reg: reg, universe: universe}
	for i, td := range file.Types {
		def := defs[i]
		self := &ir.Named{Def: def}
		for j, m := range td.Methods {
			if len(m.Body) == 0 || j >= len(def.Methods) {
				continue // an extern or empty body has nothing to check
			}
			irm := def.Methods[j]
			scope := bodyScope{self: self, params: map[string]ir.Type{}}
			for _, p := range irm.Params {
				scope.params[p.Name] = substSelf(p.Type, self)
			}
			want := substSelf(irm.Result, self)
			for _, stmt := range m.Body {
				ret, ok := stmt.(*ast.ReturnStmt)
				if !ok || ret.Value == nil {
					continue
				}
				if got := bc.infer(ret.Value, scope); !types.Assignable(reg, got, want) {
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

// bodyScope binds the names visible in a method body: the receiver (self) and
// the parameters.
type bodyScope struct {
	self   ir.Type
	params map[string]ir.Type
}

// bodyChecker infers the type of a method-body expression against a scope.
type bodyChecker struct {
	reg      *builtin.Registry
	universe map[string]*ir.TypeDef
}

// infer returns the type of a method-body expression: self, a parameter, a
// literal, a record field access, a type conversion (T(x)), or a method call
// (the form operators desugar to). An unresolvable expression is ir.Invalid.
func (bc bodyChecker) infer(e ast.Expr, scope bodyScope) ir.Type {
	switch e := e.(type) {
	case *ast.SelfExpr:
		return scope.self
	case *ast.IntLit:
		return &ir.Builtin{Name: "int"}
	case *ast.StringLit:
		return &ir.Builtin{Name: "string"}
	case *ast.BoolLit:
		return &ir.Builtin{Name: "bool"}
	case *ast.NullLit:
		return &ir.Builtin{Name: "null"}
	case *ast.CollectionLit:
		return bc.collectionType(e, scope)
	case *ast.Identifier:
		if t, ok := scope.params[e.Name]; ok {
			return t
		}
		return ir.Invalid
	case *ast.MemberExpr:
		return bc.fieldType(bc.infer(e.Receiver, scope), e.Member.Name)
	case *ast.CallExpr:
		// A call whose callee names a type is a conversion T(x): its type is T.
		if id, ok := e.Callee.(*ast.Identifier); ok {
			if _, isParam := scope.params[id.Name]; !isParam {
				if t := bc.lookupType(id.Name); t != ir.Invalid {
					return t
				}
			}
		}
		// A call whose callee is a member access is a method call.
		if member, ok := e.Callee.(*ast.MemberExpr); ok {
			recv := bc.infer(member.Receiver, scope)
			args := make([]ir.Type, len(e.Arguments))
			for i, a := range e.Arguments {
				args[i] = bc.infer(a, scope)
			}
			return types.MethodResult(bc.reg, recv, member.Member.Name, args)
		}
		return ir.Invalid
	default:
		return ir.Invalid
	}
}

// collectionType infers the type of a collection literal in a method body —
// list<E> or map<K, V> — unifying the entry types, the same rule the const path
// uses (package types/infer). An empty or non-unifying literal is ir.Invalid.
func (bc bodyChecker) collectionType(e *ast.CollectionLit, scope bodyScope) ir.Type {
	if len(e.Entries) == 0 {
		return ir.Invalid
	}
	if e.IsMap() {
		def, ok := bc.reg.Lookup("map")
		if !ok {
			return ir.Invalid
		}
		var keyT, valT ir.Type
		for i, entry := range e.Entries {
			k, v := bc.infer(entry.Key, scope), bc.infer(entry.Value, scope)
			if i == 0 {
				keyT, valT = k, v
			} else {
				keyT, valT = types.Unify(bc.reg, keyT, k), types.Unify(bc.reg, valT, v)
			}
		}
		if keyT == ir.Invalid || valT == ir.Invalid {
			return ir.Invalid
		}
		return &ir.App{Def: def, Args: []ir.Type{keyT, valT}}
	}
	def, ok := bc.reg.Lookup("list")
	if !ok {
		return ir.Invalid
	}
	var elemT ir.Type
	for i, entry := range e.Entries {
		t := bc.infer(entry.Value, scope)
		if i == 0 {
			elemT = t
		} else {
			elemT = types.Unify(bc.reg, elemT, t)
		}
	}
	if elemT == ir.Invalid {
		return ir.Invalid
	}
	return &ir.App{Def: def, Args: []ir.Type{elemT}}
}

// lookupType resolves a type name (a conversion callee) to its type.
func (bc bodyChecker) lookupType(name string) ir.Type {
	if d, ok := bc.universe[name]; ok {
		if d.Builtin {
			return &ir.Builtin{Name: name}
		}
		return &ir.Named{Def: d}
	}
	if _, ok := bc.reg.Lookup(name); ok {
		return &ir.Builtin{Name: name}
	}
	return ir.Invalid
}

// fieldType returns the type of a record's field, following named types to their
// underlying record.
func (bc bodyChecker) fieldType(recv ir.Type, name string) ir.Type {
	rec := recordOf(recv)
	if rec == nil {
		return ir.Invalid
	}
	for _, f := range rec.Fields {
		if f.Name == name {
			return f.Type
		}
	}
	return ir.Invalid
}

func recordOf(t ir.Type) *ir.Record {
	switch t := t.(type) {
	case *ir.Record:
		return t
	case *ir.Named:
		if t.Def != nil {
			return recordOf(t.Def.Body)
		}
	}
	return nil
}

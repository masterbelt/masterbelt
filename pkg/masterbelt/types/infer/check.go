// This file is the bidirectional half of the type rules: the checking walk that
// pushes an expected type into the forms that benefit from one — function,
// collection, and record literals — and falls back to synthesis plus
// subsumption for everything else. Check synthesizes (reporting operator
// errors), CheckAgainst pushes a type down, and the per-form helpers
// (checkFuncLitAgainst, checkCollectionAgainst, checkRecord*) realize each
// pushed-down rule.
package infer

import (
	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/types"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// Check type-checks an expression, reporting through sink the innermost method
// call whose operand types it is not defined on, and — inside a function
// literal's body — each return value that does not satisfy the literal's
// declared (or, across several returns, unified) result type. It returns the
// expression's type so recursion can propagate an existing error — an operand
// that is itself Invalid, or an undefined reference reported elsewhere —
// without re-reporting it.
func Check(e ast.Expr, env Env, sink *Sink) ir.Type {
	return check(e, constScope{env}, sink)
}

// CheckAgainst checks expression e against the expected type want, pushing
// want into the forms that benefit (function and collection literals) and
// falling back to synthesis plus subsumption for everything else. It returns
// the expression's type — concrete where want filled in what the expression
// omitted. A nil sink checks silently (pure typing); diagnostic callers pass
// callbacks.
func CheckAgainst(e ast.Expr, want ir.Type, env Env, sink *Sink) ir.Type {
	return checkType(e, want, constScope{env}, map[string]ir.Type{}, sink)
}

// CheckBody is CheckAgainst over a method-body scope, so a returned function
// or collection literal receives the method's declared result type.
func CheckBody(e ast.Expr, want ir.Type, s BodyScope, sink *Sink) ir.Type {
	return checkType(e, want, s, map[string]ir.Type{}, sink)
}

// CheckPredicate is Check over a body scope: it synthesizes the expression's
// type with self bound to s.Self, reporting operator errors through sink. A
// refinement predicate types this way — whether the result must be a bool is
// the caller's rule, reported as its own diagnostic rather than a mismatch.
func CheckPredicate(e ast.Expr, s BodyScope, sink *Sink) ir.Type {
	return check(e, s, sink)
}

// checkType is the checking walk: the bidirectional half of the type rules.
// want may contain still-unbound method type variables — a call site passes
// its bindings in subst and gains the ones the walk solves; the declaration
// paths pass a fresh map.
func checkType(e ast.Expr, want ir.Type, s scope, subst map[string]ir.Type, sink *Sink) ir.Type {
	if e == nil {
		return ir.Invalid
	}
	if want == ir.Invalid {
		// No usable expectation (an annotation that failed to resolve is
		// reported at its own node): synthesize, keeping the body diagnostics.
		return check(e, s, sink)
	}
	want = types.Substitute(want, subst) // pin what the context already solved
	sink.checked(e, want)
	// A bare member resolves through an enum expectation (const Top: Rarity =
	// Legend): when want is an enum and e is a bare identifier naming one of its
	// members, the expression is that enum. A bare name that is not a member
	// falls through to ordinary identifier resolution (an undefined name).
	if id, ok := e.(*ast.Identifier); ok {
		if def, ok := want.(*ir.Named); ok && def.Def != nil && def.Def.Enum != nil {
			if enumMemberIndex(def.Def, id.Name) >= 0 {
				return want
			}
		}
	}
	switch e := e.(type) {
	case *ast.AwaitExpr:
		// The expectation reaches through await into the awaited value.
		return checkType(e.Value, want, s, subst, sink)
	case *ast.FuncLit:
		fw, ok := want.(*ir.Func)
		if !ok {
			got := check(e, s, sink)
			sink.mismatch(e, got, want)
			return got
		}
		return checkFuncLitAgainst(e, fw, s, subst, sink)
	case *ast.CollectionLit:
		return checkCollectionAgainst(e, want, s, subst, sink)
	case *ast.RecordLit:
		return checkRecordAgainst(e, want, s, subst, sink)
	default:
		// Synthesis plus subsumption: any other form's type is its own; it
		// must merely satisfy want (binding any type variable want still has).
		got := check(e, s, sink)
		if got != ir.Invalid && !types.Match(s.registry(), want, got, subst) {
			sink.mismatch(e, got, want)
		}
		return got
	}
}

// checkRecordAgainst checks a record literal against an expected type. The
// inferred form takes the expectation outright: want must be a record (or a
// named record), whose declared fields reach into the field values — which is
// how an empty {} gets a type at all, exactly as an empty collection does. The
// typed form carries its own type: its fields check against its own record,
// and the type must then satisfy want like any synthesized form.
func checkRecordAgainst(e *ast.RecordLit, want ir.Type, s scope, subst map[string]ir.Type, sink *Sink) ir.Type {
	if e.TypeName != "" {
		got := checkRecordLit(e, s, sink)
		if got != ir.Invalid && !types.Match(s.registry(), want, got, subst) {
			sink.mismatch(e, got, want)
		}
		return got
	}
	rec := recordOf(want)
	if rec == nil {
		// Not a record expectation: the field values are checked bare for
		// their own errors, and the literal reports with its structural shape.
		checkRecordFieldsBare(e, s, sink)
		sink.mismatch(e, structuralRecord(e, s), want)
		return ir.Invalid
	}
	checkRecordFields(e, rec, want, s, subst, sink)
	return want
}

// checkRecordLit checks a record literal with no expected type. The typed form
// carries its own type — the named record — and its fields are checked against
// that record's field types. The inferred form has nothing to check against:
// it is reported as uninferable, after its field values are checked bare for
// their own errors.
func checkRecordLit(e *ast.RecordLit, s scope, sink *Sink) ir.Type {
	if e.TypeName == "" {
		checkRecordFieldsBare(e, s, sink)
		sink.uninferableRecord(e)
		return ir.Invalid
	}
	typ, rec := namedRecord(e.TypeName, s)
	if typ == nil {
		checkRecordFieldsBare(e, s, sink)
		sink.unknownRecordType(e, e.TypeName)
		return ir.Invalid
	}
	if rec == nil {
		checkRecordFieldsBare(e, s, sink)
		sink.notARecord(e, typ)
		return ir.Invalid
	}
	checkRecordFields(e, rec, typ, s, map[string]ir.Type{}, sink)
	return typ
}

// checkRecordFields checks a record literal's fields against the record rec
// (displayed as typ in diagnostics): every initializer must name a declared
// field — its value checked against the field's type, so the expectation
// reaches into nested literals — every declared field must be initialized
// (missing_field), and an undeclared one is rejected (unknown_field).
func checkRecordFields(lit *ast.RecordLit, rec *ir.Record, typ ir.Type, s scope, subst map[string]ir.Type, sink *Sink) {
	declared := make(map[string]ir.Type, len(rec.Fields))
	for _, f := range rec.Fields {
		declared[f.Name] = f.Type
	}
	seen := make(map[string]bool, len(lit.Fields))
	for _, f := range lit.Fields {
		if f.Name == "" {
			continue // recovered away; already a parse diagnostic
		}
		ft, ok := declared[f.Name]
		if !ok {
			sink.unknownField(f, f.Name, typ)
			if f.Value != nil {
				check(f.Value, s, sink) // its own errors still surface
			}
			continue
		}
		seen[f.Name] = true
		if f.Value != nil {
			checkType(f.Value, ft, s, subst, sink)
		}
	}
	for _, f := range rec.Fields {
		if !seen[f.Name] {
			sink.missingField(lit, f.Name, typ)
		}
	}
}

// checkRecordFieldsBare checks each field value for its own errors when there
// is no field type to check against — the literal's own problem (an unknown
// type, no expectation) is reported by the caller.
func checkRecordFieldsBare(e *ast.RecordLit, s scope, sink *Sink) {
	for _, f := range e.Fields {
		if f.Value != nil {
			check(f.Value, s, sink)
		}
	}
}

// structuralRecord renders an inferred record literal's shape for a mismatch
// message: the structural record of its field value types.
func structuralRecord(e *ast.RecordLit, s scope) ir.Type {
	fields := make([]ir.Field, 0, len(e.Fields))
	for _, f := range e.Fields {
		if f.Name == "" {
			continue
		}
		t := ir.Invalid
		if f.Value != nil {
			t = exprType(f.Value, s)
		}
		fields = append(fields, ir.Field{Name: f.Name, Type: t})
	}
	return &ir.Record{Fields: fields}
}

// checkCollectionAgainst checks a collection literal against an expected list
// or map type: the shape must agree, and every entry is checked against the
// element type — which is how an annotation reaches into the literal, and how
// an empty literal gets a type at all (it is exactly its expectation).
func checkCollectionAgainst(lit *ast.CollectionLit, want ir.Type, s scope, subst map[string]ir.Type, sink *Sink) ir.Type {
	app, ok := collectionApp(want)
	if !ok || (len(lit.Entries) > 0 && lit.IsMap() != (len(app.Args) == 2)) {
		// Not a collection expectation, or a map literal under a list type
		// (or vice versa): report with the synthesized type, as the const
		// annotation check always has.
		got := check(lit, s, sink)
		sink.mismatch(lit, got, want)
		return got
	}
	switch len(app.Args) {
	case 1:
		for _, entry := range lit.Entries {
			checkType(entry.Value, app.Args[0], s, subst, sink)
		}
	case 2:
		for _, entry := range lit.Entries {
			if entry.Key != nil {
				checkType(entry.Key, app.Args[0], s, subst, sink)
			}
			checkType(entry.Value, app.Args[1], s, subst, sink)
		}
	}
	return want
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

// checkFuncLitAgainst checks a function literal against an expected function
// type — the heart of the bidirectional rules. The expectation supplies what
// the literal omits: an unannotated parameter takes the expected parameter
// type, and an unannotated result is either checked against the expected
// result or, when that still contains an unbound method type variable (map's
// R), synthesized from the body and matched to bind the variable in subst.
// A written annotation wins for the body scope but must agree with the
// expectation (default-int adaption only).
func checkFuncLitAgainst(lit *ast.FuncLit, want *ir.Func, s scope, subst map[string]ir.Type, sink *Sink) ir.Type {
	reg := s.registry()
	if len(lit.Params) != len(want.Params) {
		sink.arityMismatchLit(lit, len(lit.Params), len(want.Params))
		return ir.Invalid
	}
	r := &TypeResolver{Defs: s.universe(), Qualified: s.qualified()}
	params := make([]ir.Type, len(lit.Params))
	names := make(map[string]ir.Type, len(lit.Params))
	for i, p := range lit.Params {
		wp := types.Substitute(want.Params[i], subst)
		switch {
		case p.Type != nil:
			ap := r.ResolveType(p.Type, nil)
			if conflicts(reg, ap, wp, subst) {
				sink.mismatch(p, ap, wp)
			}
			params[i] = ap
		case !hasTypeVar(wp):
			params[i] = wp
		default:
			// The context has not pinned the variable this parameter needs
			// (and pass-2 ordering means it never will).
			sink.uninferableParam(p)
			params[i] = ir.Invalid
		}
		names[p.Name] = params[i]
	}
	body := funcScope{outer: s, params: names}

	wr := types.Substitute(want.Result, subst)
	var result ir.Type
	switch {
	case lit.Result != nil:
		// The annotation wins for the returns, after agreeing with the
		// expectation the same way a parameter annotation must.
		result = r.ResolveType(lit.Result, nil)
		if conflicts(reg, result, wr, subst) {
			sink.mismatch(lit.Result, result, wr)
		}
		checkReturns(lit, result, body, subst, sink)
	case !hasTypeVar(wr):
		// The expectation determines the result; every return is checked
		// against it (no return at all still leaves the signature complete).
		result = wr
		checkReturns(lit, wr, body, subst, sink)
	default:
		// The expected result still has an unbound method type variable (the
		// R of map): synthesize the body's result and bind the variable.
		unified, sawReturn := synthesizeReturns(lit, body, sink)
		switch {
		case !sawReturn:
			sink.uninferableResult(lit)
			result = ir.Invalid
		case unified == ir.Invalid:
			result = ir.Invalid
		case types.Match(reg, wr, unified, subst):
			result = types.Substitute(wr, subst)
		default:
			sink.mismatch(lit, unified, wr)
			result = ir.Invalid
		}
	}
	t := &ir.Func{Params: params, Result: result}
	sink.solvedFuncLit(lit, t)
	return t
}

// conflicts reports whether a written annotation disagrees with the expected
// type at the same position. A concrete expectation must unify with the
// annotation (default-int adaption only); one still carrying a method type
// variable is instead bound by the annotation through Match — which is how a
// fully annotated literal solves the R of list<T>.map — and conflicts only if
// the variable was already bound incompatibly. An unresolved annotation was
// reported at its own node.
func conflicts(reg *builtin.Registry, annotation, want ir.Type, subst map[string]ir.Type) bool {
	if annotation == ir.Invalid {
		return false
	}
	if hasTypeVar(want) {
		return !types.Match(reg, want, annotation, subst)
	}
	return types.Unify(reg, annotation, want) == ir.Invalid
}

// checkReturns walks a literal's body with a known result type: every return
// value is checked against it (pushing it into nested literals), and bare
// expression statements are checked for their own errors.
func checkReturns(lit *ast.FuncLit, want ir.Type, body funcScope, subst map[string]ir.Type, sink *Sink) {
	for _, stmt := range lit.Body {
		switch stmt := stmt.(type) {
		case *ast.ReturnStmt:
			if stmt.Value != nil {
				checkType(stmt.Value, want, body, subst, sink)
			}
		case *ast.ExprStmt:
			check(stmt.X, body, sink)
		}
	}
}

// synthesizeReturns walks a literal's body in synthesis mode, unifying the
// returned types the way checkFuncLit does: the unified type (Invalid when a
// return is Invalid or the returns conflict, reported at the later return)
// and whether any return carried a value.
func synthesizeReturns(lit *ast.FuncLit, body funcScope, sink *Sink) (unified ir.Type, sawReturn bool) {
	reg := body.registry()
	for _, stmt := range lit.Body {
		switch stmt := stmt.(type) {
		case *ast.ReturnStmt:
			if stmt.Value == nil {
				continue
			}
			sawReturn = true
			got := check(stmt.Value, body, sink)
			switch {
			case unified == nil:
				unified = got
			case unified == ir.Invalid || got == ir.Invalid:
				unified = ir.Invalid
			default:
				if u := types.Unify(reg, unified, got); u == ir.Invalid {
					sink.mismatch(stmt.Value, got, unified)
					unified = ir.Invalid
				} else {
					unified = u
				}
			}
		case *ast.ExprStmt:
			check(stmt.X, body, sink)
		}
	}
	return unified, sawReturn
}

// check is the checking walk behind Check, parameterized over the scope so a
// function literal's body is checked in its parameter scope.
func check(e ast.Expr, s scope, sink *Sink) ir.Type {
	switch e := e.(type) {
	case *ast.IntLit:
		return &ir.Builtin{Name: "int"}
	case *ast.StringLit:
		return &ir.Builtin{Name: "string"}
	case *ast.BoolLit:
		return &ir.Builtin{Name: "bool"}
	case *ast.DatetimeLit:
		return &ir.Builtin{Name: "datetime"}
	case *ast.DurationLit:
		return &ir.Builtin{Name: "duration"}
	case *ast.CollectionLit:
		// Surface any operator error inside an entry; the element-type and range
		// checks against the (possibly annotated) element type are the caller's.
		for _, entry := range e.Entries {
			if entry.Key != nil {
				check(entry.Key, s, sink)
			}
			if entry.Value != nil {
				check(entry.Value, s, sink)
			}
		}
		return collectionType(e, s)
	case *ast.RecordLit:
		return checkRecordLit(e, s, sink)
	case *ast.FuncLit:
		return checkFuncLit(e, s, sink)
	case *ast.AwaitExpr:
		// await marks the suspension point and adds nothing to the type.
		if e.Value == nil {
			return ir.Invalid
		}
		return check(e.Value, s, sink)
	case *ast.CallExpr:
		return callType(e, s, sink)
	default:
		return s.leaf(e)
	}
}

// checkFuncLit checks a function literal with no expected type: its body is
// walked in the literal's parameter scope, each return value is checked
// against the declared result type (or unified with the other returns when
// the annotation is omitted), and a literal with neither a result annotation
// nor a return to infer one from is reported as uninferable. The signature it
// returns is built from the same walk, so it agrees with funcLitType's (the
// silent twin over exprType) without typing the body a second time.
func checkFuncLit(lit *ast.FuncLit, s scope, sink *Sink) ir.Type {
	r := &TypeResolver{Defs: s.universe(), Qualified: s.qualified()}
	params := make([]ir.Type, len(lit.Params))
	names := make(map[string]ir.Type, len(lit.Params))
	for i, p := range lit.Params {
		if p.Type == nil {
			// With no expected type there is no context to infer an
			// unannotated parameter from.
			sink.uninferableParam(p)
		}
		params[i] = r.ResolveType(p.Type, nil)
		names[p.Name] = params[i]
	}
	body := funcScope{outer: s, params: names}

	var result ir.Type
	if lit.Result != nil {
		result = r.ResolveType(lit.Result, nil)
		checkReturns(lit, result, body, map[string]ir.Type{}, sink)
	} else {
		unified, sawReturn := synthesizeReturns(lit, body, sink)
		if !sawReturn {
			sink.uninferableResult(lit)
		}
		result = unified
		if result == nil {
			result = ir.Invalid
		}
	}
	t := &ir.Func{Params: params, Result: result}
	sink.solvedFuncLit(lit, t)
	return t
}

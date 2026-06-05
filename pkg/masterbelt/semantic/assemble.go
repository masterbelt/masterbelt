// This file holds the assembler: assemble turns a file's AST plus the semantic
// queries into its IR module and diagnostics. It is shared by the reference
// analyzer (Analyze) and the incremental Program, so the two cannot diverge.
// exprSink wires the type-checking walk's findings to diagnostics, and the
// assembly-local helpers (refinedDef, typeNameReporter) live here too.
package semantic

import (
	"sort"
	"strings"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/assert"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/eval"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/lower"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/types"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/types/infer"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// exprSink wires the checking walk's findings to their diagnostics. The
// Checked stream is left unset — the const path hooks it to the eval-based
// value-range check, which needs the declaration's context.
func exprSink(at func(ast.Node) span, diags *diagnostic.List) *infer.Sink {
	return &infer.Sink{
		InvalidOp: func(node ast.Node, method, operands string) {
			s := at(node)
			diags.Add(newInvalidOperationDiagnostic(s.offset, s.width, method, operands))
		},
		NoMatchingOverload: func(node ast.Node, method, operands string) {
			s := at(node)
			diags.Add(newNoMatchingOverloadDiagnostic(s.offset, s.width, method, operands))
		},
		AmbiguousOverload: func(node ast.Node, method, operands string) {
			s := at(node)
			diags.Add(newAmbiguousOverloadDiagnostic(s.offset, s.width, method, operands))
		},
		Mismatch: func(node ast.Node, got, want ir.Type) {
			s := at(node)
			diags.Add(newTypeMismatchDiagnostic(s.offset, s.width, got.String(), want.String()))
		},
		TernaryCondNotBool: func(cond ast.Expr, got ir.Type) {
			s := at(cond)
			diags.Add(newTernaryConditionNotBoolDiagnostic(s.offset, s.width, got.String()))
		},
		TernaryBranchMismatch: func(node ast.Node, then, els ir.Type) {
			s := at(node)
			diags.Add(newTernaryBranchMismatchDiagnostic(s.offset, s.width, then.String(), els.String()))
		},
		ArityMismatch: func(lit *ast.FuncLit, got, want int) {
			s := at(lit)
			diags.Add(newLambdaArityMismatchDiagnostic(s.offset, s.width, got, want))
		},
		CallArityMismatch: func(call *ast.CallExpr, name string, got, want int) {
			s := at(call)
			diags.Add(newArityMismatchDiagnostic(s.offset, s.width, name, got, want))
		},
		NoMatchingFuncOverload: func(call *ast.CallExpr, name, types string) {
			s := at(call)
			diags.Add(newNoMatchingFuncOverloadDiagnostic(s.offset, s.width, name, types))
		},
		AmbiguousFuncOverload: func(call *ast.CallExpr, name, types string) {
			s := at(call)
			diags.Add(newAmbiguousFuncOverloadDiagnostic(s.offset, s.width, name, types))
		},
		UninferableParam: func(p *ast.ParamDef) {
			s := at(p)
			diags.Add(newUninferableParameterDiagnostic(s.offset, s.width, p.Name))
		},
		UninferableResult: func(lit *ast.FuncLit) {
			s := at(lit)
			diags.Add(newUninferableResultDiagnostic(s.offset, s.width))
		},
		MissingField: func(lit *ast.RecordLit, field string, typ ir.Type) {
			s := at(lit)
			diags.Add(newMissingFieldDiagnostic(s.offset, s.width, field, typ.String()))
		},
		UnknownField: func(field *ast.FieldInit, name string, typ ir.Type) {
			s := at(field)
			diags.Add(newUnknownFieldDiagnostic(s.offset, s.width, name, typ.String()))
		},
		UninferableRecord: func(lit *ast.RecordLit) {
			s := at(lit)
			diags.Add(newUninferableRecordDiagnostic(s.offset, s.width))
		},
		UnknownRecordType: func(lit *ast.RecordLit, name string) {
			s := at(lit)
			diags.Add(newUnknownTypeDiagnostic(s.offset, s.width, name))
		},
		NotARecord: func(lit *ast.RecordLit, typ ir.Type) {
			s := at(lit)
			diags.Add(newNotARecordDiagnostic(s.offset, s.width, typ.String()))
		},
	}
}

// assemble builds one file's IR module and semantic diagnostics from its AST,
// using q for the resolution and typing facts; fileID names the file within
// the program, scoping its identifier resolution, and shells/fnShells hold the
// program-wide IR constants and functions (this file's and every importable
// file's). It is shared by the reference and incremental analyzers, so they
// cannot diverge.
func assemble(fileID FileID, file *ast.File, positions map[cst.Green]span, q queries, shells map[*ast.ConstDecl]*ir.Const, fnShells map[*ast.FuncDecl]*ir.Function) (*ir.Module, []diagnostic.Diagnostic) {
	diags := &diagnostic.List{}
	at := func(n ast.Node) span { return spanOf(positions, n) }
	env := typeEnv{q: q, file: fileID}
	reg := q.registry()

	// The module's constants are this file's shells, in source order.
	module := &ir.Module{}
	for _, decl := range file.Decls {
		module.Consts = append(module.Consts, shells[decl])
	}

	// The use declarations' own problems: imports that resolved to no file,
	// selective names the target does not export, and module cycles.
	checkUses(fileID, file, q, at, diags)

	// Redeclarations of the same name — constants and functions each within
	// their own namespace (a call form looks up functions, a bare name
	// constants, so the two tables never collide).
	seen := map[string]bool{}
	for _, decl := range file.Decls {
		if decl.Name == "" {
			continue // already a parse diagnostic
		}
		if seen[decl.Name] {
			s := at(decl)
			diags.Add(newDuplicateDeclarationDiagnostic(s.offset, s.width, decl.Name))
		}
		seen[decl.Name] = true
	}
	cyclic := cyclicDecls(fileID, file, q)

	for _, decl := range file.Decls {
		c := shells[decl]
		// A bare member in the initializer (const Top: Rarity = Legend) lowers
		// through the annotation's enum, so resolve it first. The annotation is
		// resolved against the universe — a pure name lookup, not the type query
		// — so the value lowering stays independent of typeOf.
		c.Value = lower.Value(decl.Value, constBinder{q: q, file: fileID, irOf: shells, fnOf: fnShells, expected: annotationEnum(q, fileID, decl)})
		c.Type = q.typeOf(decl)
		c.Eval = q.valueOf(decl)

		// Resolve the annotation with reporting enabled, so an unknown type
		// name anywhere in it (e.g. list<Bogus>) is diagnosed at its own node.
		// The annotation resolves in the file's universe: its own type
		// declarations shadowing its imported ones, over the registry.
		annType := ir.Invalid
		if decl.Type != nil {
			r := &infer.TypeResolver{
				Defs:      q.universe(fileID),
				Qualified: qualifiedFrom(q, q.importsOf(fileID)),
				Report:    typeNameReporter(fileID, q, at, diags),
			}
			annType = r.ResolveType(decl.Type, nil)
		}

		// Undefined references: every value-position identifier that resolves
		// to no declaration — distinguishing names that failed because two or
		// more imports claimed them (ambiguous_import) — and every namespace
		// member access whose member the target does not export
		// (unknown_member). Method names are not value references; the walk
		// skips them, and it treats a namespace access as one unit, so its
		// receiver is never reported as an undefined value.
		if decl.Value != nil {
			reportRefIssues(fileID, decl.Value, q, at, diags, annotationEnum(q, fileID, decl))
			// One checking walk reports the expression diagnostics: operator
			// type errors, type mismatches (against the annotation when there
			// is one, and inside function-literal bodies), and literals whose
			// parameter or result types cannot be inferred. Value-range checks
			// hook the walk's Checked stream so infer stays eval-free; the
			// top-level value is range-checked against c.Type below, so only
			// the inner expressions (collection entries, record fields,
			// returns) are checked here — a typed record literal pushes its
			// field types even without an annotation.
			sink := exprSink(at, diags)
			sink.Checked = func(e ast.Expr, want ir.Type) {
				if e == decl.Value {
					return
				}
				if v := eval.Expr(e, evalEnv{q: q, file: fileID}); v != nil && v.Kind == ir.ConstInt && !types.Fits(reg, want, v.Int) {
					s := at(e)
					diags.Add(newConstantOverflowDiagnostic(s.offset, s.width, v.String(), want.String()))
				}
			}
			if annType != ir.Invalid {
				// The annotation is pushed into the value.
				infer.CheckAgainst(decl.Value, annType, env, sink)
			} else {
				if decl.Type != nil {
					// The annotation failed to resolve and was reported at its
					// own node; had it resolved, it would have supplied the
					// inferred record form its type — don't pile on.
					sink.UninferableRecord = nil
				}
				infer.Check(decl.Value, env, sink)
			}
			// Division or remainder by a zero divisor.
			checkDivByZero(decl.Value, evalEnv{q: q, file: fileID}, func(node ast.Node) {
				s := at(node)
				diags.Add(newDivisionByZeroDiagnostic(s.offset, s.width))
			})
			// A constant has no receiver: self has no meaning in its
			// initializer (nor inside a literal nested in it).
			checkNoSelf(decl.Value, func(node ast.Node) {
				s := at(node)
				diags.Add(newSelfOutsideMethodDiagnostic(s.offset, s.width))
			})
		}

		if cyclic[decl] {
			s := at(decl)
			diags.Add(newCyclicReferenceDiagnostic(s.offset, s.width, decl.Name))
		}
		// An integer value outside its concrete type's range overflows. The
		// arbitrary-precision int has no fixed range (Fits accepts any value),
		// and booleans never overflow.
		overflow := c.Eval != nil && c.Eval.Kind == ir.ConstInt && !types.Fits(reg, c.Type, c.Eval.Int)
		if overflow {
			s := at(decl.Value)
			diags.Add(newConstantOverflowDiagnostic(s.offset, s.width, c.Eval.String(), c.Type.String()))
		}
		// Refinement: a nominal annotation whose definition carries a usable
		// where-clause admits only the values that satisfy it, so the predicate
		// folds with self bound to the evaluated value. An overflowed value is
		// already reported as outside the type; a predicate that does not fold
		// to a bool was reported at the type declaration, so both stay silent
		// here (the ir.Invalid style of suppression).
		if !overflow && c.Eval != nil {
			if def := refinedDef(c.Type); def != nil {
				v := eval.Predicate(def.Where, c.Eval, evalEnv{q: q, file: fileID})
				if v != nil && v.Kind == ir.ConstBool && !v.Bool {
					s := at(decl.Value)
					// The power-assert diagram with self bound to the value,
					// indented as a block exactly like a failed assertion's —
					// it shows which comparison rejected the constant.
					d := assert.DiagramSelf(def.Where, c.Eval, evalEnv{q: q, file: fileID})
					diagram := "\n  " + strings.ReplaceAll(d, "\n", "\n  ")
					diags.Add(newRefinementViolationDiagnostic(
						s.offset, s.width, c.Eval.String(), c.Type.String(), ast.Render(def.Where), diagram))
				}
			}
		}
		// An empty or heterogeneous collection literal with no annotation has
		// no type to infer (checking mode never sees it without one).
		if lit, ok := decl.Value.(*ast.CollectionLit); ok && decl.Type == nil && c.Type == ir.Invalid {
			s := at(lit)
			diags.Add(newUninferableCollectionDiagnostic(s.offset, s.width))
		}
	}

	// Compile-time assertions: each condition must resolve, type as bool, and
	// fold to true. An assert produces no IR — it is a diagnostic-only
	// declaration — and every fact it needs is read through q, so the
	// incremental engine tracks its dependencies exactly as it does a const's.
	for _, a := range file.Asserts {
		if a.Cond == nil {
			continue // already a parse diagnostic
		}
		before := diags.Len()

		// Undefined references and unknown namespace members, exactly as for a
		// const's initializer. An assert condition has no annotation, so a bare
		// member is not in scope (nil expected enum).
		reportRefIssues(fileID, a.Cond, q, at, diags, nil)

		// Operator type errors, zero divisors, and stray selfs, through the
		// same checking walks the const path uses.
		condType := infer.Check(a.Cond, env, exprSink(at, diags))
		checkDivByZero(a.Cond, evalEnv{q: q, file: fileID}, func(node ast.Node) {
			s := at(node)
			diags.Add(newDivisionByZeroDiagnostic(s.offset, s.width))
		})
		checkNoSelf(a.Cond, func(node ast.Node) {
			s := at(node)
			diags.Add(newSelfOutsideMethodDiagnostic(s.offset, s.width))
		})

		// The outcome — the folded condition and its power-assert diagram —
		// is module data: the editor's hover and the failure diagnostic both
		// read the very values the assertion was checked with.
		v := eval.Expr(a.Cond, evalEnv{q: q, file: fileID})
		d := assert.Diagram(a.Cond, evalEnv{q: q, file: fileID})
		cond, _, _ := strings.Cut(d, "\n")
		module.Asserts = append(module.Asserts, &ir.Assert{Cond: cond, Doc: a.Doc, Eval: v, Diagram: d, Syntax: a})

		// The condition must be a bool. An Invalid type was reported above
		// (an undefined name, a misapplied operator), so it is not re-reported
		// as a non-bool here.
		if condType != ir.Invalid && !types.IsBoolean(reg, condType) {
			s := at(a.Cond)
			diags.Add(newAssertionNotBoolDiagnostic(s.offset, s.width, condType.String()))
			continue
		}

		// The condition must fold at compile time. When it does not — and
		// nothing above explained why — the assertion itself is the problem:
		// it asks for something the evaluator cannot verify.
		if v == nil || v.Kind != ir.ConstBool {
			if diags.Len() == before {
				s := at(a.Cond)
				diags.Add(newAssertionNotConstantDiagnostic(s.offset, s.width))
			}
			continue
		}

		// The assertion proper. The failure quotes the condition twice — the
		// canonical one-liner (the summary a diagnostic list shows) and the
		// power-assert diagram beneath it, indented as a block so its pipe
		// columns align independently of the message prefix — with the doc
		// comment above the diagram: the broken invariant in the author's
		// own words.
		if !v.Bool {
			s := at(a.Cond)
			doc := ""
			if len(a.Doc) > 0 {
				doc = "\n  " + strings.Join(a.Doc, "\n  ")
			}
			diagram := "\n  " + strings.ReplaceAll(d, "\n", "\n  ")
			diags.Add(newAssertionFailedDiagnostic(s.offset, s.width, cond, doc, diagram))
		}
	}

	// The module's type definitions come from the memoized query — the same
	// objects annotations resolved against, so Named identity never forks. The
	// query resolves silently (its result is reused across revisions, but
	// diagnostics carry offsets that shift on every edit), so the reporting
	// pass re-resolves the declarations fresh and discards the definitions.
	module.Types = q.typeDefs(fileID)
	imp := q.importsOf(fileID)
	bfns := bodyFuncs{local: funcShellsByName(file, fnShells), qualified: qualifiedFuncsFrom(q, imp), shells: fnShells}
	resolveTypes(evalEnv{q: q, file: fileID}, file, at, diags, reg, outerTypes(q, imp), qualifiedFrom(q, imp), bfns)

	// The module's functions are this file's shells, their signatures and
	// bodies (re)resolved here with reporting; their bodies type-check the
	// same way method bodies do.
	funcs := buildFuncSymbols(file)
	qfns := qualifiedFuncsFrom(q, imp)
	module.Funcs = resolveFuncs(file, at, diags, reg, q.universe(fileID), qualifiedFrom(q, imp), qfns, fnShells)
	bodyEnv := evalEnv{q: q, file: fileID}
	checkMethodBodies(reg, module.Types, q.universe(fileID), qualifiedFrom(q, imp), funcs, qfns, bodyEnv, exprSink(at, diags), at, diags)
	checkFuncBodies(reg, file, q.universe(fileID), qualifiedFrom(q, imp), funcs, qfns, bodyEnv, at, diags)
	checkEffects(reg, file, module.Types, q.universe(fileID), qualifiedFrom(q, imp), funcs, qfns, at, diags)

	// Compile-time positions must be pure: a constant initializer and an
	// assert condition fold to values, so an effectful call cannot appear in
	// them at all — pure folds, effectful cannot even be written.
	pureScope := infer.BodyScope{Reg: reg, Universe: q.universe(fileID), Qualified: qualifiedFrom(q, imp), Self: ir.Invalid, Funcs: funcs, QualifiedFuncs: qfns}
	for _, decl := range file.Decls {
		if decl.Value != nil {
			checkPureContext(decl.Value, "constant initializer", pureScope, at, diags)
		}
	}
	for _, a := range file.Asserts {
		if a.Cond != nil {
			checkPureContext(a.Cond, "assert condition", pureScope, at, diags)
		}
	}

	items := diags.Items()
	sort.SliceStable(items, func(i, j int) bool { return items[i].Offset < items[j].Offset })
	return module, items
}

// reportRefIssues reports the reference problems of a constant initializer or
// assert condition: undefined names (distinguishing an ambiguous import),
// namespace members the target does not export (unknown_member), and enum
// members the enum does not declare (unknown_enum_member). expectedEnum is the
// enum a bare member resolves through (a const's annotation; nil for an assert,
// which has none), so a bare member of it is not reported as undefined.
func reportRefIssues(fileID FileID, e ast.Expr, q queries, at func(ast.Node) span, diags *diagnostic.List, expectedEnum *ir.TypeDef) {
	walkRefsEnum(fileID, e, q,
		func(id *ast.Identifier) {
			if id.Name == "" || q.resolve(fileID, id) != nil {
				return
			}
			// A bare member of the expected enum is a resolved reference, not an
			// undefined name.
			if expectedEnum != nil && enumIndex(expectedEnum, id.Name) >= 0 {
				return
			}
			s := at(id)
			if q.ambiguousImport(fileID, id) {
				diags.Add(newAmbiguousImportDiagnostic(s.offset, s.width, id.Name))
				return
			}
			// A bare name under an enum expectation that is not a member of it is
			// an unknown enum member, not a bare undefined name — the author
			// reached for a member that does not exist.
			if expectedEnum != nil {
				diags.Add(newUnknownEnumMemberDiagnostic(s.offset, s.width, expectedEnum.Name, id.Name))
				return
			}
			diags.Add(newUndefinedNameDiagnostic(s.offset, s.width, id.Name))
		},
		func(m *ast.MemberExpr) {
			if m.Member.Name == "" {
				return // a recovered `ns.` — already a parse diagnostic
			}
			if q.resolveMember(fileID, m) == nil {
				s := at(m)
				ns, _ := m.Receiver.(*ast.Identifier)
				diags.Add(newUnknownMemberDiagnostic(s.offset, s.width, m.Member.Name, ns.Name))
			}
		},
		func(m *ast.MemberExpr) {
			// A qualified type-member access: the receiver names a type, so the
			// member must be one of its own — an enum member (Rarity.Bogus) or an
			// associated constant (int8.Bogus, Level.Bogus).
			if m.Member.Name == "" {
				return // a recovered `Rarity.` — already a parse diagnostic
			}
			recv := m.Receiver.(*ast.Identifier)
			def := q.universe(fileID)[recv.Name]
			if def != nil && def.Enum != nil {
				if enumIndex(def, m.Member.Name) < 0 {
					s := at(m)
					diags.Add(newUnknownEnumMemberDiagnostic(s.offset, s.width, recv.Name, m.Member.Name))
				}
				return
			}
			if assocConstIndex(def, m.Member.Name) < 0 {
				s := at(m)
				diags.Add(newUnknownAssociatedConstDiagnostic(s.offset, s.width, recv.Name, m.Member.Name))
			}
		})
}

// annotationEnum resolves a constant's type annotation to the enum it names, or
// nil when the constant has no annotation or it does not name an enum. It is a
// pure name lookup in the file's universe — it does not call typeOf — so the
// value lowering it feeds stays independent of the type query.
func annotationEnum(q queries, fileID FileID, decl *ast.ConstDecl) *ir.TypeDef {
	if decl.Type == nil {
		return nil
	}
	r := &infer.TypeResolver{Defs: q.universe(fileID), Qualified: qualifiedFrom(q, q.importsOf(fileID))}
	if n, ok := r.ResolveType(decl.Type, nil).(*ir.Named); ok && n.Def != nil && n.Def.Enum != nil {
		return n.Def
	}
	return nil
}

// refinedDef returns the definition behind a nominal (or applied) annotation
// type when it carries a usable refinement predicate, or nil. An unannotated
// constant's type is the underlying composite, never a Named, so refinement is
// annotation-driven by construction.
func refinedDef(t ir.Type) *ir.TypeDef {
	var def *ir.TypeDef
	switch t := t.(type) {
	case *ir.Named:
		def = t.Def
	case *ir.App:
		def = t.Def
	}
	if def == nil || def.Where == nil {
		return nil
	}
	return def
}

// typeNameReporter builds the callback the type resolver reports a failed type
// name through: a name two or more imports claimed is ambiguous_import, any
// other unresolved name is unknown_type.
func typeNameReporter(fileID FileID, q queries, at func(ast.Node) span, diags *diagnostic.List) func(ast.Node, string) {
	ambiguous := map[string]bool{}
	for name, b := range q.importsOf(fileID).types {
		if b.ambiguous {
			ambiguous[name] = true
		}
	}
	return func(node ast.Node, name string) {
		s := at(node)
		if ambiguous[name] {
			diags.Add(newAmbiguousImportDiagnostic(s.offset, s.width, name))
			return
		}
		diags.Add(newUnknownTypeDiagnostic(s.offset, s.width, name))
	}
}

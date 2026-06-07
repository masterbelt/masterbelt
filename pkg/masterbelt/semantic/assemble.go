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
	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
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
		AmbiguousUnionMember: func(node ast.Node, got, want ir.Type) {
			s := at(node)
			diags.Add(newAmbiguousUnionMemberDiagnostic(s.offset, s.width, got.String(), want.String()))
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
		BoundNotSatisfied: func(call *ast.CallExpr, typ, bound ir.Type) {
			s := at(call)
			diags.Add(newBoundNotSatisfiedDiagnostic(s.offset, s.width, typ.String(), bound.String()))
		},
		UninferableTypeParam: func(call *ast.CallExpr, name string) {
			s := at(call)
			diags.Add(newUninferableTypeParamDiagnostic(s.offset, s.width, name))
		},
		NoMethodOnUnboundedTypeVar: func(node ast.Node, method string) {
			s := at(node)
			diags.Add(newNoMethodOnUnboundedTypevarDiagnostic(s.offset, s.width, method))
		},
		MapKeyNotComparable: func(lit *ast.CollectionLit, key, bound ir.Type) {
			s := at(lit)
			diags.Add(newBoundNotSatisfiedDiagnostic(s.offset, s.width, key.String(), bound.String()))
		},
	}
}

// bodySink builds the function/method body checking sink: exprSink plus the
// member-aware Checked hook that range- and refinement-checks every (value,
// expected-type) pair the body walk visits — a return against the declared
// result, a let against its annotation, an argument against the parameter. It is
// the body twin of the const initializer's Checked hook, so a constant value
// flowing into a sized or refined position (the union member included) is enforced
// in a body exactly as in a const. env folds the values; a non-constant value
// (a parameter, a local) does not fold and is left to the runtime.
func bodySink(at func(ast.Node) span, diags *diagnostic.List, reg *builtin.Registry, env evalEnv) *infer.Sink {
	sink := exprSink(at, diags)
	sink.Checked = func(e ast.Expr, want ir.Type) {
		checkMemberFlow(reg, e, want, env, at, diags)
	}
	return sink
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
				Defs:           q.universe(fileID),
				Qualified:      qualifiedFrom(q, q.importsOf(fileID)),
				Report:         typeNameReporter(fileID, q, at, diags),
				Registry:       reg,
				BoundViolation: boundViolationReporter(at, diags),
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
				// Range and refinement, against the member the value flows in as —
				// so a nested position (a collection entry, a record field, an
				// argument) enforces both checks at the same sites, the union member
				// included (its Fits and refinedDef both pass through directly).
				checkMemberFlow(reg, e, want, evalEnv{q: q, file: fileID}, at, diags)
			}
			// A conversion to a sized integer (short(70000), Level(70000)) range-
			// checks its argument against the target — the diagnostic the const-level
			// check cannot make when the constant's own type is a union the value
			// flows into (the union's Fits passes through), and also the one for the
			// direct case (const A: short = short(70000)), whose folded value is now
			// nil (eval refuses the out-of-range conversion), so the const-level check
			// no longer sees it. An overflowing conversion folds to nil, so it never
			// also trips the const-level report — the two are mutually exclusive. The
			// argument is folded here (the type layer flagged the conversion through
			// ScalarConversion); a non-constant argument does not fold and is left to
			// the runtime.
			sink.ScalarConversion = func(call *ast.CallExpr, target ir.Type) {
				if len(call.Arguments) != 1 {
					return
				}
				if v := eval.Expr(call.Arguments[0], evalEnv{q: q, file: fileID}); v != nil && v.Kind == ir.ConstInt && !types.Fits(reg, target, v.Int) {
					s := at(call)
					diags.Add(newConstantOverflowDiagnostic(s.offset, s.width, v.String(), target.String()))
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
		// The top-level value's range and refinement, against the member it flows
		// in as (c.Type's selected union member, or c.Type itself). It folds the
		// value raw — the arbitrary-precision nint has no fixed range, a bool never
		// overflows, and an overflowing conversion folds to nil and is reported at
		// its own site — and an unsatisfied where-predicate carries the power-assert
		// diagram naming the comparison that rejected the constant. It is the same
		// member-aware check the nested positions run through Checked.
		if decl.Value != nil {
			checkMemberFlow(reg, decl.Value, c.Type, evalEnv{q: q, file: fileID}, at, diags)
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
	// A function or method body's returns, lets, and arguments run the same
	// member-aware range and refinement check the const initializer does, so a
	// constant value flowing into a sized or refined (union member) result, local,
	// or parameter is checked at the body site too — the position the result-type
	// soundness gap left unchecked. Only a constant value folds; a body-local or
	// parameter reference does not, and is left to the runtime.
	checkMethodBodies(reg, module.Types, q.universe(fileID), qualifiedFrom(q, imp), funcs, qfns, bodyEnv, bodySink(at, diags, reg, bodyEnv), at, diags)
	checkFuncBodies(reg, file, q.universe(fileID), qualifiedFrom(q, imp), funcs, qfns, bodyEnv, bodySink(at, diags, reg, bodyEnv), at, diags)
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
// nil when the constant has no annotation or it does not name an enum. A union
// annotation carrying an enum (R | error) resolves to that enum, so a bare
// member is accepted under it exactly as under the bare enum. It is a pure name
// lookup in the file's universe — it does not call typeOf — so the value
// lowering it feeds stays independent of the type query.
func annotationEnum(q queries, fileID FileID, decl *ast.ConstDecl) *ir.TypeDef {
	if decl.Type == nil {
		return nil
	}
	return typeExprEnum(q, fileID, decl.Type)
}

// typeExprEnum resolves a written type annotation to the enum it names, sharing
// the resolution annotationEnum feeds the value lowering: a bare enum, or a
// union carrying one (R | error), through a pure universe name lookup (never the
// type query). An App or named alias of a union does not unwrap here — the bare
// member it carries is the same set the lowering leaves unresolved, so the two
// agree. It is the channel the editor's expected-enum completion reads, so the
// candidates it offers are exactly the members the lowering would resolve.
func typeExprEnum(q queries, fileID FileID, t ast.TypeExpr) *ir.TypeDef {
	if t == nil {
		return nil
	}
	r := &infer.TypeResolver{Defs: q.universe(fileID), Qualified: qualifiedFrom(q, q.importsOf(fileID))}
	return enumDefOf(r.ResolveType(t, nil))
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

// checkMemberFlow reports the value-soundness diagnostics for a value flowing
// into the type want at expression e: an integer outside the range (or a
// where-predicate violation) of the member want resolves to. It is the unified
// member-aware check the const, nested-position (Checked), and function-body
// return sites all run, so range and refinement are enforced at exactly the same
// positions:
//
//   - the effective target is the union member the value flows in as (eval.MemberFor
//     runs the same exact→unique selection the fold tags with), or want itself when
//     it is not a union — so `sbyte | error` checks the value against `sbyte`, and a
//     refined member's predicate runs;
//   - the value is folded with no expectation (eval.Expr), so the raw value is read
//     even though the expectation-driven fold refuses to build it (memberAdmits);
//     an overflowing conversion already folds to nil and is reported at its own site
//     (ScalarConversion), so it is not seen here and never double-reported.
//
// A non-constant or unfoldable value (a parameter, a predicate that does not fold
// to a bool) is left unchecked — the runtime's job, the conservative discipline
// the range and refinement checks already share.
func checkMemberFlow(reg *builtin.Registry, e ast.Expr, want ir.Type, env evalEnv, at func(ast.Node) span, diags *diagnostic.List) {
	if want == ir.Invalid {
		return
	}
	v := eval.Expr(e, env)
	if v == nil {
		return
	}
	member := eval.MemberFor(e, want, env)
	if v.Kind == ir.ConstInt && !types.Fits(reg, member, v.Int) {
		s := at(e)
		diags.Add(newConstantOverflowDiagnostic(s.offset, s.width, v.String(), member.String()))
		return
	}
	if def := refinedDef(member); def != nil {
		p := eval.Predicate(def.Where, v, def, env)
		if p != nil && p.Kind == ir.ConstBool && !p.Bool {
			s := at(e)
			d := assert.DiagramSelf(def.Where, v, def, env)
			diagram := "\n  " + strings.ReplaceAll(d, "\n", "\n  ")
			diags.Add(newRefinementViolationDiagnostic(
				s.offset, s.width, v.String(), member.String(), ast.Render(def.Where), diagram))
		}
	}
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

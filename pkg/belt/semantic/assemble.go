// This file holds the assembler: assemble turns a file's AST plus the semantic
// queries into its IR module and diagnostics. It is shared by the reference
// analyzer (Analyze) and the incremental Program, so the two cannot diverge.
// exprSink wires the type-checking walk's findings to diagnostics, and the
// assembly-local helpers (refinedDef, typeNameReporter) live here too.

package semantic

import (
	"maps"
	"sort"
	"strings"

	"github.com/masterbelt/masterbelt/pkg/belt/assert"
	"github.com/masterbelt/masterbelt/pkg/belt/builtin"
	"github.com/masterbelt/masterbelt/pkg/belt/eval"
	"github.com/masterbelt/masterbelt/pkg/belt/lower"
	"github.com/masterbelt/masterbelt/pkg/belt/types"
	"github.com/masterbelt/masterbelt/pkg/belt/types/infer"
	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// exprSink wires the checking walk's findings to their diagnostics, and its
// overload-selection streams to the resolutions collector (res — the channel
// the IR write-back and the late re-fold read). The Checked stream is left
// unset — the const path hooks it to the eval-based value-range check, which
// needs the declaration's context.
func exprSink(at func(ast.Node) span, diags *diagnostic.List, res *callResolutions) *infer.Sink {
	sink := &infer.Sink{}
	wireResolutionStreams(sink, res)
	wireOverloadDiagnostics(sink, at, diags)
	wireExprDiagnostics(sink, at, diags)
	wireRecordAndGenericDiagnostics(sink, at, diags)
	return sink
}

// resolutionSink wires only the overload-selection and typing streams to the
// resolutions collector, with no diagnostics — the settling-only half of exprSink.
// It types a value graph for the IR write-back (so the editor reads its receiver
// types and selected overloads) without reporting, for a position whose
// diagnostics another pass owns or whose typing is informational.
func resolutionSink(res *callResolutions) *infer.Sink {
	sink := &infer.Sink{}
	wireResolutionStreams(sink, res)
	return sink
}

// wireResolutionStreams wires the informational overload-selection, typing, and
// adaption streams to the resolutions collector. res is nil where no collector
// is in play (a refinement predicate's reporting pass), and the selections are
// then simply not recorded.
func wireResolutionStreams(sink *infer.Sink, res *callResolutions) {
	sink.ResolvedMethod = func(call *ast.CallExpr, m *ir.Method) {
		if res != nil {
			res.methods[call] = m
		}
	}
	sink.ResolvedStatic = func(call *ast.CallExpr, m *ir.Method) {
		if res != nil {
			res.statics[call] = m
		}
	}
	sink.ResolvedFunc = func(call *ast.CallExpr, fd *ast.FuncDecl) {
		if res != nil {
			res.funcs[call] = fd
		}
	}
	sink.ResolvedEnumMember = func(e ast.Expr, def *ir.TypeDef, index int) {
		if res != nil {
			res.enumMembers[e] = enumMember{def: def, index: index}
		}
	}
	sink.CallSubst = func(call *ast.CallExpr, subst map[string]ir.Type) {
		if res != nil {
			// Cloned: the checker threads one live map through a call's
			// argument checking, and the record must stay the solution as
			// of this call's settling.
			res.substs[call] = maps.Clone(subst)
		}
	}
	sink.Typed = func(e ast.Expr, t ir.Type) {
		if res != nil {
			res.types[e] = t
		}
	}
	sink.Adapted = func(e ast.Expr, to ir.Type) {
		if res != nil {
			res.adapts[e] = to
		}
	}
}

// wireOverloadDiagnostics wires the method/function overload-resolution findings
// to their diagnostics.
func wireOverloadDiagnostics(sink *infer.Sink, at func(ast.Node) span, diags *diagnostic.List) {
	sink.InvalidOp = func(node ast.Node, method, operands string) {
		s := at(node)
		diags.Add(newInvalidOperationDiagnostic(s.offset, s.width, method, operands))
	}
	sink.NoMatchingOverload = func(node ast.Node, method, operands string) {
		s := at(node)
		diags.Add(newNoMatchingOverloadDiagnostic(s.offset, s.width, method, operands))
	}
	sink.AmbiguousOverload = func(node ast.Node, method, operands string) {
		s := at(node)
		diags.Add(newAmbiguousOverloadDiagnostic(s.offset, s.width, method, operands))
	}
	sink.CallArityMismatch = func(call *ast.CallExpr, name string, got, want int) {
		s := at(call)
		diags.Add(newArityMismatchDiagnostic(s.offset, s.width, name, got, want))
	}
	sink.NoMatchingFuncOverload = func(call *ast.CallExpr, name, types string) {
		s := at(call)
		diags.Add(newNoMatchingFuncOverloadDiagnostic(s.offset, s.width, name, types))
	}
	sink.AmbiguousFuncOverload = func(call *ast.CallExpr, name, types string) {
		s := at(call)
		diags.Add(newAmbiguousFuncOverloadDiagnostic(s.offset, s.width, name, types))
	}
	sink.UnknownStatic = func(call *ast.CallExpr, name, typ string) {
		s := at(call)
		diags.Add(newUnknownStaticDiagnostic(s.offset, s.width, name, typ))
	}
}

// wireExprDiagnostics wires the expression-typing findings — mismatches, union
// member ambiguity, ternaries, and function-literal inference — to their
// diagnostics.
func wireExprDiagnostics(sink *infer.Sink, at func(ast.Node) span, diags *diagnostic.List) {
	sink.Mismatch = func(node ast.Node, got, want ir.Type) {
		s := at(node)
		diags.Add(newTypeMismatchDiagnostic(s.offset, s.width, got.String(), want.String()))
	}
	sink.AmbiguousUnionMember = func(node ast.Node, got, want ir.Type) {
		s := at(node)
		diags.Add(newAmbiguousUnionMemberDiagnostic(s.offset, s.width, got.String(), want.String()))
	}
	sink.TernaryCondNotBool = func(cond ast.Expr, got ir.Type) {
		s := at(cond)
		diags.Add(newTernaryConditionNotBoolDiagnostic(s.offset, s.width, got.String()))
	}
	sink.TernaryBranchMismatch = func(node ast.Node, then, els ir.Type) {
		s := at(node)
		diags.Add(newTernaryBranchMismatchDiagnostic(s.offset, s.width, then.String(), els.String()))
	}
	sink.ArityMismatch = func(lit *ast.FuncLit, got, want int) {
		s := at(lit)
		diags.Add(newLambdaArityMismatchDiagnostic(s.offset, s.width, got, want))
	}
	sink.UninferableParam = func(p *ast.ParamDef) {
		s := at(p)
		diags.Add(newUninferableParameterDiagnostic(s.offset, s.width, p.Name))
	}
	sink.UninferableResult = func(lit *ast.FuncLit) {
		s := at(lit)
		diags.Add(newUninferableResultDiagnostic(s.offset, s.width))
	}
	sink.MetatypeSlot = func(lit *ast.FuncLit, t *ir.Func) {
		// A function literal's parameter or result may not be a type value
		// (fn(x: type)): a type-value function written inline is rejected exactly as
		// a declared one is, even when the lambda is called rather than stored.
		reportMetatypeSlot(at, diags, lit, t)
	}
}

// wireRecordAndGenericDiagnostics wires the record-literal and generic-call
// (bound, type-param, map-key) findings to their diagnostics.
func wireRecordAndGenericDiagnostics(sink *infer.Sink, at func(ast.Node) span, diags *diagnostic.List) {
	sink.MissingField = func(lit *ast.RecordLit, field string, typ ir.Type) {
		s := at(lit)
		diags.Add(newMissingFieldDiagnostic(s.offset, s.width, field, typ.String()))
	}
	sink.UnknownField = func(field *ast.FieldInit, name string, typ ir.Type) {
		s := at(field)
		diags.Add(newUnknownFieldDiagnostic(s.offset, s.width, name, typ.String()))
	}
	sink.UninferableRecord = func(lit *ast.RecordLit) {
		s := at(lit)
		diags.Add(newUninferableRecordDiagnostic(s.offset, s.width))
	}
	sink.UnknownRecordType = func(lit *ast.RecordLit, name string) {
		s := at(lit)
		diags.Add(newUnknownTypeDiagnostic(s.offset, s.width, name))
	}
	sink.NotARecord = func(lit *ast.RecordLit, typ ir.Type) {
		s := at(lit)
		diags.Add(newNotARecordDiagnostic(s.offset, s.width, typ.String()))
	}
	sink.BoundNotSatisfied = func(call *ast.CallExpr, typ, bound ir.Type) {
		s := at(call)
		diags.Add(newBoundNotSatisfiedDiagnostic(s.offset, s.width, typ.String(), bound.String()))
	}
	sink.UninferableTypeParam = func(call *ast.CallExpr, name string) {
		s := at(call)
		diags.Add(newUninferableTypeParamDiagnostic(s.offset, s.width, name))
	}
	sink.NoMethodOnUnboundedTypeVar = func(node ast.Node, method string) {
		s := at(node)
		diags.Add(newNoMethodOnUnboundedTypevarDiagnostic(s.offset, s.width, method))
	}
	sink.MapKeyNotComparable = func(lit *ast.CollectionLit, key, bound ir.Type) {
		s := at(lit)
		diags.Add(newBoundNotSatisfiedDiagnostic(s.offset, s.width, key.String(), bound.String()))
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
func bodySink(at func(ast.Node) span, diags *diagnostic.List, reg *builtin.Registry, env exprFolder, res *callResolutions) *infer.Sink {
	sink := exprSink(at, diags, res)
	sink.Checked = func(e ast.Expr, want ir.Type) {
		checkMemberFlow(reg, e, want, env, at, diags)
	}
	// A sized-scalar conversion in a body range-checks its argument exactly as it
	// does in a const initializer (short(70000) inside a return, a let, an
	// argument): eval folds an overflowing conversion to nil, so the Checked hook
	// above never sees it — this is the only site that reports the overflow.
	sink.ScalarConversion = func(call *ast.CallExpr, target ir.Type) {
		checkScalarConversion(reg, call, target, env, at, diags)
	}
	return sink
}

// assemble builds one file's IR module and semantic diagnostics from its AST,
// using q for the resolution and typing facts; fileID names the file within
// the program, scoping its identifier resolution, and shells/fnShells hold the
// program-wide IR constants and functions (this file's and every importable
// file's). It is shared by the reference and incremental analyzers, so they
// cannot diverge. The work is a fixed sequence of phases, each a method on
// the assembler, run in dependency order: the constants' value walks first,
// then types and functions, then the post-check write-back, and only then the
// passes that read the settled graph (index writes, the late re-fold, the
// asserts, the publication rule).
func assemble(fileID FileID, file *ast.File, positions map[cst.Green]span, q queries, shells map[*ast.ConstDecl]*ir.Const, fnShells map[*ast.FuncDecl]*ir.Function) (*ir.Module, []diagnostic.Diagnostic) {
	imp := q.importsOf(fileID)
	a := &assembler{
		fileID:   fileID,
		file:     file,
		q:        q,
		at:       func(n ast.Node) span { return spanOf(positions, n) },
		diags:    &diagnostic.List{},
		env:      typeEnv{q: q, file: fileID},
		reg:      q.registry(),
		res:      newCallResolutions(),
		shells:   shells,
		fnShells: fnShells,
		module:   &ir.Module{},
		imp:      imp,
		funcs:    buildFuncSymbols(file),
		qfns:     qualifiedFuncsFrom(q, imp),
		bfns: bodyFuncs{
			local:      funcShellsByName(file, fnShells),
			qualified:  qualifiedFuncsFrom(q, imp),
			shells:     fnShells,
			constRef:   constRefFrom(q, fileID),
			nsConstRef: nsConstRefFrom(q, fileID),
		},
	}

	// Phase zero: prime this file's function-body query. The query resolves
	// the bodies onto the shared shells silently; the reporting pass below
	// re-resolves them and the write-back annotates them. Priming pins the
	// memo before this assemble's annotations exist, so a later assemble of
	// an importing file can never be the FIRST to demand the query and have
	// its silent re-resolution wipe the annotations — the order-dependence
	// the full/incremental dump parity flaked on. Only the own file is
	// primed: cross-file function facts flow through the value queries,
	// whose memo edges keep the early cutoff fine-grained (priming every
	// reachable file here would couple this module to every reachable
	// file's funcs and recompute it on any function-body edit anywhere).
	q.funcsOf(fileID)

	a.collectConsts()
	checkUses(fileID, file, q, a.at, a.diags)
	a.reportRedeclarations()
	a.resolveConsts()
	a.resolveTypeDecls()
	a.resolveFuncDecls()
	// A file on the builtin-surface trust channel (a bundled std module) is
	// licensed to declare extern and `= builtin`; only assembled user files are
	// held to the surface rule. The prelude never reaches here at all.
	if !trustedFileID(fileID) {
		checkBuiltinSurface(file, a.at, a.diags)
	}
	a.checkAssocConstRefs()
	a.settleInitializerTypes()
	a.reportMasterValidationRefs()
	genv := a.writeBack()
	checkIndexWritesIR(a.module, genv, a.at, a.diags)
	a.refoldConsts(genv)
	a.evaluateAsserts(genv)
	a.checkPureContexts()
	enforceEvalPublication(fileID, file, a.module, shells, q, a.own, genv, a.at, a.diags)

	items := a.diags.Items()
	sort.SliceStable(items, func(i, j int) bool { return items[i].Offset < items[j].Offset })
	return a.module, items
}

// assembler carries one assemble run's shared state: the inputs, the
// diagnostic sink, and the facts the later phases read off the earlier ones
// (the import surface, the function symbol tables, the call resolutions the
// checking walks stream and the write-back binds).
type assembler struct {
	fileID   FileID
	file     *ast.File
	q        queries
	at       func(ast.Node) span
	diags    *diagnostic.List
	env      typeEnv
	reg      *builtin.Registry
	res      *callResolutions
	shells   map[*ast.ConstDecl]*ir.Const
	fnShells map[*ast.FuncDecl]*ir.Function
	module   *ir.Module

	imp   importTable
	funcs map[string][]*ast.FuncDecl
	qfns  func(namespace, name string) []*ast.FuncDecl
	bfns  bodyFuncs

	// own indexes this file's constant shells, set by writeBack for the
	// phases that read only the file's own declarations (the fold env, the
	// publication rule).
	own map[*ast.ConstDecl]*ir.Const
}

// folder returns the lower-then-fold channel the in-walk checks read.
func (a *assembler) folder() exprFolder {
	return exprFolder{q: a.q, file: a.fileID}
}

// collectConsts fills the module's constants: this file's shells, in source
// order.
func (a *assembler) collectConsts() {
	for _, decl := range a.file.Decls {
		a.module.Consts = append(a.module.Consts, a.shells[decl])
	}
}

// reportRedeclarations reports redeclarations of the same name — constants
// and functions each within their own namespace (a call form looks up
// functions, a bare name constants, so the two tables never collide).
func (a *assembler) reportRedeclarations() {
	seen := map[string]bool{}
	for _, decl := range a.file.Decls {
		if decl.Name == "" {
			continue // already a parse diagnostic
		}
		if seen[decl.Name] {
			s := a.at(decl)
			a.diags.Add(newDuplicateDeclarationDiagnostic(s.offset, s.width, decl.Name))
		}
		seen[decl.Name] = true
	}
}

// resolveConsts runs the constants' phase: each declaration's value is
// lowered, its facts are read off the queries, and its initializer walked
// with the reporting sinks.
func (a *assembler) resolveConsts() {
	cyclic := cyclicDecls(a.fileID, a.file, a.q)
	for _, decl := range a.file.Decls {
		a.resolveConst(decl, cyclic[decl])
	}
}

// resolveConst assembles one constant: the lowered value graph, the settled
// type and fold off the queries, the annotation resolution, and the checking
// walks over the initializer.
func (a *assembler) resolveConst(decl *ast.ConstDecl, cyclic bool) {
	c := a.shells[decl]
	// A bare member in the initializer (const Top: Rarity = Legend) lowers
	// through the annotation's enum, so resolve it first. The annotation is
	// resolved against the universe — a pure name lookup, not the type query
	// — so the value lowering stays independent of typeOf.
	c.Value = lower.Value(decl.Value, constBinder{q: a.q, file: a.fileID, irOf: a.shells, fnOf: a.fnShells, expected: annotationEnum(a.q, a.fileID, decl)})
	c.Type = a.q.typeOf(decl)
	c.Eval = a.q.valueOf(decl)

	// A const may not hold a type value: const x = sbyte or const x: type is
	// type_in_value_position. A projected type is named with a type alias (type X
	// = Character.level), never bound to a const; the type value lives only inside
	// a comptime expression.
	reportMetatypeSlot(a.at, a.diags, decl, c.Type)

	// Resolve the annotation with reporting enabled, so an unknown type
	// name anywhere in it (e.g. list<Bogus>) is diagnosed at its own node.
	// The annotation resolves in the file's universe: its own type
	// declarations shadowing its imported ones, over the registry.
	annType := ir.Invalid
	if decl.Type != nil {
		r := &infer.TypeResolver{
			Defs:            a.q.universe(a.fileID),
			Qualified:       qualifiedFrom(a.q, a.imp),
			Report:          typeNameReporter(a.fileID, a.q, a.at, a.diags),
			Registry:        a.reg,
			BoundViolation:  boundViolationReporter(a.at, a.diags),
			ArityMismatch:   arityMismatchReporter(a.at, a.diags),
			ProjectionError: projectionErrorReporter(a.at, a.diags),
		}
		annType = r.ResolveType(decl.Type, nil)
	}

	if decl.Value != nil {
		a.checkConstValue(decl, annType)
	}

	if cyclic {
		s := a.at(decl)
		a.diags.Add(newCyclicReferenceDiagnostic(s.offset, s.width, decl.Name))
	}
	// The top-level value's range and refinement, against the member it flows
	// in as (c.Type's selected union member, or c.Type itself). It folds the
	// value raw — the arbitrary-precision nint has no fixed range, a bool never
	// overflows, and an overflowing conversion folds to nil and is reported at
	// its own site — and an unsatisfied where-predicate carries the power-assert
	// diagram naming the comparison that rejected the constant. It is the same
	// member-aware check the nested positions run through Checked.
	if decl.Value != nil {
		checkMemberFlow(a.reg, decl.Value, c.Type, a.folder(), a.at, a.diags)
	}
	// An empty or heterogeneous collection literal with no annotation has
	// no type to infer (checking mode never sees it without one).
	if lit, ok := decl.Value.(*ast.CollectionLit); ok && decl.Type == nil && c.Type == ir.Invalid {
		s := a.at(lit)
		a.diags.Add(newUninferableCollectionDiagnostic(s.offset, s.width))
	}
}

// checkConstValue runs the reporting walks over one constant's initializer:
// the reference issues, the type-checking walk with the range/refinement and
// conversion hooks, and the expression-level checks (zero divisors, zero
// range steps, stray selfs).
func (a *assembler) checkConstValue(decl *ast.ConstDecl, annType ir.Type) {
	// Undefined references: every value-position identifier that resolves
	// to no declaration — distinguishing names that failed because two or
	// more imports claimed them (ambiguous_import) — and every namespace
	// member access whose member the target does not export
	// (unknown_member). Method names are not value references; the walk
	// skips them, and it treats a namespace access as one unit, so its
	// receiver is never reported as an undefined value.
	reportRefIssues(a.fileID, decl.Value, a.q, a.at, a.diags, annotationEnum(a.q, a.fileID, decl), nil)
	// One checking walk reports the expression diagnostics: operator
	// type errors, type mismatches (against the annotation when there
	// is one, and inside function-literal bodies), and literals whose
	// parameter or result types cannot be inferred. Value-range checks
	// hook the walk's Checked stream so infer stays eval-free; the
	// top-level value is range-checked against c.Type by the caller, so only
	// the inner expressions (collection entries, record fields,
	// returns) are checked here — a typed record literal pushes its
	// field types even without an annotation.
	sink := exprSink(a.at, a.diags, a.res)
	sink.Checked = func(e ast.Expr, want ir.Type) {
		if e == decl.Value {
			return
		}
		// Range and refinement, against the member the value flows in as —
		// so a nested position (a collection entry, a record field, an
		// argument) enforces both checks at the same sites, the union member
		// included (its Fits and refinedDef both pass through directly).
		checkMemberFlow(a.reg, e, want, a.folder(), a.at, a.diags)
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
		checkScalarConversion(a.reg, call, target, a.folder(), a.at, a.diags)
	}
	if annType != ir.Invalid {
		// The annotation is pushed into the value.
		infer.CheckAgainst(decl.Value, annType, a.env, sink)
	} else {
		if decl.Type != nil {
			// The annotation failed to resolve and was reported at its
			// own node; had it resolved, it would have supplied the
			// inferred record form its type — don't pile on.
			sink.UninferableRecord = nil
		}
		infer.Check(decl.Value, a.env, sink)
	}
	a.checkExprDiagnostics(decl.Value)
}

// checkExprDiagnostics runs the expression-level checks shared by a constant
// initializer and an assert condition: division or remainder by a zero
// divisor, range(start, end, step) with a step that folds to zero, and self
// outside a method body (a constant and an assert have no receiver).
func (a *assembler) checkExprDiagnostics(e ast.Expr) {
	checkDivByZero(e, a.folder(), func(node ast.Node) {
		s := a.at(node)
		a.diags.Add(newDivisionByZeroDiagnostic(s.offset, s.width))
	})
	checkRangeStepZero(e, a.folder(), func(node ast.Node) {
		s := a.at(node)
		a.diags.Add(newRangeStepZeroDiagnostic(s.offset, s.width))
	})
	checkNoSelf(e, func(node ast.Node) {
		s := a.at(node)
		a.diags.Add(newSelfOutsideMethodDiagnostic(s.offset, s.width))
	})
}

// resolveTypeDecls fills the module's type definitions and re-resolves the
// declarations with reporting. The definitions come from the memoized query —
// the same objects annotations resolved against, so Named identity never
// forks. The query resolves silently (its result is reused across revisions,
// but diagnostics carry offsets that shift on every edit), so the reporting
// pass re-resolves the declarations fresh and discards the definitions.
func (a *assembler) resolveTypeDecls() {
	a.module.Types = a.q.typeDefs(a.fileID)
	resolveTypes(a.folder(), a.file, a.at, a.diags, a.res, a.reg, outerTypes(a.q, a.imp), qualifiedFrom(a.q, a.imp), a.bfns)
}

// resolveFuncDecls fills the module's functions — this file's shells, their
// signatures and bodies (re)resolved with reporting — and runs the body and
// effect checks. A function or method body's returns, lets, and arguments run
// the same member-aware range and refinement check the const initializer
// does, so a constant value flowing into a sized or refined (union member)
// result, local, or parameter is checked at the body site too. Only a
// constant value folds; a body-local or parameter reference does not, and is
// left to the runtime.
func (a *assembler) resolveFuncDecls() {
	a.module.Funcs = resolveFuncs(a.file, a.at, a.diags, a.reg, a.q.universe(a.fileID), qualifiedFrom(a.q, a.imp), a.bfns)
	bodyEnv := a.folder()
	constShadows := constShadowsFrom(a.q, a.fileID)
	nsShadows := namespaceShadowsFrom(a.q, a.fileID)
	checkMethodBodies(a.reg, a.module.Types, a.q.universe(a.fileID), qualifiedFrom(a.q, a.imp), a.funcs, a.qfns, constShadows, nsShadows, bodyEnv, bodySink(a.at, a.diags, a.reg, bodyEnv, a.res), a.at, a.diags)
	checkFuncBodies(a.reg, a.file, a.q.universe(a.fileID), qualifiedFrom(a.q, a.imp), a.funcs, a.qfns, constShadows, nsShadows, bodyEnv, bodySink(a.at, a.diags, a.reg, bodyEnv, a.res), a.at, a.diags)
	checkEffects(a.reg, a.file, a.module.Types, a.q.universe(a.fileID), qualifiedFrom(a.q, a.imp), a.funcs, a.qfns, constShadows, a.res, a.at, a.diags)
}

// checkAssocConstRefs reports the reference diagnostics for the associated-
// constant initializers: the undefined names, unknown members, and stray
// selfs the const phase reports for a top-level initializer, anchored the
// same way — an unresolvable reference in an impl-block const must be as loud
// as anywhere else. A bare member resolves through the annotation's enum,
// exactly as a top-level const's does.
func (a *assembler) checkAssocConstRefs() {
	check := func(consts []*ast.ConstDecl) {
		for _, c := range consts {
			if c.Value == nil {
				continue
			}
			reportRefIssues(a.fileID, c.Value, a.q, a.at, a.diags, typeExprEnum(a.q, a.fileID, c.Type), nil)
			checkNoSelf(c.Value, func(node ast.Node) {
				s := a.at(node)
				a.diags.Add(newSelfOutsideMethodDiagnostic(s.offset, s.width))
			})
		}
	}
	for _, td := range a.file.Types {
		check(td.Consts)
	}
	for _, ed := range a.file.Enums {
		check(ed.Consts)
	}
	for _, md := range a.file.Masters {
		check(md.Consts)
	}
}

// settleInitializerTypes types the associated-constant and enum-member initializers,
// streaming the overload selections and settled types into res so the write-back
// annotates their retained value graphs the way it annotates a body's — giving the
// editor the receiver types and selected overloads that hover and go-to-definition
// read — and reporting the type errors an associated constant's initializer carries
// (an operator on mismatched operands, a call with no matching overload), which were
// folded but never typed before. The reference and stray-self checks are already
// reported (checkAssocConstRefs) and value ranges stay the fold's, so nothing is
// reported twice.
func (a *assembler) settleInitializerTypes() {
	assocSink := func(annotationInvalid bool) *infer.Sink {
		sink := exprSink(a.at, a.diags, a.res)
		if annotationInvalid {
			// The annotation failed to resolve (reported at its own site); with no
			// resolved type to supply, an inferred record literal would pile on an
			// uninferable_record, so it is suppressed the way the top-level const path
			// suppresses it in the same situation.
			sink.UninferableRecord = nil
		}
		return sink
	}
	// An enum member's initializer is settled silently for the editor, not reported:
	// reporting its type errors needs the broken-value withholding the enum value
	// resolution does not yet do (a type-invalid member still folds and feeds the
	// duplicate-value check), so enum-member reporting is its own follow-up.
	settleInitializers(a.module.Types, a.file.Enums, a.env, assocSink, resolutionSink(a.res))
}

// settleInitializers types every associated-constant initializer through assocSink
// and every enum-member initializer through enumSink, streaming the facts to those
// sinks. It is shared by the assemble pass (whose sinks feed the IR write-back,
// reporting on the associated constants it type-checks) and the editor's type-query
// walk (whose one sink captures the expression types and reports nothing), so the two
// settle these positions identically. An associated constant is checked in the owning
// type's generic-parameter scope, so a value reading the owner's parameter — a function
// literal whose parameter is T — resolves it the way a method body does rather than
// reporting it uninferable; assocSink only suppresses the inferred-record pile-on of a
// constant whose annotation failed to resolve.
func settleInitializers(defs []*ir.TypeDef, enums []*ast.EnumDecl, env infer.Env, assocSink func(annotationInvalid bool) *infer.Sink, enumSink *infer.Sink) {
	for _, def := range defs {
		tscope := ownerTScope(def)
		for _, ac := range def.Consts {
			if ac == nil || ac.Syntax == nil || ac.Syntax.Value == nil {
				continue
			}
			// Push only a written annotation that resolved cleanly. An unannotated
			// constant's resolved type is inferred from its folded value's kind (an
			// nint for sbyte(1)), not the value's static type, so pushing it would
			// settle the value against itself; and an annotation with an invalid part
			// (a generic parameter the annotation resolver left unresolved on a
			// degenerate form) is no usable expectation. Both are settled without one.
			annotationInvalid := ac.Syntax.Type != nil && ir.HasInvalid(ac.Type)
			sink := assocSink(annotationInvalid)
			if ac.Syntax.Type != nil && !ir.HasInvalid(ac.Type) {
				infer.CheckConstAgainst(ac.Syntax.Value, ac.Type, env, tscope, sink)
			} else {
				infer.CheckConst(ac.Syntax.Value, env, tscope, sink)
			}
		}
	}
	for _, ed := range enums {
		for _, m := range ed.Members {
			if m.Value != nil {
				infer.Check(m.Value, env, enumSink)
			}
		}
	}
}

// ownerTScope is the generic type-parameter scope a type's own members see — each
// owner parameter mapped to its resolved bound — or nil for a non-generic type. An
// associated constant is checked through it so a value reading the owner's parameter
// resolves it to a rigid type variable, the way a method body of the same type does.
func ownerTScope(def *ir.TypeDef) infer.TypeScope {
	if len(def.Params) == 0 {
		return nil
	}
	ts := make(infer.TypeScope, len(def.Params))
	for _, p := range def.Params {
		ts[p.Name] = p.Bound
	}
	return ts
}

// writeBack binds the checker-selected overloads, the settled types, and the
// explicit adaptions into the IR — every checking walk has run, so the
// resolutions are complete — and builds the graph fold env the settled-graph
// phases read (the doctrine that every reference is bound to its declaration,
// met for overloaded calls).
func (a *assembler) writeBack() graphFoldEnv {
	writeBackResolutions(a.module, a.res, a.fnShells, a.reg)
	a.own = make(map[*ast.ConstDecl]*ir.Const, len(a.file.Decls))
	for _, decl := range a.file.Decls {
		a.own[decl] = a.shells[decl]
	}
	return graphFoldEnv{q: a.q, file: a.fileID, own: a.own}
}

// refoldConsts is the late re-fold: a constant the type-blind value query
// left unfolded is folded once more — through the IR interpreter, over the
// annotated value graph the write-back just settled (node types, selections,
// and explicit adaptions all on the graph). The annotations only widen the
// foldable set (a graph without them folds by the same value-kind rules the
// query did), so the memoized value query and this pass agree wherever both
// fold — the parity the fold gate pins. The loop runs to a fixpoint: genv
// reads this file's published values, so a reader of a re-folded constant
// folds in a later round, whatever the declaration order.
func (a *assembler) refoldConsts(genv graphFoldEnv) {
	for progress := true; progress; {
		progress = false
		for _, decl := range a.file.Decls {
			c := a.shells[decl]
			if c.Eval == nil && c.Value != nil {
				if c.Eval = eval.GraphExpecting(c.Value, c.Type, genv); c.Eval != nil {
					progress = true
				}
			}
		}
	}
}

// reportMasterValidationRefs reports the reference problems of a master's per-row
// validate checks — an undefined name or an unknown namespace member — the same
// reference diagnostics a constant initializer and a top-level assert run, since
// a validate check is a predicate folded over data the same way. Without it an
// undefined name in a check folds to nothing and every row passes silently. self
// is bound to the row here, so a self reference is left alone (unlike a const).
func (a *assembler) reportMasterValidationRefs() {
	for _, md := range a.file.Masters {
		// A per-row check is a body over the row (self), so a bare name reading a
		// readable member of the row — a field or getter, self omitted — or calling
		// one of its methods is a resolved reference, not an undefined name. The row
		// receiver is the master's own resolved type, the same self the checker and
		// lowering bind there.
		var row ir.Type
		if def := a.masterDef(md.Name); def != nil {
			row = &ir.Named{Def: def}
		}
		for _, clause := range md.Validations {
			for _, stmt := range clause.Body {
				as, ok := stmt.(*ast.AssertStmt)
				if !ok || as.Cond == nil {
					continue
				}
				var member func(*ast.Identifier) bool
				switch {
				case clause.PerRow && row != nil:
					// The callee set is per-condition, so a self-method exemption
					// reaches only a genuine implicit call (assert ok(x)), not a bare
					// method name (assert ok), which is not a value.
					callees := callCalleeIdents(as.Cond)
					member = func(id *ast.Identifier) bool { return selfReference(a.reg, row, id, callees) }
				case !clause.PerRow:
					// A per-table check reads relation operations (count) as bare names:
					// resolved references, not undefined names. The exemption is for a
					// bare read only — count is the row count, not a function — so count
					// used as a call callee (count(...)) is left to be reported as a
					// misuse rather than silently treated as a resolved reference.
					callees := callCalleeIdents(as.Cond)
					member = func(id *ast.Identifier) bool {
						return id.Name == infer.RelationCountName && !callees[id]
					}
				}
				// reportRefIssues walks with ast.WalkExprs, which stops at a function
				// literal's boundary — so a name a lambda binds (its own parameter) is
				// not seen here and not misreported, and an undefined name inside a
				// lambda body is left to the fold, which yields no value and fails the
				// check under the data layer's fail-safe.
				reportRefIssues(a.fileID, as.Cond, a.q, a.at, a.diags, nil, member)
			}
		}
	}
}

// selfReference reports whether an identifier in a self-bearing predicate — a
// refinement's where-clause or a master per-row validate — resolves through self
// (self omitted) rather than as an undefined name. A readable member (a field or
// getter) is one in any position; an instance method is one only as the callee of
// an implicit self-method call, as in `where ok(x)` or `assert ok(x)`. A method
// callee is not a readable member, so a reference check keyed on readable members
// alone misreports the resolved callee as undefined — even when the call is valid
// — while a bare method name (assert ok) is not a value and must stay reported, so
// the method exemption is limited to the call-callee position.
func selfReference(reg *builtin.Registry, self ir.Type, id *ast.Identifier, callees map[*ast.Identifier]bool) bool {
	if infer.IsReadableMember(reg, self, id.Name) {
		return true
	}
	if !callees[id] {
		return false
	}
	_, _, ok := types.Candidates(reg, self, id.Name)
	return ok
}

// callCalleeIdents collects the identifiers an expression uses as a call callee —
// the ok in ok(x) — so a self-method exemption applies to a genuine implicit
// self-method call and not to a bare method name read as a value.
func callCalleeIdents(e ast.Expr) map[*ast.Identifier]bool {
	out := map[*ast.Identifier]bool{}
	ast.WalkExprs(e, func(n ast.Expr) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if id, ok := call.Callee.(*ast.Identifier); ok {
				out[id] = true
			}
		}
		return true
	})
	return out
}

// masterDef returns the assembled type definition of the named master, or nil
// when no master of that name resolved — the row receiver a per-row check's bare
// names read their fields and getters off.
func (a *assembler) masterDef(name string) *ir.TypeDef {
	if name == "" {
		return nil
	}
	for _, def := range a.module.Types {
		if def != nil && def.Name == name && def.Master != nil {
			return def
		}
	}
	return nil
}

// evaluateAsserts checks the compile-time assertions: each condition must
// resolve, type as bool, and fold to true. An assert produces no IR — it is a
// diagnostic-only declaration — and every fact it needs is read through q, so
// the incremental engine tracks its dependencies exactly as it does a
// const's. The phase runs after every body has been checked, so the condition
// folds with the complete overload-resolution map armed — a call the
// value-kind rule cannot split, in the condition or in a body it applies,
// folds by the checker's selection.
func (a *assembler) evaluateAsserts(genv graphFoldEnv) {
	for _, decl := range a.file.Asserts {
		if decl.Cond == nil {
			continue // already a parse diagnostic
		}
		a.evaluateAssert(decl, genv)
	}
}

// evaluateAssert checks one assertion and publishes its outcome.
func (a *assembler) evaluateAssert(decl *ast.AssertDecl, genv graphFoldEnv) {
	before := a.diags.Len()

	// Undefined references and unknown namespace members, exactly as for a
	// const's initializer. An assert condition has no annotation, so a bare
	// member is not in scope (nil expected enum). Then the operator type
	// errors, zero divisors, and stray selfs, through the same checking
	// walks the const path uses.
	reportRefIssues(a.fileID, decl.Cond, a.q, a.at, a.diags, nil, nil)
	condType := infer.Check(decl.Cond, a.env, exprSink(a.at, a.diags, a.res))
	a.checkExprDiagnostics(decl.Cond)

	// The outcome — the folded condition and its power-assert diagram —
	// is module data: the editor's hover and the failure diagnostic both
	// read the very values the assertion was checked with. The condition
	// lowers to its value graph, takes the checker's write-back (the walks
	// above streamed its facts into res), and folds through the IR
	// interpreter — sub-expression by sub-expression for the diagram.
	condGraph := annotateGraph(lower.Value(decl.Cond, constBinder{q: a.q, file: a.fileID, irOf: a.shells, fnOf: a.fnShells}), a.res, a.fnShells, a.reg)
	condNodes := nodesBySyntax(condGraph)
	foldCondAt := func(e ast.Expr) *ir.Constant {
		if n, ok := condNodes[e]; ok {
			return eval.Graph(n, genv)
		}
		return nil
	}
	v := eval.Graph(condGraph, genv)
	d := assert.Diagram(decl.Cond, foldCondAt)
	cond, _, _ := strings.Cut(d, "\n")

	// A poisoned condition type — the assert's own error, or a broken
	// dependency's Invalid propagating in — publishes no outcome: the
	// type-blind fold may well have produced a value, but one that never
	// passed the type-bound checks must not turn an assertion green (the
	// soundness half of the publication rule). The cause carries its own
	// diagnostic at its origin.
	if condType == ir.Invalid {
		a.module.Asserts = append(a.module.Asserts, &ir.Assert{Cond: cond, Doc: decl.Doc, CondGraph: condGraph, Syntax: decl})
		return
	}
	a.module.Asserts = append(a.module.Asserts, &ir.Assert{Cond: cond, Doc: decl.Doc, Eval: v, Diagram: d, CondGraph: condGraph, Syntax: decl})

	// The condition must be a bool. An Invalid type was reported above
	// (an undefined name, a misapplied operator), so it is not re-reported
	// as a non-bool here.
	if !types.IsBoolean(a.reg, condType) {
		s := a.at(decl.Cond)
		a.diags.Add(newAssertionNotBoolDiagnostic(s.offset, s.width, condType.String()))
		return
	}

	// The condition must fold at compile time. When it does not — and
	// nothing above explained why — the assertion itself is the problem:
	// it asks for something the evaluator cannot verify.
	if v == nil || v.Kind != ir.ConstBool {
		if a.diags.Len() == before {
			s := a.at(decl.Cond)
			a.diags.Add(newAssertionNotConstantDiagnostic(s.offset, s.width))
		}
		return
	}

	// The assertion proper. The failure quotes the condition twice — the
	// canonical one-liner (the summary a diagnostic list shows) and the
	// power-assert diagram beneath it, indented as a block so its pipe
	// columns align independently of the message prefix — with the doc
	// comment above the diagram: the broken invariant in the author's
	// own words.
	if !v.Bool {
		s := a.at(decl.Cond)
		doc := ""
		if len(decl.Doc) > 0 {
			doc = "\n  " + strings.Join(decl.Doc, "\n  ")
		}
		diagram := "\n  " + strings.ReplaceAll(d, "\n", "\n  ")
		a.diags.Add(newAssertionFailedDiagnostic(s.offset, s.width, cond, doc, diagram))
	}
}

// checkPureContexts enforces that compile-time positions are pure: a constant
// initializer, an assert condition, an enum member initializer, an associated
// constant initializer, and a refinement (where) predicate all fold to
// values, so an effectful call cannot appear in any of them — pure folds,
// effectful cannot even be written. Every such expression goes through the
// same checkPureContext, so a new fold position cannot be added without a
// matching purity check.
func (a *assembler) checkPureContexts() {
	scope := infer.BodyScope{Reg: a.reg, Universe: a.q.universe(a.fileID), Qualified: qualifiedFrom(a.q, a.imp), Self: ir.Invalid, Funcs: a.funcs, QualifiedFuncs: a.qfns, ConstShadows: constShadowsFrom(a.q, a.fileID)}
	resolved := callEffectsFrom(a.res)
	check := func(e ast.Expr, position string) {
		if e != nil {
			checkPureContext(e, position, scope, resolved, a.at, a.diags)
		}
	}
	for _, decl := range a.file.Decls {
		check(decl.Value, "constant initializer")
	}
	for _, decl := range a.file.Asserts {
		check(decl.Cond, "assert condition")
	}
	for _, td := range a.file.Types {
		for _, c := range td.Consts {
			check(c.Value, "associated constant initializer")
		}
		check(td.Where, "refinement predicate")
	}
	for _, ed := range a.file.Enums {
		for _, m := range ed.Members {
			check(m.Value, "enum member initializer")
		}
		for _, c := range ed.Consts {
			check(c.Value, "associated constant initializer")
		}
	}
	// A master's impl constants are checked like a type's; its row predicate
	// (where) is not resolved in this slice, so it is left for the work that makes
	// row predicates active rather than checked here on inactive syntax.
	for _, md := range a.file.Masters {
		for _, c := range md.Consts {
			check(c.Value, "associated constant initializer")
		}
	}
}

// reportRefIssues reports the reference problems of a constant initializer or
// assert condition: undefined names (distinguishing an ambiguous import),
// namespace members the target does not export (unknown_member), and enum
// members the enum does not declare (unknown_enum_member). expectedEnum is the
// enum a bare member resolves through (a const's annotation; nil for an assert,
// which has none), so a bare member of it is not reported as undefined.
// reportUnknownNamespaceMember reports a namespace member access (geo.X) that
// resolves to no exported constant — unless X is an exported type, which makes
// geo.X a namespace-qualified type name, a valid field-projection receiver
// (geo.X.id) rather than an unknown member.
func reportUnknownNamespaceMember(fileID FileID, m *ast.MemberExpr, q queries, at func(ast.Node) span, diags *diagnostic.List) {
	if m.Member.Name == "" {
		return // a recovered `ns.` — already a parse diagnostic
	}
	if q.resolveMember(fileID, m) != nil {
		return
	}
	ns, _ := m.Receiver.(*ast.Identifier)
	if ns != nil && qualifiedFrom(q, q.importsOf(fileID))(ns.Name, m.Member.Name) != nil {
		return
	}
	s := at(m)
	diags.Add(newUnknownMemberDiagnostic(s.offset, s.width, m.Member.Name, ns.Name))
}

func reportRefIssues(fileID FileID, e ast.Expr, q queries, at func(ast.Node) span, diags *diagnostic.List, expectedEnum *ir.TypeDef, isSelfMember func(*ast.Identifier) bool) {
	// A bare name reading a column of the relation a query method is called on
	// (Cards.where(cost > 100)) is a resolved reference, not an undefined name — the
	// columns binding omitted the way self is. The exempt set is computed structurally
	// from the relation each method is called on, so a column of the right master is
	// exempt while a typo, or a name that is no column, is still reported.
	columns := bareColumnIdents(fileID, e, q)
	walkRefsEnum(fileID, e, q,
		func(id *ast.Identifier) {
			if id.Name == "" || q.resolve(fileID, id) != nil {
				return
			}
			if columns[id] {
				return
			}
			// A bare member of the expected enum is a resolved reference, not an
			// undefined name.
			if expectedEnum != nil && enumIndex(expectedEnum, id.Name) >= 0 {
				return
			}
			// In a body with a receiver (a master per-row check), a bare name that
			// reads a readable member of the row — a field or getter — or calls one
			// of its methods with self omitted is a resolved self reference, not an
			// undefined name.
			if isSelfMember != nil && isSelfMember(id) {
				return
			}
			// A bare type name is a compile-time type value (const x = int8), not
			// an undefined name — the value-position reading the lowering and the
			// type checker both give it.
			if q.universe(fileID)[id.Name] != nil {
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
			reportUnknownNamespaceMember(fileID, m, q, at, diags)
		},
		func(m *ast.MemberExpr) {
			reportTypeMemberIssue(fileID, m, q, at, diags)
		})
}

// bareColumnIdents collects the identifiers in e that read a column of the relation a
// query method is called on — the bare-column form (Cards.where(cost > 100)), so the
// reference check exempts a column the way it exempts a self member. The walk tracks
// the columns master in force: a query method's argument reads the columns of the
// relation the method is called on, while its receiver and every other position read in
// the outer context (nil at the top), so a nested correlated query (where(cost >
// Other.sum(price))) resolves price against Other and cost against the outer master. A
// bare enum member compared against an enum column (where(rarity == legend)) is exempted
// too, resolved through the column's element type the way the checker resolves it. A
// name that is no column (nor such a member) of the master in force is left unrecorded
// and so still reported.
func bareColumnIdents(fileID FileID, e ast.Expr, q queries) map[*ast.Identifier]bool {
	out := map[*ast.Identifier]bool{}
	var walk func(e ast.Expr, master *ir.TypeDef)
	walk = func(e ast.Expr, master *ir.TypeDef) {
		switch n := e.(type) {
		case *ast.Identifier:
			if master != nil && infer.MasterHasColumn(q.registry(), master, n.Name) {
				out[n] = true
			}
		case *ast.CallExpr:
			if member, ok := n.Callee.(*ast.MemberExpr); ok {
				if m := columnsCallMaster(fileID, member, q); m != nil {
					// The receiver reads in the outer context; each argument reads the
					// columns of the relation the method is called on.
					walk(member.Receiver, master)
					for _, a := range n.Arguments {
						walk(a, m)
					}
					return
				}
				exemptColumnEnumArgs(out, q.registry(), master, member, n.Arguments)
			}
			walk(n.Callee, master)
			for _, a := range n.Arguments {
				walk(a, master)
			}
		case *ast.MemberExpr:
			walk(n.Receiver, master)
		case *ast.AwaitExpr:
			walk(n.Value, master)
		case *ast.TernaryExpr:
			walk(n.Cond, master)
			walk(n.Then, master)
			walk(n.Else, master)
		case *ast.RangeExpr:
			walk(n.Lower, master)
			walk(n.Upper, master)
		case *ast.CollectionLit:
			for _, entry := range n.Entries {
				walk(entry.Key, master)
				walk(entry.Value, master)
			}
		case *ast.RecordLit:
			for _, f := range n.Fields {
				walk(f.Value, master)
			}
		}
	}
	walk(e, nil)
	return out
}

// columnsCallMaster returns the master a columns-context query call (member) reads its
// argument columns off — the relation the method is called on — or nil when the call is
// not a bare-column query. It is nil unless the method is one of the columns-context
// names, the receiver names a master's relation, and the master does not declare a
// static fn of that name (which would make Cards.where(...) a static call, not a query,
// so its argument is ordinary and a bare column there is a genuine undefined name).
func columnsCallMaster(fileID FileID, member *ast.MemberExpr, q queries) *ir.TypeDef {
	if !infer.ColumnsContextMethods[member.Member.Name] {
		return nil
	}
	m := relationReceiverMaster(fileID, member.Receiver, q)
	if m == nil {
		return nil
	}
	// A static fn shadows the relation method only on a direct master call (Cards.where):
	// a chain (Cards.limit(1).where) dispatches to the relation limit returns, so where is
	// the relation method there regardless of a same-named static.
	if directMasterReceiver(member.Receiver) && masterHasStaticFn(m, member.Member.Name) {
		return nil
	}
	return m
}

// directMasterReceiver reports whether a query method's receiver names a master through
// a bare identifier — the only receiver a static fn shadows. A static call resolves only
// through an identifier receiver (Cards.where), never a namespace-qualified one
// (deck.Cards.where, which is the relation method) nor a chain (already a relation value),
// so the static-fn shadow guard applies to a bare master name alone.
func directMasterReceiver(recv ast.Expr) bool {
	switch recv.(type) {
	case *ast.Identifier:
		return true
	default:
		return false
	}
}

// exemptColumnEnumArgs exempts a bare enum member compared against an enum column
// (rarity == legend, lowered to rarity.eql(legend)) — the argument identifiers that name
// a member of the column's element enum — so the reference check resolves it through the
// column the way the checker does rather than reporting an undefined name. It runs in a
// columns context (master set) on a call whose receiver is a bare column identifier.
func exemptColumnEnumArgs(out map[*ast.Identifier]bool, reg *builtin.Registry, master *ir.TypeDef, member *ast.MemberExpr, args []ast.Expr) {
	if master == nil {
		return
	}
	col, ok := member.Receiver.(*ast.Identifier)
	if !ok || !infer.MasterHasColumn(reg, master, col.Name) {
		return
	}
	for _, a := range args {
		if id, ok := a.(*ast.Identifier); ok && infer.ColumnEnumMember(reg, master, col.Name, id.Name) {
			out[id] = true
		}
	}
}

// masterHasStaticFn reports whether master declares a static fn of the given name — so a
// call Cards.where(...) on a master that declares a static where is recognized as a
// static call rather than a relation query, and its argument is not exempted as columns.
func masterHasStaticFn(master *ir.TypeDef, name string) bool {
	for _, m := range master.Methods {
		if m.Kind == ir.MethodStatic && m.Name == name {
			return true
		}
	}
	return false
}

// relationReceiverMaster returns the master the relation a query method is called on is
// over — a master named directly (Cards.where), through an imported namespace
// (deck.Cards.where), or reached through a chain of query methods
// (Cards.where(...).order(...)) — or nil when the receiver does not name a master's
// relation, so a same-named user method on a non-relation type is not mistaken for one.
// It walks the receiver chain to its base name and reads that name's master from the
// universe (or, for a qualified name, the imported namespace).
func relationReceiverMaster(fileID FileID, recv ast.Expr, q queries) *ir.TypeDef {
	for {
		switch r := recv.(type) {
		case *ast.Identifier:
			return masterDefOf(q.universe(fileID)[r.Name])
		case *ast.MemberExpr:
			// A value of the same name shadows the namespace import (a const named deck
			// makes deck.Cards read the const's field, not the imported master), so the
			// qualified-master reading applies only when the receiver names no value.
			if ns, ok := r.Receiver.(*ast.Identifier); ok && q.resolve(fileID, ns) == nil {
				return masterDefOf(qualifiedFrom(q, q.importsOf(fileID))(ns.Name, r.Member.Name))
			}
			return nil
		case *ast.CallExpr:
			// A chain continues only through a method that returns a relation, so a
			// query written after an aggregate (Cards.count().where(...)) does not
			// resolve to Cards: the chain is invalid and its bare column is a genuine
			// undefined name rather than an exempted column. A receiver that is not such
			// a chain (a relation-returning function call, say) is not recognized — the
			// bare form reads its columns off a named, qualified, parameter, self, or
			// chained relation — so its bare column stays an undefined name, and the
			// lambda form names the columns binding explicitly there.
			if member, ok := r.Callee.(*ast.MemberExpr); ok && infer.RelationReturningMethods[member.Member.Name] {
				// A direct master call link shadowed by a static fn (Cards.limit(1) for a
				// master with a static limit) is the static, returning a non-relation, so
				// the chain is broken and its bare column is not exempted.
				if directMasterReceiver(member.Receiver) {
					if base := relationReceiverMaster(fileID, member.Receiver, q); base != nil && masterHasStaticFn(base, member.Member.Name) {
						return nil
					}
				}
				recv = member.Receiver
				continue
			}
			return nil
		default:
			return nil
		}
	}
}

// masterDefOf returns def when it names a master, else nil — the membership a relation
// receiver must satisfy.
func masterDefOf(def *ir.TypeDef) *ir.TypeDef {
	if def != nil && def.Master != nil {
		return def
	}
	return nil
}

// reportTypeMemberIssue validates a type-member read — T.member or the
// namespace-qualified geo.T.member: the member must be an enum member of an enum
// T, or an associated constant or declared field of any other T. A member that
// is none is reported (unknown_enum_member, unknown_associated_const). The
// receiver is a local type name or a qualified one; a static fn call was already
// exempted, leaving the read forms here.
func reportTypeMemberIssue(fileID FileID, m *ast.MemberExpr, q queries, at func(ast.Node) span, diags *diagnostic.List) {
	if m.Member.Name == "" {
		return // a recovered `Rarity.` — already a parse diagnostic
	}
	def := typeReceiverDef(fileID, m.Receiver, q)
	recvName := receiverName(m.Receiver)
	member := types.ResolveMember(def, m.Member.Name)
	if def != nil && def.Enum != nil {
		// An enum type: the member must be one of its members, or a readable member
		// (a getter the enum's impl declares — an enum has no fields). A name that
		// resolves to neither is an unknown enum member.
		if member.Kind != types.MemberEnum {
			if _, ok := types.ReadableMemberType(q.registry(), reifyType(def), m.Member.Name); ok {
				return
			}
			s := at(m)
			diags.Add(newUnknownEnumMemberDiagnostic(s.offset, s.width, recvName, m.Member.Name))
		}
		return
	}
	// A non-enum type: the member must be an associated constant or a readable
	// member (a declared field or a getter) projected in value position
	// (Character.level — a type value the comptime expression consumes).
	if member.Kind != types.MemberConst {
		if def != nil {
			if _, ok := types.ReadableMemberType(q.registry(), reifyType(def), m.Member.Name); ok {
				return
			}
		}
		s := at(m)
		diags.Add(newUnknownAssociatedConstDiagnostic(s.offset, s.width, recvName, m.Member.Name))
	}
}

// typeReceiverDef resolves a type-member access's receiver to its definition: a
// local type name (Item) through the universe, or a namespace-qualified type name
// (geo.Item) through the import lookup. nil for any other receiver.
func typeReceiverDef(fileID FileID, recv ast.Expr, q queries) *ir.TypeDef {
	switch r := recv.(type) {
	case *ast.Identifier:
		return q.universe(fileID)[r.Name]
	case *ast.MemberExpr:
		if ns, ok := r.Receiver.(*ast.Identifier); ok {
			return qualifiedFrom(q, q.importsOf(fileID))(ns.Name, r.Member.Name)
		}
	}
	return nil
}

// receiverName renders a type-member access's receiver for a diagnostic: the bare
// name (Item) or the qualified name (geo.Item).
func receiverName(recv ast.Expr) string {
	switch r := recv.(type) {
	case *ast.Identifier:
		return r.Name
	case *ast.MemberExpr:
		if ns, ok := r.Receiver.(*ast.Identifier); ok {
			return ns.Name + "." + r.Member.Name
		}
	}
	return ""
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
//   - the effective target is the union member the value flows in as (the same
//     exact→unique selection the fold tags with), or want itself when it is not a
//     union — so `sbyte | error` checks the value against `sbyte`, and a refined
//     member's predicate runs;
//   - the value is folded with no expectation, so the raw value is read even
//     though the expectation-driven fold refuses to build it (memberAdmits);
//     an overflowing conversion already folds to nil and is reported at its own site
//     (ScalarConversion), so it is not seen here and never double-reported.
//
// A non-constant or unfoldable value (a parameter, a predicate that does not fold
// to a bool) is left unchecked — the runtime's job, the conservative discipline
// the range and refinement checks already share.
func checkMemberFlow(reg *builtin.Registry, e ast.Expr, want ir.Type, env exprFolder, at func(ast.Node) span, diags *diagnostic.List) {
	if want == ir.Invalid {
		return
	}
	v := env.fold(e)
	if v == nil {
		return
	}
	member := env.memberFor(e, want)
	if v.Kind == ir.ConstInt && !types.Fits(reg, member, v.Int) {
		s := at(e)
		diags.Add(newConstantOverflowDiagnostic(s.offset, s.width, v.String(), member.String()))
		return
	}
	if def := refinedDef(member); def != nil {
		p := eval.GraphPredicate(def.Where, v, def, env.env())
		if p != nil && p.Kind == ir.ConstBool && !p.Bool {
			s := at(e)
			d := assert.Diagram(def.WhereSyntax(), whereFoldAt(def, v, env))
			diagram := "\n  " + strings.ReplaceAll(d, "\n", "\n  ")
			diags.Add(newRefinementViolationDiagnostic(
				s.offset, s.width, v.String(), member.String(), ast.Render(def.WhereSyntax()), diagram))
		}
	}
}

// whereFoldAt folds a refinement predicate's sub-expressions for the violation
// diagram: each anchor reads its node off the definition's Where graph and
// folds with self bound to the rejected value.
func whereFoldAt(def *ir.TypeDef, self *ir.Constant, env exprFolder) func(ast.Expr) *ir.Constant {
	nodes := nodesBySyntax(def.Where)
	genv := env.env()
	return func(e ast.Expr) *ir.Constant {
		if n, ok := nodes[e]; ok {
			return eval.GraphPredicate(n, self, def, genv)
		}
		return nil
	}
}

// checkScalarConversion range-checks a sized-scalar conversion's argument against
// the target type — short(70000), Level(70000) — reporting constant_overflow at
// the conversion site. It is the eval-based body of the Sink.ScalarConversion
// hook, shared by the const initializer and every function/method/lambda body
// (through bodySink), so a conversion overflows the same way everywhere. eval
// refuses to fold an out-of-range conversion (it folds to nil), so checkMemberFlow
// never sees the value — this is the only site that reports it, and a non-constant
// argument does not fold and is left to the runtime.
func checkScalarConversion(reg *builtin.Registry, call *ast.CallExpr, target ir.Type, env exprFolder, at func(ast.Node) span, diags *diagnostic.List) {
	if len(call.Arguments) != 1 {
		return
	}
	if v := env.fold(call.Arguments[0]); v != nil && v.Kind == ir.ConstInt && !types.Fits(reg, target, v.Int) {
		s := at(call)
		diags.Add(newConstantOverflowDiagnostic(s.offset, s.width, v.String(), target.String()))
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

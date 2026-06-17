package semantic

import (
	"maps"
	"slices"

	"github.com/masterbelt/masterbelt/pkg/belt/builtin"
	"github.com/masterbelt/masterbelt/pkg/belt/eval"
	"github.com/masterbelt/masterbelt/pkg/belt/lint"
	"github.com/masterbelt/masterbelt/pkg/belt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/belt/types"
	"github.com/masterbelt/masterbelt/pkg/belt/types/infer"
	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// Program is an incrementally analyzed multi-file program: the files reachable
// from a project's entry point, each with the use targets the project layer
// resolved for it. One query database spans all files, so a cross-file fact —
// an imported constant's type, a target's exports — is computed once and
// shared, and an edit invalidates only what transitively read it.
type Program struct {
	db      *database
	docs    map[FileID]*abstract.Document
	modules map[FileID]*ir.Module
	diags   map[FileID][]diagnostic.Diagnostic
}

// NewProgram returns an empty program; add files with SetFile and analyze with
// Refresh.
func NewProgram() *Program {
	return &Program{
		db:      newDatabase(universe()),
		docs:    map[FileID]*abstract.Document{},
		modules: map[FileID]*ir.Module{},
		diags:   map[FileID][]diagnostic.Diagnostic{},
	}
}

// SetFile installs (or replaces) one file: its parsed document and the
// resolved targets of its use declarations. Call Refresh after the batch of
// changes to re-analyze.
func (p *Program) SetFile(id FileID, doc *abstract.Document, uses map[*ast.UseDecl]FileID) {
	p.docs[id] = doc
	p.db.setInput(id, doc.File(), uses)
}

// UsesOf re-keys a use table from another layer's file identifier to the
// engine's. The project layer's FileID is deliberately a distinct type — the
// compiler core never imports the project layer — so the binding layers (the
// CLI, the LSP) bridge the two here, in exactly one place.
func UsesOf[T ~string](uses map[*ast.UseDecl]T) map[*ast.UseDecl]FileID {
	out := make(map[*ast.UseDecl]FileID, len(uses))
	for u, target := range uses {
		out[u] = FileID(target)
	}
	return out
}

// RemoveFile drops a file that left the project.
func (p *Program) RemoveFile(id FileID) {
	delete(p.docs, id)
	delete(p.modules, id)
	delete(p.diags, id)
	p.db.dropInput(id)
}

// Refresh brings every file's module and diagnostics up to date over the
// engine. Assembly itself is a memoized query: a file is re-assembled only
// when its text or a fact its last assembly read changed; an untouched file
// costs one verification walk.
func (p *Program) Refresh() {
	q := engineQueries{p.db}
	for _, id := range p.Files() {
		a := q.moduleOf(id)
		p.modules[id] = a.module
		p.diags[id] = a.diags
	}
}

// Files returns the program's file ids, sorted.
func (p *Program) Files() []FileID {
	return slices.Sorted(maps.Keys(p.docs))
}

// Module returns a file's resolved, typed IR from the last Refresh.
func (p *Program) Module(id FileID) *ir.Module { return p.modules[id] }

// EvalEnv returns the evaluation environment a value graph in file folds in —
// the builtin registry and the file's type universe, the same env the post-check
// folds use. It is the seam a layer above the engine (the master data layer,
// checking a coerced cell value against its field type's refinement predicate)
// reads to run eval.GraphPredicate against the resolved definitions, without
// reaching into the engine's internals. The env is a thin view over the live
// query database, so it reflects the last Refresh.
func (p *Program) EvalEnv(file FileID) eval.GraphEnv {
	return graphFoldEnv{q: engineQueries{p.db}, file: file}
}

// Stats returns the query-engine work of the last Refresh: per-kind counts of
// the queries recomputed versus reused. It is a side-channel read — calling it
// changes nothing the engine memoizes.
func (p *Program) Stats() Stats { return p.db.stats() }

// MemoCount returns the number of live entries in the engine's memo table — the
// program's retained query-cache size. It is a side-channel read (len of the
// memo map); calling it changes nothing the engine memoizes. The LSP samples it
// across a long editing session as the memo-table leak signal: monotonic growth
// over many edits is a table that never sheds stale keys.
func (p *Program) MemoCount() int { return p.db.memoCount() }

// Diagnostics returns a file's semantic diagnostics from the last Refresh,
// ordered by offset.
func (p *Program) Diagnostics(id FileID) []diagnostic.Diagnostic { return p.diags[id] }

// Lint returns a file's advisory lint diagnostics — unreachable code, unused
// declarations — computed over its resolved module. They are deliberately kept
// out of Diagnostics, the analyzer's errors: lint is a layer a surface opts
// into (check and the LSP surface it), not a result the analyzer's own callers
// and tests must account for. A declaration the analyzer already reported an
// error for is left alone, so a broken declaration is not also called dead.
func (p *Program) Lint(id FileID) []diagnostic.Diagnostic {
	module := p.modules[id]
	doc := p.docs[id]
	if module == nil || doc == nil {
		return nil
	}
	positions := positionsOf(doc.Concrete().Tree())
	span := func(n ast.Node) (int, int) {
		s := spanOf(positions, n)
		return s.offset, s.width
	}
	return lint.Check(module, span, p.diags[id])
}

// Document returns the file's underlying syntax document.
func (p *Program) Document(id FileID) *abstract.Document { return p.docs[id] }

// Resolve returns the constant a value-position identifier in file refers to —
// a local declaration or an imported one — or nil if it resolves to no
// declaration. It is the resolution the editor uses for hover,
// go-to-definition, and find-references, reading through the engine so the
// memoized resolution of the last analysis is reused.
func (p *Program) Resolve(file FileID, id *ast.Identifier) *ir.Const {
	q := engineQueries{p.db}
	return p.db.shells[q.resolve(file, id)]
}

// ResolveFunc resolves a call's callee identifier to the overload set of the
// function it names in file — every same-name declaration, in source order —
// or nil when no function has that name.
func (p *Program) ResolveFunc(file FileID, id *ast.Identifier) []*ast.FuncDecl {
	return engineQueries{p.db}.resolveFunc(file, id)
}

// ResolveMember returns the constant a namespace member access (geo.Origin) in
// file refers to, or nil.
func (p *Program) ResolveMember(file FileID, m *ast.MemberExpr) *ir.Const {
	q := engineQueries{p.db}
	return p.db.shells[q.resolveMember(file, m)]
}

// ResolveUseName returns the constant a selective-import name in one of file's
// use declarations binds (use { Origin } from ...), or nil when the use is
// unresolved or the target exports no such value. It is how the editor treats
// an import-list name as one more occurrence of the constant it imports.
func (p *Program) ResolveUseName(file FileID, u *ast.UseDecl, name string) *ir.Const {
	q := engineQueries{p.db}
	target, ok := q.usesOf(file)[u]
	if !ok {
		return nil
	}
	return p.db.shells[q.exportsOf(target).consts[name]]
}

// ResolveUseType returns the type definition a selective-import name binds
// (use { Point } from ...), or nil when the use resolves to no file or the
// name is not among the target's exported types.
func (p *Program) ResolveUseType(file FileID, u *ast.UseDecl, name string) *ir.TypeDef {
	q := engineQueries{p.db}
	target, ok := q.usesOf(file)[u]
	if !ok {
		return nil
	}
	return q.exportsOf(target).types[name]
}

// TypeOfExpr infers an expression's type in file's top-level scope — what a
// member-access receiver needs when it is not a plain reference (a collection
// literal, an operator chain). Identifiers resolve to the file's constants;
// self and parameters mean nothing at the top level (ir.Invalid).
func (p *Program) TypeOfExpr(file FileID, e ast.Expr) ir.Type {
	return infer.Expr(e, typeEnv{q: engineQueries{p.db}, file: file})
}

// EnumOfAnnotation resolves a written type annotation to the enum a bare member
// in the annotated value position would fold through — a bare enum, or a union
// carrying one (R | error) — or nil for any other annotation. It shares the
// exact resolution the const initializer's value lowering uses (a pure universe
// name lookup, never the type query), so the editor's expected-enum completion
// offers exactly the members the lowering would resolve and never one it would
// leave undefined. It is how the editor completes a bare member at an annotated
// const or let initializer.
func (p *Program) EnumOfAnnotation(file FileID, t ast.TypeExpr) *ir.TypeDef {
	return typeExprEnum(engineQueries{p.db}, file, t)
}

// EnumOf returns the enum a type names for the purpose of bare-member resolution
// — a nominal enum, or a union carrying one — or nil otherwise. It is the same
// rule EnumOfAnnotation applies to a resolved annotation, exposed for the static
// type the editor reads from a switch scrutinee (a parameter, self, or a let
// local) so that the arm completes bare members exactly as the const annotation
// path does.
func (p *Program) EnumOf(t ir.Type) *ir.TypeDef {
	return enumDefOf(t)
}

// MethodCandidates returns the overload set recv binds for name — through a
// named type, a builtin, or a generic application — together with the
// substitution the receiver's type arguments pin (a list<int> receiver pins
// the element parameter), or false when the receiver has no such method. An
// un-overloaded name is a one-element set.
func (p *Program) MethodCandidates(recv ir.Type, name string) ([]*ir.Method, map[string]ir.Type, bool) {
	return types.Candidates(p.db.reg, recv, name)
}

// ReceiverMethods returns every method recv binds, with the substitution its
// type arguments pin — what completion offers after a member dot.
func (p *Program) ReceiverMethods(recv ir.Type) ([]*ir.Method, map[string]ir.Type, bool) {
	return types.ReceiverMethods(p.db.reg, recv)
}

// FileOf returns the file a constant of the last Refresh is declared in.
func (p *Program) FileOf(c *ir.Const) (FileID, bool) {
	if c == nil || c.Syntax == nil {
		return "", false
	}
	id, ok := p.db.declFile[c.Syntax]
	return id, ok
}

// FileOfType returns the file a type definition was declared in — a type, enum,
// interface, or master declaration, read through its DeclSyntax backpointer. A
// definition outside the program — the prelude's — is in no file.
func (p *Program) FileOfType(t *ir.TypeDef) (FileID, bool) {
	if t == nil {
		return "", false
	}
	decl := t.DeclSyntax()
	if decl == nil {
		return "", false
	}
	id, ok := p.db.typeFile[decl]
	return id, ok
}

// ResolveFuncMember resolves a namespace function call's callee (geo.area) in
// file to the overload set the namespace's target exports, or nil.
func (p *Program) ResolveFuncMember(file FileID, m *ast.MemberExpr) []*ast.FuncDecl {
	return engineQueries{p.db}.resolveFuncMember(file, m)
}

// FunctionOf returns the resolved ir.Function of a declaration — the very
// shell every module and FuncCall publishes — or nil for a declaration
// outside the program.
func (p *Program) FunctionOf(fd *ast.FuncDecl) *ir.Function {
	return p.db.fnShells[fd]
}

// FileOfFunc returns the file a function declaration sits in.
func (p *Program) FileOfFunc(fd *ast.FuncDecl) (FileID, bool) {
	for id, in := range p.db.files {
		if in.file == nil {
			continue
		}
		for _, f := range in.file.Funcs {
			if f == fd {
				return id, true
			}
		}
	}
	return "", false
}

// FunctionsInScope returns the functions callable by bare name in file: its
// own declarations, then the imported names no local declaration shadows —
// what value-position completion offers.
func (p *Program) FunctionsInScope(id FileID) []*ir.Function {
	q := engineQueries{p.db}
	out := slices.Clone(p.Module(id).Funcs)
	local := map[string]bool{}
	for _, f := range out {
		local[f.Name] = true
	}
	imp := q.importsOf(id)
	for _, name := range slices.Sorted(maps.Keys(imp.funcs)) {
		b := imp.funcs[name]
		if b.ambiguous || local[name] {
			continue
		}
		for _, fd := range b.targets {
			if f := p.db.fnShells[fd]; f != nil {
				out = append(out, f)
			}
		}
	}
	return out
}

// FuncLitTypes returns the settled signature of every function literal in
// file, exactly as the checking walk settles them — what the editor reads to
// hover a lambda parameter or render its inlay hint.
func (p *Program) FuncLitTypes(id FileID) map[*ast.FuncLit]*ir.Func {
	doc := p.docs[id]
	if doc == nil {
		return nil
	}
	return funcLitTypesOf(p.db, id, doc.File())
}

// funcLitTypesOf is the walk behind FuncLitTypes, reading every fact through
// the engine so it reuses the memoized resolution and types of the last
// analysis.
func funcLitTypesOf(db *database, fileID FileID, file *ast.File) map[*ast.FuncLit]*ir.Func {
	out := map[*ast.FuncLit]*ir.Func{}
	sink := &infer.Sink{SolvedFuncLit: func(lit *ast.FuncLit, t *ir.Func) { out[lit] = t }}
	runCheckWalks(db, fileID, file, sink)
	return out
}

// ExprTypes returns the type the checking walk settles for every expression in
// file it types with a usable (non-Invalid) value — the typed-value-graph stream
// (Sink.Typed), captured over the same walks assemble runs. It is the editor's
// single source of truth for a sub-expression's type: a master in a body reads as
// its relation, a relation chain carries its result type, a shadowing local wins —
// all by the checker's own scope rules, so the editor reads the type rather than
// re-deriving the rules. A constant initializer is walked in the const scope (a
// master stays a metatype there, since a const cannot evaluate a relation), so the
// body-vs-const distinction falls out of the walk rather than being special-cased.
func (p *Program) ExprTypes(id FileID) map[ast.Expr]ir.Type {
	doc := p.docs[id]
	if doc == nil {
		return nil
	}
	return exprTypesOf(p.db, id, doc.File())
}

// exprTypesOf is the walk behind ExprTypes: it captures the Typed stream over the
// shared checking walks, keyed by expression node.
func exprTypesOf(db *database, fileID FileID, file *ast.File) map[ast.Expr]ir.Type {
	out := map[ast.Expr]ir.Type{}
	sink := &infer.Sink{Typed: func(e ast.Expr, t ir.Type) { out[e] = t }}
	runCheckWalks(db, fileID, file, sink)
	return out
}

// runCheckWalks runs every checking walk assemble runs — the constant
// initializers, the asserts, and the method and function bodies (a master's
// validate clauses among them) — driving sink, reading every fact through the
// engine so it reuses the memoized resolution and types of the last analysis. It
// is the shared walk behind the editor-facing type queries (FuncLitTypes,
// ExprTypes): each builds a sink that captures one of the walk's informational
// streams. Diagnostics are suppressed (a nil diagnostic list and the zero folder),
// so only the streams the caller wired fire, while the body walk still reaches
// every expression, including those in a switch arm or a lambda nested in a
// top-level fn body, exactly as assemble does.
func runCheckWalks(db *database, fileID FileID, file *ast.File, sink *infer.Sink) {
	q := engineQueries{db}
	env := typeEnv{q: q, file: fileID}
	reg := q.registry()
	qualified := qualifiedFrom(q, q.importsOf(fileID))
	for _, decl := range file.Decls {
		if decl.Value == nil {
			continue
		}
		// The same annotated/un-annotated split assemble makes, with the
		// annotation resolved silently (its problems are already diagnosed).
		annType := ir.Invalid
		if decl.Type != nil {
			r := &infer.TypeResolver{Defs: q.universe(fileID), Qualified: qualified}
			annType = r.ResolveType(decl.Type, nil)
		}
		if annType != ir.Invalid {
			infer.CheckAgainst(decl.Value, annType, env, sink)
		} else {
			infer.Check(decl.Value, env, sink)
		}
	}
	// An assert condition is checked exactly as an un-annotated initializer.
	for _, a := range file.Asserts {
		if a.Cond != nil {
			infer.Check(a.Cond, env, sink)
		}
	}
	funcs := buildFuncSymbols(file)
	qualifiedFuncs := qualifiedFuncsFrom(q, q.importsOf(fileID))
	constShadows := constShadowsFrom(q, fileID)
	nsShadows := namespaceShadowsFrom(q, fileID)
	// A nil diagnostic list (the sink-only walks) folds nothing, so the fold
	// channel is the zero folder (its queries unread), exactly as the method-body
	// walk here always has.
	checkMethodBodies(reg, q.typeDefs(fileID), q.universe(fileID), qualified, funcs, qualifiedFuncs, constShadows, nsShadows, exprFolder{}, sink, nil, nil)
	checkFuncBodies(reg, file, q.universe(fileID), qualified, funcs, qualifiedFuncs, constShadows, nsShadows, exprFolder{}, sink, nil, nil)
}

// QualifiedTypeNames returns the namespace-qualified type names in scope in
// file: each namespace import paired with its target's exported types, in
// name order — what an editor offers in a type position beyond the plain
// names of TypeNames (geo.Point for use geo from ...).
func (p *Program) QualifiedTypeNames(id FileID) map[string][]*ir.TypeDef {
	q := engineQueries{p.db}
	out := map[string][]*ir.TypeDef{}
	for ns, target := range q.importsOf(id).namespaces {
		exp := q.exportsOf(target).types
		if len(exp) == 0 {
			continue
		}
		defs := make([]*ir.TypeDef, 0, len(exp))
		for _, name := range slices.Sorted(maps.Keys(exp)) {
			defs = append(defs, exp[name])
		}
		out[ns] = defs
	}
	return out
}

// TypeNames returns the type definitions in scope in file, for an editor
// completing a type name: the file's own type declarations, then the rest of
// its universe — imports over the prelude — by name, with nearer declarations
// shadowing. The universe is the complete answer: the prelude is its base
// tier, not a second source.
func (p *Program) TypeNames(id FileID) []*ir.TypeDef {
	q := engineQueries{p.db}
	seen := map[string]bool{}
	var out []*ir.TypeDef
	add := func(t *ir.TypeDef) {
		if t == nil || t.Name == "" || seen[t.Name] {
			return
		}
		seen[t.Name] = true
		out = append(out, t)
	}
	for _, t := range q.typeDefs(id) {
		add(t)
	}
	uni := q.universe(id)
	for _, name := range slices.Sorted(maps.Keys(uni)) {
		add(uni[name])
	}
	return out
}

// Constructors returns the types in scope in file whose conversion form T(...)
// constructs a value — what an editor offers in a value position alongside the
// constants and functions. Today that is the error type (error("msg")) and the
// range type (range(start, end)); a user type shadowing the name is not a native
// constructor and is excluded.
func (p *Program) Constructors(id FileID) []*ir.TypeDef {
	q := engineQueries{p.db}
	reg := q.registry()
	var out []*ir.TypeDef
	for _, t := range p.TypeNames(id) {
		if t.Builtin && isConstructorBuiltin(reg, t.Name) {
			out = append(out, t)
		}
	}
	return out
}

// isConstructorBuiltin reports whether a builtin type name has a value-
// constructing conversion form an editor should offer in a value position: the
// natively-backed error type (error("msg")), or the range type (range(start,
// end)), which the evaluator constructs directly rather than through a native
// descriptor. The integer aliases and the collections are conversions whose
// argument is itself a value, not constructors offered on their own.
func isConstructorBuiltin(reg *builtin.Registry, name string) bool {
	if name == builtin.NameRange {
		return true
	}
	n, ok := reg.Native(name)
	return ok && n.Err
}

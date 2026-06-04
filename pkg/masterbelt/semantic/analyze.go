// Package semantic resolves names and infers types for a masterbelt program,
// producing the resolved IR (package source/ir).
//
// Operators have already been desugared to method calls by the AST layer, so
// 1 + 2 arrives as 1.add(2). Typing and evaluation are therefore uniform: every
// expression is a literal, a value reference, or a method call, and a call's
// type comes from the method's signature (package types) while its value comes
// from the method's native implementation (the builtin registry's intrinsics).
//
// The semantic facts a program needs — the symbol table, each constant's type,
// and each constant's evaluated value — are expressed as a small set of pure
// queries (the queries interface). assemble turns those queries plus the AST
// into the IR and diagnostics. Two query implementations share that one
// assembler: a direct one (this file), used by Analyze for a full recompute and
// as the oracle, and an incremental, memoizing one backed by the query database
// (engine.go), used by Document. Because both feed the same assembler, the
// incremental result is identical to the full one.
//
// The package is split by concern: this file holds the query interface and the
// assembler; eval.go folds constants; lower.go binds names for the AST-to-IR
// walk (package lower); resolve.go resolves type declarations; check.go runs the
// expression and method-body diagnostics; positions.go anchors diagnostics to
// source; engine.go and document.go are the incremental façade.
package semantic

import (
	"sort"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/eval"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/lower"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/types"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/types/infer"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// queries are the pure, memoizable semantic facts the assembler needs.
type queries interface {
	// resolve returns the declaration a value-position identifier refers to, or
	// nil if no declaration has that name. The file is the one the identifier
	// sits in — it decides the resolution scope. Keying resolution on the
	// identifier (not the whole symbol table) is what keeps early cutoff sharp:
	// a reference in an unedited declaration is a stable pointer, so editing an
	// unrelated constant does not invalidate it.
	resolve(file FileID, id *ast.Identifier) *ast.ConstDecl
	// ambiguousImport reports whether the identifier's name arrived from two or
	// more imports with distinct targets — harmless until used, then reported
	// at the reference.
	ambiguousImport(file FileID, id *ast.Identifier) bool
	// resolveMember returns the declaration a namespace member access
	// (geo.Origin) refers to, or nil when the receiver names no namespace or
	// the member is not among the target's exported values.
	resolveMember(file FileID, m *ast.MemberExpr) *ast.ConstDecl
	// typeOf returns a constant's type (ir.Invalid when undeterminable).
	typeOf(decl *ast.ConstDecl) ir.Type
	// valueOf returns a constant's evaluated value, or nil when it cannot be
	// evaluated (missing initializer, undefined reference, cycle, type error,
	// or division by zero).
	valueOf(decl *ast.ConstDecl) *ir.Constant
	// typeDefs returns a file's resolved type definitions in source order —
	// the very objects Named types point at, shared by module assembly and
	// annotation resolution so type identity never forks.
	typeDefs(file FileID) []*ir.TypeDef
	// universe returns the named type definitions a file's type annotations
	// resolve in: its own declarations shadowing its imported ones.
	universe(file FileID) map[string]*ir.TypeDef
	// exportsOf returns a file's public surface.
	exportsOf(file FileID) exports
	// importsOf returns the bindings a file's use declarations introduce.
	importsOf(file FileID) importTable
	// usesOf returns where the project layer resolved a file's use paths; a
	// use absent from the table resolved to no file.
	usesOf(file FileID) map[*ast.UseDecl]FileID
	// registry returns the builtin registry the analysis types and evaluates
	// against — the source of primitive types, their value ranges, and the
	// native implementations of their operator methods.
	registry() *builtin.Registry
}

// typeEnv adapts the semantic query interface to infer.Env, so the type
// inference and checking in package types/infer can read resolution, declaration
// types, and the builtin registry through the same memoizing engine. It carries
// the file whose scope identifiers resolve in, so infer.Env stays file-blind.
type typeEnv struct {
	q    queries
	file FileID
}

func (e typeEnv) Resolve(id *ast.Identifier) *ast.ConstDecl { return e.q.resolve(e.file, id) }
func (e typeEnv) ResolveMember(m *ast.MemberExpr) *ast.ConstDecl {
	return e.q.resolveMember(e.file, m)
}
func (e typeEnv) TypeOf(decl *ast.ConstDecl) ir.Type { return e.q.typeOf(decl) }
func (e typeEnv) Universe() map[string]*ir.TypeDef   { return e.q.universe(e.file) }
func (e typeEnv) QualifiedType(namespace, name string) *ir.TypeDef {
	return qualifiedFrom(e.q, e.q.importsOf(e.file))(namespace, name)
}
func (e typeEnv) Registry() *builtin.Registry { return e.q.registry() }

// exprSink wires the checking walk's findings to their diagnostics. The
// Checked stream is left unset — the const path hooks it to the eval-based
// value-range check, which needs the declaration's context.
func exprSink(at func(ast.Node) span, diags *diagnostic.List) *infer.Sink {
	return &infer.Sink{
		InvalidOp: func(node ast.Node, method, operands string) {
			s := at(node)
			diags.Add(newInvalidOperationDiagnostic(s.offset, s.width, method, operands))
		},
		Mismatch: func(node ast.Node, got, want ir.Type) {
			s := at(node)
			diags.Add(newTypeMismatchDiagnostic(s.offset, s.width, got.String(), want.String()))
		},
		ArityMismatch: func(lit *ast.FuncLit, got, want int) {
			s := at(lit)
			diags.Add(newLambdaArityMismatchDiagnostic(s.offset, s.width, got, want))
		},
		UninferableParam: func(p *ast.ParamDef) {
			s := at(p)
			diags.Add(newUninferableParameterDiagnostic(s.offset, s.width, p.Name))
		},
		UninferableResult: func(lit *ast.FuncLit) {
			s := at(lit)
			diags.Add(newUninferableResultDiagnostic(s.offset, s.width))
		},
	}
}

// Analyze resolves and types the document's program, returning the IR module and
// the semantic diagnostics. It recomputes everything from scratch; it is the
// reference analysis and the oracle for the incremental Document.
func Analyze(doc *abstract.Document) (*ir.Module, []diagnostic.Diagnostic) {
	file := doc.File()
	files := map[FileID]*ast.File{soleFileID: file}
	q := newDirectQueries(files, nil, universe())
	return assemble(soleFileID, file, positionsOf(doc.Concrete().Tree()), q, constShells(files))
}

// AnalyzeProgram resolves and types a whole program from scratch: every file
// reachable from the entry, with the use targets the project layer resolved
// for each. It is the reference analysis and the oracle the incremental
// Program is checked against.
func AnalyzeProgram(docs map[FileID]*abstract.Document, uses map[FileID]map[*ast.UseDecl]FileID) (map[FileID]*ir.Module, map[FileID][]diagnostic.Diagnostic) {
	files := make(map[FileID]*ast.File, len(docs))
	for id, doc := range docs {
		files[id] = doc.File()
	}
	q := newDirectQueries(files, uses, universe())
	shells := constShells(files)

	modules := make(map[FileID]*ir.Module, len(docs))
	diags := make(map[FileID][]diagnostic.Diagnostic, len(docs))
	for id, doc := range docs {
		modules[id], diags[id] = assemble(id, doc.File(), positionsOf(doc.Concrete().Tree()), q, shells)
	}
	return modules, diags
}

// constShells creates the identity ir.Const for every declaration across the
// program — references, including cross-file ones, bind to the same objects
// the owning module publishes, which is what makes the IR one pointer graph.
func constShells(files map[FileID]*ast.File) map[*ast.ConstDecl]*ir.Const {
	shells := map[*ast.ConstDecl]*ir.Const{}
	for _, f := range files {
		if f == nil {
			continue
		}
		for _, decl := range f.Decls {
			shells[decl] = &ir.Const{Name: decl.Name, Public: decl.Public, Doc: decl.Doc, Syntax: decl}
		}
	}
	return shells
}

// assemble builds one file's IR module and semantic diagnostics from its AST,
// using q for the resolution and typing facts; fileID names the file within
// the program, scoping its identifier resolution, and shells holds the
// program-wide IR constants (this file's and every importable file's). It is
// shared by the reference and incremental analyzers, so they cannot diverge.
func assemble(fileID FileID, file *ast.File, positions map[cst.Green]span, q queries, shells map[*ast.ConstDecl]*ir.Const) (*ir.Module, []diagnostic.Diagnostic) {
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

	// Redeclarations of the same name.
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
		c.Value = lower.Value(decl.Value, constBinder{q: q, file: fileID, irOf: shells})
		c.Type = q.typeOf(decl)
		c.Eval = q.valueOf(decl)

		// Resolve the annotation with reporting enabled, so an unknown type
		// name anywhere in it (e.g. list<Bogus>) is diagnosed at its own node.
		// The annotation resolves in the file's universe: its own type
		// declarations shadowing its imported ones, over the registry.
		annType := ir.Invalid
		if decl.Type != nil {
			r := &infer.TypeResolver{
				Reg:       reg,
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
			walkRefs(fileID, decl.Value, q,
				func(id *ast.Identifier) {
					if id.Name == "" || q.resolve(fileID, id) != nil {
						return
					}
					s := at(id)
					if q.ambiguousImport(fileID, id) {
						diags.Add(newAmbiguousImportDiagnostic(s.offset, s.width, id.Name))
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
				})
			// One checking walk reports the expression diagnostics: operator
			// type errors, type mismatches (against the annotation when there
			// is one, and inside function-literal bodies), and literals whose
			// parameter or result types cannot be inferred.
			sink := exprSink(at, diags)
			if annType != ir.Invalid {
				// The annotation is pushed into the value. Value-range checks
				// hook the walk's Checked stream so infer stays eval-free; the
				// top-level value is range-checked against c.Type below, so
				// only the inner expressions (collection entries, returns)
				// are checked here.
				sink.Checked = func(e ast.Expr, want ir.Type) {
					if e == decl.Value {
						return
					}
					if v := eval.Expr(e, evalEnv{q: q, file: fileID}); v != nil && v.Kind == ir.ConstInt && !types.Fits(reg, want, v.Int) {
						s := at(e)
						diags.Add(newConstantOverflowDiagnostic(s.offset, s.width, v.String(), want.String()))
					}
				}
				infer.CheckAgainst(decl.Value, annType, env, sink)
			} else {
				infer.Check(decl.Value, env, sink)
			}
			// Division or remainder by a zero divisor.
			checkDivByZero(decl.Value, evalEnv{q: q, file: fileID}, func(node ast.Node) {
				s := at(node)
				diags.Add(newDivisionByZeroDiagnostic(s.offset, s.width))
			})
		}

		if cyclic[decl] {
			s := at(decl)
			diags.Add(newCyclicReferenceDiagnostic(s.offset, s.width, decl.Name))
		}
		// An integer value outside its concrete type's range overflows. The
		// arbitrary-precision int has no fixed range (Fits accepts any value),
		// and booleans never overflow.
		if c.Eval != nil && c.Eval.Kind == ir.ConstInt && !types.Fits(reg, c.Type, c.Eval.Int) {
			s := at(decl.Value)
			diags.Add(newConstantOverflowDiagnostic(s.offset, s.width, c.Eval.String(), c.Type.String()))
		}
		// An empty or heterogeneous collection literal with no annotation has
		// no type to infer (checking mode never sees it without one).
		if lit, ok := decl.Value.(*ast.CollectionLit); ok && decl.Type == nil && c.Type == ir.Invalid {
			s := at(lit)
			diags.Add(newUninferableCollectionDiagnostic(s.offset, s.width))
		}
	}

	// The module's type definitions come from the memoized query — the same
	// objects annotations resolved against, so Named identity never forks. The
	// query resolves silently (its result is reused across revisions, but
	// diagnostics carry offsets that shift on every edit), so the reporting
	// pass re-resolves the declarations fresh and discards the definitions.
	module.Types = q.typeDefs(fileID)
	imp := q.importsOf(fileID)
	resolveTypes(file, reg, at, diags, externTypes(imp), qualifiedFrom(q, imp))
	checkMethodBodies(file, reg, module.Types, q.universe(fileID), qualifiedFrom(q, imp), exprSink(at, diags))

	items := diags.Items()
	sort.SliceStable(items, func(i, j int) bool { return items[i].Offset < items[j].Offset })
	return module, items
}

// buildSymbols maps each declared name to its first declaration. A nil file
// (an input never set) has no symbols.
func buildSymbols(file *ast.File) map[string]*ast.ConstDecl {
	syms := map[string]*ast.ConstDecl{}
	if file == nil {
		return syms
	}
	for _, decl := range file.Decls {
		if decl.Name != "" {
			if _, exists := syms[decl.Name]; !exists {
				syms[decl.Name] = decl
			}
		}
	}
	return syms
}

// cyclicDecls returns the declarations caught in a type-inference cycle. A
// declaration's type depends on the types of the value references in its
// initializer, unless an annotation fixes it; the result is a general directed
// graph (an expression may reference several names), so its cycles are found
// with a coloured depth-first search.
func cyclicDecls(fileID FileID, file *ast.File, q queries) map[*ast.ConstDecl]bool {
	// The walk stays within the file: an identifier of another file's decl
	// would resolve in the wrong scope here, and a cross-file inference cycle
	// necessarily rides a module cycle, which checkUses reports (the engine's
	// runtime cycle guard keeps such types finite, as Invalid).
	own := make(map[*ast.ConstDecl]bool, len(file.Decls))
	for _, decl := range file.Decls {
		own[decl] = true
	}
	deps := func(decl *ast.ConstDecl) []*ast.ConstDecl {
		if decl.Type != nil || decl.Value == nil {
			return nil // an annotation breaks the inheritance chain
		}
		var out []*ast.ConstDecl
		ast.WalkValueIdents(decl.Value, func(id *ast.Identifier) {
			if t := q.resolve(fileID, id); t != nil && own[t] {
				out = append(out, t)
			}
		})
		return out
	}

	const (
		white = iota
		gray
		black
	)
	color := map[*ast.ConstDecl]int{}
	cyclic := map[*ast.ConstDecl]bool{}
	var stack []*ast.ConstDecl

	var dfs func(decl *ast.ConstDecl)
	dfs = func(decl *ast.ConstDecl) {
		color[decl] = gray
		stack = append(stack, decl)
		for _, dep := range deps(decl) {
			switch color[dep] {
			case white:
				dfs(dep)
			case gray:
				// Back edge: everything from dep to the top of the stack is on
				// the cycle.
				for i := len(stack) - 1; i >= 0; i-- {
					cyclic[stack[i]] = true
					if stack[i] == dep {
						break
					}
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[decl] = black
	}

	for _, decl := range file.Decls {
		if color[decl] == white {
			dfs(decl)
		}
	}
	return cyclic
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

// checkUses reports the problems of a file's use declarations: a path that
// resolved to no file (use_not_found), a selectively imported name the target
// does not export (not_exported), and an import that can reach back to this
// file (cyclic_module — reported, Go style, on each edge that closes a cycle).
func checkUses(fileID FileID, file *ast.File, q queries, at func(ast.Node) span, diags *diagnostic.List) {
	uses := q.usesOf(fileID)
	for _, u := range file.Uses {
		if u.Path == "" {
			continue // already a parse diagnostic
		}
		s := at(u)
		target, ok := uses[u]
		if !ok {
			diags.Add(newUseNotFoundDiagnostic(s.offset, s.width, u.Path))
			continue
		}
		for _, name := range u.Names {
			exp := q.exportsOf(target)
			if _, isConst := exp.consts[name]; isConst {
				continue
			}
			if _, isType := exp.types[name]; isType {
				continue
			}
			diags.Add(newNotExportedDiagnostic(s.offset, s.width, name, u.Path))
		}
		if reaches(q, target, fileID, map[FileID]bool{}) {
			diags.Add(newCyclicModuleDiagnostic(s.offset, s.width, u.Path))
		}
	}
}

// reaches reports whether the use graph can reach goal from id.
func reaches(q queries, id, goal FileID, visited map[FileID]bool) bool {
	if id == goal {
		return true
	}
	if visited[id] {
		return false
	}
	visited[id] = true
	for _, next := range q.usesOf(id) {
		if reaches(q, next, goal, visited) {
			return true
		}
	}
	return false
}

// walkRefs visits the value references of an expression: every value-position
// identifier through onIdent, except that a namespace member access
// (geo.Origin) is one unit visited through onMember — its receiver names a
// namespace, not a value. Like ast.WalkValueIdents it does not descend into a
// FuncLit body (a lambda has its own parameter scope).
func walkRefs(fileID FileID, e ast.Expr, q queries, onIdent func(*ast.Identifier), onMember func(*ast.MemberExpr)) {
	switch e := e.(type) {
	case *ast.Identifier:
		onIdent(e)
	case *ast.MemberExpr:
		if recv, ok := e.Receiver.(*ast.Identifier); ok && isNamespace(fileID, recv, q) {
			onMember(e)
			return
		}
		walkRefs(fileID, e.Receiver, q, onIdent, onMember)
	case *ast.CallExpr:
		walkRefs(fileID, e.Callee, q, onIdent, onMember)
		for _, a := range e.Arguments {
			walkRefs(fileID, a, q, onIdent, onMember)
		}
	case *ast.CollectionLit:
		for _, entry := range e.Entries {
			if entry.Key != nil {
				walkRefs(fileID, entry.Key, q, onIdent, onMember)
			}
			if entry.Value != nil {
				walkRefs(fileID, entry.Value, q, onIdent, onMember)
			}
		}
	}
}

// isNamespace reports whether an identifier names a namespace import in its
// file — and no value, since locals and imported values shadow namespaces.
func isNamespace(fileID FileID, id *ast.Identifier, q queries) bool {
	if q.resolve(fileID, id) != nil {
		return false
	}
	_, ok := q.importsOf(fileID).namespaces[id.Name]
	return ok
}

// --- direct (reference) query implementation --------------------------------

// directQueries computes the semantic facts directly, memoizing within a
// single analysis and guarding against cycles — both the per-declaration kind
// (an un-annotated reference chain) and the per-file kind (a module cycle
// re-entering its own tables, mirroring the engine's running guard). It
// carries no state across analyses — that is the incremental engine's job.
type directQueries struct {
	files    map[FileID]*ast.File
	uses     map[FileID]map[*ast.UseDecl]FileID
	declFile map[*ast.ConstDecl]FileID
	reg      *builtin.Registry

	syms map[FileID]map[string]*ast.ConstDecl
	imps map[FileID]*importTable
	exps map[FileID]*exports
	defs map[FileID]*typeDefs

	importing map[FileID]bool
	exporting map[FileID]bool
	resolving map[FileID]bool

	typeMemo  map[*ast.ConstDecl]ir.Type
	typing    map[*ast.ConstDecl]bool
	valueMemo map[*ast.ConstDecl]*ir.Constant
	valuing   map[*ast.ConstDecl]bool
}

func newDirectQueries(files map[FileID]*ast.File, uses map[FileID]map[*ast.UseDecl]FileID, reg *builtin.Registry) *directQueries {
	d := &directQueries{
		files:     files,
		uses:      uses,
		declFile:  map[*ast.ConstDecl]FileID{},
		reg:       reg,
		syms:      map[FileID]map[string]*ast.ConstDecl{},
		imps:      map[FileID]*importTable{},
		exps:      map[FileID]*exports{},
		defs:      map[FileID]*typeDefs{},
		importing: map[FileID]bool{},
		exporting: map[FileID]bool{},
		resolving: map[FileID]bool{},
		typeMemo:  map[*ast.ConstDecl]ir.Type{},
		typing:    map[*ast.ConstDecl]bool{},
		valueMemo: map[*ast.ConstDecl]*ir.Constant{},
		valuing:   map[*ast.ConstDecl]bool{},
	}
	for id, f := range files {
		if f == nil {
			continue
		}
		for _, decl := range f.Decls {
			d.declFile[decl] = id
		}
	}
	return d
}

func (d *directQueries) registry() *builtin.Registry { return d.reg }

func (d *directQueries) symbols(f FileID) map[string]*ast.ConstDecl {
	if s, ok := d.syms[f]; ok {
		return s
	}
	s := buildSymbols(d.files[f])
	d.syms[f] = s
	return s
}

func (d *directQueries) resolve(f FileID, id *ast.Identifier) *ast.ConstDecl {
	if decl := d.symbols(f)[id.Name]; decl != nil {
		return decl
	}
	if b, ok := d.importsOf(f).values[id.Name]; ok && !b.ambiguous {
		return b.target
	}
	return nil
}

func (d *directQueries) ambiguousImport(f FileID, id *ast.Identifier) bool {
	if d.symbols(f)[id.Name] != nil {
		return false
	}
	b, ok := d.importsOf(f).values[id.Name]
	return ok && b.ambiguous
}

func (d *directQueries) resolveMember(f FileID, m *ast.MemberExpr) *ast.ConstDecl {
	return resolveMemberThrough(d, f, m)
}

func (d *directQueries) typeOf(decl *ast.ConstDecl) ir.Type {
	if t, done := d.typeMemo[decl]; done {
		return t
	}
	if d.typing[decl] {
		return ir.Invalid // cycle
	}
	d.typing[decl] = true
	t := infer.Decl(decl, typeEnv{q: d, file: d.declFile[decl]})
	d.typing[decl] = false
	d.typeMemo[decl] = t
	return t
}

func (d *directQueries) valueOf(decl *ast.ConstDecl) *ir.Constant {
	if v, done := d.valueMemo[decl]; done {
		return v
	}
	if d.valuing[decl] {
		return nil // cycle
	}
	d.valuing[decl] = true
	v := computeValue(d.declFile[decl], decl, d)
	d.valuing[decl] = false
	d.valueMemo[decl] = v
	return v
}

func (d *directQueries) importsOf(f FileID) importTable {
	if t, ok := d.imps[f]; ok {
		return *t
	}
	if d.importing[f] {
		return importTable{}
	}
	d.importing[f] = true
	t := buildImports(d, d.files[f], d.uses[f])
	delete(d.importing, f)
	d.imps[f] = &t
	return t
}

func (d *directQueries) exportsOf(f FileID) exports {
	if e, ok := d.exps[f]; ok {
		return *e
	}
	if d.exporting[f] {
		return exports{}
	}
	d.exporting[f] = true
	e := buildExports(d, d.files[f], d.uses[f], d.typeDefsOf(f).byName)
	delete(d.exporting, f)
	d.exps[f] = &e
	return e
}

func (d *directQueries) typeDefsOf(f FileID) typeDefs {
	if td, ok := d.defs[f]; ok {
		return *td
	}
	if d.resolving[f] {
		return typeDefs{}
	}
	d.resolving[f] = true
	td := buildTypeDefs(d, d.files[f], d.reg, d.importsOf(f))
	delete(d.resolving, f)
	d.defs[f] = &td
	return td
}

func (d *directQueries) typeDefs(f FileID) []*ir.TypeDef { return d.typeDefsOf(f).list }

func (d *directQueries) universe(f FileID) map[string]*ir.TypeDef { return d.typeDefsOf(f).universe }

func (d *directQueries) usesOf(f FileID) map[*ast.UseDecl]FileID { return d.uses[f] }

package semantic

import (
	"maps"
	"slices"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/types/infer"
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

// Diagnostics returns a file's semantic diagnostics from the last Refresh,
// ordered by offset.
func (p *Program) Diagnostics(id FileID) []diagnostic.Diagnostic { return p.diags[id] }

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

// FileOf returns the file a constant of the last Refresh is declared in.
func (p *Program) FileOf(c *ir.Const) (FileID, bool) {
	if c == nil || c.Syntax == nil {
		return "", false
	}
	id, ok := p.db.declFile[c.Syntax]
	return id, ok
}

// FileOfType returns the file a type definition was declared in. A definition
// outside the program — the prelude's — is in no file.
func (p *Program) FileOfType(t *ir.TypeDef) (FileID, bool) {
	if t == nil || t.Syntax == nil {
		return "", false
	}
	id, ok := p.db.typeFile[t.Syntax]
	return id, ok
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
	// An assert condition is checked exactly as an un-annotated initializer,
	// so a literal inside one settles its signature here too.
	for _, a := range file.Asserts {
		if a.Cond != nil {
			infer.Check(a.Cond, env, sink)
		}
	}
	checkMethodBodies(file, reg, q.typeDefs(fileID), q.universe(fileID), qualified, sink)
	return out
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

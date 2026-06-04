package semantic

import (
	"maps"
	"slices"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/parser/abstract"
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
	uses    map[FileID]map[*ast.UseDecl]FileID
	modules map[FileID]*ir.Module
	diags   map[FileID][]diagnostic.Diagnostic
	shells  map[*ast.ConstDecl]*ir.Const
}

// NewProgram returns an empty program; add files with SetFile and analyze with
// Refresh.
func NewProgram() *Program {
	return &Program{
		db:      newDatabase(universe()),
		docs:    map[FileID]*abstract.Document{},
		uses:    map[FileID]map[*ast.UseDecl]FileID{},
		modules: map[FileID]*ir.Module{},
		diags:   map[FileID][]diagnostic.Diagnostic{},
	}
}

// SetFile installs (or replaces) one file: its parsed document and the
// resolved targets of its use declarations. Call Refresh after the batch of
// changes to re-analyze.
func (p *Program) SetFile(id FileID, doc *abstract.Document, uses map[*ast.UseDecl]FileID) {
	p.docs[id] = doc
	p.uses[id] = uses
	p.db.setInput(id, doc.File(), uses)
}

// RemoveFile drops a file that left the project.
func (p *Program) RemoveFile(id FileID) {
	delete(p.docs, id)
	delete(p.uses, id)
	delete(p.modules, id)
	delete(p.diags, id)
	p.db.dropInput(id)
}

// Refresh re-assembles every file's module and diagnostics over the engine.
// As in Document, assembling is the cheap outer pass re-done per change (its
// diagnostics carry offsets); the expensive facts behind it are memoized.
func (p *Program) Refresh() {
	files := make(map[FileID]*ast.File, len(p.docs))
	for id, doc := range p.docs {
		files[id] = doc.File()
	}
	p.shells = constShells(files)

	q := engineQueries{p.db}
	for _, id := range p.Files() {
		doc := p.docs[id]
		module, diags := assemble(id, doc.File(), positionsOf(doc.Concrete().Tree()), q, p.shells)
		p.modules[id] = module
		p.diags[id] = diags
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
	return p.shells[q.resolve(file, id)]
}

// ResolveMember returns the constant a namespace member access (geo.Origin) in
// file refers to, or nil.
func (p *Program) ResolveMember(file FileID, m *ast.MemberExpr) *ir.Const {
	q := engineQueries{p.db}
	return p.shells[q.resolveMember(file, m)]
}

// FileOf returns the file a constant of the last Refresh is declared in.
func (p *Program) FileOf(c *ir.Const) (FileID, bool) {
	if c == nil || c.Syntax == nil {
		return "", false
	}
	id, ok := p.db.declFile[c.Syntax]
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

// TypeNames returns the type definitions in scope in file, for an editor
// completing a type name: the file's own type declarations, its imported
// ones, and the builtin/prelude types, by name, with nearer declarations
// shadowing.
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
	for _, def := range p.db.reg.Defs() {
		add(def)
	}
	return out
}

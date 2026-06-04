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
	shells := constShells(files)

	q := engineQueries{p.db}
	for _, id := range p.Files() {
		doc := p.docs[id]
		module, diags := assemble(id, doc.File(), positionsOf(doc.Concrete().Tree()), q, shells)
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

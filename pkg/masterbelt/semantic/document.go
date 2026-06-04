package semantic

import (
	"github.com/masterbelt/masterbelt/pkg/masterbelt/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ast"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ir"
)

// Document is an incrementally analyzed program. It layers the query engine over
// the incremental abstract syntax tree: after an edit, only the declarations the
// change actually affected are re-resolved and re-typed (the engine reuses the
// rest), yet the resulting module and diagnostics are always identical to a full
// Analyze of the current source.
type Document struct {
	ast    *abstract.Document
	db     *database
	module *ir.Module
	diags  []diagnostic.Diagnostic
	byDecl map[*ast.ConstDecl]*ir.Const // module consts indexed by their syntax, for Resolve
}

// NewDocument lexes, parses, lowers, and analyzes src, then keeps the analysis
// up to date across Edits.
func NewDocument(src []byte) *Document {
	d := &Document{ast: abstract.NewDocument(src), db: newDatabase(universe())}
	d.refresh()
	return d
}

// Edit applies e and incrementally re-analyzes.
func (d *Document) Edit(e source.Edit) {
	d.ast.Edit(e)
	d.refresh()
}

// Module returns the resolved, typed IR of the current source.
func (d *Document) Module() *ir.Module { return d.module }

// Diagnostics returns the current semantic diagnostics, ordered by offset.
func (d *Document) Diagnostics() []diagnostic.Diagnostic { return d.diags }

// AST returns the underlying incremental syntax document.
func (d *Document) AST() *abstract.Document { return d.ast }

// Resolve returns the constant a value-position identifier refers to, or nil if
// it resolves to no declaration. It is the resolution the editor uses for hover,
// go-to-definition, find-references, and rename, including references nested in
// an expression. The lookup goes through the engine, so it reuses the memoized
// resolution from the last analysis.
func (d *Document) Resolve(id *ast.Identifier) *ir.Const {
	decl, _ := d.db.read(resolveKey(id)).(*ast.ConstDecl)
	if decl == nil {
		return nil
	}
	return d.byDecl[decl]
}

// Buffer returns the underlying editable source buffer.
func (d *Document) Buffer() source.Buffer { return d.ast.Buffer() }

// refresh re-runs the assembler over the engine. setInput opens a new revision
// so the engine knows the input changed; the assembler then pulls resolution and
// type facts through the engine, which recomputes only what the edit disturbed.
// Assembling the IR and diagnostics is itself cheap (a pass over the
// declarations) and re-done each edit, since diagnostics carry source offsets
// that shift on every edit and so cannot be memoized.
func (d *Document) refresh() {
	d.db.setInput(d.ast.File())
	d.module, d.diags = assemble(
		d.ast.File(),
		positionsOf(d.ast.Concrete().Tree()),
		engineQueries{d.db},
	)
	d.byDecl = make(map[*ast.ConstDecl]*ir.Const, len(d.module.Consts))
	for _, c := range d.module.Consts {
		d.byDecl[c.Syntax] = c
	}
}

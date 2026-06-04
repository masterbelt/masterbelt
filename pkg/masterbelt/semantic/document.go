package semantic

import (
	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/types/infer"
	"github.com/masterbelt/masterbelt/pkg/source"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
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
	r, _ := d.db.read(resolveKey(soleFileID, id)).(resolution)
	if r.decl == nil {
		return nil
	}
	return d.byDecl[r.decl]
}

// Buffer returns the underlying editable source buffer.
func (d *Document) Buffer() source.Buffer { return d.ast.Buffer() }

// TypeNames returns the type definitions in scope, for an editor completing a
// type name: the file's own type declarations and the builtin/prelude types, by
// name, with a file declaration shadowing a builtin of the same name.
func (d *Document) TypeNames() []*ir.TypeDef {
	seen := map[string]bool{}
	var out []*ir.TypeDef
	for _, t := range d.module.Types {
		if t.Name == "" || seen[t.Name] {
			continue
		}
		seen[t.Name] = true
		out = append(out, t)
	}
	for _, def := range d.db.reg.Defs() {
		if def.Name == "" || seen[def.Name] {
			continue
		}
		seen[def.Name] = true
		out = append(out, def)
	}
	return out
}

// FuncLitTypes returns the settled signature of every function literal in the
// document — annotations, pushed-down expectations, and inferred parts
// combined, exactly as the checking walk settles them. It is what the editor
// reads to hover a lambda parameter or render its inlay hint. The walk reads
// through the engine, so it reuses the memoized resolution and types of the
// last analysis.
func (d *Document) FuncLitTypes() map[*ast.FuncLit]*ir.Func {
	out := map[*ast.FuncLit]*ir.Func{}
	sink := &infer.Sink{SolvedFuncLit: func(lit *ast.FuncLit, t *ir.Func) { out[lit] = t }}

	q := engineQueries{d.db}
	env := typeEnv{q: q, file: soleFileID}
	reg := q.registry()
	file := d.ast.File()
	for _, decl := range file.Decls {
		if decl.Value == nil {
			continue
		}
		// The same annotated/un-annotated split assemble makes, with the
		// annotation resolved silently (its problems are already diagnosed).
		annType := ir.Invalid
		if decl.Type != nil {
			r := &infer.TypeResolver{Reg: reg}
			annType = r.ResolveType(decl.Type, nil)
		}
		if annType != ir.Invalid {
			infer.CheckAgainst(decl.Value, annType, env, sink)
		} else {
			infer.Check(decl.Value, env, sink)
		}
	}
	checkMethodBodies(file, reg, d.module.Types, sink)
	return out
}

// refresh re-runs the assembler over the engine. setInput opens a new revision
// so the engine knows the input changed; the assembler then pulls resolution and
// type facts through the engine, which recomputes only what the edit disturbed.
// Assembling the IR and diagnostics is itself cheap (a pass over the
// declarations) and re-done each edit, since diagnostics carry source offsets
// that shift on every edit and so cannot be memoized.
func (d *Document) refresh() {
	d.db.setInput(soleFileID, d.ast.File(), nil)
	d.module, d.diags = assemble(
		soleFileID,
		d.ast.File(),
		positionsOf(d.ast.Concrete().Tree()),
		engineQueries{d.db},
		constShells(map[FileID]*ast.File{soleFileID: d.ast.File()}),
	)
	d.byDecl = make(map[*ast.ConstDecl]*ir.Const, len(d.module.Consts))
	for _, c := range d.module.Consts {
		d.byDecl[c.Syntax] = c
	}
}

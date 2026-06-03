// Package semantic resolves names and infers types for a masterbelt program,
// producing the resolved IR (package source/ir).
//
// The semantic facts a program needs — the symbol table, what each reference
// resolves to, and each constant's type — are expressed as a small set of pure
// queries (the queries interface). assemble turns those queries plus the AST
// into the IR and diagnostics. Two query implementations share that one
// assembler: a direct one (this file), used by Analyze for a full recompute and
// as the oracle, and an incremental, memoizing one backed by the query database
// (engine.go), used by Document. Because both feed the same assembler, the
// incremental result is identical to the full one.
package semantic

import (
	"math/big"
	"sort"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ast"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/cst"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ir"
)

// queries are the pure, memoizable semantic facts the assembler needs.
type queries interface {
	// resolve returns the declaration a constant's reference initializer binds
	// to, or nil if its initializer is not a (resolvable) reference.
	resolve(decl *ast.ConstDecl) *ast.ConstDecl
	// typeOf returns a constant's type (ir.Invalid when undeterminable).
	typeOf(decl *ast.ConstDecl) ir.Type
	// valueOf returns a constant's evaluated value, or nil when it cannot be
	// evaluated (missing initializer, undefined reference, or cycle).
	valueOf(decl *ast.ConstDecl) *big.Int
}

// Analyze resolves and types the document's program, returning the IR module and
// the semantic diagnostics. It recomputes everything from scratch; it is the
// reference analysis and the oracle for the incremental Document.
func Analyze(doc *abstract.Document) (*ir.Module, []diagnostic.Diagnostic) {
	file := doc.File()
	return assemble(file, positionsOf(doc.Concrete().Tree()), newDirectQueries(file))
}

// assemble builds the IR module and all semantic diagnostics from the AST, using
// q for the resolution and typing facts. It is shared by the reference and
// incremental analyzers, so they cannot diverge.
func assemble(file *ast.File, positions map[cst.Green]span, q queries) (*ir.Module, []diagnostic.Diagnostic) {
	diags := &diagnostic.List{}
	at := func(n ast.Node) span { return spanOf(positions, n) }

	// Create the IR constants first so references can bind to them.
	module := &ir.Module{}
	irOf := make(map[*ast.ConstDecl]*ir.Const, len(file.Decls))
	for _, decl := range file.Decls {
		c := &ir.Const{Name: decl.Name, Public: decl.Public, Doc: decl.Doc, Syntax: decl}
		irOf[decl] = c
		module.Consts = append(module.Consts, c)
	}

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

	cyclic := cyclicDecls(file, q)

	for _, decl := range file.Decls {
		c := irOf[decl]

		switch v := decl.Value.(type) {
		case *ast.IntLit:
			c.Value = &ir.IntLiteral{Text: v.Text}
		case *ast.Identifier:
			if target := q.resolve(decl); target != nil {
				c.Value = &ir.Reference{Target: irOf[target]}
			} else if v.Name != "" {
				s := at(v)
				diags.Add(newUndefinedNameDiagnostic(s.offset, s.width, v.Name))
			}
		}

		c.Type = q.typeOf(decl)
		c.Eval = q.valueOf(decl)

		if decl.Type != nil {
			if _, ok := ir.LookupType(decl.Type.Name); !ok {
				s := at(decl.Type)
				diags.Add(newUnknownTypeDiagnostic(s.offset, s.width, decl.Type.Name))
			}
		}
		if cyclic[decl] {
			s := at(decl)
			diags.Add(newCyclicReferenceDiagnostic(s.offset, s.width, decl.Name))
		}
		// A value that does not fit its concrete type overflows. Untyped
		// constants have no fixed range, so Fits accepts them.
		if c.Eval != nil && !c.Type.Fits(c.Eval) {
			s := at(decl.Value)
			diags.Add(newConstantOverflowDiagnostic(s.offset, s.width, c.Eval.String(), c.Type.String()))
		}
	}

	items := diags.Items()
	sort.SliceStable(items, func(i, j int) bool { return items[i].Offset < items[j].Offset })
	return module, items
}

// buildSymbols maps each declared name to its first declaration.
func buildSymbols(file *ast.File) map[string]*ast.ConstDecl {
	syms := map[string]*ast.ConstDecl{}
	for _, decl := range file.Decls {
		if decl.Name != "" {
			if _, exists := syms[decl.Name]; !exists {
				syms[decl.Name] = decl
			}
		}
	}
	return syms
}

// computeType is the type rule, shared by both query implementations: an
// annotation gives a concrete type, an integer literal is untyped, and a
// reference inherits its referent's type. It reads other facts through q so the
// memoizing engine can track the dependencies.
func computeType(decl *ast.ConstDecl, q queries) ir.Type {
	if decl.Type != nil {
		if t, ok := ir.LookupType(decl.Type.Name); ok {
			return t
		}
		return ir.Invalid
	}
	switch decl.Value.(type) {
	case *ast.IntLit:
		return ir.UntypedInt
	case *ast.Identifier:
		if target := q.resolve(decl); target != nil {
			return q.typeOf(target)
		}
		return ir.Invalid
	default:
		return ir.Invalid
	}
}

// computeValue is the evaluation rule, shared by both query implementations: a
// literal parses to its integer, a reference takes its referent's value. Reading
// other facts through q lets the engine track dependencies (and reuses its cycle
// guard). Overflow is intentionally not checked here — untyped constants are
// arbitrary precision; the range check happens in assemble where a concrete type
// is known.
func computeValue(decl *ast.ConstDecl, q queries) *big.Int {
	switch v := decl.Value.(type) {
	case *ast.IntLit:
		n, ok := new(big.Int).SetString(v.Text, 10)
		if !ok {
			return nil
		}
		return n
	case *ast.Identifier:
		if target := q.resolve(decl); target != nil {
			return q.valueOf(target)
		}
		return nil
	default:
		return nil
	}
}

// cyclicDecls returns the declarations caught in a type-inference cycle. The
// only type-level dependency is an un-annotated reference inheriting its
// referent's type, so the dependency graph is functional (each such declaration
// has exactly one out-edge); its cycles are found with a coloured chain walk.
func cyclicDecls(file *ast.File, q queries) map[*ast.ConstDecl]bool {
	next := func(decl *ast.ConstDecl) *ast.ConstDecl {
		if decl.Type != nil {
			return nil // an annotation breaks the inheritance chain
		}
		if _, ok := decl.Value.(*ast.Identifier); !ok {
			return nil
		}
		return q.resolve(decl)
	}

	const (
		white = iota
		gray
		black
	)
	color := map[*ast.ConstDecl]int{}
	cyclic := map[*ast.ConstDecl]bool{}

	for _, start := range file.Decls {
		if color[start] != white {
			continue
		}
		var path []*ast.ConstDecl
		decl := start
		for decl != nil && color[decl] == white {
			color[decl] = gray
			path = append(path, decl)
			decl = next(decl)
		}
		if decl != nil && color[decl] == gray {
			// The chain re-entered a node on the current path: everything from
			// that node to the end of the path forms a cycle.
			inCycle := false
			for _, n := range path {
				if n == decl {
					inCycle = true
				}
				if inCycle {
					cyclic[n] = true
				}
			}
		}
		for _, n := range path {
			color[n] = black
		}
	}
	return cyclic
}

// --- direct (reference) query implementation --------------------------------

// directQueries computes the semantic facts directly, memoizing types within a
// single analysis (and guarding against cycles). It carries no state across
// analyses — that is the incremental engine's job.
type directQueries struct {
	file      *ast.File
	syms      map[string]*ast.ConstDecl
	typeMemo  map[*ast.ConstDecl]ir.Type
	typing    map[*ast.ConstDecl]bool
	valueMemo map[*ast.ConstDecl]*big.Int
	valuing   map[*ast.ConstDecl]bool
}

func newDirectQueries(file *ast.File) *directQueries {
	return &directQueries{
		file:      file,
		typeMemo:  map[*ast.ConstDecl]ir.Type{},
		typing:    map[*ast.ConstDecl]bool{},
		valueMemo: map[*ast.ConstDecl]*big.Int{},
		valuing:   map[*ast.ConstDecl]bool{},
	}
}

func (d *directQueries) symbols() map[string]*ast.ConstDecl {
	if d.syms == nil {
		d.syms = buildSymbols(d.file)
	}
	return d.syms
}

func (d *directQueries) resolve(decl *ast.ConstDecl) *ast.ConstDecl {
	ref, ok := decl.Value.(*ast.Identifier)
	if !ok {
		return nil
	}
	return d.symbols()[ref.Name]
}

func (d *directQueries) typeOf(decl *ast.ConstDecl) ir.Type {
	if t, done := d.typeMemo[decl]; done {
		return t
	}
	if d.typing[decl] {
		return ir.Invalid // cycle
	}
	d.typing[decl] = true
	t := computeType(decl, d)
	d.typing[decl] = false
	d.typeMemo[decl] = t
	return t
}

func (d *directQueries) valueOf(decl *ast.ConstDecl) *big.Int {
	if v, done := d.valueMemo[decl]; done {
		return v
	}
	if d.valuing[decl] {
		return nil // cycle
	}
	d.valuing[decl] = true
	v := computeValue(decl, d)
	d.valuing[decl] = false
	d.valueMemo[decl] = v
	return v
}

// --- positions --------------------------------------------------------------

type span struct{ offset, width int }

func spanOf(positions map[cst.Green]span, n ast.Node) span {
	if n == nil {
		return span{}
	}
	if s, ok := positions[n.Syntax()]; ok {
		return s
	}
	return span{}
}

// positionsOf records the offset and width of every element of the positioned
// concrete tree, keyed by its green node, so diagnostics can be anchored from a
// position-independent AST node back to its source.
func positionsOf(root cst.Tree) map[cst.Green]span {
	positions := map[cst.Green]span{}
	var walk func(t cst.Tree)
	walk = func(t cst.Tree) {
		positions[t.Green()] = span{offset: t.Offset(), width: t.End() - t.Offset()}
		for _, child := range t.Children() {
			walk(child)
		}
	}
	walk(root)
	return positions
}

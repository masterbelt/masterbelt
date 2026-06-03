// Package semantic resolves names and infers types for a masterbelt program,
// producing the resolved IR (package source/ir).
//
// Analyze here is the reference analysis: it recomputes everything from the AST
// in one pass set. It defines the language's static semantics and serves as the
// oracle the incremental (query-based) analyzer is checked against.
package semantic

import (
	"sort"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ast"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/cst"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ir"
)

// Analyze resolves and types the document's program, returning the IR module and
// the semantic diagnostics (ordered by source position).
func Analyze(doc *abstract.Document) (*ir.Module, []diagnostic.Diagnostic) {
	a := &analyzer{
		positions: positionsOf(doc.Concrete().Tree()),
		irByDecl:  map[*ast.ConstDecl]*ir.Const{},
		byName:    map[string]*ast.ConstDecl{},
		typeMemo:  map[*ast.ConstDecl]ir.Type{},
		typing:    map[*ast.ConstDecl]bool{},
		diags:     &diagnostic.List{},
	}
	return a.run(doc.File())
}

type span struct{ offset, width int }

type analyzer struct {
	positions map[cst.Green]span
	irByDecl  map[*ast.ConstDecl]*ir.Const
	byName    map[string]*ast.ConstDecl // first declaration of each name
	typeMemo  map[*ast.ConstDecl]ir.Type
	typing    map[*ast.ConstDecl]bool // decls whose type is being inferred (cycle guard)
	diags     *diagnostic.List
}

func (a *analyzer) run(file *ast.File) (*ir.Module, []diagnostic.Diagnostic) {
	module := &ir.Module{}

	// Pass 1: collect declarations, so references can bind to their IR nodes,
	// and flag redeclarations of the same name.
	for _, decl := range file.Decls {
		c := &ir.Const{Name: decl.Name, Public: decl.Public, Doc: decl.Doc, Syntax: decl}
		a.irByDecl[decl] = c
		module.Consts = append(module.Consts, c)

		if decl.Name == "" {
			continue // a missing name is already a parse diagnostic
		}
		if _, dup := a.byName[decl.Name]; dup {
			s := a.span(decl)
			a.diags.Add(newDuplicateDeclarationDiagnostic(s.offset, s.width, decl.Name))
		} else {
			a.byName[decl.Name] = decl
		}
	}

	// Pass 2: resolve initializers (binding references, flagging undefined names).
	for _, decl := range file.Decls {
		a.irByDecl[decl].Value = a.resolveValue(decl)
	}

	// Pass 3: infer types (annotation / untyped literal / referent, with cycle
	// and unknown-type checks).
	for _, decl := range file.Decls {
		a.irByDecl[decl].Type = a.typeOf(decl)
	}

	items := a.diags.Items()
	sort.SliceStable(items, func(i, j int) bool { return items[i].Offset < items[j].Offset })
	return module, items
}

// resolveValue lowers a declaration's initializer to a resolved ir.Value, or nil
// when it is missing (already a parse diagnostic) or names an undefined constant.
func (a *analyzer) resolveValue(decl *ast.ConstDecl) ir.Value {
	switch v := decl.Value.(type) {
	case *ast.IntLit:
		return &ir.IntLiteral{Text: v.Text}
	case *ast.NameRef:
		target, ok := a.byName[v.Name]
		if !ok {
			s := a.span(v)
			a.diags.Add(newUndefinedNameDiagnostic(s.offset, s.width, v.Name))
			return nil
		}
		return &ir.Reference{Target: a.irByDecl[target]}
	default:
		return nil
	}
}

// typeOf returns a declaration's type, memoizing the result and detecting cycles
// among un-annotated references.
func (a *analyzer) typeOf(decl *ast.ConstDecl) ir.Type {
	if t, done := a.typeMemo[decl]; done {
		return t
	}
	if a.typing[decl] {
		s := a.span(decl)
		a.diags.Add(newCyclicReferenceDiagnostic(s.offset, s.width, decl.Name))
		return ir.Invalid
	}
	a.typing[decl] = true
	t := a.computeType(decl)
	a.typing[decl] = false
	a.typeMemo[decl] = t
	return t
}

func (a *analyzer) computeType(decl *ast.ConstDecl) ir.Type {
	// An annotation gives a concrete type directly.
	if decl.Type != nil {
		t, ok := ir.LookupType(decl.Type.Name)
		if !ok {
			s := a.span(decl.Type)
			a.diags.Add(newUnknownTypeDiagnostic(s.offset, s.width, decl.Type.Name))
			return ir.Invalid
		}
		return t
	}
	// Otherwise infer from the initializer: an integer literal is untyped, a
	// reference inherits its referent's type.
	switch v := decl.Value.(type) {
	case *ast.IntLit:
		return ir.UntypedInt
	case *ast.NameRef:
		if target, ok := a.byName[v.Name]; ok {
			return a.typeOf(target)
		}
		return ir.Invalid // undefined; already reported
	default:
		return ir.Invalid // missing initializer
	}
}

// span returns the byte range of an AST node, read from the positioned concrete
// tree it was lowered from.
func (a *analyzer) span(n ast.Node) span {
	if n == nil {
		return span{}
	}
	if s, ok := a.positions[n.Syntax()]; ok {
		return s
	}
	return span{}
}

// positionsOf records the offset and width of every element of the positioned
// concrete tree, keyed by its green node, so the analyzer can anchor diagnostics
// from a position-independent AST node back to its source.
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

package abstract

import (
	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/parser/concrete"
	"github.com/masterbelt/masterbelt/pkg/source"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
)

// Document is an incrementally maintained abstract syntax tree over an editable
// source.
//
// It wraps the concrete Document and re-lowers after each edit, but the
// re-lowering is cheap: declarations the concrete parser left untouched keep the
// very same green CST node (by pointer), and Document caches each lowered
// declaration under that pointer. So a re-lower only rebuilds the declarations
// that were actually reparsed; the rest are reused verbatim from the cache.
//
// The File is always identical to lowering the current concrete tree from
// scratch.
type Document struct {
	cst         *concrete.Document
	file        *ast.File
	cache       map[*cst.Node]*ast.ConstDecl
	typeCache   map[*cst.Node]*ast.TypeDecl
	funcCache   map[*cst.Node]*ast.FuncDecl
	useCache    map[*cst.Node]*ast.UseDecl
	assertCache map[*cst.Node]*ast.AssertDecl
}

// NewDocument lexes, parses, and lowers src, then keeps the AST up to date
// across Edits.
func NewDocument(src []byte) *Document {
	d := &Document{
		cst:         concrete.NewDocument(src),
		cache:       map[*cst.Node]*ast.ConstDecl{},
		typeCache:   map[*cst.Node]*ast.TypeDecl{},
		funcCache:   map[*cst.Node]*ast.FuncDecl{},
		useCache:    map[*cst.Node]*ast.UseDecl{},
		assertCache: map[*cst.Node]*ast.AssertDecl{},
	}
	d.rebuild()
	return d
}

// File returns the current abstract syntax tree.
func (d *Document) File() *ast.File { return d.file }

// Buffer returns the underlying editable buffer, for resolving source spans of
// a node's Syntax link.
func (d *Document) Buffer() source.Buffer { return d.cst.Buffer() }

// Concrete returns the underlying concrete Document, for reaching the lossless
// tree, the token stream, and trivia behind any AST node.
func (d *Document) Concrete() *concrete.Document { return d.cst }

// Diagnostics returns the current parse diagnostics, ordered by offset.
func (d *Document) Diagnostics() []diagnostic.Diagnostic { return d.cst.Diagnostics() }

// Edit applies e and incrementally updates the abstract syntax tree.
func (d *Document) Edit(e source.Edit) {
	d.cst.Edit(e)
	d.rebuild()
}

// rebuild re-lowers the File from the current concrete tree, reusing cached
// declarations whose backing CST node survived the edit unchanged. The cache is
// rebuilt from scratch each time so it never retains nodes that have left the
// tree; carrying a hit over costs only a map lookup and assignment.
func (d *Document) rebuild() {
	root := d.cst.Tree()
	rootNode, _ := root.Node()
	buf := d.cst.Buffer()

	next := make(map[*cst.Node]*ast.ConstDecl, len(d.cache))
	nextTypes := make(map[*cst.Node]*ast.TypeDecl, len(d.typeCache))
	nextFuncs := make(map[*cst.Node]*ast.FuncDecl, len(d.funcCache))
	nextUses := make(map[*cst.Node]*ast.UseDecl, len(d.useCache))
	nextAsserts := make(map[*cst.Node]*ast.AssertDecl, len(d.assertCache))
	var uses []*ast.UseDecl
	var decls []*ast.ConstDecl
	var types []*ast.TypeDecl
	var funcs []*ast.FuncDecl
	var asserts []*ast.AssertDecl
	foreachDecl(root, func(child cst.Tree, green *cst.Node) {
		switch green.Kind() {
		case cst.UseDecl:
			ud, ok := d.useCache[green]
			if !ok {
				ud = lowerUseDecl(child, buf)
			}
			nextUses[green] = ud
			uses = append(uses, ud)
		case cst.ConstDecl:
			decl, ok := d.cache[green]
			if !ok {
				decl = lowerConstDecl(child, buf)
			}
			next[green] = decl
			decls = append(decls, decl)
		case cst.TypeDecl:
			td, ok := d.typeCache[green]
			if !ok {
				td = lowerTypeDecl(child, buf)
			}
			nextTypes[green] = td
			types = append(types, td)
		case cst.FuncDecl:
			fd, ok := d.funcCache[green]
			if !ok {
				fd = lowerFuncDecl(child, buf)
			}
			nextFuncs[green] = fd
			funcs = append(funcs, fd)
		case cst.AssertDecl:
			ad, ok := d.assertCache[green]
			if !ok {
				ad = lowerAssertDecl(child, buf)
			}
			nextAsserts[green] = ad
			asserts = append(asserts, ad)
		}
	})

	d.cache = next
	d.typeCache = nextTypes
	d.funcCache = nextFuncs
	d.useCache = nextUses
	d.assertCache = nextAsserts
	d.file = ast.NewFile(uses, decls, types, funcs, asserts, rootNode)
}

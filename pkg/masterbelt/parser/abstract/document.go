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
	cst            *concrete.Document
	file           *ast.File
	cache          map[*cst.Node]*ast.ConstDecl
	typeCache      map[*cst.Node]*ast.TypeDecl
	enumCache      map[*cst.Node]*ast.EnumDecl
	interfaceCache map[*cst.Node]*ast.InterfaceDecl
	funcCache      map[*cst.Node]*ast.FuncDecl
	useCache       map[*cst.Node]*ast.UseDecl
	assertCache    map[*cst.Node]*ast.AssertDecl
}

// NewDocument lexes, parses, and lowers src, then keeps the AST up to date
// across Edits.
func NewDocument(src []byte) *Document {
	d := &Document{
		cst:            concrete.NewDocument(src),
		cache:          map[*cst.Node]*ast.ConstDecl{},
		typeCache:      map[*cst.Node]*ast.TypeDecl{},
		enumCache:      map[*cst.Node]*ast.EnumDecl{},
		interfaceCache: map[*cst.Node]*ast.InterfaceDecl{},
		funcCache:      map[*cst.Node]*ast.FuncDecl{},
		useCache:       map[*cst.Node]*ast.UseDecl{},
		assertCache:    map[*cst.Node]*ast.AssertDecl{},
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

	// Phase 1: walk the top-level declarations, lowering each (or reusing the
	// cache hit) into the fresh caches and ordered slices a rebuildState holds.
	rs := d.newRebuildState(buf)
	foreachDecl(root, func(child cst.Tree, green *cst.Node) {
		rs.collect(child, green)
	})

	// Phase 2: swap the rebuilt caches in and publish the new File.
	rs.commit(d, rootNode)
}

// rebuildState accumulates one rebuild pass: the fresh per-kind caches (keyed by
// surviving green node) and the ordered slices that become the new File. It reads
// the prior caches off the Document for hit reuse and lowers the misses through
// the buffer it was created with.
type rebuildState struct {
	d   *Document
	buf source.Buffer

	uses       map[*cst.Node]*ast.UseDecl
	consts     map[*cst.Node]*ast.ConstDecl
	types      map[*cst.Node]*ast.TypeDecl
	enums      map[*cst.Node]*ast.EnumDecl
	interfaces map[*cst.Node]*ast.InterfaceDecl
	funcs      map[*cst.Node]*ast.FuncDecl
	asserts    map[*cst.Node]*ast.AssertDecl

	useList       []*ast.UseDecl
	constList     []*ast.ConstDecl
	typeList      []*ast.TypeDecl
	enumList      []*ast.EnumDecl
	interfaceList []*ast.InterfaceDecl
	funcList      []*ast.FuncDecl
	assertList    []*ast.AssertDecl
}

// newRebuildState allocates the fresh caches, each sized from its predecessor so
// a full reuse pass does no growth.
func (d *Document) newRebuildState(buf source.Buffer) *rebuildState {
	return &rebuildState{
		d:          d,
		buf:        buf,
		uses:       make(map[*cst.Node]*ast.UseDecl, len(d.useCache)),
		consts:     make(map[*cst.Node]*ast.ConstDecl, len(d.cache)),
		types:      make(map[*cst.Node]*ast.TypeDecl, len(d.typeCache)),
		enums:      make(map[*cst.Node]*ast.EnumDecl, len(d.enumCache)),
		interfaces: make(map[*cst.Node]*ast.InterfaceDecl, len(d.interfaceCache)),
		funcs:      make(map[*cst.Node]*ast.FuncDecl, len(d.funcCache)),
		asserts:    make(map[*cst.Node]*ast.AssertDecl, len(d.assertCache)),
	}
}

// collect lowers one top-level declaration, reusing the prior cache entry when
// the backing green node survived the edit, and records it in the matching fresh
// cache and ordered slice. Any non-declaration kind is skipped.
func (rs *rebuildState) collect(child cst.Tree, green *cst.Node) {
	switch green.Kind() {
	case cst.UseDecl:
		rs.useList = collectDecl(green, child, rs.buf, rs.d.useCache, rs.uses, rs.useList, lowerUseDecl)
	case cst.ConstDecl:
		rs.constList = collectDecl(green, child, rs.buf, rs.d.cache, rs.consts, rs.constList, lowerConstDecl)
	case cst.TypeDecl:
		rs.typeList = collectDecl(green, child, rs.buf, rs.d.typeCache, rs.types, rs.typeList, lowerTypeDecl)
	case cst.EnumDecl:
		rs.enumList = collectDecl(green, child, rs.buf, rs.d.enumCache, rs.enums, rs.enumList, lowerEnumDecl)
	case cst.InterfaceDecl:
		rs.interfaceList = collectDecl(green, child, rs.buf, rs.d.interfaceCache, rs.interfaces, rs.interfaceList, lowerInterfaceDecl)
	case cst.FuncDecl:
		rs.funcList = collectDecl(green, child, rs.buf, rs.d.funcCache, rs.funcs, rs.funcList, lowerFuncDecl)
	case cst.AssertDecl:
		rs.assertList = collectDecl(green, child, rs.buf, rs.d.assertCache, rs.asserts, rs.assertList, lowerAssertDecl)
	default:
		// Any other kind is not a top-level declaration this document
		// lowers: it is skipped and never enters the rebuilt File.
	}
}

// collectDecl resolves one declaration of kind D: it reuses prev's entry for
// green when the edit left the node unchanged, otherwise lowers child through
// lower. The result is stored under green in the fresh cache next and appended to
// list, whose extended value is returned.
func collectDecl[D any](
	green *cst.Node,
	child cst.Tree,
	buf source.Buffer,
	prev, next map[*cst.Node]D,
	list []D,
	lower func(cst.Tree, source.Buffer) D,
) []D {
	d, ok := prev[green]
	if !ok {
		d = lower(child, buf)
	}
	next[green] = d
	return append(list, d)
}

// commit swaps the rebuilt caches into the Document and publishes the new File
// in source order under rootNode.
func (rs *rebuildState) commit(d *Document, rootNode *cst.Node) {
	d.cache = rs.consts
	d.typeCache = rs.types
	d.enumCache = rs.enums
	d.interfaceCache = rs.interfaces
	d.funcCache = rs.funcs
	d.useCache = rs.uses
	d.assertCache = rs.asserts
	d.file = ast.NewFile(rs.useList, rs.constList, rs.typeList, rs.enumList, rs.interfaceList, rs.funcList, rs.assertList, rootNode)
}

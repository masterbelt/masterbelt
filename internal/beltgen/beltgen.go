// Package beltgen is a deterministic synthetic-project generator (D-1 M2): it
// produces a map of .belt sources whose use-graph has real cross-file depth and
// branching, so a benchmark can exercise reachability and early cutoff at scale.
//
// Everything is derived from the seed and the file/decl index — there is no
// clock and no unseeded randomness — so the same Params always yield byte-equal
// sources. The generated language is the real one (study
// pkg/masterbelt/testdata/examples/*.belt): pub const, type, enum, fn, and use
// declarations. The cross-file edges are genuine: a file imports its children
// by namespace and reads their exported constants, so the dependency graph the
// engine walks matches the file tree the generator lays out.
package beltgen

import (
	"fmt"
	"strings"
)

// Params describes a synthetic project. The file count is derived from Depth
// and Branching (a tree), then padded with leaves until it reaches Files; the
// per-file declaration count is DeclsPerFile. Seed makes the whole thing
// reproducible.
type Params struct {
	// Files is the minimum number of .belt files to emit. The use-tree of the
	// given Depth and Branching is generated first; if it has fewer files than
	// Files, extra leaf files are appended so a bench can dial the file count
	// independently of the graph shape.
	Files int
	// DeclsPerFile is the number of declarations each file carries (its own
	// constants, plus the cross-file references that form the use edges).
	DeclsPerFile int
	// Depth is the height of the use-tree: the entry is at depth 0, its imports
	// at depth 1, and so on. Depth 0 means a single file with no imports.
	Depth int
	// Branching is how many children each non-leaf file imports.
	Branching int
	// Seed makes generation reproducible: identical Params give byte-equal
	// sources.
	Seed int64
}

// EntryFile is the id of the generated entry point — the root of the use-tree
// and the file a project manifest would name.
const EntryFile = "entry.belt"

// Project generates the synthetic project's sources, keyed by file id (a
// "/"-free flat layout, so a use path is just the file name). The result is
// deterministic in p and always includes EntryFile.
func Project(p Params) map[string][]byte {
	g := newGen(p)
	g.buildTree()
	g.pad()
	return g.emit()
}

// node is one file in the use-tree: its id, the children it imports, and the
// index the deterministic content derives from.
type node struct {
	id       string
	index    int
	children []*node
}

// gen carries the generation state for one Project call.
type gen struct {
	p     Params
	nodes []*node
	byID  map[string]*node
}

func newGen(p Params) *gen {
	return &gen{p: p, byID: map[string]*node{}}
}

// fileID names the i-th generated file. The entry is index 0; the rest are
// flat, numbered names so a use path is the bare file name.
func fileID(i int) string {
	if i == 0 {
		return EntryFile
	}
	return fmt.Sprintf("mod%04d.belt", i)
}

// newNode appends and registers a fresh node for the next free index.
func (g *gen) newNode() *node {
	n := &node{id: fileID(len(g.nodes)), index: len(g.nodes)}
	g.nodes = append(g.nodes, n)
	g.byID[n.id] = n
	return n
}

// buildTree lays out the use-tree breadth-first to the requested depth and
// branching, wiring each non-leaf file to the children it will import.
func (g *gen) buildTree() {
	root := g.newNode()
	frontier := []*node{root}
	for range g.p.Depth {
		next := make([]*node, 0, len(frontier)*g.p.Branching)
		for _, parent := range frontier {
			for range g.p.Branching {
				child := g.newNode()
				parent.children = append(parent.children, child)
				next = append(next, child)
			}
		}
		frontier = next
	}
}

// pad appends childless leaf files until the file count reaches Files, so a
// bench can scale the file count beyond what the tree shape alone produces.
func (g *gen) pad() {
	for len(g.nodes) < g.p.Files {
		g.newNode()
	}
}

// emit renders every node to source.
func (g *gen) emit() map[string][]byte {
	out := make(map[string][]byte, len(g.nodes))
	for _, n := range g.nodes {
		out[n.id] = []byte(g.renderFile(n))
	}
	return out
}

// UsePaths returns, for each generated file id, the use paths it imports — the
// inverse of the file tree, in source order. A caller that wants to resolve the
// engine's use table without re-parsing can pair these with the file ids; the
// generated layout is flat, so a use path is just the imported file's id.
func UsePaths(p Params) map[string][]string {
	g := newGen(p)
	g.buildTree()
	g.pad()
	out := make(map[string][]string, len(g.nodes))
	for _, n := range g.nodes {
		paths := make([]string, 0, len(n.children))
		for _, c := range n.children {
			paths = append(paths, c.id)
		}
		out[n.id] = paths
	}
	return out
}

// namespaceOf is the import namespace a parent binds a child under. It is
// derived from the child's index so two children of one parent never collide.
func namespaceOf(child *node) string {
	return fmt.Sprintf("m%d", child.index)
}

// renderFile produces one file's source: a header, its own declarations, the
// imports of its children, the cross-file references that make the use edges
// real, and a closing assertion that the engine must fold.
func (g *gen) renderFile(n *node) string {
	var b strings.Builder
	fmt.Fprintf(&b, "// Generated by beltgen (seed %d, file %d).\n", g.p.Seed, n.index)
	writeImports(&b, n)
	g.writeDecls(&b, n)
	g.writeEdges(&b, n)
	return b.String()
}

// writeImports emits a namespace import for each child the file depends on.
func writeImports(b *strings.Builder, n *node) {
	for _, c := range n.children {
		fmt.Fprintf(b, "use %s from %q\n", namespaceOf(c), c.id)
	}
}

// writeEdges emits one cross-file reference per imported child: a constant that
// reads the child's first exported constant through the namespace. These reads
// are the real use-graph edges — they make the child's value query a dependency
// of this file's assembly, which is exactly what an edit must propagate (or be
// cut off from). A leaf with no children emits nothing here.
func (g *gen) writeEdges(b *strings.Builder, n *node) {
	for i, c := range n.children {
		// constName(c.index, 0) is the child's first declaration, always a plain
		// exported int const (writeOneDecl's default arm), so the reference and
		// the fold below are always well-typed.
		fmt.Fprintf(b, "pub const ref%d_%d = %s.%s + %d\n",
			n.index, i, namespaceOf(c), constName(c.index, 0), i)
	}
}

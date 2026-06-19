package lsp

import (
	"net/url"
	"path/filepath"
	"strings"

	protocol "github.com/owenrumney/go-lsp/lsp"

	"github.com/masterbelt/masterbelt/pkg/belt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/belt/semantic"
	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/project"
	"github.com/masterbelt/masterbelt/pkg/source"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// workspace is one analyzed program: the file closure of a project an open
// document belongs to, or a single project-less file. Opening a file of a
// project loads its siblings from disk, so imports resolve in the editor even
// when their files are not open; open buffers override the disk copies.
type workspace struct {
	root  string           // the project root directory, "" when standalone
	proj  *project.Project // nil when standalone
	prog  *semantic.Program
	open  map[semantic.FileID]protocol.DocumentURI
	trees map[semantic.FileID]*treeIndex // positioned-tree cache, one entry per parse
}

// treeIndex is one file's positioned-tree cache: the green root it was built
// over — the parse's identity — and the green-to-positioned-tree map.
type treeIndex struct {
	root  cst.Green
	trees map[cst.Green]cst.Tree
}

// view is one file of a workspace, presenting the surface the feature
// implementations consume, routed through the program so imports resolve
// across files.
type view struct {
	ws  *workspace
	id  semantic.FileID
	uri protocol.DocumentURI
}

func (v view) AST() *abstract.Document { return v.ws.prog.Document(v.id) }

// Trees returns the positioned-tree index of the file's concrete tree,
// cached on the workspace and rebuilt only when an edit produced a new tree
// (the green root's identity is the parse's identity). The read-only
// handlers — hover, definition, references, highlights, hints, actions —
// share one index per parse instead of re-walking the tree per request.
func (v view) Trees() map[cst.Green]cst.Tree {
	root := v.AST().Concrete().Tree()
	if c := v.ws.trees[v.id]; c != nil && c.root == root.Green() {
		return c.trees
	}
	trees := positionedTrees(root)
	if v.ws.trees == nil {
		v.ws.trees = map[semantic.FileID]*treeIndex{}
	}
	v.ws.trees[v.id] = &treeIndex{root: root.Green(), trees: trees}
	return trees
}

func (v view) Buffer() source.Buffer { return v.AST().Buffer() }

func (v view) Module() *ir.Module { return v.ws.prog.Module(v.id) }

func (v view) Resolve(id *ast.Identifier) *ir.Const { return v.ws.prog.Resolve(v.id, id) }

func (v view) ResolveMember(m *ast.MemberExpr) *ir.Const {
	return v.ws.prog.ResolveMember(v.id, m)
}

func (v view) ResolveFunc(id *ast.Identifier) []*ast.FuncDecl {
	return v.ws.prog.ResolveFunc(v.id, id)
}

func (v view) ResolveFuncMember(m *ast.MemberExpr) []*ast.FuncDecl {
	return v.ws.prog.ResolveFuncMember(v.id, m)
}

func (v view) FunctionOf(fd *ast.FuncDecl) *ir.Function { return v.ws.prog.FunctionOf(fd) }

func (v view) FunctionsInScope() []*ir.Function { return v.ws.prog.FunctionsInScope(v.id) }

// viewOfFunc is viewOf for a function declaration.
func (v view) viewOfFunc(fd *ast.FuncDecl) (view, bool) {
	id, ok := v.ws.prog.FileOfFunc(fd)
	if !ok {
		return view{}, false
	}
	if id == v.id {
		return v, true
	}
	return view{ws: v.ws, id: id, uri: v.ws.uriFor(id)}, true
}

func (v view) ResolveUseName(u *ast.UseDecl, name string) *ir.Const {
	return v.ws.prog.ResolveUseName(v.id, u, name)
}

func (v view) ResolveUseType(u *ast.UseDecl, name string) *ir.TypeDef {
	return v.ws.prog.ResolveUseType(v.id, u, name)
}

func (v view) MethodCandidates(recv ir.Type, name string) ([]*ir.Method, map[string]ir.Type, bool) {
	return v.ws.prog.MethodCandidates(recv, name)
}

func (v view) TypeOfExpr(e ast.Expr) ir.Type { return v.ws.prog.TypeOfExpr(v.id, e) }

func (v view) EnumOfAnnotation(t ast.TypeExpr) *ir.TypeDef {
	return v.ws.prog.EnumOfAnnotation(v.id, t)
}

func (v view) EnumOf(t ir.Type) *ir.TypeDef { return v.ws.prog.EnumOf(t) }

func (v view) ReceiverMethods(recv ir.Type) ([]*ir.Method, map[string]ir.Type, bool) {
	return v.ws.prog.ReceiverMethods(recv)
}

func (v view) QueryColumns(recv ir.Type) ([]ir.Field, bool) {
	return v.ws.prog.QueryColumns(recv)
}

func (v view) ResolvedMethodCall(call *ast.CallExpr) (*ir.Method, bool) {
	return v.ws.prog.ResolvedMethodCall(v.id, call)
}

func (v view) AssignTargetReceiverType(member *ast.MemberExpr) (ir.Type, bool) {
	return v.ws.prog.AssignTargetReceiverType(v.id, member)
}

func (v view) FuncLitTypes() map[*ast.FuncLit]*ir.Func { return v.ws.prog.FuncLitTypes(v.id) }

// ExprTypes is the type the checker settled for every expression node it typed
// with a usable value — the typed-value-graph stream, keyed by node. It is the
// editor's read of the checker's own typing, the single source of truth a
// member-access receiver resolves through (a body's master-as-relation, a relation
// chain's result) instead of re-deriving the scope rules. Building it runs the
// checking walks over the file, so a request reads it once and threads the map
// through the receiver resolver rather than rebuilding it per lookup.
func (v view) ExprTypes() map[ast.Expr]ir.Type { return v.ws.prog.ExprTypes(v.id) }

func (v view) Diagnostics() []diagnostic.Diagnostic { return v.ws.prog.Diagnostics(v.id) }

// Lint returns the file's advisory lint diagnostics — the editor surfaces them
// (faded dead code) alongside the analyzer's, but they stay a separate channel.
func (v view) Lint() []diagnostic.Diagnostic { return v.ws.prog.Lint(v.id) }

func (v view) TypeNames() []*ir.TypeDef { return v.ws.prog.TypeNames(v.id) }

func (v view) Constructors() []*ir.TypeDef { return v.ws.prog.Constructors(v.id) }

func (v view) QualifiedTypeNames() map[string][]*ir.TypeDef {
	return v.ws.prog.QualifiedTypeNames(v.id)
}

// viewOf returns the view of the file that declares c — v itself, or a sibling
// of the same workspace. This is how a definition or hover follows a
// cross-file reference home.
func (v view) viewOf(c *ir.Const) (view, bool) {
	id, ok := v.ws.prog.FileOf(c)
	if !ok {
		return view{}, false
	}
	if id == v.id {
		return v, true
	}
	return view{ws: v.ws, id: id, uri: v.ws.uriFor(id)}, true
}

// viewOfType is viewOf for a type definition. The prelude's definitions are
// declared in no workspace file, so they resolve to no view.
func (v view) viewOfType(t *ir.TypeDef) (view, bool) {
	id, ok := v.ws.prog.FileOfType(t)
	if !ok {
		return view{}, false
	}
	if id == v.id {
		return v, true
	}
	return view{ws: v.ws, id: id, uri: v.ws.uriFor(id)}, true
}

// uriFor returns the URI of a workspace file: the open document's URI when the
// editor has it open, otherwise the file's location on disk.
func (ws *workspace) uriFor(id semantic.FileID) protocol.DocumentURI {
	if uri, ok := ws.open[id]; ok {
		return uri
	}
	return pathURI(filepath.Join(ws.root, filepath.FromSlash(string(id))))
}

// pathURI renders an absolute filesystem path as a file:// URI: forward
// slashes, a leading slash even for a Windows drive path (file:///C:/...),
// and percent-escaped segments — the inverse of uriPath.
func pathURI(path string) protocol.DocumentURI {
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	u := url.URL{Scheme: "file", Path: p}
	return protocol.DocumentURI(u.String())
}

// sync mirrors the project's current file set into the program — files the
// project pruned leave it, the rest are (re)pushed — and re-analyzes. A
// standalone workspace has exactly one file, pushed by its caller.
func (ws *workspace) sync() {
	if ws.proj != nil {
		current := map[semantic.FileID]bool{}
		for _, f := range ws.proj.Files() {
			id := semantic.FileID(f.ID)
			current[id] = true
			ws.prog.SetFile(id, f.AST, semantic.UsesOf(f.Uses))
		}
		for _, id := range ws.prog.Files() {
			if !current[id] {
				ws.prog.RemoveFile(id)
				delete(ws.trees, id)
			}
		}
	}
	ws.prog.Refresh()
}

// uriPath converts a file:// URI to a filesystem path. Anything else is
// returned as-is (tests drive the handlers with bare paths).
func uriPath(uri protocol.DocumentURI) string {
	s := strings.TrimPrefix(string(uri), "file://")
	if p, err := url.PathUnescape(s); err == nil {
		s = p
	}
	// A Windows URI carries the drive behind the empty authority
	// (file:///C:/...): drop the slash in front of the drive letter.
	if len(s) >= 3 && s[0] == '/' && s[2] == ':' {
		s = s[1:]
	}
	return s
}

package lsp

import (
	"net/url"
	"path/filepath"
	"strings"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/semantic"
	"github.com/masterbelt/masterbelt/pkg/project"
	"github.com/masterbelt/masterbelt/pkg/source"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
	protocol "github.com/owenrumney/go-lsp/lsp"
)

// workspace is one analyzed program: the file closure of a project an open
// document belongs to, or a single project-less file. Opening a file of a
// project loads its siblings from disk, so imports resolve in the editor even
// when their files are not open; open buffers override the disk copies.
type workspace struct {
	root string           // the project root directory, "" when standalone
	proj *project.Project // nil when standalone
	prog *semantic.Program
	open map[semantic.FileID]protocol.DocumentURI
}

// view is one file of a workspace, presenting the surface the feature
// implementations consume — the same one semantic.Document presents, routed
// through the program so imports resolve across files.
type view struct {
	ws  *workspace
	id  semantic.FileID
	uri protocol.DocumentURI
}

func (v view) AST() *abstract.Document { return v.ws.prog.Document(v.id) }

func (v view) Buffer() source.Buffer { return v.AST().Buffer() }

func (v view) Module() *ir.Module { return v.ws.prog.Module(v.id) }

func (v view) Resolve(id *ast.Identifier) *ir.Const { return v.ws.prog.Resolve(v.id, id) }

func (v view) ResolveMember(m *ast.MemberExpr) *ir.Const {
	return v.ws.prog.ResolveMember(v.id, m)
}

func (v view) FuncLitTypes() map[*ast.FuncLit]*ir.Func { return v.ws.prog.FuncLitTypes(v.id) }

func (v view) Diagnostics() []diagnostic.Diagnostic { return v.ws.prog.Diagnostics(v.id) }

func (v view) TypeNames() []*ir.TypeDef { return v.ws.prog.TypeNames(v.id) }

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

// uriFor returns the URI of a workspace file: the open document's URI when the
// editor has it open, otherwise the file's location on disk.
func (ws *workspace) uriFor(id semantic.FileID) protocol.DocumentURI {
	if uri, ok := ws.open[id]; ok {
		return uri
	}
	path := filepath.Join(ws.root, filepath.FromSlash(string(id)))
	return protocol.DocumentURI("file://" + filepath.ToSlash(path))
}

// sync pushes the project's current file set into the program and re-analyzes.
// A standalone workspace has exactly one file, pushed by its caller.
func (ws *workspace) sync() {
	if ws.proj != nil {
		for _, f := range ws.proj.Files() {
			uses := make(map[*ast.UseDecl]semantic.FileID, len(f.Uses))
			for u, target := range f.Uses {
				uses[u] = semantic.FileID(target)
			}
			ws.prog.SetFile(semantic.FileID(f.ID), f.AST, uses)
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
	return s
}

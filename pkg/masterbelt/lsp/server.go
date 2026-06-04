// Package lsp implements a Language Server Protocol server for masterbelt.
//
// It is a thin adapter: the language work — lexing, parsing, lowering, name
// resolution, and type inference — all lives in the incremental pipeline under
// parser/, source/, and semantic/, and this package only translates LSP requests
// into that pipeline and its results back into LSP types. The translation hinges
// on source.Buffer's UTF-16 support (see convert.go), which is exactly LSP's
// position model.
//
// An opened file is analyzed in its project: the server finds masterbelt.toml
// above it, loads the import closure from disk (open buffers override the disk
// copies), and keeps one incremental semantic.Program per project — so use
// imports resolve in the editor, an edit re-analyzes only what it touched, and
// a change in one file updates its importers' diagnostics. A file outside any
// project analyzes standalone. (workspace.go holds that machinery.)
//
// Implemented features: lifecycle, incremental text sync, push diagnostics
// (lexer, parser, and semantic), completion, document symbols, formatting,
// semantic-token highlighting, hover, go-to-definition (across files),
// find-references, document highlight, inlay type hints, code actions, and
// rename. The protocol plumbing (JSON-RPC over stdio, request routing) is
// provided by github.com/owenrumney/go-lsp.
package lsp

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/semantic"
	"github.com/masterbelt/masterbelt/pkg/project"
	"github.com/masterbelt/masterbelt/pkg/source"
	protocol "github.com/owenrumney/go-lsp/lsp"
	"github.com/owenrumney/go-lsp/server"
)

const serverName = "masterbelt"

// Server is the masterbelt language server. It satisfies go-lsp's
// LifecycleHandler plus the optional handler interfaces for the features it
// implements; unimplemented requests are declined by the library.
type Server struct {
	mu     sync.Mutex
	open   map[protocol.DocumentURI]view
	roots  map[string]*workspace // project workspaces, by root directory
	client *server.Client
}

// NewServer creates a language server with no open documents.
func NewServer() *Server {
	return &Server{
		open:  map[protocol.DocumentURI]view{},
		roots: map[string]*workspace{},
	}
}

// Initialize advertises the server's capabilities.
func (s *Server) Initialize(_ context.Context, _ *protocol.InitializeParams) (*protocol.InitializeResult, error) {
	return &protocol.InitializeResult{
		Capabilities: protocol.ServerCapabilities{
			TextDocumentSync: &protocol.TextDocumentSyncOptions{
				OpenClose: new(true),
				Change:    protocol.SyncIncremental,
			},
			CompletionProvider:         &protocol.CompletionOptions{},
			DocumentSymbolProvider:     new(true),
			DocumentFormattingProvider: new(true),
			DocumentHighlightProvider:  new(true),
			HoverProvider:              new(true),
			DefinitionProvider:         new(true),
			ReferencesProvider:         new(true),
			RenameProvider:             &protocol.RenameOptions{PrepareProvider: new(true)},
			InlayHintProvider:          &protocol.InlayHintOptions{},
			CodeActionProvider:         &protocol.CodeActionOptions{},
			SemanticTokensProvider: &protocol.SemanticTokensOptions{
				Legend: semanticLegend,
				Full:   &protocol.SemanticTokensFull{},
			},
		},
		ServerInfo: &protocol.ServerInfo{Name: serverName, Version: "0.1.0"},
	}, nil
}

// Shutdown does nothing: the server holds only in-memory state.
func (s *Server) Shutdown(_ context.Context) error { return nil }

// SetClient receives the channel for server→client notifications (used to push
// diagnostics).
func (s *Server) SetClient(client *server.Client) { s.client = client }

// DidOpen starts tracking a document — in its project's workspace when it has
// one — and publishes diagnostics for the workspace's open files.
func (s *Server) DidOpen(ctx context.Context, params *protocol.DidOpenTextDocumentParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	v := s.openFile(ctx, params.TextDocument.URI, []byte(params.TextDocument.Text))
	s.publishWorkspace(ctx, v.ws)
	return nil
}

// openFile places a document in a workspace: the project found at or above it
// (siblings load from disk; the editor's text overrides the disk copy), or a
// standalone single-file workspace.
func (s *Server) openFile(ctx context.Context, uri protocol.DocumentURI, text []byte) view {
	path := uriPath(uri)

	if root, ok := project.FindRoot(filepath.Dir(path)); ok {
		ws := s.roots[root]
		if ws == nil {
			proj, diags := project.Open(root)
			if !diags.HasErrors() {
				ws = &workspace{
					root: root,
					proj: proj,
					prog: semantic.NewProgram(),
					open: map[semantic.FileID]protocol.DocumentURI{},
				}
				s.roots[root] = ws
			} else if s.client != nil {
				// The manifest fails to load, so the file analyzes standalone
				// and its imports will not resolve. Say why — otherwise the
				// user sees only mystery use_not_found diagnostics.
				for _, d := range diags.Items() {
					_ = s.client.LogMessage(ctx, &protocol.LogMessageParams{
						Type:    protocol.MessageTypeWarning,
						Message: serverName + ": project at " + root + " not loaded: " + d.String(),
					})
				}
			}
		}
		if ws != nil {
			if rel, err := filepath.Rel(root, path); err == nil {
				id := project.FileID(filepath.ToSlash(rel))
				if f := ws.proj.Include(id); f != nil {
					// The editor's text wins over the disk copy.
					buf := f.AST.Buffer()
					f.AST.Edit(source.Edit{Start: 0, End: buf.Len(), NewText: text})
					ws.proj.Resync(id)
					ws.sync()

					v := view{ws: ws, id: semantic.FileID(id), uri: uri}
					ws.open[v.id] = uri
					s.open[uri] = v
					return v
				}
			}
		}
	}

	// No project (or the file is unreachable in it): analyze standalone.
	ws := &workspace{
		prog: semantic.NewProgram(),
		open: map[semantic.FileID]protocol.DocumentURI{},
	}
	id := semantic.FileID(path)
	ws.prog.SetFile(id, abstract.NewDocument(text), nil)
	ws.prog.Refresh()
	ws.open[id] = uri

	v := view{ws: ws, id: id, uri: uri}
	s.open[uri] = v
	return v
}

// DidChange applies the edits to the document incrementally, re-resolves its
// imports, and republishes diagnostics for the workspace's open files (a
// change here may surface in an importer). Range-based changes drive the
// incremental pipeline; a change without a range replaces the whole text.
func (s *Server) DidChange(ctx context.Context, params *protocol.DidChangeTextDocumentParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	v, ok := s.open[params.TextDocument.URI]
	if !ok {
		return nil
	}

	doc := v.AST()
	for _, change := range params.ContentChanges {
		buf := doc.Buffer()
		start, end := 0, buf.Len()
		if change.Range != nil {
			// Each change's range refers to the document state left by the
			// previous changes in this batch.
			start = fromPosition(buf, change.Range.Start)
			end = fromPosition(buf, change.Range.End)
		}
		doc.Edit(source.Edit{Start: start, End: end, NewText: []byte(change.Text)})
	}

	if v.ws.proj != nil {
		v.ws.proj.Resync(project.FileID(v.id))
		v.ws.sync()
	} else {
		v.ws.prog.SetFile(v.id, doc, nil)
		v.ws.prog.Refresh()
	}
	s.publishWorkspace(ctx, v.ws)
	return nil
}

// DidClose stops tracking a document and clears its diagnostics. A project
// workspace is dropped with its last open file; while siblings stay open, the
// closed file reverts to its on-disk content — the buffer's unsaved edits die
// with it, and the remaining importers' diagnostics must stop reflecting them.
func (s *Server) DidClose(ctx context.Context, params *protocol.DidCloseTextDocumentParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	uri := params.TextDocument.URI
	if v, ok := s.open[uri]; ok {
		delete(v.ws.open, v.id)
		if v.ws.root != "" && len(v.ws.open) == 0 {
			delete(s.roots, v.ws.root)
		} else if v.ws.proj != nil {
			id := project.FileID(v.id)
			revertToDisk(v)
			v.ws.proj.Resync(id)  // rewire the reverted file's uses
			v.ws.proj.Release(id) // unpin it: closed files stay only while imported
			v.ws.sync()
			s.publishWorkspace(ctx, v.ws)
		}
	}
	delete(s.open, uri)
	if s.client != nil {
		_ = s.client.PublishDiagnostics(ctx, &protocol.PublishDiagnosticsParams{
			URI:         uri,
			Diagnostics: []protocol.Diagnostic{},
		})
	}
	return nil
}

// revertToDisk replaces a closed project file's text with its on-disk content,
// so the workspace's remaining files see the file as it is, not as the
// abandoned buffer left it. A file that cannot be read (deleted while open)
// keeps its last text — the import closure may still reference it.
func revertToDisk(v view) {
	f := v.ws.proj.File(project.FileID(v.id))
	if f == nil {
		return
	}
	data, err := os.ReadFile(f.Path)
	if err != nil {
		return
	}
	buf := f.AST.Buffer()
	if bytes.Equal(buf.Slice(0, buf.Len()), data) {
		return // never edited (or saved): nothing to revert
	}
	f.AST.Edit(source.Edit{Start: 0, End: buf.Len(), NewText: data})
}

// Completion returns the value-namespace candidates at the cursor.
func (s *Server) Completion(_ context.Context, params *protocol.CompletionParams) (*protocol.CompletionList, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	v, ok := s.open[params.TextDocument.URI]
	if !ok {
		return nil, nil
	}
	return completion(v, fromPosition(v.Buffer(), params.Position)), nil
}

// DocumentSymbol returns the document's outline.
func (s *Server) DocumentSymbol(_ context.Context, params *protocol.DocumentSymbolParams) ([]protocol.DocumentSymbol, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	v, ok := s.open[params.TextDocument.URI]
	if !ok {
		return nil, nil
	}
	return documentSymbols(v), nil
}

// SemanticTokensFull returns the syntax-highlighting tokens for the whole
// document, classified from the concrete tree.
func (s *Server) SemanticTokensFull(_ context.Context, params *protocol.SemanticTokensParams) (*protocol.SemanticTokens, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	v, ok := s.open[params.TextDocument.URI]
	if !ok {
		return nil, nil
	}
	return semanticTokensIn(v), nil
}

// Formatting returns the edits to format the whole document.
func (s *Server) Formatting(_ context.Context, params *protocol.DocumentFormattingParams) ([]protocol.TextEdit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	v, ok := s.open[params.TextDocument.URI]
	if !ok {
		return nil, nil
	}
	return formatEdits(v.AST()), nil
}

// Hover returns documentation and type information for the symbol under the
// cursor.
func (s *Server) Hover(_ context.Context, params *protocol.HoverParams) (*protocol.Hover, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	v, ok := s.open[params.TextDocument.URI]
	if !ok {
		return nil, nil
	}
	return hover(v, fromPosition(v.Buffer(), params.Position)), nil
}

// Definition resolves the reference under the cursor to its declaration —
// possibly in another file of the project.
func (s *Server) Definition(_ context.Context, params *protocol.DefinitionParams) ([]protocol.Location, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	v, ok := s.open[params.TextDocument.URI]
	if !ok {
		return nil, nil
	}
	return definition(v, fromPosition(v.Buffer(), params.Position)), nil
}

// DocumentHighlight highlights every occurrence of the symbol under the cursor.
func (s *Server) DocumentHighlight(_ context.Context, params *protocol.DocumentHighlightParams) ([]protocol.DocumentHighlight, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	v, ok := s.open[params.TextDocument.URI]
	if !ok {
		return nil, nil
	}
	return documentHighlights(v, fromPosition(v.Buffer(), params.Position)), nil
}

// InlayHint returns the inferred-type hints for un-annotated constants in range.
func (s *Server) InlayHint(_ context.Context, params *protocol.InlayHintParams) ([]protocol.InlayHint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	v, ok := s.open[params.TextDocument.URI]
	if !ok {
		return nil, nil
	}
	buf := v.Buffer()
	return inlayHints(v, fromPosition(buf, params.Range.Start), fromPosition(buf, params.Range.End)), nil
}

// CodeAction returns the refactorings available for the requested range.
func (s *Server) CodeAction(_ context.Context, params *protocol.CodeActionParams) ([]protocol.CodeAction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	v, ok := s.open[params.TextDocument.URI]
	if !ok {
		return nil, nil
	}
	buf := v.Buffer()
	return codeActions(v, fromPosition(buf, params.Range.Start), fromPosition(buf, params.Range.End)), nil
}

// References returns every reference to the symbol under the cursor.
func (s *Server) References(_ context.Context, params *protocol.ReferenceParams) ([]protocol.Location, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	v, ok := s.open[params.TextDocument.URI]
	if !ok {
		return nil, nil
	}
	offset := fromPosition(v.Buffer(), params.Position)
	return references(v, offset, params.Context.IncludeDeclaration), nil
}

// PrepareRename reports whether (and where) the symbol under the cursor can be
// renamed.
func (s *Server) PrepareRename(_ context.Context, params *protocol.PrepareRenameParams) (*protocol.PrepareRenameResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	v, ok := s.open[params.TextDocument.URI]
	if !ok {
		return nil, nil
	}
	return prepareRename(v, fromPosition(v.Buffer(), params.Position)), nil
}

// Rename renames the symbol under the cursor — its declaration and every
// reference — to params.NewName.
func (s *Server) Rename(_ context.Context, params *protocol.RenameParams) (*protocol.WorkspaceEdit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	v, ok := s.open[params.TextDocument.URI]
	if !ok {
		return nil, nil
	}
	offset := fromPosition(v.Buffer(), params.Position)
	return rename(v, offset, params.NewName), nil
}

// publishWorkspace sends current diagnostics for every open file of the
// workspace — a change in one file may add or clear diagnostics in an
// importer. It is a no-op when no client is attached (as in unit tests that
// drive the handlers directly).
func (s *Server) publishWorkspace(ctx context.Context, ws *workspace) {
	if s.client == nil {
		return
	}
	for id, uri := range ws.open {
		_ = s.client.PublishDiagnostics(ctx, &protocol.PublishDiagnosticsParams{
			URI:         uri,
			Diagnostics: toDiagnostics(view{ws: ws, id: id, uri: uri}),
		})
	}
}

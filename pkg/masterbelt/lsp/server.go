// Package lsp implements a Language Server Protocol server for masterbelt.
//
// It is a thin adapter: the language work — lexing, parsing, lowering, name
// resolution, and type inference — all lives in the incremental pipeline under
// parser/, source/, and semantic/, and this package only translates LSP requests
// into that pipeline and its results back into LSP types. The translation hinges
// on source.Buffer's UTF-16 support (see convert.go), which is exactly LSP's
// position model.
//
// The server keeps one incremental semantic.Document per open file. A didChange
// with a range becomes a source.Edit, so a keystroke re-lexes, re-parses,
// re-lowers, and re-analyzes only what it touched.
//
// Implemented features: lifecycle, incremental text sync, push diagnostics
// (lexer, parser, and semantic), completion, document symbols, formatting,
// semantic-token highlighting, hover, go-to-definition, find-references,
// document highlight, inlay type hints, code actions, and rename. The protocol
// plumbing (JSON-RPC over stdio, request routing) is provided by
// github.com/owenrumney/go-lsp.
package lsp

import (
	"context"
	"sync"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/semantic"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source"
	protocol "github.com/owenrumney/go-lsp/lsp"
	"github.com/owenrumney/go-lsp/server"
)

const serverName = "masterbelt"

// Server is the masterbelt language server. It satisfies go-lsp's
// LifecycleHandler plus the optional handler interfaces for the features it
// implements; unimplemented requests are declined by the library.
type Server struct {
	mu     sync.Mutex
	docs   map[protocol.DocumentURI]*semantic.Document
	client *server.Client
}

// NewServer creates a language server with no open documents.
func NewServer() *Server {
	return &Server{docs: map[protocol.DocumentURI]*semantic.Document{}}
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

// DidOpen starts tracking a document and publishes its initial diagnostics.
func (s *Server) DidOpen(ctx context.Context, params *protocol.DidOpenTextDocumentParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	uri := params.TextDocument.URI
	doc := semantic.NewDocument([]byte(params.TextDocument.Text))
	s.docs[uri] = doc
	s.publish(ctx, uri, doc)
	return nil
}

// DidChange applies the edits to the document incrementally and republishes its
// diagnostics. Range-based changes drive the incremental pipeline; a change
// without a range (a whole-document replacement) re-analyzes from scratch.
func (s *Server) DidChange(ctx context.Context, params *protocol.DidChangeTextDocumentParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	uri := params.TextDocument.URI
	doc := s.docs[uri]
	if doc == nil {
		return nil
	}

	for _, change := range params.ContentChanges {
		if change.Range == nil {
			doc = semantic.NewDocument([]byte(change.Text))
			continue
		}
		// Each change's range refers to the document state left by the previous
		// changes in this batch, so resolve offsets against the current buffer.
		buf := doc.Buffer()
		start := fromPosition(buf, change.Range.Start)
		end := fromPosition(buf, change.Range.End)
		doc.Edit(source.Edit{Start: start, End: end, NewText: []byte(change.Text)})
	}

	s.docs[uri] = doc
	s.publish(ctx, uri, doc)
	return nil
}

// DidClose stops tracking a document and clears its diagnostics.
func (s *Server) DidClose(ctx context.Context, params *protocol.DidCloseTextDocumentParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	uri := params.TextDocument.URI
	delete(s.docs, uri)
	if s.client != nil {
		_ = s.client.PublishDiagnostics(ctx, &protocol.PublishDiagnosticsParams{
			URI:         uri,
			Diagnostics: []protocol.Diagnostic{},
		})
	}
	return nil
}

// Completion returns the value-namespace candidates at the cursor.
func (s *Server) Completion(_ context.Context, params *protocol.CompletionParams) (*protocol.CompletionList, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	doc := s.docs[params.TextDocument.URI]
	if doc == nil {
		return nil, nil
	}
	return completion(doc, fromPosition(doc.Buffer(), params.Position)), nil
}

// DocumentSymbol returns the document's outline.
func (s *Server) DocumentSymbol(_ context.Context, params *protocol.DocumentSymbolParams) ([]protocol.DocumentSymbol, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	doc := s.docs[params.TextDocument.URI]
	if doc == nil {
		return nil, nil
	}
	return documentSymbols(doc), nil
}

// SemanticTokensFull returns the syntax-highlighting tokens for the whole
// document, classified from the concrete tree.
func (s *Server) SemanticTokensFull(_ context.Context, params *protocol.SemanticTokensParams) (*protocol.SemanticTokens, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	doc := s.docs[params.TextDocument.URI]
	if doc == nil {
		return nil, nil
	}
	return semanticTokens(doc.AST()), nil
}

// Formatting returns the edits to format the whole document.
func (s *Server) Formatting(_ context.Context, params *protocol.DocumentFormattingParams) ([]protocol.TextEdit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	doc := s.docs[params.TextDocument.URI]
	if doc == nil {
		return nil, nil
	}
	return formatEdits(doc.AST()), nil
}

// Hover returns documentation and type information for the symbol under the
// cursor.
func (s *Server) Hover(_ context.Context, params *protocol.HoverParams) (*protocol.Hover, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	doc := s.docs[params.TextDocument.URI]
	if doc == nil {
		return nil, nil
	}
	return hover(doc, fromPosition(doc.Buffer(), params.Position)), nil
}

// Definition resolves the reference under the cursor to its declaration.
func (s *Server) Definition(_ context.Context, params *protocol.DefinitionParams) ([]protocol.Location, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	doc := s.docs[params.TextDocument.URI]
	if doc == nil {
		return nil, nil
	}
	return definition(doc, fromPosition(doc.Buffer(), params.Position), params.TextDocument.URI), nil
}

// DocumentHighlight highlights every occurrence of the symbol under the cursor.
func (s *Server) DocumentHighlight(_ context.Context, params *protocol.DocumentHighlightParams) ([]protocol.DocumentHighlight, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	doc := s.docs[params.TextDocument.URI]
	if doc == nil {
		return nil, nil
	}
	return documentHighlights(doc, fromPosition(doc.Buffer(), params.Position)), nil
}

// InlayHint returns the inferred-type hints for un-annotated constants in range.
func (s *Server) InlayHint(_ context.Context, params *protocol.InlayHintParams) ([]protocol.InlayHint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	doc := s.docs[params.TextDocument.URI]
	if doc == nil {
		return nil, nil
	}
	buf := doc.Buffer()
	return inlayHints(doc, fromPosition(buf, params.Range.Start), fromPosition(buf, params.Range.End)), nil
}

// CodeAction returns the refactorings available for the requested range.
func (s *Server) CodeAction(_ context.Context, params *protocol.CodeActionParams) ([]protocol.CodeAction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	doc := s.docs[params.TextDocument.URI]
	if doc == nil {
		return nil, nil
	}
	buf := doc.Buffer()
	return codeActions(doc, fromPosition(buf, params.Range.Start), fromPosition(buf, params.Range.End), params.TextDocument.URI), nil
}

// References returns every reference to the symbol under the cursor.
func (s *Server) References(_ context.Context, params *protocol.ReferenceParams) ([]protocol.Location, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	doc := s.docs[params.TextDocument.URI]
	if doc == nil {
		return nil, nil
	}
	offset := fromPosition(doc.Buffer(), params.Position)
	return references(doc, offset, params.TextDocument.URI, params.Context.IncludeDeclaration), nil
}

// PrepareRename reports whether (and where) the symbol under the cursor can be
// renamed.
func (s *Server) PrepareRename(_ context.Context, params *protocol.PrepareRenameParams) (*protocol.PrepareRenameResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	doc := s.docs[params.TextDocument.URI]
	if doc == nil {
		return nil, nil
	}
	return prepareRename(doc, fromPosition(doc.Buffer(), params.Position)), nil
}

// Rename renames the symbol under the cursor — its declaration and every
// reference — to params.NewName.
func (s *Server) Rename(_ context.Context, params *protocol.RenameParams) (*protocol.WorkspaceEdit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	doc := s.docs[params.TextDocument.URI]
	if doc == nil {
		return nil, nil
	}
	offset := fromPosition(doc.Buffer(), params.Position)
	return rename(doc, offset, params.NewName, params.TextDocument.URI), nil
}

// publish sends the document's current diagnostics to the client. It is a no-op
// when no client is attached (as in unit tests that drive the handler directly).
func (s *Server) publish(ctx context.Context, uri protocol.DocumentURI, doc *semantic.Document) {
	if s.client == nil {
		return
	}
	_ = s.client.PublishDiagnostics(ctx, &protocol.PublishDiagnosticsParams{
		URI:         uri,
		Diagnostics: toDiagnostics(doc),
	})
}

// Package lsp implements a Language Server Protocol server for masterbelt.
//
// It is a thin adapter: the language work — lexing, parsing, lowering, and
// diagnostics — all lives in the incremental pipeline under parser/ and source/,
// and this package only translates LSP requests into that pipeline and its
// results back into LSP types. The translation hinges on source.Buffer's UTF-16
// support (see convert.go), which is exactly LSP's position model.
//
// The server keeps one incremental abstract.Document per open file. A
// didChange with a range becomes a source.Edit, so a keystroke re-lexes,
// re-parses, and re-lowers only the touched declaration rather than the whole
// file — the property the whole pipeline was built for.
//
// Implemented features: lifecycle, incremental text sync, push diagnostics,
// document symbols (outline), and minimal formatting. The protocol plumbing
// (JSON-RPC over stdio, request routing) is provided by github.com/owenrumney/
// go-lsp; this package implements only the handler interfaces it needs.
package lsp

import (
	"context"
	"sync"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source"
	protocol "github.com/owenrumney/go-lsp/lsp"
	"github.com/owenrumney/go-lsp/server"
)

const serverName = "masterbelt"

// Server is the masterbelt language server. It satisfies go-lsp's
// LifecycleHandler plus the optional sync, symbol, and formatting handler
// interfaces; unimplemented requests are declined by the library.
type Server struct {
	mu     sync.Mutex
	docs   map[protocol.DocumentURI]*abstract.Document
	client *server.Client
}

// NewServer creates a language server with no open documents.
func NewServer() *Server {
	return &Server{docs: map[protocol.DocumentURI]*abstract.Document{}}
}

// Initialize advertises the server's capabilities.
func (s *Server) Initialize(_ context.Context, _ *protocol.InitializeParams) (*protocol.InitializeResult, error) {
	return &protocol.InitializeResult{
		Capabilities: protocol.ServerCapabilities{
			TextDocumentSync: &protocol.TextDocumentSyncOptions{
				OpenClose: new(true),
				Change:    protocol.SyncIncremental,
			},
			DocumentSymbolProvider:     new(true),
			DocumentFormattingProvider: new(true),
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
	doc := abstract.NewDocument([]byte(params.TextDocument.Text))
	s.docs[uri] = doc
	s.publish(ctx, uri, doc)
	return nil
}

// DidChange applies the edits to the document incrementally and republishes its
// diagnostics. Range-based changes drive the incremental pipeline; a change
// without a range (a whole-document replacement) reparses from scratch.
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
			doc = abstract.NewDocument([]byte(change.Text))
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

// Formatting returns the edits to format the whole document.
func (s *Server) Formatting(_ context.Context, params *protocol.DocumentFormattingParams) ([]protocol.TextEdit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	doc := s.docs[params.TextDocument.URI]
	if doc == nil {
		return nil, nil
	}
	return formatEdits(doc), nil
}

// publish sends the document's current diagnostics to the client. It is a no-op
// when no client is attached (as in unit tests that drive the handler directly).
func (s *Server) publish(ctx context.Context, uri protocol.DocumentURI, doc *abstract.Document) {
	if s.client == nil {
		return
	}
	_ = s.client.PublishDiagnostics(ctx, &protocol.PublishDiagnosticsParams{
		URI:         uri,
		Diagnostics: toDiagnostics(doc),
	})
}

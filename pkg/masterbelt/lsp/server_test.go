package lsp

import (
	"context"
	"testing"
	"time"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ast"
	protocol "github.com/owenrumney/go-lsp/lsp"
	"github.com/owenrumney/go-lsp/servertest"
)

// TestIncrementalDidChange drives the range-based change path directly: an LSP
// range becomes a source.Edit, which the incremental pipeline applies. The
// servertest harness only sends whole-document changes, so this is the white-box
// test that exercises the incremental path the server was built for.
func TestIncrementalDidChange(t *testing.T) {
	s := NewServer()
	ctx := context.Background()
	uri := protocol.DocumentURI("file:///x.belt")

	if err := s.DidOpen(ctx, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: uri, Text: "const x = 1\n"},
	}); err != nil {
		t.Fatal(err)
	}

	// Replace "1" (line 0, characters 10..11) with "42".
	err := s.DidChange(ctx, &protocol.DidChangeTextDocumentParams{
		TextDocument: protocol.VersionedTextDocumentIdentifier{
			TextDocumentIdentifier: protocol.TextDocumentIdentifier{URI: uri},
			Version:                2,
		},
		ContentChanges: []protocol.TextDocumentContentChangeEvent{{
			Range: &protocol.Range{
				Start: protocol.Position{Line: 0, Character: 10},
				End:   protocol.Position{Line: 0, Character: 11},
			},
			Text: "42",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	doc := s.docs[uri]
	if doc == nil {
		t.Fatal("document is no longer tracked")
	}
	if got := string(doc.Buffer().Slice(0, doc.Buffer().Len())); got != "const x = 42\n" {
		t.Fatalf("document text = %q, want %q", got, "const x = 42\n")
	}
	lit, ok := doc.File().Decls[0].Value.(*ast.IntLit)
	if !ok || lit.Text != "42" {
		t.Fatalf("decl value = %+v, want IntLit 42", doc.File().Decls[0].Value)
	}
}

// TestServerEndToEnd drives the server over the in-process JSON-RPC harness,
// covering the full request/response and notification path: open, diagnostics,
// symbols, formatting, and a (whole-document) change.
func TestServerEndToEnd(t *testing.T) {
	h := servertest.New(t, NewServer())
	uri := protocol.DocumentURI("file:///example.belt")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	const src = "const MaxLevel: int64 = 100\nconst Min = 0\n"
	if err := h.DidOpen(uri, "masterbelt", src); err != nil {
		t.Fatal(err)
	}

	// A valid document publishes an empty diagnostic set.
	diags, err := h.WaitForDiagnostics(ctx, uri)
	if err != nil {
		t.Fatal(err)
	}
	if len(diags) != 0 {
		t.Fatalf("valid document should have no diagnostics, got %+v", diags)
	}

	// Outline.
	syms, err := h.DocumentSymbol(uri)
	if err != nil {
		t.Fatal(err)
	}
	if len(syms) != 2 || syms[0].Name != "MaxLevel" || syms[1].Name != "Min" {
		t.Fatalf("document symbols = %+v", syms)
	}

	// Already-formatted document needs no edits.
	if edits, err := h.Formatting(uri); err != nil {
		t.Fatal(err)
	} else if len(edits) != 0 {
		t.Fatalf("formatted document should need no edits, got %+v", edits)
	}

	// Change to a document with a parse error and trailing whitespace.
	h.ClearDiagnostics()
	if err := h.DidChange(uri, 2, "const = 1  \n"); err != nil {
		t.Fatal(err)
	}
	diags, err = h.WaitForDiagnostics(ctx, uri)
	if err != nil {
		t.Fatal(err)
	}
	if len(diags) == 0 || diags[0].Source != "masterbelt" {
		t.Fatalf("expected a masterbelt diagnostic, got %+v", diags)
	}

	// Formatting still works (and is independent of the parse error).
	edits, err := h.Formatting(uri)
	if err != nil {
		t.Fatal(err)
	}
	if len(edits) != 1 || edits[0].NewText != "const = 1\n" {
		t.Fatalf("formatting edits = %+v", edits)
	}
}

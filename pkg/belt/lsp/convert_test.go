package lsp

import (
	"testing"

	protocol "github.com/owenrumney/go-lsp/lsp"

	"github.com/masterbelt/masterbelt/pkg/belt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/source"
)

func TestPositionUTF16RoundTrip(t *testing.T) {
	// "あ" is three UTF-8 bytes but one UTF-16 code unit, so byte offsets and LSP
	// character columns diverge — exactly what the conversion must get right.
	buf := source.NewFile("", []byte("あx\ny\n"))
	cases := []struct {
		offset     int
		line, char int
	}{
		{0, 0, 0}, // start
		{3, 0, 1}, // after あ
		{4, 0, 2}, // after x
		{5, 1, 0}, // start of line 1
		{6, 1, 1}, // after y
	}
	for _, c := range cases {
		got := toPosition(buf, c.offset)
		if got.Line != c.line || got.Character != c.char {
			t.Errorf("toPosition(%d) = {%d,%d}, want {%d,%d}", c.offset, got.Line, got.Character, c.line, c.char)
		}
		if back := fromPosition(buf, protocol.Position{Line: c.line, Character: c.char}); back != c.offset {
			t.Errorf("fromPosition({%d,%d}) = %d, want %d", c.line, c.char, back, c.offset)
		}
	}
}

func TestToDiagnostics(t *testing.T) {
	doc := testView("const = 1\n")
	diags := toDiagnostics(doc)
	if len(diags) != 1 {
		t.Fatalf("got %d diagnostics, want 1: %+v", len(diags), diags)
	}
	d := diags[0]
	if d.Source != "masterbelt" {
		t.Errorf("Source = %q, want masterbelt", d.Source)
	}
	if d.Severity == nil || *d.Severity != protocol.SeverityError {
		t.Errorf("Severity = %v, want Error", d.Severity)
	}
	if d.Message != "expected identifier" {
		t.Errorf("Message = %q, want %q", d.Message, "expected identifier")
	}
	if got, want := string(d.Code), `"belt.parser.concrete.expected_identifier"`; got != want {
		t.Errorf("Code = %s, want %s", got, want)
	}
	want := protocol.Range{Start: protocol.Position{Line: 0, Character: 0}, End: protocol.Position{Line: 0, Character: 0}}
	if d.Range != want {
		t.Errorf("Range = %+v, want %+v", d.Range, want)
	}
}

func TestToDiagnosticsEmptyIsNonNil(t *testing.T) {
	diags := toDiagnostics(testView("const X = 1\n"))
	if diags == nil {
		t.Fatal("toDiagnostics returned nil; want an empty (clearing) slice")
	}
	if len(diags) != 0 {
		t.Errorf("got %d diagnostics, want 0", len(diags))
	}
}

func TestDocumentSymbols(t *testing.T) {
	// The assert contributes no symbol: it has no name to outline.
	doc := testView("const MaxLevel: long = 100\nconst Min = 0\nassert MaxLevel > Min\n")
	syms := documentSymbols(doc)
	if len(syms) != 2 {
		t.Fatalf("got %d symbols, want 2", len(syms))
	}

	if syms[0].Name != "MaxLevel" || syms[0].Kind != protocol.SymbolKindConstant {
		t.Errorf("symbol 0 = %+v", syms[0])
	}
	if syms[0].Detail != ": long  ·  belt:test/MaxLevel" {
		t.Errorf("symbol 0 detail = %q, want the type and anchor", syms[0].Detail)
	}
	// SelectionRange must cover just the name "MaxLevel" (columns 6..14).
	sel := syms[0].SelectionRange
	if sel.Start.Line != 0 || sel.Start.Character != 6 || sel.End.Character != 14 {
		t.Errorf("symbol 0 selection range = %+v, want cols 6..14 on line 0", sel)
	}

	if syms[1].Name != "Min" || syms[1].Detail != ": nint  ·  belt:test/Min" {
		t.Errorf("symbol 1 = %+v, want Min: nint with anchor", syms[1])
	}
}

func TestFormatEdits(t *testing.T) {
	t.Run("trims trailing space and normalises final newline", func(t *testing.T) {
		doc := abstract.NewDocument([]byte("const x = 1   \n\n\n"))
		edits := formatEdits(doc)
		if len(edits) != 1 {
			t.Fatalf("got %d edits, want 1", len(edits))
		}
		if edits[0].NewText != "const x = 1\n" {
			t.Errorf("NewText = %q, want %q", edits[0].NewText, "const x = 1\n")
		}
	})

	t.Run("no edits when already formatted", func(t *testing.T) {
		doc := abstract.NewDocument([]byte("const x = 1\n"))
		if edits := formatEdits(doc); edits != nil {
			t.Errorf("got %+v, want nil", edits)
		}
	})
}

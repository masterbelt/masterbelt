package lsp

import (
	"os"
	"path/filepath"
	"testing"

	protocol "github.com/owenrumney/go-lsp/lsp"

	"github.com/masterbelt/masterbelt/pkg/belt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/source"
	"github.com/masterbelt/masterbelt/pkg/source/formatter"
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

func TestToTags(t *testing.T) {
	// The diagnostic tag scale maps onto the protocol's, and an untagged
	// diagnostic carries no tags field (nil, not an empty slice).
	if got := toTags(nil); got != nil {
		t.Errorf("toTags(nil) = %v, want nil", got)
	}
	got := toTags([]diagnostic.Tag{diagnostic.TagUnnecessary, diagnostic.TagDeprecated})
	want := []protocol.DiagnosticTag{protocol.TagUnnecessary, protocol.TagDeprecated}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("toTags = %v, want %v", got, want)
	}
}

func TestToDiagnosticsEmptyIsNonNil(t *testing.T) {
	diags := toDiagnostics(testView("pub const X = 1\n"))
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

func TestDocumentSymbolsMaster(t *testing.T) {
	// A master outlines as a single struct-kinded symbol, read from the IR like
	// every other declaration (master/0002 decision 5), so its detail carries the
	// anchor the way a const's does. Its SelectionRange covers just the name.
	doc := testView("pub master Skill {\n  record {\n    id: int\n  }\n  primary id\n}\n")
	syms := documentSymbols(doc)
	if len(syms) != 1 {
		t.Fatalf("got %d symbols, want 1: %+v", len(syms), syms)
	}
	if syms[0].Name != "Skill" || syms[0].Kind != protocol.SymbolKindStruct {
		t.Errorf("symbol 0 = %+v, want Skill as a struct", syms[0])
	}
	if syms[0].Detail != "belt:test/Skill" {
		t.Errorf("symbol 0 detail = %q, want the anchor", syms[0].Detail)
	}
	// SelectionRange must cover just the name "Skill" (line 0, cols 11..16).
	sel := syms[0].SelectionRange
	if sel.Start.Line != 0 || sel.Start.Character != 11 || sel.End.Character != 16 {
		t.Errorf("symbol 0 selection range = %+v, want cols 11..16 on line 0", sel)
	}
}

func TestFormatEdits(t *testing.T) {
	t.Run("trims trailing space and normalises final newline", func(t *testing.T) {
		doc := abstract.NewDocument([]byte("const x = 1   \n\n\n"))
		edits := formatEdits(doc, formatter.DefaultLayout)
		if len(edits) != 1 {
			t.Fatalf("got %d edits, want 1", len(edits))
		}
		if edits[0].NewText != "const x = 1\n" {
			t.Errorf("NewText = %q, want %q", edits[0].NewText, "const x = 1\n")
		}
	})

	t.Run("no edits when already formatted", func(t *testing.T) {
		doc := abstract.NewDocument([]byte("const x = 1\n"))
		if edits := formatEdits(doc, formatter.DefaultLayout); edits != nil {
			t.Errorf("got %+v, want nil", edits)
		}
	})
}

// TestFormatLayout pins the LSP's Layout precedence: a project .editorconfig
// outranks the editor's FormattingOptions, which outrank the house default.
func TestFormatLayout(t *testing.T) {
	editorOpts := protocol.FormattingOptions{TabSize: 4, InsertSpaces: true}

	t.Run("editorconfig overrides editor options", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, ".editorconfig"),
			"root = true\n[*.belt]\nindent_style = tab\nend_of_line = crlf\n")
		got := formatLayout(protocol.DocumentURI(filepath.Join(dir, "foo.belt")), editorOpts)
		if want := (formatter.Layout{Indent: "\t", EndOfLine: "\r\n"}); got != want {
			t.Errorf("layout = %#v, want %#v", got, want)
		}
	})

	t.Run("editor options fill in where editorconfig is silent", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, ".editorconfig"), "root = true\n")
		got := formatLayout(protocol.DocumentURI(filepath.Join(dir, "foo.belt")), editorOpts)
		if want := (formatter.Layout{Indent: "    ", EndOfLine: "\n"}); got != want {
			t.Errorf("layout = %#v, want %#v", got, want)
		}
	})

	t.Run("house default when neither speaks", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, ".editorconfig"), "root = true\n")
		got := formatLayout(protocol.DocumentURI(filepath.Join(dir, "foo.belt")), protocol.FormattingOptions{})
		if got != formatter.DefaultLayout {
			t.Errorf("layout = %#v, want house default %#v", got, formatter.DefaultLayout)
		}
	})
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

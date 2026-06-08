package lsp

import (
	"strings"
	"testing"

	protocol "github.com/owenrumney/go-lsp/lsp"

	"github.com/masterbelt/masterbelt/internal/belttest"
)

// funcSrc declares a documented function and a call site.
const funcSrc = "/// doubles x\npub fn double(x: nint): nint -> x * 2\nconst A = double(21)\n"

func TestCompletionOffersFunctions(t *testing.T) {
	doc := testView(funcSrc)

	// In the value position of A's initializer.
	offset := strings.Index(funcSrc, "= double") + 4
	got := byLabel(completion(doc, offset).Items)

	item, ok := got["double"]
	if !ok {
		t.Fatalf("value completion missing the function double: %v", got)
	}
	if k := item.Kind; k == nil || *k != protocol.CompletionItemKindFunction {
		t.Errorf("double kind = %v, want Function", k)
	}
	if item.Detail != "pub fn double(x: nint): nint" {
		t.Errorf("double detail = %q, want the signature", item.Detail)
	}
	if item.InsertText != "double(${1:x})" {
		t.Errorf("double snippet = %q, want double(${1:x})", item.InsertText)
	}
	if doc := item.Documentation; doc == nil || !strings.Contains(doc.Value, "doubles x") {
		t.Errorf("double documentation = %v, want the doc comment", item.Documentation)
	}
}

func TestHoverFunction(t *testing.T) {
	doc := testView(funcSrc)

	t.Run("declaration name", func(t *testing.T) {
		h := hover(doc, strings.Index(funcSrc, "double(x"))
		if h == nil {
			t.Fatal("no hover on the declared function name")
		}
		if !strings.Contains(h.Contents.Value, "pub fn double(x: nint): nint") {
			t.Errorf("hover = %q, want the signature", h.Contents.Value)
		}
		if !strings.Contains(h.Contents.Value, "doubles x") {
			t.Errorf("hover = %q, want the doc comment", h.Contents.Value)
		}
	})

	t.Run("call site", func(t *testing.T) {
		h := hover(doc, strings.Index(funcSrc, "double(21)"))
		if h == nil {
			t.Fatal("no hover on the call site")
		}
		if !strings.Contains(h.Contents.Value, "pub fn double(x: nint): nint") {
			t.Errorf("hover = %q, want the signature", h.Contents.Value)
		}
	})

	t.Run("parameter", func(t *testing.T) {
		h := hover(doc, strings.Index(funcSrc, "x * 2"))
		if h == nil {
			t.Fatal("no hover on the parameter use")
		}
		if !strings.Contains(h.Contents.Value, "x: nint") {
			t.Errorf("hover = %q, want x: nint", h.Contents.Value)
		}
	})
}

func TestDefinitionFunction(t *testing.T) {
	doc := testView(funcSrc)

	locs := definition(doc, strings.Index(funcSrc, "double(21)"))
	if len(locs) != 1 {
		t.Fatalf("got %d locations, want 1", len(locs))
	}
	// double's name is on line 1, columns 7..13 ("pub fn double...").
	r := locs[0].Range
	if r.Start.Line != 1 || r.Start.Character != 7 || r.End.Character != 13 {
		t.Errorf("definition range = %+v, want line 1 cols 7..13", r)
	}
}

func TestSemanticTokensFunction(t *testing.T) {
	// The declared name colours as a function (declaration); the callee — a
	// resolution fact — colours as a function through the program-aware pass.
	doc := testView("fn f(): nint -> 1\nconst A = f()\n")
	got := decode(semanticTokensIn(doc).Data)

	want := []decodedToken{
		{0, 0, 2, stKeyword, 0},                           // fn
		{0, 3, 1, stFunction, smDeclaration},              // f (declared)
		{0, 6, 1, stOperator, 0},                          // :
		{0, 8, 4, stType, 0},                              // nint
		{0, 13, 2, stOperator, 0},                         // ->
		{0, 16, 1, stNumber, 0},                           // 1
		{1, 0, 5, stKeyword, 0},                           // const
		{1, 6, 1, stVariable, smDeclaration | smReadonly}, // A
		{1, 8, 1, stOperator, 0},                          // =
		{1, 10, 1, stFunction, 0},                         // f (callee)
	}

	if len(got) != len(want) {
		t.Fatalf("got %d tokens, want %d:\n got:  %+v\n want: %+v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("token[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestDocumentSymbolsIncludeFunctions(t *testing.T) {
	doc := testView(funcSrc)
	syms := documentSymbols(doc)
	var fn *protocol.DocumentSymbol
	for i := range syms {
		if syms[i].Name == "double" {
			fn = &syms[i]
		}
	}
	if fn == nil {
		t.Fatalf("symbols = %v, want double", syms)
	}
	if fn.Kind != protocol.SymbolKindFunction {
		t.Errorf("double kind = %v, want Function", fn.Kind)
	}
	if fn.Detail != "pub fn double(x: nint): nint  ·  belt:test/double" {
		t.Errorf("double detail = %q, want the signature with anchor", fn.Detail)
	}
}

// --- cross-file functions ---------------------------------------------------------

const funcMainSrc = "use { double } from \"math.belt\"\nuse math from \"math.belt\"\nconst A = double(21)\nconst B = math.greet(\"belt\")\n"

func funcProject(t *testing.T) (root string) {
	t.Helper()
	return belttest.WriteFiles(t, map[string]string{
		"masterbelt.toml": "entry = \"main.belt\"\n",
		"main.belt":       funcMainSrc,
		"math.belt":       "/// doubles x\npub fn double(x: nint): nint -> x * 2\n/// greets\npub fn greet(name: string): string -> name\n",
	})
}

// TestCrossFileFunctionHover: hovering an imported callee and a namespace
// callee shows the exporter's signature and doc.
func TestCrossFileFunctionHover(t *testing.T) {
	root := funcProject(t)
	s := NewServer()
	uri := openOnDisk(t, s, root, "main.belt")
	v := s.open[uri]

	if diags := v.Diagnostics(); len(diags) != 0 {
		t.Fatalf("diagnostics = %v, want none", diags)
	}

	h := hover(v, strings.Index(funcMainSrc, "double(21)"))
	if h == nil {
		t.Fatal("no hover on the imported callee")
	}
	if !strings.Contains(h.Contents.Value, "pub fn double(x: nint): nint") || !strings.Contains(h.Contents.Value, "doubles x") {
		t.Errorf("hover = %q, want the exporter's signature and doc", h.Contents.Value)
	}

	h = hover(v, strings.Index(funcMainSrc, "greet(\"belt\")"))
	if h == nil {
		t.Fatal("no hover on the namespace callee")
	}
	if !strings.Contains(h.Contents.Value, "pub fn greet(name: string): string") {
		t.Errorf("hover = %q, want the exporter's signature", h.Contents.Value)
	}
}

// TestCrossFileFunctionDefinition: go-to-definition on an imported callee and
// a namespace callee jumps into the exporting file.
func TestCrossFileFunctionDefinition(t *testing.T) {
	root := funcProject(t)
	s := NewServer()
	uri := openOnDisk(t, s, root, "main.belt")
	v := s.open[uri]

	cases := []struct {
		name   string
		offset int
		// math.belt line 1: "pub fn double(x: int): int -> x * 2"
		//           line 3: "pub fn greet(name: string): string -> name"
		wantLine, wantStart, wantEnd int
	}{
		{"imported callee", strings.Index(funcMainSrc, "double(21)"), 1, 7, 13},
		{"namespace callee", strings.Index(funcMainSrc, "greet(\"belt\")"), 3, 7, 12},
	}
	mathURI := fileURI(root, "math.belt")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			locs := definition(v, tc.offset)
			if len(locs) != 1 {
				t.Fatalf("got %d locations, want 1", len(locs))
			}
			if locs[0].URI != mathURI {
				t.Errorf("URI = %q, want %q", locs[0].URI, mathURI)
			}
			r := locs[0].Range
			if r.Start.Line != tc.wantLine || r.Start.Character != tc.wantStart || r.End.Character != tc.wantEnd {
				t.Errorf("range = %+v, want line %d cols %d..%d", r, tc.wantLine, tc.wantStart, tc.wantEnd)
			}
		})
	}
}

// TestCompletionOffersImportedFunctions: an imported function appears in
// value-position completion alongside the local names.
func TestCompletionOffersImportedFunctions(t *testing.T) {
	root := funcProject(t)
	s := NewServer()
	uri := openOnDisk(t, s, root, "main.belt")
	v := s.open[uri]

	offset := strings.Index(funcMainSrc, "double(21)") + 3
	got := byLabel(completion(v, offset).Items)
	if _, ok := got["double"]; !ok {
		t.Fatalf("completion missing the imported function double")
	}
	if d := got["double"].Detail; d != "pub fn double(x: nint): nint" {
		t.Errorf("double detail = %q, want the signature", d)
	}
	// greet arrives only through the namespace import, not by bare name.
	if _, ok := got["greet"]; ok {
		t.Error("completion offered greet, which no selective import binds")
	}
}

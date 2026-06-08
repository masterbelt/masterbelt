package lsp

import (
	"strings"
	"testing"

	protocol "github.com/owenrumney/go-lsp/lsp"

	"github.com/masterbelt/masterbelt/pkg/belt/parser/abstract"
)

const accessorType = "pub type Celsius = { deg: nint } impl {\n" +
	"  /// The freezing point.\n" +
	"  pub static fn freezing(): Celsius {\n    return Celsius{ deg: 0 }\n  }\n" +
	"  /// Fahrenheit reading.\n" +
	"  pub get fahrenheit(): nint {\n    return self.deg * 9 / 5 + 32\n  }\n" +
	"  /// Fahrenheit write.\n" +
	"  pub set fahrenheit(v: nint): self {\n    return Celsius{ deg: (v - 32) * 5 / 9 }\n  }\n" +
	"}\n"

// TestSemanticTokensModifiers checks the accessor/static modifiers colour as
// keywords (the context-keyword classification).
func TestSemanticTokensModifiers(t *testing.T) {
	src := "type C = sbyte impl {\n  get g(): nint {\n    return 0\n  }\n  set g(v: nint): self {\n    return self\n  }\n  static fn s(): C {\n    return self\n  }\n}\n"
	doc := abstract.NewDocument([]byte(src))
	got := decode(semanticTokens(doc).Data)

	// Each modifier word must appear as a keyword token at its source column.
	want := []struct {
		line, char, length int
	}{
		{1, 2, 3}, // get
		{4, 2, 3}, // set
		{7, 2, 6}, // static
	}
	for _, w := range want {
		found := false
		for _, tok := range got {
			if tok.line == w.line && tok.char == w.char && tok.length == w.length && tok.tokenType == stKeyword {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no keyword token at line %d col %d len %d; tokens = %+v", w.line, w.char, w.length, got)
		}
	}
}

// TestCompletionGetterAndStatic checks a value member completion offers a getter
// as a Property (no call snippet), and a type member completion offers a static
// fn as a Function.
func TestCompletionGetterAndStatic(t *testing.T) {
	src := accessorType +
		"const C: Celsius = Celsius{ deg: 0 }\n" +
		"const A = C.\n" +
		"const B = Celsius.\n"
	doc := testView(src)

	t.Run("value member offers the getter as a property, not the setter", func(t *testing.T) {
		off := strings.Index(src, "C.\n") + 2
		items := byLabel(completion(doc, off).Items)
		fah, ok := items["fahrenheit"]
		if !ok {
			t.Fatalf("no fahrenheit getter in value member completion: %v", labels(items))
		}
		if fah.Kind == nil || *fah.Kind != protocol.CompletionItemKindProperty {
			t.Errorf("fahrenheit kind = %v, want Property", fah.Kind)
		}
		if fah.InsertText != "" {
			t.Errorf("a getter is read, not called: InsertText = %q, want empty", fah.InsertText)
		}
		// The static fn must not appear in a value member completion.
		if _, ok := items["freezing"]; ok {
			t.Errorf("a static fn must not appear after a value dot: %v", labels(items))
		}
		// The record field is offered.
		if _, ok := items["deg"]; !ok {
			t.Errorf("the record field deg should be offered: %v", labels(items))
		}
	})

	t.Run("type member offers the static fn as a function", func(t *testing.T) {
		off := strings.Index(src, "Celsius.\n") + len("Celsius.")
		items := byLabel(completion(doc, off).Items)
		fr, ok := items["freezing"]
		if !ok {
			t.Fatalf("no freezing static fn in type member completion: %v", labels(items))
		}
		if fr.Kind == nil || *fr.Kind != protocol.CompletionItemKindFunction {
			t.Errorf("freezing kind = %v, want Function", fr.Kind)
		}
		// A getter must not appear after a type dot.
		if _, ok := items["fahrenheit"]; ok {
			t.Errorf("a getter must not appear after a type dot: %v", labels(items))
		}
	})
}

// TestHoverGetterRead checks a getter read (value.name) hovers with the getter's
// signature and doc.
func TestHoverGetterRead(t *testing.T) {
	src := accessorType +
		"const C: Celsius = Celsius{ deg: 0 }\n" +
		"const F = C.fahrenheit\n"
	doc := testView(src)
	h := hover(doc, strings.Index(src, "C.fahrenheit")+4)
	if h == nil {
		t.Fatal("no hover on the getter read")
	}
	for _, want := range []string{"get fahrenheit(): nint", "Fahrenheit reading"} {
		if !strings.Contains(h.Contents.Value, want) {
			t.Errorf("hover = %q, want it to contain %q", h.Contents.Value, want)
		}
	}
}

// TestHoverStaticCall checks a static fn call (Type.name) hovers with the static
// fn's signature and doc.
func TestHoverStaticCall(t *testing.T) {
	src := accessorType + "const X = Celsius.freezing()\n"
	doc := testView(src)
	h := hover(doc, strings.Index(src, "Celsius.freezing()")+len("Celsius."))
	if h == nil {
		t.Fatal("no hover on the static call")
	}
	for _, want := range []string{"static fn freezing(): Celsius", "The freezing point"} {
		if !strings.Contains(h.Contents.Value, want) {
			t.Errorf("hover = %q, want it to contain %q", h.Contents.Value, want)
		}
	}
}

// TestHoverAccessorDeclaration checks the get/set/static modifier leads the
// declaration hover card.
func TestHoverAccessorDeclaration(t *testing.T) {
	doc := testView(accessorType)
	cases := []struct {
		at   string
		want string
	}{
		{"static fn freezing", "static fn freezing(): Celsius"},
		{"get fahrenheit", "get fahrenheit(): nint"},
		{"set fahrenheit", "set fahrenheit(v: nint): self"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			// Hover on the declared name (past the modifier and a space).
			h := hover(doc, strings.Index(accessorType, tc.at)+strings.LastIndex(tc.at, " ")+2)
			if h == nil {
				t.Fatalf("no hover at %q", tc.at)
			}
			if !strings.Contains(h.Contents.Value, tc.want) {
				t.Errorf("hover = %q, want it to contain %q", h.Contents.Value, tc.want)
			}
		})
	}
}

// TestDocumentSymbolsAccessors checks a type's accessors and static fns appear as
// child symbols of the type, a getter/setter as a Property and a static fn as a
// Function.
func TestDocumentSymbolsAccessors(t *testing.T) {
	doc := testView(accessorType)
	syms := documentSymbols(doc)
	var celsius *protocol.DocumentSymbol
	for i := range syms {
		if syms[i].Name == "Celsius" {
			celsius = &syms[i]
		}
	}
	if celsius == nil {
		t.Fatalf("no Celsius type symbol: %+v", syms)
	}
	kinds := map[string]protocol.SymbolKind{}
	for _, c := range celsius.Children {
		kinds[c.Name] = c.Kind
	}
	if kinds["freezing"] != protocol.SymbolKindFunction {
		t.Errorf("freezing kind = %v, want Function", kinds["freezing"])
	}
	if kinds["fahrenheit"] != protocol.SymbolKindProperty {
		t.Errorf("fahrenheit kind = %v, want Property", kinds["fahrenheit"])
	}
}

// TestDocumentSymbolAnchors checks the outline carries each declaration's
// stable anchor in its detail: the type on its own line, and a member as
// the type's anchor with the member appended.
func TestDocumentSymbolAnchors(t *testing.T) {
	doc := testView(accessorType)
	syms := documentSymbols(doc)
	var celsius *protocol.DocumentSymbol
	for i := range syms {
		if syms[i].Name == "Celsius" {
			celsius = &syms[i]
		}
	}
	if celsius == nil {
		t.Fatalf("no Celsius type symbol: %+v", syms)
	}
	if !strings.Contains(celsius.Detail, "belt:test/Celsius") {
		t.Errorf("Celsius detail = %q, want it to carry the anchor belt:test/Celsius", celsius.Detail)
	}
	details := map[string]string{}
	for _, c := range celsius.Children {
		details[c.Name] = c.Detail
	}
	if d := details["freezing"]; !strings.Contains(d, "belt:test/Celsius#freezing") {
		t.Errorf("freezing detail = %q, want it to carry belt:test/Celsius#freezing", d)
	}
}

// labels returns the keys of a label map, for diagnostics.
func labels(items map[string]protocol.CompletionItem) []string {
	out := make([]string, 0, len(items))
	for k := range items {
		out = append(out, k)
	}
	return out
}

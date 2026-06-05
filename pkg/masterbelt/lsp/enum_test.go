package lsp

import (
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/token"
	protocol "github.com/owenrumney/go-lsp/lsp"
)

const enumSrc = "/// rarity tier\npub enum Rarity: uint8 {\n  Common = 1\n  Rare = 2\n  Legend = 10\n}\nconst Top: Rarity = Rarity.Legend\n"

func TestEnumMemberCompletion(t *testing.T) {
	doc := testView(enumSrc)

	// After "Rarity." in the const initializer: the enum's members, each
	// labelled with its value.
	offset := strings.Index(enumSrc, "= Rarity.") + len("= Rarity.")
	got := byLabel(completion(doc, offset).Items)
	for _, name := range []string{"Common", "Rare", "Legend"} {
		item, ok := got[name]
		if !ok {
			t.Errorf("enum member completion missing %q; got %v", name, got)
			continue
		}
		if item.Kind == nil || *item.Kind != protocol.CompletionItemKindEnumMember {
			t.Errorf("%s kind = %v, want EnumMember", name, item.Kind)
		}
	}
	if got["Legend"].Detail != "= 10" {
		t.Errorf("Legend detail = %q, want = 10", got["Legend"].Detail)
	}
	// Member completion is exclusive: the value namespace is not offered.
	if _, ok := got["Top"]; ok {
		t.Errorf("enum member completion should not offer the value Top")
	}
}

func TestEnumMemberCompletionAfterBareDot(t *testing.T) {
	src := "enum E {\n  Fire, Water\n}\nconst x: E = E.\n"
	doc := testView(src)
	got := byLabel(completion(doc, strings.Index(src, "= E.")+4).Items)
	for _, name := range []string{"Fire", "Water"} {
		if _, ok := got[name]; !ok {
			t.Errorf("completion after the enum dot missing %q; got %v", name, got)
		}
	}
}

func TestEnumMemberHover(t *testing.T) {
	doc := testView(enumSrc)
	// Hover on "Legend" in "Rarity.Legend".
	offset := strings.Index(enumSrc, "Rarity.Legend") + len("Rarity.")
	h := hover(doc, offset)
	if h == nil {
		t.Fatal("no hover for the enum member")
	}
	if !strings.Contains(h.Contents.Value, "Rarity.Legend = 10") {
		t.Errorf("hover = %q, want it to show Rarity.Legend = 10", h.Contents.Value)
	}
}

func TestEnumDocumentSymbols(t *testing.T) {
	doc := testView(enumSrc)
	syms := documentSymbols(doc)
	var enum *protocol.DocumentSymbol
	for i := range syms {
		if syms[i].Name == "Rarity" {
			enum = &syms[i]
		}
	}
	if enum == nil {
		t.Fatalf("document symbols missing the enum Rarity; got %v", syms)
	}
	if enum.Kind != protocol.SymbolKindEnum {
		t.Errorf("Rarity kind = %v, want Enum", enum.Kind)
	}
	if len(enum.Children) != 3 {
		t.Fatalf("Rarity has %d member symbols, want 3", len(enum.Children))
	}
	names := []string{}
	for _, c := range enum.Children {
		names = append(names, c.Name)
		if c.Kind != protocol.SymbolKindEnumMember {
			t.Errorf("member %s kind = %v, want EnumMember", c.Name, c.Kind)
		}
	}
	if strings.Join(names, ",") != "Common,Rare,Legend" {
		t.Errorf("member symbols = %v, want [Common Rare Legend]", names)
	}
}

func TestEnumSemanticTokens(t *testing.T) {
	// The enum's declared name colours as a type; its members as enum members.
	if ty, mods, ok := classifyToken(token.Ident, cst.EnumDecl, false); !ok || ty != stType || mods&smDeclaration == 0 {
		t.Errorf("enum name token = (%d, %d, %v), want type with declaration", ty, mods, ok)
	}
	if ty, mods, ok := classifyToken(token.Ident, cst.EnumMember, false); !ok || ty != stEnumMember || mods&smDeclaration == 0 {
		t.Errorf("enum member token = (%d, %d, %v), want enumMember with declaration", ty, mods, ok)
	}
	if ty, _, ok := classifyToken(token.Enum, cst.EnumDecl, false); !ok || ty != stKeyword {
		t.Errorf("enum keyword token = (%d, %v), want keyword", ty, ok)
	}
}

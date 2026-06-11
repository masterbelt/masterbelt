package lsp

import (
	"strings"
	"testing"

	protocol "github.com/owenrumney/go-lsp/lsp"

	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/token"
)

const enumSrc = "/// rarity tier\npub enum Rarity: byte {\n  Common = 1\n  Rare = 2\n  Legend = 10\n}\nconst Top: Rarity = Rarity.Legend\n"

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

// TestEnumTypeNameHover pins that hovering an enum — by a reference annotation or
// by its own declaration name — reads as an enum: the enum keyword, its base, and
// its members, not the bare `type Name` the type-alias card would show.
func TestEnumTypeNameHover(t *testing.T) {
	doc := testView(enumSrc)

	for _, probe := range []struct {
		name   string
		offset int
	}{
		{"annotation reference", strings.Index(enumSrc, ": Rarity") + 3},
		{"declaration name", strings.Index(enumSrc, "enum Rarity") + len("enum ")},
	} {
		t.Run(probe.name, func(t *testing.T) {
			h := hover(doc, probe.offset)
			if h == nil {
				t.Fatal("no hover on the enum type name")
			}
			val := h.Contents.Value
			for _, want := range []string{
				"enum Rarity: byte", "Common = 1", "Legend = 10",
				// Every enum opts into comparable and orderable; the hover surfaces
				// that conformance, the way a plain type's impls are shown.
				"impl comparable", "impl orderable",
			} {
				if !strings.Contains(val, want) {
					t.Errorf("enum hover = %q, want it to contain %q", val, want)
				}
			}
			if strings.Contains(val, "type Rarity") {
				t.Errorf("enum hover renders it as a plain type: %q", val)
			}
		})
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
	names := make([]string, 0, len(enum.Children))
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
	if ty, mods, ok := classifyToken(token.Ident, cst.EnumDecl, false, false); !ok || ty != stType || mods&smDeclaration == 0 {
		t.Errorf("enum name token = (%d, %d, %v), want type with declaration", ty, mods, ok)
	}
	if ty, mods, ok := classifyToken(token.Ident, cst.EnumMember, false, false); !ok || ty != stEnumMember || mods&smDeclaration == 0 {
		t.Errorf("enum member token = (%d, %d, %v), want enumMember with declaration", ty, mods, ok)
	}
	if ty, _, ok := classifyToken(token.Enum, cst.EnumDecl, false, false); !ok || ty != stKeyword {
		t.Errorf("enum keyword token = (%d, %v), want keyword", ty, ok)
	}
}

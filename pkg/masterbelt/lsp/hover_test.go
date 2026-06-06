package lsp

import (
	"strings"
	"testing"
)

// const source used by the hover/definition tests. Offsets:
//
//	line 0: "/// docs"                              (doc comment)
//	line 1: "const MaxLevel: int64 = 100"           name [15,23)
//	line 2: "const Alias = MaxLevel"                reference [51,59)
const hoverSrc = "/// docs\nconst MaxLevel: long = 100\nconst Alias = MaxLevel\n"

func TestDefinition(t *testing.T) {
	doc := testView(hoverSrc)

	locs := definition(doc, 54) // on the "MaxLevel" reference
	if len(locs) != 1 {
		t.Fatalf("got %d locations, want 1", len(locs))
	}
	if locs[0].URI != doc.uri {
		t.Errorf("URI = %q, want %q", locs[0].URI, doc.uri)
	}
	// MaxLevel's name is on line 1, columns 6..14.
	r := locs[0].Range
	if r.Start.Line != 1 || r.Start.Character != 6 || r.End.Line != 1 || r.End.Character != 14 {
		t.Errorf("definition range = %+v, want line 1 cols 6..14", r)
	}
}

func TestDefinitionInExpression(t *testing.T) {
	doc := testView(exprRefSrc)

	locs := definition(doc, 26) // second M reference, inside the expression
	if len(locs) != 1 {
		t.Fatalf("got %d locations, want 1", len(locs))
	}
	// M's name is on line 0, columns 6..7.
	r := locs[0].Range
	if r.Start.Line != 0 || r.Start.Character != 6 || r.End.Character != 7 {
		t.Errorf("definition range = %+v, want line 0 cols 6..7", r)
	}
}

func TestTypeHover(t *testing.T) {
	src := "/// a coin\npub type Coin = sbyte\nconst c: Coin = 1\n"
	doc := testView(src)

	t.Run("annotation reference describes the type", func(t *testing.T) {
		h := hover(doc, strings.Index(src, ": Coin")+3)
		if h == nil {
			t.Fatal("no hover on the type reference")
		}
		if !strings.Contains(h.Contents.Value, "pub type Coin = sbyte") {
			t.Errorf("hover = %q, want the type signature", h.Contents.Value)
		}
		if !strings.Contains(h.Contents.Value, "a coin") {
			t.Errorf("hover = %q, want the doc comment", h.Contents.Value)
		}
	})

	t.Run("declaration name describes itself", func(t *testing.T) {
		h := hover(doc, strings.Index(src, "type Coin")+6)
		if h == nil {
			t.Fatal("no hover on the declaration name")
		}
		if !strings.Contains(h.Contents.Value, "pub type Coin = sbyte") {
			t.Errorf("hover = %q, want the type signature", h.Contents.Value)
		}
	})

	t.Run("a builtin from the prelude describes itself", func(t *testing.T) {
		h := hover(doc, strings.Index(src, "= sbyte")+3)
		if h == nil {
			t.Fatal("no hover on sbyte")
		}
		if !strings.Contains(h.Contents.Value, "type sbyte = builtin") {
			t.Errorf("hover = %q, want the builtin signature", h.Contents.Value)
		}
	})

	t.Run("the range builtin describes itself", func(t *testing.T) {
		rsrc := "pub fn f(r: range): nint -> r.count()\n"
		rdoc := testView(rsrc)
		h := hover(rdoc, strings.Index(rsrc, ": range")+3)
		if h == nil {
			t.Fatal("no hover on range")
		}
		if !strings.Contains(h.Contents.Value, "type range = builtin") {
			t.Errorf("hover = %q, want the builtin signature", h.Contents.Value)
		}
		if !strings.Contains(h.Contents.Value, "half-open") {
			t.Errorf("hover = %q, want the doc comment", h.Contents.Value)
		}
	})
}

func TestInterfaceHover(t *testing.T) {
	src := "/// a behaviour\npub interface foldable<V> {\n  count(): nint\n}\n" +
		"pub type Bag = list<nint> impl foldable<nint> {\n  count(): nint {\n    return 0\n  }\n}\n"
	doc := testView(src)

	t.Run("the interface declaration describes itself", func(t *testing.T) {
		h := hover(doc, strings.Index(src, "interface foldable")+11)
		if h == nil {
			t.Fatal("no hover on the interface declaration name")
		}
		if !strings.Contains(h.Contents.Value, "pub interface foldable<V>") {
			t.Errorf("hover = %q, want the interface signature", h.Contents.Value)
		}
		if !strings.Contains(h.Contents.Value, "a behaviour") {
			t.Errorf("hover = %q, want the doc comment", h.Contents.Value)
		}
	})

	t.Run("a type that impls an interface shows it on its card", func(t *testing.T) {
		h := hover(doc, strings.Index(src, "type Bag")+6)
		if h == nil {
			t.Fatal("no hover on Bag")
		}
		if !strings.Contains(h.Contents.Value, "impl foldable<nint>") {
			t.Errorf("hover = %q, want the impl'd interface on the card", h.Contents.Value)
		}
	})
}

func TestTypeHoverWhere(t *testing.T) {
	// The refinement predicate is part of the signature: hovering the type
	// shows the values it admits, in canonical surface form.
	src := "pub type Port = int where self >= 1 && self <= 65535\nconst p: Port = 8080\n"
	doc := testView(src)

	h := hover(doc, strings.Index(src, ": Port")+3)
	if h == nil {
		t.Fatal("no hover on the refined type reference")
	}
	if !strings.Contains(h.Contents.Value, "pub type Port = int where self >= 1 && self <= 65535") {
		t.Errorf("hover = %q, want the signature with the where clause", h.Contents.Value)
	}
}

func TestTypeHoverGenericParams(t *testing.T) {
	src := "type Opt<T> = T | null\nconst o: Opt<sbyte> = 1\n"
	doc := testView(src)

	h := hover(doc, strings.Index(src, ": Opt")+3)
	if h == nil {
		t.Fatal("no hover on the generic type reference")
	}
	if !strings.Contains(h.Contents.Value, "type Opt<T> = T | null") {
		t.Errorf("hover = %q, want the generic signature", h.Contents.Value)
	}
}

func TestTypeDefinition(t *testing.T) {
	src := "pub type Coin = sbyte\nconst c: Coin = 1\n"
	doc := testView(src)

	locs := definition(doc, strings.Index(src, ": Coin")+3)
	if len(locs) != 1 {
		t.Fatalf("definition = %d locations, want 1", len(locs))
	}
	// The location is the declaration's name, "Coin" on line 0 (cols 9..13).
	r := locs[0].Range
	if r.Start.Line != 0 || r.Start.Character != 9 || r.End.Character != 13 {
		t.Errorf("definition range = %+v, want line 0 cols 9..13", r)
	}

	// A prelude builtin is declared in no workspace file: no jump, no panic.
	if locs := definition(doc, strings.Index(src, "= sbyte")+3); locs != nil {
		t.Errorf("definition(sbyte) = %v, want nil for a prelude type", locs)
	}
}

func TestTypeHoverMethods(t *testing.T) {
	// The type hover reads like a card: the signature, the doc, then every
	// method's signature — what the type can do, at a glance.
	src := "/// a level\ntype Level = sbyte impl {\n" +
		"  pub increment(): self {\n    return self + 1\n  }\n" +
		"  extern shift(by: sbyte): self\n" +
		"}\nconst l: Level = 1\n"
	doc := testView(src)

	h := hover(doc, strings.Index(src, ": Level")+3)
	if h == nil {
		t.Fatal("no hover on the type reference")
	}
	val := h.Contents.Value
	for _, want := range []string{"type Level = sbyte", "a level", "pub increment(): self", "extern shift(by: sbyte): self"} {
		if !strings.Contains(val, want) {
			t.Errorf("hover = %q, want it to contain %q", val, want)
		}
	}
	// Signature, then doc, then methods.
	sig := strings.Index(val, "type Level")
	docAt := strings.Index(val, "a level")
	methods := strings.Index(val, "pub increment")
	if sig >= docAt || docAt >= methods {
		t.Errorf("hover sections out of order: sig %d, doc %d, methods %d", sig, docAt, methods)
	}

	// A prelude builtin lists its operator methods the same way, each under
	// its doc comment, with the type's own doc up top.
	h = hover(doc, strings.Index(src, "= sbyte")+3)
	if h == nil {
		t.Fatal("no hover on sbyte")
	}
	for _, want := range []string{
		"A signed 8-bit integer (-128 to 127).",
		"/// The + operator: the sum.\npub extern add(other: self): self",
	} {
		if !strings.Contains(h.Contents.Value, want) {
			t.Errorf("sbyte hover = %q, want it to contain %q", h.Contents.Value, want)
		}
	}
}

func TestEffectfulFunctionHover(t *testing.T) {
	src := "extern fn io async fetch(url: string): string\n" +
		"pub fn io async page(url: string): string {\n" +
		"  return await fetch(url)\n" +
		"}\n"
	doc := testView(src)

	t.Run("extern root shows its effects", func(t *testing.T) {
		h := hover(doc, strings.Index(src, "fetch")+2)
		if h == nil {
			t.Fatal("no hover on the extern declaration name")
		}
		if !strings.Contains(h.Contents.Value, "extern fn io async fetch(url: string): string") {
			t.Errorf("hover = %q, want the effectful extern signature", h.Contents.Value)
		}
	})

	t.Run("callee shows the declared effects", func(t *testing.T) {
		h := hover(doc, strings.Index(src, "await fetch")+8)
		if h == nil {
			t.Fatal("no hover on the callee")
		}
		if !strings.Contains(h.Contents.Value, "extern fn io async fetch") {
			t.Errorf("hover = %q, want the effectful signature", h.Contents.Value)
		}
	})
}

package lsp

import (
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/semantic"
	protocol "github.com/owenrumney/go-lsp/lsp"
)

// const source used by the hover/definition tests. Offsets:
//
//	line 0: "/// docs"                              (doc comment)
//	line 1: "const MaxLevel: int64 = 100"           name [15,23)
//	line 2: "const Alias = MaxLevel"                reference [51,59)
const hoverSrc = "/// docs\nconst MaxLevel: int64 = 100\nconst Alias = MaxLevel\n"

func TestHover(t *testing.T) {
	doc := semantic.NewDocument([]byte(hoverSrc))

	t.Run("declaration name shows type and doc", func(t *testing.T) {
		h := hover(doc, 18) // inside "MaxLevel"
		if h == nil {
			t.Fatal("no hover on declaration name")
		}
		val := h.Contents.Value
		if !strings.Contains(val, "const MaxLevel: int64") {
			t.Errorf("hover = %q, want a const MaxLevel: int64 signature", val)
		}
		if !strings.Contains(val, "docs") {
			t.Errorf("hover = %q, want the doc comment", val)
		}
	})

	t.Run("reference describes its target", func(t *testing.T) {
		h := hover(doc, 54) // inside the "MaxLevel" reference in Alias
		if h == nil {
			t.Fatal("no hover on reference")
		}
		if !strings.Contains(h.Contents.Value, "const MaxLevel: int64") {
			t.Errorf("reference hover = %q, want it to describe MaxLevel", h.Contents.Value)
		}
	})
}

func TestDefinition(t *testing.T) {
	doc := semantic.NewDocument([]byte(hoverSrc))
	uri := protocol.DocumentURI("file:///x.belt")

	locs := definition(doc, 54, uri) // on the "MaxLevel" reference
	if len(locs) != 1 {
		t.Fatalf("got %d locations, want 1", len(locs))
	}
	if locs[0].URI != uri {
		t.Errorf("URI = %q, want %q", locs[0].URI, uri)
	}
	// MaxLevel's name is on line 1, columns 6..14.
	r := locs[0].Range
	if r.Start.Line != 1 || r.Start.Character != 6 || r.End.Line != 1 || r.End.Character != 14 {
		t.Errorf("definition range = %+v, want line 1 cols 6..14", r)
	}
}

func TestHoverInExpression(t *testing.T) {
	// exprRefSrc is "const M = 1\nconst z = M + M\n"; the first M reference is at
	// offset 22, inside the expression.
	doc := semantic.NewDocument([]byte(exprRefSrc))
	h := hover(doc, 22)
	if h == nil {
		t.Fatal("no hover on a reference inside an expression")
	}
	if !strings.Contains(h.Contents.Value, "const M") {
		t.Errorf("hover = %q, want it to describe M", h.Contents.Value)
	}
}

func TestDefinitionInExpression(t *testing.T) {
	doc := semantic.NewDocument([]byte(exprRefSrc))
	uri := protocol.DocumentURI("file:///x.belt")

	locs := definition(doc, 26, uri) // second M reference, inside the expression
	if len(locs) != 1 {
		t.Fatalf("got %d locations, want 1", len(locs))
	}
	// M's name is on line 0, columns 6..7.
	r := locs[0].Range
	if r.Start.Line != 0 || r.Start.Character != 6 || r.End.Character != 7 {
		t.Errorf("definition range = %+v, want line 0 cols 6..7", r)
	}
}

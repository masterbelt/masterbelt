package lsp

// Hover over the special, non-member contexts: a constant's declaration name or
// reference, a datetime or duration literal's canonical value, and an
// assertion's power-assert diagram.

import (
	"strings"
	"testing"
)

func TestHover(t *testing.T) {
	doc := testView(hoverSrc)

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

func TestHoverInExpression(t *testing.T) {
	// exprRefSrc is "const M = 1\nconst z = M + M\n"; the first M reference is at
	// offset 22, inside the expression.
	doc := testView(exprRefSrc)
	h := hover(doc, 22)
	if h == nil {
		t.Fatal("no hover on a reference inside an expression")
	}
	if !strings.Contains(h.Contents.Value, "const M") {
		t.Errorf("hover = %q, want it to describe M", h.Contents.Value)
	}
}

func TestHoverInTernaryBranch(t *testing.T) {
	// A reference in either branch of a ternary keeps its const hover, reached
	// through the same expression walk every other operand uses.
	src := "const A = 1\nconst B = 2\nconst C = true ? A : B\n"
	doc := testView(src)

	t.Run("then-branch reference", func(t *testing.T) {
		h := hover(doc, strings.Index(src, "? A")+2) // inside the "A" reference
		if h == nil {
			t.Fatal("no hover on the then-branch reference")
		}
		if !strings.Contains(h.Contents.Value, "const A") {
			t.Errorf("hover = %q, want it to describe A", h.Contents.Value)
		}
	})

	t.Run("else-branch reference", func(t *testing.T) {
		h := hover(doc, strings.Index(src, ": B")+2) // inside the "B" reference
		if h == nil {
			t.Fatal("no hover on the else-branch reference")
		}
		if !strings.Contains(h.Contents.Value, "const B") {
			t.Errorf("hover = %q, want it to describe B", h.Contents.Value)
		}
	})
}

func TestAssertHover(t *testing.T) {
	src := "const Max = 100\nconst Min = 0\nassert Max > Min\n"
	doc := testView(src)

	t.Run("keyword shows the power-assert diagram", func(t *testing.T) {
		h := hover(doc, strings.Index(src, "assert")+2) // inside "assert"
		if h == nil {
			t.Fatal("no hover on the assert keyword")
		}
		want := "```\n" +
			"assert Max > Min\n" +
			"       ^   ^ ^\n" +
			"       100 | 0\n" +
			"           true\n" +
			"```"
		if h.Contents.Value != want {
			t.Errorf("hover = %q, want %q", h.Contents.Value, want)
		}
	})

	t.Run("identifiers in the condition keep their const hover", func(t *testing.T) {
		h := hover(doc, strings.Index(src, "assert Max")+8) // inside the "Max" reference
		if h == nil {
			t.Fatal("no hover on the reference")
		}
		if !strings.Contains(h.Contents.Value, "const Max") {
			t.Errorf("hover = %q, want it to describe the constant Max", h.Contents.Value)
		}
	})

	t.Run("leading trivia hovers nothing", func(t *testing.T) {
		if h := hover(doc, strings.Index(src, "\nassert")); h != nil {
			t.Errorf("hover on the newline before assert = %q, want nil", h.Contents.Value)
		}
	})
}

func TestAssertHoverDoc(t *testing.T) {
	src := "const Max = 100\n/// the range is not empty\nassert Max > 0\n"
	doc := testView(src)

	h := hover(doc, strings.Index(src, "assert")+2)
	if h == nil {
		t.Fatal("no hover on the assert keyword")
	}
	if !strings.HasSuffix(h.Contents.Value, "```\n\nthe range is not empty") {
		t.Errorf("hover = %q, want the doc comment after the diagram", h.Contents.Value)
	}
}

func TestLiteralHover(t *testing.T) {
	// A datetime or duration literal hovers as its canonical value: the UTC
	// instant an offset spelling normalizes to, and the largest-units-first
	// decomposition of a duration.
	src := "const Launch = D2026-06-05T09:00:00.000+09:00\n" +
		"const Wait = 90m\n" +
		"const Sum = 90m + D2026-06-05T00:00:00.000Z - D2026-06-05T00:00:00.000Z\n"
	doc := testView(src)

	t.Run("offset datetime normalizes", func(t *testing.T) {
		h := hover(doc, strings.Index(src, "D2026")+3)
		if h == nil {
			t.Fatal("no hover on the datetime literal")
		}
		if want := "datetime = D2026-06-05T00:00:00.000Z"; !strings.Contains(h.Contents.Value, want) {
			t.Errorf("hover = %q, want it to contain %q", h.Contents.Value, want)
		}
	})

	t.Run("duration shows canonical decomposition", func(t *testing.T) {
		h := hover(doc, strings.Index(src, "90m")+1)
		if h == nil {
			t.Fatal("no hover on the duration literal")
		}
		if want := "duration = 1h30m"; !strings.Contains(h.Contents.Value, want) {
			t.Errorf("hover = %q, want it to contain %q", h.Contents.Value, want)
		}
	})
}

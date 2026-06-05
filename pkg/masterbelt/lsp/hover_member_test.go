package lsp

// Hover over members: a record field, a method (declaration, call site,
// overload set, prelude, generic substitution), or a parameter (a lambda's or
// a method's), including resolution through the receiver's type.

import (
	"strings"
	"testing"
)

func TestLambdaParamHover(t *testing.T) {
	src := "const Doubled = [1, 2].map(fn(x) { return x * 2 })\n"
	doc := testView(src)

	t.Run("parameter declaration", func(t *testing.T) {
		h := hover(doc, strings.Index(src, "x)")) // the x in fn(x)
		if h == nil {
			t.Fatal("no hover on the lambda parameter")
		}
		if !strings.Contains(h.Contents.Value, "x: int") {
			t.Errorf("hover = %q, want the inferred x: int", h.Contents.Value)
		}
	})

	t.Run("use in the body", func(t *testing.T) {
		h := hover(doc, strings.Index(src, "x * 2")) // the x in return x * 2
		if h == nil {
			t.Fatal("no hover on the parameter use")
		}
		if !strings.Contains(h.Contents.Value, "x: int") {
			t.Errorf("hover = %q, want the inferred x: int", h.Contents.Value)
		}
	})

	t.Run("nested literal shadows", func(t *testing.T) {
		// The outer x is int, the inner x is bool (pushed in from the
		// annotation); the innermost scope wins for the body use.
		nested := "const F: fn(x: int): fn(x: bool): bool = fn(x) { return fn(x) { return x } }\n"
		ndoc := testView(nested)
		h := hover(ndoc, strings.LastIndex(nested, "x }")) // the inner return x
		if h == nil {
			t.Fatal("no hover on the nested parameter use")
		}
		if !strings.Contains(h.Contents.Value, "x: bool") {
			t.Errorf("hover = %q, want the inner x: bool", h.Contents.Value)
		}
	})
}

func TestMethodParamHover(t *testing.T) {
	src := "type Lvl = int8 impl {\n  inc(amount: int8): self {\n    return self + amount\n  }\n}\n"
	doc := testView(src)

	t.Run("declaration in the signature", func(t *testing.T) {
		h := hover(doc, strings.Index(src, "amount")+2)
		if h == nil {
			t.Fatal("no hover on the parameter declaration")
		}
		if !strings.Contains(h.Contents.Value, "amount: int8") {
			t.Errorf("hover = %q, want amount: int8", h.Contents.Value)
		}
	})

	t.Run("reference in the body", func(t *testing.T) {
		h := hover(doc, strings.Index(src, "+ amount")+4)
		if h == nil {
			t.Fatal("no hover on the body reference")
		}
		if !strings.Contains(h.Contents.Value, "amount: int8") {
			t.Errorf("hover = %q, want amount: int8", h.Contents.Value)
		}
	})
}

func TestLambdaParamHoverInAssert(t *testing.T) {
	// A function literal inside an assert condition is part of the expression
	// walk like any other: its parameter hovers with its inferred type.
	src := "assert [1, 2].map(fn(v) { return v * 2 }) == [2, 4]\n"
	doc := testView(src)

	h := hover(doc, strings.Index(src, "return v")+7)
	if h == nil {
		t.Fatal("no hover on the lambda parameter in an assert")
	}
	if !strings.Contains(h.Contents.Value, "v: int") {
		t.Errorf("hover = %q, want v: int", h.Contents.Value)
	}
}

func TestMethodHover(t *testing.T) {
	src := "type Level = int8 impl {\n" +
		"  /// the next level up\n" +
		"  pub increment(): self {\n    return self\n  }\n" +
		"}\n" +
		"const l: Level = 1\n" +
		"const n = l.increment()\n"
	doc := testView(src)

	t.Run("declaration name shows the signature and doc", func(t *testing.T) {
		h := hover(doc, strings.Index(src, "pub increment")+6)
		if h == nil {
			t.Fatal("no hover on the method declaration")
		}
		for _, want := range []string{"pub increment(): self", "the next level up"} {
			if !strings.Contains(h.Contents.Value, want) {
				t.Errorf("hover = %q, want it to contain %q", h.Contents.Value, want)
			}
		}
	})

	t.Run("call site resolves through the receiver's type", func(t *testing.T) {
		h := hover(doc, strings.Index(src, "l.increment")+4)
		if h == nil {
			t.Fatal("no hover on the call site")
		}
		for _, want := range []string{"pub increment(): self", "the next level up"} {
			if !strings.Contains(h.Contents.Value, want) {
				t.Errorf("hover = %q, want it to contain %q", h.Contents.Value, want)
			}
		}
	})
}

func TestMethodHoverOverloads(t *testing.T) {
	// An overloaded name hovers as the whole overload set: every signature,
	// each under its own doc comment.
	src := "pub type Score = int32 impl {\n" +
		"  /// Merge points in.\n" +
		"  pub fn merge(points: self): self {\n    return self + points\n  }\n" +
		"  /// Whether an active run counts.\n" +
		"  pub fn merge(active: bool): bool {\n    return active && self > 0\n  }\n" +
		"}\n" +
		"const Base: Score = 100\n" +
		"const Bumped = Base.merge(50)\n"
	doc := testView(src)

	h := hover(doc, strings.Index(src, "Base.merge(50)")+7)
	if h == nil {
		t.Fatal("no hover on the overloaded call site")
	}
	for _, want := range []string{
		"pub merge(points: self): self",
		"pub merge(active: bool): bool",
		"/// Merge points in.",
		"/// Whether an active run counts.",
	} {
		if !strings.Contains(h.Contents.Value, want) {
			t.Errorf("hover = %q, want it to contain %q", h.Contents.Value, want)
		}
	}
}

func TestMethodHoverPrelude(t *testing.T) {
	// A prelude method resolves through the builtin's definition, doc and all.
	src := "const a = 1\nconst b = a.add(2)\n"
	doc := testView(src)

	h := hover(doc, strings.Index(src, "a.add")+3)
	if h == nil {
		t.Fatal("no hover on the prelude method")
	}
	for _, want := range []string{"pub extern add(other: self): self", "The + operator: the sum."} {
		if !strings.Contains(h.Contents.Value, want) {
			t.Errorf("hover = %q, want it to contain %q", h.Contents.Value, want)
		}
	}
}

func TestMethodHoverGenericSubstitution(t *testing.T) {
	// The receiver's type arguments substitute into the signature: map on a
	// list<int8> takes an int8 item.
	src := "const xs: list<int8> = [1]\nconst ys = xs.map(fn(x) { return x })\n"
	doc := testView(src)

	h := hover(doc, strings.Index(src, "xs.map")+4)
	if h == nil {
		t.Fatal("no hover on map")
	}
	// The function type renders from the type algebra, which carries no
	// parameter names: fn(int8), with the receiver's element substituted.
	if !strings.Contains(h.Contents.Value, "map(func: fn(int8): R): list<R>") {
		t.Errorf("hover = %q, want the substituted signature", h.Contents.Value)
	}
}

func TestFieldHover(t *testing.T) {
	src := "type Rec = {\n  id: int8\n  level: int16\n} impl {\n" +
		"  get(): int8 {\n    return self.id\n  }\n" +
		"}\n" +
		"const r: Rec = 0\nconst sum = r.id\n"
	doc := testView(src)

	t.Run("self field access shows the field's type", func(t *testing.T) {
		h := hover(doc, strings.Index(src, "self.id")+5)
		if h == nil {
			t.Fatal("no hover on the field access")
		}
		if !strings.Contains(h.Contents.Value, "id: int8") {
			t.Errorf("hover = %q, want id: int8", h.Contents.Value)
		}
	})

	t.Run("field access through a constant", func(t *testing.T) {
		h := hover(doc, strings.Index(src, "r.id")+2)
		if h == nil {
			t.Fatal("no hover on the field access")
		}
		if !strings.Contains(h.Contents.Value, "id: int8") {
			t.Errorf("hover = %q, want id: int8", h.Contents.Value)
		}
	})
}

func TestMethodHoverLiteralReceiver(t *testing.T) {
	// The receiver of 0007-listmap's shape: a collection literal, typed by
	// the real inference rather than name resolution.
	src := "const Doubled = [1, 2, 3].map(fn(x: int): int { return x * 2 })\n"
	doc := testView(src)

	h := hover(doc, strings.Index(src, ".map")+2)
	if h == nil {
		t.Fatal("no hover on map with a literal receiver")
	}
	for _, want := range []string{"map(func: fn(int): R): list<R>", "A new list: func applied to each element, in order."} {
		if !strings.Contains(h.Contents.Value, want) {
			t.Errorf("hover = %q, want it to contain %q", h.Contents.Value, want)
		}
	}
}

func TestMethodHoverParamReceiver(t *testing.T) {
	t.Run("lambda parameter", func(t *testing.T) {
		src := "const ys = [1, 2].map(fn(x: int): int { return x.add(1) })\n"
		doc := testView(src)
		h := hover(doc, strings.Index(src, "x.add")+3)
		if h == nil {
			t.Fatal("no hover on a method through a lambda parameter")
		}
		if !strings.Contains(h.Contents.Value, "The + operator: the sum.") {
			t.Errorf("hover = %q, want int's add doc", h.Contents.Value)
		}
	})

	t.Run("self-typed method parameter", func(t *testing.T) {
		src := "type Lvl = int8 impl {\n  /// the larger of the two\n  max(other: self): self {\n    return other\n  }\n" +
			"  pick(other: self): self {\n    return other.max(self)\n  }\n}\n"
		doc := testView(src)
		h := hover(doc, strings.Index(src, "other.max")+7)
		if h == nil {
			t.Fatal("no hover on a method through a self-typed parameter")
		}
		for _, want := range []string{"max(other: self): self", "the larger of the two"} {
			if !strings.Contains(h.Contents.Value, want) {
				t.Errorf("hover = %q, want it to contain %q", h.Contents.Value, want)
			}
		}
	})
}

func TestErrorConversionHover(t *testing.T) {
	src := "const E = error(\"boom\")\nconst M = E.message()\n"
	doc := testView(src)

	t.Run("conversion callee describes the error type", func(t *testing.T) {
		h := hover(doc, strings.Index(src, "error(")+2)
		if h == nil {
			t.Fatal("no hover on the conversion callee")
		}
		if !strings.Contains(h.Contents.Value, "pub type error = builtin") {
			t.Errorf("hover = %q, want the error type signature", h.Contents.Value)
		}
		if !strings.Contains(h.Contents.Value, "recoverable failure") {
			t.Errorf("hover = %q, want the prelude doc", h.Contents.Value)
		}
	})

	t.Run("message resolves as the error method", func(t *testing.T) {
		h := hover(doc, strings.Index(src, ".message")+3)
		if h == nil {
			t.Fatal("no hover on message")
		}
		if !strings.Contains(h.Contents.Value, "message(): string") {
			t.Errorf("hover = %q, want the message signature", h.Contents.Value)
		}
	})
}

func TestEffectfulMethodHover(t *testing.T) {
	src := "extern fn io async fetch(url: string): string\n" +
		"pub type Client = { base: string } impl {\n" +
		"  pub fn io async get(path: string): string {\n" +
		"    return await fetch(self.base + path)\n" +
		"  }\n" +
		"}\n"
	doc := testView(src)
	h := hover(doc, strings.Index(src, "get(path")+1)
	if h == nil {
		t.Fatal("no hover on the method name")
	}
	if !strings.Contains(h.Contents.Value, "io async get(path: string): string") {
		t.Errorf("hover = %q, want the effectful method signature", h.Contents.Value)
	}
}

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
		if !strings.Contains(h.Contents.Value, "x: nint") {
			t.Errorf("hover = %q, want the inferred x: nint", h.Contents.Value)
		}
	})

	t.Run("use in the body", func(t *testing.T) {
		h := hover(doc, strings.Index(src, "x * 2")) // the x in return x * 2
		if h == nil {
			t.Fatal("no hover on the parameter use")
		}
		if !strings.Contains(h.Contents.Value, "x: nint") {
			t.Errorf("hover = %q, want the inferred x: nint", h.Contents.Value)
		}
	})

	t.Run("nested literal shadows", func(t *testing.T) {
		// The outer x is int, the inner x is bool (pushed in from the
		// annotation); the innermost scope wins for the body use.
		nested := "const F: fn(x: nint): fn(x: bool): bool = fn(x) { return fn(x) { return x } }\n"
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
	src := "type Lvl = sbyte impl {\n  inc(amount: sbyte): self {\n    return self + amount\n  }\n}\n"
	doc := testView(src)

	t.Run("declaration in the signature", func(t *testing.T) {
		h := hover(doc, strings.Index(src, "amount")+2)
		if h == nil {
			t.Fatal("no hover on the parameter declaration")
		}
		if !strings.Contains(h.Contents.Value, "amount: sbyte") {
			t.Errorf("hover = %q, want amount: sbyte", h.Contents.Value)
		}
	})

	t.Run("reference in the body", func(t *testing.T) {
		h := hover(doc, strings.Index(src, "+ amount")+4)
		if h == nil {
			t.Fatal("no hover on the body reference")
		}
		if !strings.Contains(h.Contents.Value, "amount: sbyte") {
			t.Errorf("hover = %q, want amount: sbyte", h.Contents.Value)
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
	if !strings.Contains(h.Contents.Value, "v: nint") {
		t.Errorf("hover = %q, want v: nint", h.Contents.Value)
	}
}

func TestMethodHover(t *testing.T) {
	src := "type Level = sbyte impl {\n" +
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
	src := "pub type Score = int impl {\n" +
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

func TestMethodHoverListStackQueue(t *testing.T) {
	// The list stack/queue methods hover with the prelude doc, the receiver's
	// element type substituted: push takes a nint, pop yields its optional.
	src := "const Pushed = [1, 2].push(3)\nconst Last = [1, 2, 3].pop()\n"
	doc := testView(src)

	t.Run("push", func(t *testing.T) {
		h := hover(doc, strings.Index(src, ".push")+3)
		if h == nil {
			t.Fatal("no hover on push")
		}
		for _, want := range []string{"pub extern push(value: nint): self", "A new list with value pushed at the end"} {
			if !strings.Contains(h.Contents.Value, want) {
				t.Errorf("hover = %q, want it to contain %q", h.Contents.Value, want)
			}
		}
	})

	t.Run("pop", func(t *testing.T) {
		h := hover(doc, strings.Index(src, ".pop")+2)
		if h == nil {
			t.Fatal("no hover on pop")
		}
		for _, want := range []string{"pub extern pop(): optional<nint>", "The last element, or null when the list is empty."} {
			if !strings.Contains(h.Contents.Value, want) {
				t.Errorf("hover = %q, want it to contain %q", h.Contents.Value, want)
			}
		}
	})
}

func TestMethodHoverListAddOverloads(t *testing.T) {
	// The overloaded list + hovers as the whole overload set: the concatenation
	// and the element push, each under its own doc comment.
	src := "const xs: list<sbyte> = [1]\nconst ys = xs.add(2)\n"
	doc := testView(src)

	h := hover(doc, strings.Index(src, "xs.add")+4)
	if h == nil {
		t.Fatal("no hover on the overloaded prelude call site")
	}
	for _, want := range []string{
		"pub extern add(other: self): self",
		"pub extern add(element: sbyte): self",
		"/// The + operator: the concatenation.",
		"/// The + operator with one element:",
	} {
		if !strings.Contains(h.Contents.Value, want) {
			t.Errorf("hover = %q, want it to contain %q", h.Contents.Value, want)
		}
	}
}

func TestMethodHoverGenericSubstitution(t *testing.T) {
	// The receiver's type arguments substitute into the signature: map on a
	// list<int8> takes an int8 item.
	src := "const xs: list<sbyte> = [1]\nconst ys = xs.map(fn(x) { return x })\n"
	doc := testView(src)

	h := hover(doc, strings.Index(src, "xs.map")+4)
	if h == nil {
		t.Fatal("no hover on map")
	}
	// The function type renders from the type algebra, which carries no
	// parameter names: fn(int8), with the receiver's element substituted.
	if !strings.Contains(h.Contents.Value, "map(func: fn(sbyte): R): list<R>") {
		t.Errorf("hover = %q, want the substituted signature", h.Contents.Value)
	}
}

// TestMethodHoverBoundSubstitution checks that a method type-parameter's bound that
// mentions the receiver's own parameter takes the receiver's substitution: on a
// Box<int>, pick<U: wrapper<T>> renders U: wrapper<int>, not the unbound owner T —
// the bound is rendered through the same substitution the parameters and result are.
func TestMethodHoverBoundSubstitution(t *testing.T) {
	src := "pub interface wrapper<X> {\n  pub unwrap(): X\n}\n" +
		"pub type Box<T> = { v: T } impl {\n  pub fn pick<U: wrapper<T>>(u: U): T {\n    return self.v\n  }\n}\n" +
		"fn probe(b: Box<int>): int {\n  return b.pick(b)\n}\n"
	doc := testView(src)
	h := hover(doc, strings.Index(src, "b.pick(")+len("b."))
	if h == nil {
		t.Fatal("no hover on pick")
	}
	if !strings.Contains(h.Contents.Value, "wrapper<int>") {
		t.Errorf("hover should substitute the receiver's int into the method bound: %q", h.Contents.Value)
	}
	if strings.Contains(h.Contents.Value, "wrapper<T>") {
		t.Errorf("hover should not leave the owner variable T unbound: %q", h.Contents.Value)
	}
}

// TestFieldHoverInsideIf checks that a field access in an if condition is
// hoverable — the body-expression walk descends through an if's control flow.
func TestFieldHoverInsideIf(t *testing.T) {
	src := "type Rec = {\n  id: sbyte\n} impl {\n" +
		"  describe(): string {\n    if self.id > 0 {\n      return \"p\"\n    }\n    return \"z\"\n  }\n" +
		"}\n"
	doc := testView(src)
	h := hover(doc, strings.Index(src, "self.id > 0")+5) // the id member of self.id
	if h == nil {
		t.Fatal("no hover on the field access in the if condition")
	}
	if !strings.Contains(h.Contents.Value, "id: sbyte") {
		t.Errorf("hover = %q, want id: sbyte", h.Contents.Value)
	}
}

func TestFieldHover(t *testing.T) {
	src := "type Rec = {\n  id: sbyte\n  level: short\n} impl {\n" +
		"  get(): sbyte {\n    return self.id\n  }\n" +
		"}\n" +
		"const r: Rec = 0\nconst sum = r.id\n"
	doc := testView(src)

	t.Run("self field access shows the field's type", func(t *testing.T) {
		h := hover(doc, strings.Index(src, "self.id")+5)
		if h == nil {
			t.Fatal("no hover on the field access")
		}
		if !strings.Contains(h.Contents.Value, "id: sbyte") {
			t.Errorf("hover = %q, want id: sbyte", h.Contents.Value)
		}
	})

	t.Run("field access through a constant", func(t *testing.T) {
		h := hover(doc, strings.Index(src, "r.id")+2)
		if h == nil {
			t.Fatal("no hover on the field access")
		}
		if !strings.Contains(h.Contents.Value, "id: sbyte") {
			t.Errorf("hover = %q, want id: sbyte", h.Contents.Value)
		}
	})
}

func TestMethodHoverLiteralReceiver(t *testing.T) {
	// The receiver of 0007-listmap's shape: a collection literal, typed by
	// the real inference rather than name resolution.
	src := "const Doubled = [1, 2, 3].map(fn(x: nint): nint { return x * 2 })\n"
	doc := testView(src)

	h := hover(doc, strings.Index(src, ".map")+2)
	if h == nil {
		t.Fatal("no hover on map with a literal receiver")
	}
	for _, want := range []string{"map(func: fn(nint): R): list<R>", "A new list: func applied to each element, in order."} {
		if !strings.Contains(h.Contents.Value, want) {
			t.Errorf("hover = %q, want it to contain %q", h.Contents.Value, want)
		}
	}
}

func TestMethodHoverParamReceiver(t *testing.T) {
	t.Run("lambda parameter", func(t *testing.T) {
		src := "const ys = [1, 2].map(fn(x: nint): nint { return x.add(1) })\n"
		doc := testView(src)
		h := hover(doc, strings.Index(src, "x.add")+3)
		if h == nil {
			t.Fatal("no hover on a method through a lambda parameter")
		}
		if !strings.Contains(h.Contents.Value, "The + operator: the sum.") {
			t.Errorf("hover = %q, want nint's add doc", h.Contents.Value)
		}
	})

	t.Run("self-typed method parameter", func(t *testing.T) {
		src := "type Lvl = sbyte impl {\n  /// the larger of the two\n  max(other: self): self {\n    return other\n  }\n" +
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

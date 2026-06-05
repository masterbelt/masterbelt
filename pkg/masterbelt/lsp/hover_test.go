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
const hoverSrc = "/// docs\nconst MaxLevel: int64 = 100\nconst Alias = MaxLevel\n"

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

func TestTypeHover(t *testing.T) {
	src := "/// a coin\npub type Coin = int8\nconst c: Coin = 1\n"
	doc := testView(src)

	t.Run("annotation reference describes the type", func(t *testing.T) {
		h := hover(doc, strings.Index(src, ": Coin")+3)
		if h == nil {
			t.Fatal("no hover on the type reference")
		}
		if !strings.Contains(h.Contents.Value, "pub type Coin = int8") {
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
		if !strings.Contains(h.Contents.Value, "pub type Coin = int8") {
			t.Errorf("hover = %q, want the type signature", h.Contents.Value)
		}
	})

	t.Run("a builtin from the prelude describes itself", func(t *testing.T) {
		h := hover(doc, strings.Index(src, "= int8")+3)
		if h == nil {
			t.Fatal("no hover on int8")
		}
		if !strings.Contains(h.Contents.Value, "type int8 = builtin") {
			t.Errorf("hover = %q, want the builtin signature", h.Contents.Value)
		}
	})
}

func TestTypeHoverWhere(t *testing.T) {
	// The refinement predicate is part of the signature: hovering the type
	// shows the values it admits, in canonical surface form.
	src := "pub type Port = int32 where self >= 1 && self <= 65535\nconst p: Port = 8080\n"
	doc := testView(src)

	h := hover(doc, strings.Index(src, ": Port")+3)
	if h == nil {
		t.Fatal("no hover on the refined type reference")
	}
	if !strings.Contains(h.Contents.Value, "pub type Port = int32 where self >= 1 && self <= 65535") {
		t.Errorf("hover = %q, want the signature with the where clause", h.Contents.Value)
	}
}

func TestTypeHoverGenericParams(t *testing.T) {
	src := "type Opt<T> = T | null\nconst o: Opt<int8> = 1\n"
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
	src := "pub type Coin = int8\nconst c: Coin = 1\n"
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
	if locs := definition(doc, strings.Index(src, "= int8")+3); locs != nil {
		t.Errorf("definition(int8) = %v, want nil for a prelude type", locs)
	}
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

func TestTypeHoverMethods(t *testing.T) {
	// The type hover reads like a card: the signature, the doc, then every
	// method's signature — what the type can do, at a glance.
	src := "/// a level\ntype Level = int8 impl {\n" +
		"  pub increment(): self {\n    return self + 1\n  }\n" +
		"  extern shift(by: int8): self\n" +
		"}\nconst l: Level = 1\n"
	doc := testView(src)

	h := hover(doc, strings.Index(src, ": Level")+3)
	if h == nil {
		t.Fatal("no hover on the type reference")
	}
	val := h.Contents.Value
	for _, want := range []string{"type Level = int8", "a level", "pub increment(): self", "extern shift(by: int8): self"} {
		if !strings.Contains(val, want) {
			t.Errorf("hover = %q, want it to contain %q", val, want)
		}
	}
	// Signature, then doc, then methods.
	sig := strings.Index(val, "type Level")
	docAt := strings.Index(val, "a level")
	methods := strings.Index(val, "pub increment")
	if !(sig < docAt && docAt < methods) {
		t.Errorf("hover sections out of order: sig %d, doc %d, methods %d", sig, docAt, methods)
	}

	// A prelude builtin lists its operator methods the same way, each under
	// its doc comment, with the type's own doc up top.
	h = hover(doc, strings.Index(src, "= int8")+3)
	if h == nil {
		t.Fatal("no hover on int8")
	}
	for _, want := range []string{
		"A signed 8-bit integer (-128 to 127).",
		"/// The + operator: the sum.\npub extern add(other: self): self",
	} {
		if !strings.Contains(h.Contents.Value, want) {
			t.Errorf("int8 hover = %q, want it to contain %q", h.Contents.Value, want)
		}
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

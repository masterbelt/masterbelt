// This file tests the lowering of expression CST nodes — the operator and
// literal desugaring, collection, record, and function-literal forms — and the
// render round-trip that pins it, mirroring lower_expr.go.

package abstract

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source/ast"
)

func TestLowerExpressions(t *testing.T) {
	// Operators desugar to method calls: 1 + 2 is 1.add(2). dumpExpr renders a
	// CallExpr as (call callee args...) and a MemberExpr as (. receiver member).
	cases := []struct{ src, want string }{
		{"const x = 1 + 2\n", `(call (. IntLit "1" add) IntLit "2")`},
		{"const x = 1 + 2 * 3\n", `(call (. IntLit "1" add) (call (. IntLit "2" mul) IntLit "3"))`},
		{"const x = +1\n", `(call (. IntLit "1" pos))`}, // unary: receiver, no args
		{"const x = -1\n", `(call (. IntLit "1" neg))`},
		{"const x = !true\n", `(call (. BoolLit true not))`},
		{"const x = a && b\n", `(call (. Identifier "a" anan) Identifier "b")`},
		{"const x = false\n", `BoolLit false`},
		{"const x = 1 <= 2\n", `(call (. IntLit "1" lteq) IntLit "2")`},
		{"const x = 1 +\n", `(call (. IntLit "1" add))`}, // recovered: right operand absent
		// Parenthesized groupings unwrap: the tree shape already encodes the
		// precedence they overrode, so the AST carries no grouping node.
		{"const x = (1 + 2) * 3\n", `(call (. (call (. IntLit "1" add) IntLit "2") mul) IntLit "3")`},
		{"const x = !(a && b)\n", `(call (. (call (. Identifier "a" anan) Identifier "b") not))`},
		{"const x = (1)\n", `IntLit "1"`},
		// String literals are decoded at lowering: quotes dropped, escapes
		// interpreted (so the dump shows the value, which %q then re-quotes).
		{"const x = \"hi\"\n", `StringLit "hi"`},
		{"const x = \"a\\tb\\n\"\n", `StringLit "a\tb\n"`},
		{"const x = \"say \\\"hi\\\"\"\n", `StringLit "say \"hi\""`},
		{"const x = \"\\u{1F389}\"\n", `StringLit "🎉"`},
		{"const x = \"a\" == \"b\"\n", `(call (. StringLit "a" eql) StringLit "b")`},
		// Datetime and duration literals keep their raw text, exactly like
		// IntLit (their normalization happens where the constant folds), and
		// the operator desugaring carries them as receivers and arguments.
		{"const x = D2009-03-31T23:59:59.000Z\n", `DatetimeLit "D2009-03-31T23:59:59.000Z"`},
		{"const x = 3w4d5h6m7s8ms\n", `DurationLit "3w4d5h6m7s8ms"`},
		{"const x = D2009-03-31T23:59:59.000Z + 7d\n", `(call (. DatetimeLit "D2009-03-31T23:59:59.000Z" add) DurationLit "7d")`},
		{"const x = 1h > 59m\n", `(call (. DurationLit "1h" gt) DurationLit "59m")`},
		{"const x = 5m * 3\n", `(call (. DurationLit "5m" mul) IntLit "3")`},
		// Collection literals: a list renders (list elems...), a map (map k: v ...),
		// and an empty literal (collection) since its kind is not yet fixed.
		{"const x = [1, 2, 3]\n", `(list IntLit "1" IntLit "2" IntLit "3")`},
		{"const x = [\"a\": 1, \"b\": 2]\n", `(map StringLit "a": IntLit "1" StringLit "b": IntLit "2")`},
		{"const x = []\n", `(collection)`},
		{"const x = [[1], [2]]\n", `(list (list IntLit "1") (list IntLit "2"))`},
		// Record literals: the typed form keeps its type name, the inferred form
		// has none, and both carry their field initializers in source order.
		{"const x = Point{ x: 1, y: 2 }\n", `(record Point x: IntLit "1" y: IntLit "2")`},
		{"const x = { x: 1, y: 2 }\n", `(record x: IntLit "1" y: IntLit "2")`},
		{"const x = Point{\n  x: 1\n  y: 2\n}\n", `(record Point x: IntLit "1" y: IntLit "2")`},
		{"const x = Item{ pos: Point{ x: 1 } }\n", `(record Item pos: (record Point x: IntLit "1"))`},
		{"const x = {}\n", `(record)`},
		{"const x = Point{ x: }\n", `(record Point x: <missing>)`}, // recovered: value absent
		{"const x = [{ a: 1 }]\n", `(list (record a: IntLit "1"))`},
		// The ternary keeps its own node: dumpExpr renders it (? cond then else),
		// with the condition's operator desugared like any expression.
		{"const x = a ? b : c\n", `(? Identifier "a" Identifier "b" Identifier "c")`},
		{"const x = n > 1 ? 1 : 0\n", `(? (call (. Identifier "n" gt) IntLit "1") IntLit "1" IntLit "0")`},
		// Right-associative: the else-branch is itself a ternary.
		{"const x = a ? b : c ? d : e\n", `(? Identifier "a" Identifier "b" (? Identifier "c" Identifier "d" Identifier "e"))`},
		// A parenthesized ternary is one operand of the surrounding operator: the
		// grouping unwraps, leaving the ternary as the receiver of .add.
		{"const x = (a ? b : c) + 1\n", `(call (. (? Identifier "a" Identifier "b" Identifier "c") add) IntLit "1")`},
		{"const x = a ? b\n", `(? Identifier "a" Identifier "b" <missing>)`}, // recovered: else absent
		// Index reads desugar to a get call: xs[0] is xs.get(0), with the receiver
		// the collection and the index the single argument — the same call shape an
		// operator takes. Chains and operators compose around it like any postfix.
		{"const x = xs[0]\n", `(call (. Identifier "xs" get) IntLit "0")`},
		{"const x = m[\"k\"]\n", `(call (. Identifier "m" get) StringLit "k")`},
		{"const x = xs[i + 1]\n", `(call (. Identifier "xs" get) (call (. Identifier "i" add) IntLit "1"))`},
		{"const x = xs[0][1]\n", `(call (. (call (. Identifier "xs" get) IntLit "0") get) IntLit "1")`},
		{"const x = a + xs[i]\n", `(call (. Identifier "a" add) (call (. Identifier "xs" get) Identifier "i"))`},
		{"const x = [1, 2][0]\n", `(call (. (list IntLit "1" IntLit "2") get) IntLit "0")`},
		{"const x = xs[]\n", `(call (. Identifier "xs" get))`}, // recovered: index absent
	}
	for _, tc := range cases {
		if got := valueLine(t, tc.src); got != tc.want {
			t.Errorf("%q: value = %s, want %s", tc.src, got, tc.want)
		}
	}
}

// TestLowerFuncLit checks that omitted function-literal annotations lower to
// nil — the checker later fills them in from the expected type — while written
// annotations survive as type expressions.
func TestLowerFuncLit(t *testing.T) {
	file, diags := Lower([]byte("const f = fn(x: nint, y): nint { return y }\nconst g = fn(x) { return x }\n"))
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if len(file.Decls) != 2 {
		t.Fatalf("got %d decls, want 2", len(file.Decls))
	}

	f, ok := file.Decls[0].Value.(*ast.FuncLit)
	if !ok {
		t.Fatalf("decl 0 Value = %+v, want FuncLit", file.Decls[0].Value)
	}
	if len(f.Params) != 2 {
		t.Fatalf("f has %d params, want 2", len(f.Params))
	}
	if nt, ok := f.Params[0].Type.(*ast.NamedType); !ok || nt.Name != "nint" {
		t.Errorf("f param 0 Type = %+v, want NamedType nint", f.Params[0].Type)
	}
	if f.Params[1].Type != nil {
		t.Errorf("f param 1 Type = %+v, want nil (omitted)", f.Params[1].Type)
	}
	if nt, ok := f.Result.(*ast.NamedType); !ok || nt.Name != "nint" {
		t.Errorf("f Result = %+v, want NamedType nint", f.Result)
	}

	g, ok := file.Decls[1].Value.(*ast.FuncLit)
	if !ok {
		t.Fatalf("decl 1 Value = %+v, want FuncLit", file.Decls[1].Value)
	}
	if len(g.Params) != 1 || g.Params[0].Type != nil {
		t.Errorf("g params = %+v, want one unannotated param", g.Params)
	}
	if g.Result != nil {
		t.Errorf("g Result = %+v, want nil (omitted)", g.Result)
	}
}

// TestLowerArrowFuncLit checks that an arrow body normalizes to a single
// implicit return — the same FuncLit shape a block with one return lowers to,
// so the layers below never see which body form was written.
func TestLowerArrowFuncLit(t *testing.T) {
	arrow := valueLine(t, "const f = fn(x) -> x * 2\n")
	block := valueLine(t, "const f = fn(x) { return x * 2 }\n")
	if arrow != block {
		t.Errorf("arrow lowers to %s, want the block form %s", arrow, block)
	}
	if want := `(fn(x: <missing>): <missing> (return (call (. Identifier "x" mul) IntLit "2")))`; arrow != want {
		t.Errorf("arrow = %s, want %s", arrow, want)
	}

	// A result annotation survives alongside the arrow body.
	annotated := valueLine(t, "const f = fn(x): nint -> x\n")
	if want := `(fn(x: <missing>): nint (return Identifier "x"))`; annotated != want {
		t.Errorf("annotated arrow = %s, want %s", annotated, want)
	}

	// Arrow bodies nest: the outer body is the inner literal's return.
	nested := valueLine(t, "const f = fn(x) -> fn(y) -> y\n")
	if want := `(fn(x: <missing>): <missing> (return (fn(y: <missing>): <missing> (return Identifier "y"))))`; nested != want {
		t.Errorf("nested arrow = %s, want %s", nested, want)
	}
}

// TestRenderRoundTrip pins ast.Render to the desugaring: parsing then
// rendering reproduces the expression text, so the renderer's operator
// spellings and precedences cannot drift from binaryMethod/unaryMethod here
// and the precedence table in parser/concrete.
func TestRenderRoundTrip(t *testing.T) {
	cases := []string{
		"1 + 2 * 3",
		"(1 + 2) * 3",
		"(1 + 2).foo(3)", // a postfix on an operator form keeps its grouping
		"a && !b || c",
		"1 < 2 == true",
		"a == (b == c)",
		"!(a && b)",
		"-x.value",
		"Level(50)",
		"x.increment()",
		"geo.Origin",
		"[1, 2, 3].map(fn(x) { return x * 2 })",
		"[\"a\": 1, \"b\": 2]",
		"\"hi\" == \"yo\"",
		"100 % 7 - -1",
		"fn(x: nint): nint { return x }",
		"self",
		"null",
		"Point{ x: 1, y: 2 }",
		"{ x: 1, y: 2 }",
		"Item{ pos: Point{ x: 0 } }",
		"{}",
		// The ternary binds loosest: a binary condition needs no parentheses, and
		// the chain nests on the right without them either.
		"a > b ? a : b",
		"a ? b : c ? d : e",
		// A ternary inside a binary operand, or in the then-branch, is
		// parenthesized — it would otherwise rebind the surrounding operator.
		"(a ? b : c) + 1",
		"a ? (b ? c : d) : e",
		// Range literals: closed and half-open, an arithmetic bound (which binds
		// tighter, so no parentheses), and a range as a ternary branch (which
		// binds tighter than the ternary, so no parentheses).
		"0..9",
		"0...9",
		"0..n + 1",
		"b ? 0..9 : 1..2",
		// A range as a binary operand is parenthesized — it binds looser than the
		// operator, so the grouping is what keeps the operand a whole range.
		"(0..9).count()",
		"a || (0..9)",
	}
	for _, expr := range cases {
		src := "const x = " + expr + "\n"
		file, diags := Lower([]byte(src))
		if len(diags) != 0 {
			t.Errorf("%q: unexpected diagnostics: %v", expr, diags)
			continue
		}
		if got := ast.Render(file.Decls[0].Value); got != expr {
			t.Errorf("Render = %q, want %q", got, expr)
		}
	}
}

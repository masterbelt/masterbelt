package abstract

import (
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source/ast"
)

func TestLowerConstDecl(t *testing.T) {
	file, diags := Lower([]byte("/// the max\n/// second line\npub const MaxLevel: int64 = 100\n"))
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if len(file.Decls) != 1 {
		t.Fatalf("got %d decls, want 1", len(file.Decls))
	}
	d := file.Decls[0]
	if !d.Public {
		t.Error("Public = false, want true")
	}
	if d.Name != "MaxLevel" {
		t.Errorf("Name = %q, want MaxLevel", d.Name)
	}
	if nt, ok := d.Type.(*ast.NamedType); !ok || nt.Name != "int64" {
		t.Errorf("Type = %+v, want NamedType int64", d.Type)
	}
	lit, ok := d.Value.(*ast.IntLit)
	if !ok || lit.Text != "100" {
		t.Errorf("Value = %+v, want IntLit 100", d.Value)
	}
	if len(d.Doc) != 2 || d.Doc[0] != "the max" || d.Doc[1] != "second line" {
		t.Errorf("Doc = %q, want [the max, second line]", d.Doc)
	}
}

func TestLowerInference(t *testing.T) {
	file, _ := Lower([]byte("const MinLevel = 0\nconst Alias = MinLevel\n"))
	if len(file.Decls) != 2 {
		t.Fatalf("got %d decls, want 2", len(file.Decls))
	}
	if d := file.Decls[0]; d.Type != nil {
		t.Errorf("decl 0 Type = %+v, want nil (inferred)", d.Type)
	}
	ref, ok := file.Decls[1].Value.(*ast.Identifier)
	if !ok || ref.Name != "MinLevel" {
		t.Errorf("decl 1 Value = %+v, want Identifier MinLevel", file.Decls[1].Value)
	}
}

// valueLine lowers src and returns the rendered initializer of its first
// declaration, read from the AST dump so the test exercises the same rendering
// the snapshots use.
func valueLine(t *testing.T, src string) string {
	t.Helper()
	file, _ := Lower([]byte(src))
	for _, line := range strings.Split(ast.Dump(file), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "value "); ok {
			return rest
		}
	}
	t.Fatalf("no value line in dump of %q", src)
	return ""
}

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
		// Collection literals: a list renders (list elems...), a map (map k: v ...),
		// and an empty literal (collection) since its kind is not yet fixed.
		{"const x = [1, 2, 3]\n", `(list IntLit "1" IntLit "2" IntLit "3")`},
		{"const x = [\"a\": 1, \"b\": 2]\n", `(map StringLit "a": IntLit "1" StringLit "b": IntLit "2")`},
		{"const x = []\n", `(collection)`},
		{"const x = [[1], [2]]\n", `(list (list IntLit "1") (list IntLit "2"))`},
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
	file, diags := Lower([]byte("const f = fn(x: int, y): int { return y }\nconst g = fn(x) { return x }\n"))
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
	if nt, ok := f.Params[0].Type.(*ast.NamedType); !ok || nt.Name != "int" {
		t.Errorf("f param 0 Type = %+v, want NamedType int", f.Params[0].Type)
	}
	if f.Params[1].Type != nil {
		t.Errorf("f param 1 Type = %+v, want nil (omitted)", f.Params[1].Type)
	}
	if nt, ok := f.Result.(*ast.NamedType); !ok || nt.Name != "int" {
		t.Errorf("f Result = %+v, want NamedType int", f.Result)
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

func TestLowerUseDecl(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want ast.UseDecl
	}{
		{"namespace", "use geo from \"geometry.belt\"\n",
			ast.UseDecl{Namespace: "geo", Path: "geometry.belt"}},
		{"selective", "use { Point, Vector } from \"shapes.belt\"\n",
			ast.UseDecl{Names: []string{"Point", "Vector"}, Path: "shapes.belt"}},
		{"wildcard", "use * from \"prelude.belt\"\n",
			ast.UseDecl{Star: true, Path: "prelude.belt"}},
		{"re-export", "pub use { Color } from \"palette.belt\"\n",
			ast.UseDecl{Public: true, Names: []string{"Color"}, Path: "palette.belt"}},
		{"barrel", "pub use * from \"geometry.belt\"\n",
			ast.UseDecl{Public: true, Star: true, Path: "geometry.belt"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			file, diags := Lower([]byte(tc.src))
			if len(diags) != 0 {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			if len(file.Uses) != 1 {
				t.Fatalf("got %d uses, want 1", len(file.Uses))
			}
			u := file.Uses[0]
			if u.Public != tc.want.Public || u.Namespace != tc.want.Namespace || u.Star != tc.want.Star || u.Path != tc.want.Path {
				t.Errorf("use = %+v, want %+v", u, tc.want)
			}
			if strings.Join(u.Names, ",") != strings.Join(tc.want.Names, ",") {
				t.Errorf("Names = %v, want %v", u.Names, tc.want.Names)
			}
			if u.Syntax() == nil {
				t.Error("Syntax() = nil, want the backing CST node")
			}
		})
	}
}

func TestLowerUseDeclMalformed(t *testing.T) {
	// A missing path lowers to "", not a panic; the decl is still present so
	// later layers can anchor diagnostics to it.
	file, diags := Lower([]byte("use geo from\n"))
	if len(diags) == 0 {
		t.Fatal("expected a diagnostic for the missing path")
	}
	if len(file.Uses) != 1 {
		t.Fatalf("got %d uses, want 1", len(file.Uses))
	}
	if u := file.Uses[0]; u.Namespace != "geo" || u.Path != "" {
		t.Errorf("use = %+v, want namespace geo with empty Path", u)
	}
}

func TestLowerAssertDecl(t *testing.T) {
	file, diags := Lower([]byte("/// the range is not empty\nassert Max > Min\n"))
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if len(file.Asserts) != 1 {
		t.Fatalf("got %d asserts, want 1", len(file.Asserts))
	}
	a := file.Asserts[0]
	if len(a.Doc) != 1 || a.Doc[0] != "the range is not empty" {
		t.Errorf("Doc = %q, want [the range is not empty]", a.Doc)
	}
	// The condition desugars like any expression: Max > Min is Max.gt(Min).
	want := `cond (call (. Identifier "Max" gt) Identifier "Min")`
	if got := ast.Dump(file); !strings.Contains(got, want) {
		t.Errorf("dump = %s, want it to contain %s", got, want)
	}
	if a.Syntax() == nil {
		t.Error("Syntax() = nil, want the backing CST node")
	}
}

func TestLowerAssertDeclMalformed(t *testing.T) {
	// A missing expression lowers to a nil Cond, not a panic; the decl is still
	// present so the semantic layer can anchor diagnostics to it.
	file, diags := Lower([]byte("assert\n"))
	if len(diags) == 0 {
		t.Fatal("expected a diagnostic for the missing expression")
	}
	if len(file.Asserts) != 1 {
		t.Fatalf("got %d asserts, want 1", len(file.Asserts))
	}
	if a := file.Asserts[0]; a.Cond != nil {
		t.Errorf("Cond = %+v, want nil", a.Cond)
	}
}

func TestLowerSkipsNonDecls(t *testing.T) {
	// A stray "= 1" forms an Error node in the CST; it must not appear as a decl.
	file, diags := Lower([]byte("= 1\nconst X = 2\n"))
	if len(diags) == 0 {
		t.Fatal("expected a diagnostic for the stray tokens")
	}
	if len(file.Decls) != 1 || file.Decls[0].Name != "X" {
		t.Fatalf("got %d decls, want only X", len(file.Decls))
	}
}

func TestLowerMalformedRecovers(t *testing.T) {
	// Missing initializer: the decl is still present, with a nil Value.
	file, _ := Lower([]byte("const X\n"))
	if len(file.Decls) != 1 {
		t.Fatalf("got %d decls, want 1", len(file.Decls))
	}
	if d := file.Decls[0]; d.Name != "X" || d.Value != nil {
		t.Errorf("decl = %+v, want name X with nil Value", d)
	}

	// An empty grouping unwraps to nothing: a nil Value, not a panic.
	file, diags := Lower([]byte("const X = ()\n"))
	if len(diags) == 0 {
		t.Fatal("expected a diagnostic for the empty grouping")
	}
	if d := file.Decls[0]; d.Value != nil {
		t.Errorf("decl Value = %+v, want nil", d.Value)
	}
}

func TestSyntaxBacklink(t *testing.T) {
	d := NewDocument([]byte("const X = 1\n"))
	decl := d.File().Decls[0]
	// The AST node's Syntax link is the same green node the concrete tree holds.
	if decl.Syntax() != d.Concrete().Root().Children()[0] {
		t.Error("ConstDecl.Syntax() is not the backing CST node")
	}
}

// TestLowerQualifiedTypeName: a namespace-qualified type annotation carries
// its qualifier and name separately; a dangling qualifier (geo.) keeps the
// namespace with an empty name, mirroring the parser's recovery.
func TestLowerQualifiedTypeName(t *testing.T) {
	cases := []struct {
		name      string
		src       string
		namespace string
		typeName  string
	}{
		{"qualified", "const a: geo.Point = 1\n", "geo", "Point"},
		{"plain", "const a: Point = 1\n", "", "Point"},
		{"dangling qualifier", "const a: geo. = 1\n", "geo", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			file, _ := Lower([]byte(tc.src))
			if len(file.Decls) != 1 {
				t.Fatalf("got %d decls, want 1", len(file.Decls))
			}
			named, ok := file.Decls[0].Type.(*ast.NamedType)
			if !ok {
				t.Fatalf("type = %T, want *ast.NamedType", file.Decls[0].Type)
			}
			if named.Namespace != tc.namespace || named.Name != tc.typeName {
				t.Errorf("type = %q.%q, want %q.%q", named.Namespace, named.Name, tc.namespace, tc.typeName)
			}
		})
	}
}

// TestLowerQualifiedTypeNameInGenericArg: the qualifier survives nested type
// positions (list<geo.Point>).
func TestLowerQualifiedTypeNameInGenericArg(t *testing.T) {
	file, diags := Lower([]byte("const a: list<geo.Point> = [1]\n"))
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	outer, ok := file.Decls[0].Type.(*ast.NamedType)
	if !ok || outer.Name != "list" || len(outer.Args) != 1 {
		t.Fatalf("type = %+v, want list with one argument", file.Decls[0].Type)
	}
	arg, ok := outer.Args[0].(*ast.NamedType)
	if !ok || arg.Namespace != "geo" || arg.Name != "Point" {
		t.Fatalf("argument = %+v, want geo.Point", outer.Args[0])
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
		"fn(x: int): int { return x }",
		"self",
		"null",
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

func TestLowerMethodDoc(t *testing.T) {
	// A doc comment before a method attaches to the method, not to the
	// surrounding impl block.
	file, diags := Lower([]byte("type L = int8 impl {\n  /// bumps the level\n  inc(): self {\n    return self\n  }\n}\n"))
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	m := file.Types[0].Methods[0]
	if len(m.Doc) != 1 || m.Doc[0] != "bumps the level" {
		t.Fatalf("method Doc = %q, want [bumps the level]", m.Doc)
	}
}

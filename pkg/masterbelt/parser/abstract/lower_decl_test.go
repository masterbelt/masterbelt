// This file tests the lowering of top-level and member declarations — const,
// use, assert, function, and type declarations together with the impl methods
// they carry — mirroring lower_decl.go.

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

// TestLowerFuncDecl checks the function-declaration lowering: modifiers, doc,
// name, annotated parameters, the required result type, and both body forms —
// the arrow body normalized to a single implicit return.
func TestLowerFuncDecl(t *testing.T) {
	src := "/// doubles x\npub fn double(x: int): int -> x * 2\nfn area(w: int, h: int): int {\n  return w * h\n}\n"
	file, diags := Lower([]byte(src))
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if len(file.Funcs) != 2 {
		t.Fatalf("got %d funcs, want 2", len(file.Funcs))
	}

	d := file.Funcs[0]
	if !d.Public || d.Name != "double" {
		t.Errorf("func 0 = pub %v name %q, want pub double", d.Public, d.Name)
	}
	if len(d.Doc) != 1 || d.Doc[0] != "doubles x" {
		t.Errorf("func 0 doc = %v, want [doubles x]", d.Doc)
	}
	if len(d.Params) != 1 || d.Params[0].Name != "x" {
		t.Fatalf("func 0 params = %v, want [x]", d.Params)
	}
	if nt, ok := d.Params[0].Type.(*ast.NamedType); !ok || nt.Name != "int" {
		t.Errorf("func 0 param type = %+v, want int", d.Params[0].Type)
	}
	if nt, ok := d.Result.(*ast.NamedType); !ok || nt.Name != "int" {
		t.Errorf("func 0 result = %+v, want int", d.Result)
	}
	// The arrow body is one implicit return.
	if len(d.Body) != 1 {
		t.Fatalf("func 0 body = %v, want one return", d.Body)
	}
	if _, ok := d.Body[0].(*ast.ReturnStmt); !ok {
		t.Errorf("func 0 body stmt = %T, want ReturnStmt", d.Body[0])
	}

	a := file.Funcs[1]
	if a.Public || a.Name != "area" || len(a.Params) != 2 || len(a.Body) != 1 {
		t.Errorf("func 1 = %+v, want area(w, h) with one return", a)
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

func TestLowerTypeDeclWhere(t *testing.T) {
	file, diags := Lower([]byte("pub type Port = int32 where self >= 1 && self <= 65535\n"))
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if len(file.Types) != 1 {
		t.Fatalf("got %d types, want 1", len(file.Types))
	}
	// The predicate desugars like any expression: self >= 1 && self <= 65535 is
	// self.gteq(1).anan(self.lteq(65535)).
	want := `where (call (. (call (. self gteq) IntLit "1") anan) (call (. self lteq) IntLit "65535"))`
	if got := ast.Dump(file); !strings.Contains(got, want) {
		t.Errorf("dump = %s, want it to contain %s", got, want)
	}
}

func TestLowerTypeDeclWhereMalformed(t *testing.T) {
	// A missing predicate lowers to a nil Where, not a panic; the decl is still
	// present so the semantic layer can anchor diagnostics to it.
	file, diags := Lower([]byte("type Bad = int8 where\n"))
	if len(diags) == 0 {
		t.Fatal("expected a diagnostic for the missing predicate")
	}
	if len(file.Types) != 1 {
		t.Fatalf("got %d types, want 1", len(file.Types))
	}
	if d := file.Types[0]; d.Where != nil {
		t.Errorf("Where = %+v, want nil", d.Where)
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

func TestLowerImplConst(t *testing.T) {
	// An impl block's const items lower to the type's Consts, alongside its
	// Methods. A typed const keeps its annotation; a method beside them is
	// unaffected.
	src := "type L = int8 impl {\n  /// the cap\n  pub const Max = 100\n  const Width: int32 = 32\n  pub inc(): self {\n    return self + 1\n  }\n}\n"
	file, diags := Lower([]byte(src))
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	d := file.Types[0]
	if len(d.Consts) != 2 {
		t.Fatalf("got %d consts, want 2", len(d.Consts))
	}
	if len(d.Methods) != 1 || d.Methods[0].Name != "inc" {
		t.Fatalf("methods = %+v, want one inc method", d.Methods)
	}
	max := d.Consts[0]
	if !max.Public || max.Name != "Max" || max.Builtin {
		t.Errorf("Max const = %+v, want pub non-builtin Max", max)
	}
	if len(max.Doc) != 1 || max.Doc[0] != "the cap" {
		t.Errorf("Max doc = %q, want [the cap]", max.Doc)
	}
	width := d.Consts[1]
	if width.Public || width.Name != "Width" || width.Type == nil {
		t.Errorf("Width const = %+v, want a typed bare const", width)
	}
	if got := ast.Dump(file); !strings.Contains(got, "const \"Max\"") || !strings.Contains(got, "const \"Width\"") {
		t.Errorf("dump = %s, want it to contain the consts", got)
	}
}

func TestLowerImplConstBuiltin(t *testing.T) {
	// A `= builtin` associated constant lowers to a Builtin-marked const with no
	// Value — its value comes from the registry, not an initializer.
	src := "type I8 = builtin impl {\n  pub const Max = builtin\n  pub const Min = builtin\n}\n"
	file, diags := Lower([]byte(src))
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	d := file.Types[0]
	if len(d.Consts) != 2 {
		t.Fatalf("got %d consts, want 2", len(d.Consts))
	}
	for _, c := range d.Consts {
		if !c.Builtin {
			t.Errorf("const %q: want Builtin set", c.Name)
		}
		if c.Value != nil {
			t.Errorf("const %q: want no Value, got %v", c.Name, c.Value)
		}
	}
	if got := ast.Dump(file); !strings.Contains(got, "builtin") {
		t.Errorf("dump = %s, want it to contain builtin", got)
	}
}

func TestLowerEnumImplConst(t *testing.T) {
	// An enum's impl block carries associated constants too, the same mechanism
	// a type declaration's does.
	src := "enum Color {\n  Red, Green, Blue\n} impl {\n  pub const Count = 3\n}\n"
	file, diags := Lower([]byte(src))
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	d := file.Enums[0]
	if len(d.Consts) != 1 || d.Consts[0].Name != "Count" {
		t.Fatalf("enum consts = %+v, want one Count const", d.Consts)
	}
}

func TestLowerEnumDecl(t *testing.T) {
	src := "/// rarity tier\npub enum Rarity: uint8 {\n  Common = 1\n  Rare = 2\n  Legend = 10\n}\n"
	file, diags := Lower([]byte(src))
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if len(file.Enums) != 1 {
		t.Fatalf("got %d enums, want 1", len(file.Enums))
	}
	d := file.Enums[0]
	if !d.Public || d.Name != "Rarity" {
		t.Errorf("enum = %+v, want pub Rarity", d)
	}
	if len(d.Doc) != 1 || d.Doc[0] != "rarity tier" {
		t.Errorf("doc = %q, want [rarity tier]", d.Doc)
	}
	if got := ast.Dump(file); !strings.Contains(got, "base uint8") {
		t.Errorf("dump = %s, want it to contain base uint8", got)
	}
	if len(d.Members) != 3 {
		t.Fatalf("got %d members, want 3", len(d.Members))
	}
	names := []string{}
	for _, m := range d.Members {
		names = append(names, m.Name)
		if m.Value == nil {
			t.Errorf("member %q: want an initializer value", m.Name)
		}
	}
	if strings.Join(names, ",") != "Common,Rare,Legend" {
		t.Errorf("member names = %v, want [Common Rare Legend]", names)
	}
}

func TestLowerEnumNoBaseWithImpl(t *testing.T) {
	src := "enum Element {\n  Fire, Water, Wind\n} impl {\n  isFire(): bool {\n    return self == Element.Fire\n  }\n}\n"
	file, diags := Lower([]byte(src))
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	d := file.Enums[0]
	if d.Base != nil {
		t.Errorf("base = %+v, want nil (default int)", d.Base)
	}
	if len(d.Members) != 3 {
		t.Fatalf("got %d members, want 3", len(d.Members))
	}
	for _, m := range d.Members {
		if m.Value != nil {
			t.Errorf("member %q: want no initializer (auto-numbered)", m.Name)
		}
	}
	if len(d.Methods) != 1 || d.Methods[0].Name != "isFire" {
		t.Fatalf("methods = %+v, want one isFire method", d.Methods)
	}
}

func TestLowerEnumStringBase(t *testing.T) {
	src := "enum Locale: string {\n  Ja\n  En = \"en-US\"\n}\n"
	file, diags := Lower([]byte(src))
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	d := file.Enums[0]
	if got := ast.Dump(file); !strings.Contains(got, "base string") {
		t.Errorf("dump = %s, want it to contain base string", got)
	}
	if d.Members[0].Value != nil {
		t.Errorf("Ja: want no initializer (name-defaulted)")
	}
	lit, ok := d.Members[1].Value.(*ast.StringLit)
	if !ok || lit.Value != "en-US" {
		t.Errorf("En value = %+v, want StringLit en-US", d.Members[1].Value)
	}
}

func TestLowerEnumMalformedRecovers(t *testing.T) {
	// A missing member value lowers to a nil Value, not a panic; the member is
	// still present so the semantic layer can anchor diagnostics to it.
	file, diags := Lower([]byte("enum E {\n  A =\n}\n"))
	if len(diags) == 0 {
		t.Fatal("expected a diagnostic for the missing member value")
	}
	if len(file.Enums) != 1 || len(file.Enums[0].Members) != 1 {
		t.Fatalf("enums = %+v, want one enum with one member", file.Enums)
	}
	if m := file.Enums[0].Members[0]; m.Name != "A" || m.Value != nil {
		t.Errorf("member = %+v, want name A with nil Value", m)
	}
}

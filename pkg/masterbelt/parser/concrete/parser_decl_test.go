// This file tests the parsing of declarations — const, type, enum, interface,
// use, assert, function, impl, and the generic/where clauses they carry —
// mirroring parser_decl.go.
package concrete

import (
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/source"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/token"
)

func TestParseConstDeclChildren(t *testing.T) {
	root, _ := Parse([]byte("const Max: long = 100"))
	decl := root.Children()[0].(*cst.Node)
	if decl.Kind() != cst.ConstDecl {
		t.Fatalf("first child kind = %s, want ConstDecl", decl.Kind())
	}
	var nodeKinds []cst.Kind
	for _, c := range decl.Children() {
		if n, ok := c.(*cst.Node); ok {
			nodeKinds = append(nodeKinds, n.Kind())
		}
	}
	if len(nodeKinds) != 2 || nodeKinds[0] != cst.TypeClause || nodeKinds[1] != cst.Initializer {
		t.Fatalf("decl sub-nodes = %v, want [TypeClause Initializer]", nodeKinds)
	}
}

// TestParseTypeDeclFileShape checks that type declarations are recognised at the
// file level and that the const/type choice is made by looking past pub.
func TestParseTypeDeclFileShape(t *testing.T) {
	root, diags := Parse([]byte("const X = 1\npub type Coin = sbyte\ntype Pair = A | B\n"))
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	got := declKinds(root)
	want := []string{"ConstDecl", "TypeDecl", "TypeDecl", "<Newline>", "<EOF>"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("file children = %v, want %v", got, want)
	}
}

func TestParseTypeDeclChildren(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []cst.Kind
	}{
		{"nominal", "type Coin = sbyte\n", []cst.Kind{cst.TypeName}},
		{"union", "type Pair = A | B\n", []cst.Kind{cst.UnionType}},
		{"generic union", "pub type Opt<T> = T | null\n", []cst.Kind{cst.GenericParams, cst.UnionType}},
		{"constrained generic", "type Num<T: sbyte | short> = T\n", []cst.Kind{cst.GenericParams, cst.TypeName}},
		{"record", "type Rec = {\n  a: sbyte\n}\n", []cst.Kind{cst.RecordType}},
		{"func type", "type M<T, R> = fn(src: T): R\n", []cst.Kind{cst.GenericParams, cst.FuncType}},
		{"impl", "type Lvl = sbyte impl {\n  pub inc(): self {\n    return self\n  }\n}\n", []cst.Kind{cst.TypeName, cst.ImplBlock}},
		{"null name", "pub type null = builtin\n", []cst.Kind{cst.BuiltinType}}, // null may be declared
		{"where", "type Port = int where self <= 65535\n", []cst.Kind{cst.TypeName, cst.WhereClause}},
		{"where impl", "type Pct = sbyte where self >= 0 impl {\n  inc(): self {\n    return self\n  }\n}\n", []cst.Kind{cst.TypeName, cst.WhereClause, cst.ImplBlock}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, diags := Parse([]byte(tc.src))
			if len(diags) != 0 {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			decl := root.Children()[0].(*cst.Node)
			if decl.Kind() != cst.TypeDecl {
				t.Fatalf("first child kind = %s, want TypeDecl", decl.Kind())
			}
			got := subNodeKinds(decl)
			if len(got) != len(tc.want) {
				t.Fatalf("sub-nodes = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("sub-nodes = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// TestParseImplConst checks that an impl block's associated-constant items are
// recognised as ConstDecl nodes (the same node a top-level constant uses),
// alongside its methods, and that the const/method choice looks past pub.
func TestParseImplConst(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []cst.Kind // the impl block's direct child node kinds
	}{
		{"pub const and bare const", "type L = sbyte impl {\n  pub const Max = 100\n  const Min = 0\n}\n",
			[]cst.Kind{cst.ConstDecl, cst.ConstDecl}},
		{"const then method", "type L = sbyte impl {\n  const Max = 100\n  pub inc(): self {\n    return self\n  }\n}\n",
			[]cst.Kind{cst.ConstDecl, cst.MethodDecl}},
		{"typed const", "type B = int impl {\n  pub const Width: int = 32\n}\n",
			[]cst.Kind{cst.ConstDecl}},
		{"builtin const", "type I8 = builtin impl {\n  pub const Max = builtin\n}\n",
			[]cst.Kind{cst.ConstDecl}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, diags := Parse([]byte(tc.src))
			if len(diags) != 0 {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			decl := root.Children()[0].(*cst.Node)
			var impl *cst.Node
			for _, c := range decl.Children() {
				if n, ok := c.(*cst.Node); ok && n.Kind() == cst.ImplBlock {
					impl = n
				}
			}
			if impl == nil {
				t.Fatalf("no impl block found in %q", tc.src)
			}
			got := subNodeKinds(impl)
			if len(got) != len(tc.want) {
				t.Fatalf("impl child nodes = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("impl child nodes = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// TestParseImplConstChildren checks the sub-node shape of an associated
// constant: a typed one carries a TypeClause then an Initializer; an untyped
// one only an Initializer; and a "= builtin" one an Initializer wrapping a
// BuiltinType.
func TestParseImplConstChildren(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []cst.Kind
	}{
		{"untyped", "type L = sbyte impl {\n  const Max = 100\n}\n", []cst.Kind{cst.Initializer}},
		{"typed", "type B = int impl {\n  const Width: int = 32\n}\n", []cst.Kind{cst.TypeClause, cst.Initializer}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, diags := Parse([]byte(tc.src))
			if len(diags) != 0 {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			decl := root.Children()[0].(*cst.Node)
			c := findImplConstDecl(decl)
			if c == nil {
				t.Fatalf("no impl ConstDecl found in %q", tc.src)
			}
			got := subNodeKinds(c)
			if len(got) != len(tc.want) {
				t.Fatalf("const sub-nodes = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("const sub-nodes = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// findImplConstDecl returns the last ConstDecl node nested in decl's ImplBlock
// children, or nil when none is present.
func findImplConstDecl(decl *cst.Node) *cst.Node {
	var c *cst.Node
	for _, child := range decl.Children() {
		n, ok := child.(*cst.Node)
		if !ok || n.Kind() != cst.ImplBlock {
			continue
		}
		for _, ic := range n.Children() {
			if cn, ok := ic.(*cst.Node); ok && cn.Kind() == cst.ConstDecl {
				c = cn
			}
		}
	}
	return c
}

// TestParseInterfaceDeclFileShape checks that an interface declaration is
// recognised at the file level, the choice made by looking past pub.
func TestParseInterfaceDeclFileShape(t *testing.T) {
	root, diags := Parse([]byte("pub interface foldable<T> {\n  fold(init: T): T\n}\n"))
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	got := declKinds(root)
	want := []string{"InterfaceDecl", "<Newline>", "<EOF>"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("file children = %v, want %v", got, want)
	}
}

// TestParseInterfaceDeclChildren checks the sub-node shape of an interface: its
// optional generic parameters and its members (required and provided alike land
// as InterfaceMember nodes).
func TestParseInterfaceDeclChildren(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []cst.Kind
	}{
		{"required only", "interface eq {\n  eql(other: self): bool\n}\n",
			[]cst.Kind{cst.InterfaceMember}},
		{"generic with provided", "pub interface foldable<K, V> {\n  fold<A>(init: A): A\n  pub count(): nint {\n    return 0\n  }\n}\n",
			[]cst.Kind{cst.GenericParams, cst.InterfaceMember, cst.InterfaceMember}},
		{"empty", "interface marker {\n}\n", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, diags := Parse([]byte(tc.src))
			if len(diags) != 0 {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			decl := root.Children()[0].(*cst.Node)
			if decl.Kind() != cst.InterfaceDecl {
				t.Fatalf("first child kind = %s, want InterfaceDecl", decl.Kind())
			}
			got := subNodeKinds(decl)
			if len(got) != len(tc.want) {
				t.Fatalf("sub-nodes = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("sub-nodes = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// TestParseInterfaceParents checks the parent-interface list: a single parent,
// several comma-separated parents, and an applied (generic) parent all land as
// an InterfaceParents node before the members, after the optional generic
// parameters.
func TestParseInterfaceParents(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []cst.Kind
	}{
		{"single", "interface a: b {\n}\n",
			[]cst.Kind{cst.InterfaceParents}},
		{"multiple", "interface a: b, c {\n}\n",
			[]cst.Kind{cst.InterfaceParents}},
		{"generic parent", "interface a: foldable<nint, T> {\n}\n",
			[]cst.Kind{cst.InterfaceParents}},
		{"after generic params", "interface a<T>: b {\n}\n",
			[]cst.Kind{cst.GenericParams, cst.InterfaceParents}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, diags := Parse([]byte(tc.src))
			if len(diags) != 0 {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			decl := root.Children()[0].(*cst.Node)
			if decl.Kind() != cst.InterfaceDecl {
				t.Fatalf("first child kind = %s, want InterfaceDecl", decl.Kind())
			}
			got := subNodeKinds(decl)
			if len(got) != len(tc.want) {
				t.Fatalf("sub-nodes = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("sub-nodes = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// TestParseInterfaceParentsRecovery checks that a colon with no parent after it
// records a diagnostic and still produces an InterfaceParents node, keeping the
// parse lossless and the rest of the declaration intact.
func TestParseInterfaceParentsRecovery(t *testing.T) {
	root, diags := Parse([]byte("interface a: {\n}\n"))
	if len(diags) == 0 {
		t.Fatalf("expected a diagnostic for the missing parent")
	}
	decl := root.Children()[0].(*cst.Node)
	if decl.Kind() != cst.InterfaceDecl {
		t.Fatalf("first child kind = %s, want InterfaceDecl", decl.Kind())
	}
	var parents *cst.Node
	for _, c := range decl.Children() {
		if n, ok := c.(*cst.Node); ok && n.Kind() == cst.InterfaceParents {
			parents = n
		}
	}
	if parents == nil {
		t.Fatalf("no InterfaceParents node in %q", "interface a: {\n}\n")
	}
}

// TestParseInterfaceMemberChildren checks that a required member carries no
// Block (only its ParamList and result type) while a provided member carries a
// Block, and that an explicit member type parameter lands as GenericParams.
func TestParseInterfaceMemberChildren(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []cst.Kind
	}{
		{"required", "interface i {\n  m(x: nint): nint\n}\n",
			[]cst.Kind{cst.ParamList, cst.TypeName}},
		{"provided", "interface i {\n  m(x: nint): nint {\n    return x\n  }\n}\n",
			[]cst.Kind{cst.ParamList, cst.TypeName, cst.Block}},
		{"generic required", "interface i {\n  fold<A>(init: A): A\n}\n",
			[]cst.Kind{cst.GenericParams, cst.ParamList, cst.TypeName}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, diags := Parse([]byte(tc.src))
			if len(diags) != 0 {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			decl := root.Children()[0].(*cst.Node)
			var member *cst.Node
			for _, c := range decl.Children() {
				if n, ok := c.(*cst.Node); ok && n.Kind() == cst.InterfaceMember {
					member = n
				}
			}
			if member == nil {
				t.Fatalf("no interface member found in %q", tc.src)
			}
			got := subNodeKinds(member)
			if len(got) != len(tc.want) {
				t.Fatalf("member sub-nodes = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("member sub-nodes = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// TestParseImplInterfaceTag checks that the optional interface name after impl
// lands as a TypeName child of the impl block, before its brace, while a bare
// impl carries no such tag.
func TestParseImplInterfaceTag(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []cst.Kind // the impl block's direct child node kinds
	}{
		{"tagged", "type Bag<T> = list<T> impl foldable<nint, T> {\n  fold<A>(init: A): A {\n    return init\n  }\n}\n",
			[]cst.Kind{cst.TypeName, cst.MethodDecl}},
		{"bare", "type L = sbyte impl {\n  inc(): self {\n    return self\n  }\n}\n",
			[]cst.Kind{cst.MethodDecl}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, diags := Parse([]byte(tc.src))
			if len(diags) != 0 {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			decl := root.Children()[0].(*cst.Node)
			var impl *cst.Node
			for _, c := range decl.Children() {
				if n, ok := c.(*cst.Node); ok && n.Kind() == cst.ImplBlock {
					impl = n
				}
			}
			if impl == nil {
				t.Fatalf("no impl block found in %q", tc.src)
			}
			got := subNodeKinds(impl)
			if len(got) != len(tc.want) {
				t.Fatalf("impl child nodes = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("impl child nodes = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// TestParseWhereClauseDiagnostics checks local recovery for malformed
// where-clauses.
func TestParseWhereClauseDiagnostics(t *testing.T) {
	cases := []struct {
		name string
		src  string
		code diagnostic.Code
	}{
		{"missing predicate", "type Bad = sbyte where\n", CodeExpectedExpression},
		{"keyword predicate", "type Bad = sbyte where impl {}\n", CodeExpectedExpression},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, diags := Parse([]byte(tc.src))
			found := false
			for _, d := range diags {
				if d.Code == tc.code {
					found = true
				}
			}
			if !found {
				t.Fatalf("src %q: want diagnostic %s, got %v", tc.src, tc.code, diags)
			}
			assertLossless(t, tc.src)
		})
	}
}

// TestParseFuncDecl checks the top-level function declaration: both body
// forms, the pub modifier, and the file-level dispatch on fn followed by a
// name.
func TestParseFuncDecl(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []cst.Kind
	}{
		{"block body", "fn area(w: nint, h: nint): nint {\n  return w * h\n}\n", []cst.Kind{cst.ParamList, cst.TypeName, cst.Block}},
		{"arrow body", "fn double(x: nint): nint -> x * 2\n", []cst.Kind{cst.ParamList, cst.TypeName, cst.BinaryExpr}},
		{"pub", "pub fn zero(): nint -> 0\n", []cst.Kind{cst.ParamList, cst.TypeName, cst.Literal}},
		{"record result", "pub fn origin(): Point -> Point{ x: 0 }\n", []cst.Kind{cst.ParamList, cst.TypeName, cst.RecordLit}},
		{"unbounded type param", "fn id<T>(x: T): nint -> 0\n", []cst.Kind{cst.GenericParams, cst.ParamList, cst.TypeName, cst.Literal}},
		{"bounded type param", "fn total<T: foldable<nint, nint>>(c: T): nint -> 0\n", []cst.Kind{cst.GenericParams, cst.ParamList, cst.TypeName, cst.Literal}},
		{"several type params", "fn pair<T, U>(a: T, b: U): nint -> 0\n", []cst.Kind{cst.GenericParams, cst.ParamList, cst.TypeName, cst.Literal}},
		{"parameterized bound", "fn first<T: foldable<U>, U>(c: T): nint -> 0\n", []cst.Kind{cst.GenericParams, cst.ParamList, cst.TypeName, cst.Literal}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, diags := Parse([]byte(tc.src))
			if len(diags) != 0 {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			assertLossless(t, tc.src)
			decl := root.Children()[0].(*cst.Node)
			if decl.Kind() != cst.FuncDecl {
				t.Fatalf("first child kind = %s, want FuncDecl", decl.Kind())
			}
			got := subNodeKinds(decl)
			if len(got) != len(tc.want) {
				t.Fatalf("sub-nodes = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("sub-nodes = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// TestParseFnThreeUses pins the three uses of fn apart: a function type (type
// position), a function literal (value position, no name), and a function
// declaration (top level, a name follows) — all in one file, parse-clean.
func TestParseFnThreeUses(t *testing.T) {
	src := "type F = fn(x: nint): nint\nconst g = fn(x) -> x\nfn h(x: nint): nint -> x\n"
	root, diags := Parse([]byte(src))
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	assertLossless(t, src)
	got := declKinds(root)
	want := []string{"TypeDecl", "ConstDecl", "FuncDecl", "<Newline>", "<EOF>"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("file children = %v, want %v", got, want)
	}
}

// TestParseFuncDeclRecovery checks the malformed declaration forms: a pub fn
// missing its name still parses as a (reported) declaration, a bare nameless
// fn is a stray expression, and an error run stops before a following
// function declaration.
// funcDeclRecoveryCases drives TestParseFuncDeclRecovery: each case parses src
// and runs check against the resulting tree and diagnostics. The per-case
// assertions vary (some inspect the first child's kind, one the full file-child
// sequence, some only assert a diagnostic code), so they ride in a closure rather
// than being flattened to columns.
var funcDeclRecoveryCases = []struct {
	name  string
	src   string
	check func(t *testing.T, src string, root *cst.Node, diags []diagnostic.Diagnostic)
}{
	{
		name: "pub fn without a name is a reported FuncDecl",
		src:  "pub fn(x: nint): nint -> x\n",
		check: func(t *testing.T, src string, root *cst.Node, diags []diagnostic.Diagnostic) {
			t.Helper()
			if len(diags) == 0 {
				t.Fatal("want a diagnostic for the missing name")
			}
			assertLossless(t, src)
			decl := root.Children()[0].(*cst.Node)
			if decl.Kind() != cst.FuncDecl {
				t.Fatalf("first child kind = %s, want FuncDecl", decl.Kind())
			}
		},
	},
	{
		name: "bare nameless fn is an error run",
		src:  "fn(x: nint): nint -> x\n",
		check: func(t *testing.T, src string, root *cst.Node, diags []diagnostic.Diagnostic) {
			t.Helper()
			if len(diags) == 0 {
				t.Fatal("want a diagnostic for the stray literal")
			}
			assertLossless(t, src)
			decl := root.Children()[0].(*cst.Node)
			if decl.Kind() != cst.Error {
				t.Fatalf("first child kind = %s, want Error", decl.Kind())
			}
		},
	},
	{
		name: "an error run stops before a fn declaration",
		src:  "1 + 2\nfn h(): nint -> 0\n",
		check: func(t *testing.T, src string, root *cst.Node, diags []diagnostic.Diagnostic) {
			t.Helper()
			if len(diags) == 0 {
				t.Fatal("want a diagnostic for the stray expression")
			}
			assertLossless(t, src)
			got := declKinds(root)
			want := []string{"Error", "FuncDecl", "<Newline>", "<EOF>"}
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("file children = %v, want %v", got, want)
			}
		},
	},
	{
		name: "missing body is reported",
		src:  "fn h(): nint\n",
		check: func(t *testing.T, src string, _ *cst.Node, diags []diagnostic.Diagnostic) {
			t.Helper()
			found := false
			for _, d := range diags {
				if d.Code == CodeExpectedFuncBody {
					found = true
				}
			}
			if !found {
				t.Fatalf("want expected_func_body, got %v", diags)
			}
			assertLossless(t, src)
		},
	},
	{
		name: "arrow block body is reported",
		src:  "fn h(): nint -> { return 1 }\n",
		check: func(t *testing.T, _ string, _ *cst.Node, diags []diagnostic.Diagnostic) {
			t.Helper()
			found := false
			for _, d := range diags {
				if d.Code == CodeArrowBlockBody {
					found = true
				}
			}
			if !found {
				t.Fatalf("want arrow_block_body, got %v", diags)
			}
		},
	},
}

func TestParseFuncDeclRecovery(t *testing.T) {
	for _, tt := range funcDeclRecoveryCases {
		t.Run(tt.name, func(t *testing.T) {
			root, diags := Parse([]byte(tt.src))
			tt.check(t, tt.src, root, diags)
		})
	}
}

// TestParamAnnotationStillRequired checks that relaxing the function-literal
// header did not leak into the forms whose signatures are the source of types:
// method declarations and function types still require parameter annotations,
// and a written ":" still promises a type everywhere.
func TestParamAnnotationStillRequired(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{"method param", "type L = sbyte impl {\n  m(x): self {\n    return self\n  }\n}\n"},
		{"func type param", "type F = fn(x): nint\n"},
		{"dangling colon in func lit", "const f = fn(x:) { return x }\n"},
		{"dangling result colon in func lit", "const f = fn(x): { return x }\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, diags := Parse([]byte(tc.src))
			found := false
			for _, d := range diags {
				if d.Code == CodeExpectedType {
					found = true
				}
			}
			if !found {
				t.Fatalf("src %q: want diagnostic %s, got %v", tc.src, CodeExpectedType, diags)
			}
			assertLossless(t, tc.src)
		})
	}
}

// TestParseGenericConstAnnotation checks that a constant's type annotation is a
// full type expression, so generic types like list<int> are accepted.
func TestParseGenericConstAnnotation(t *testing.T) {
	root, diags := Parse([]byte("const x: list<nint> = [1]\n"))
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	decl := root.Children()[0].(*cst.Node)
	var clause *cst.Node
	for _, c := range decl.Children() {
		if n, ok := c.(*cst.Node); ok && n.Kind() == cst.TypeClause {
			clause = n
		}
	}
	if clause == nil {
		t.Fatal("no TypeClause")
	}
	if got := subNodeKinds(clause); len(got) != 1 || got[0] != cst.TypeName {
		t.Fatalf("type clause sub-nodes = %v, want [TypeName]", got)
	}
}

// --- use declarations ---------------------------------------------------------

// TestParseUseDeclForms checks that every use form — namespace, selective,
// wildcard, and their pub re-export variants — parses to a single clean
// UseDecl file child.
func TestParseUseDeclForms(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{"namespace", "use geo from \"geometry.belt\"\n"},
		{"selective", "use { Point, Vector } from \"shapes.belt\"\n"},
		{"wildcard", "use * from \"prelude.belt\"\n"},
		{"re-export", "pub use { Color } from \"palette.belt\"\n"},
		{"barrel", "pub use * from \"geometry.belt\"\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, diags := Parse([]byte(tc.src))
			if len(diags) != 0 {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			decl, ok := root.Children()[0].(*cst.Node)
			if !ok || decl.Kind() != cst.UseDecl {
				t.Fatalf("first child = %v, want UseDecl", root.Children()[0])
			}
			assertLossless(t, tc.src)
		})
	}
}

func TestParseUseListChildren(t *testing.T) {
	root, _ := Parse([]byte("use { Point, Vector } from \"shapes.belt\""))
	decl := root.Children()[0].(*cst.Node)
	if kinds := subNodeKinds(decl); len(kinds) != 1 || kinds[0] != cst.UseList {
		t.Fatalf("decl sub-nodes = %v, want [UseList]", kinds)
	}
}

// TestParseUseDiagnostics checks local recovery for malformed use
// declarations: each case reports its specific diagnostic and stays lossless.
func TestParseUseDiagnostics(t *testing.T) {
	cases := []struct {
		name string
		src  string
		code diagnostic.Code
	}{
		{"missing target", "use from \"a.belt\"\n", CodeExpectedIdentifier},
		{"missing from", "use geo \"a.belt\"\n", CodeExpectedFrom},
		{"missing path", "use geo from\n", CodeExpectedPath},
		{"empty list", "use {} from \"a.belt\"\n", CodeExpectedIdentifier},
		{"name after comma", "use { a, } from \"x.belt\"\n", CodeExpectedIdentifier},
		{"junk after star", "use * x from \"a.belt\"\n", CodeExpectedFrom},
		{"unclosed list", "use { a from \"x.belt\"\n", CodeUnexpectedToken},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, diags := Parse([]byte(tc.src))
			found := false
			for _, d := range diags {
				if d.Code == tc.code {
					found = true
				}
			}
			if !found {
				t.Fatalf("src %q: want diagnostic %s, got %v", tc.src, tc.code, diags)
			}
			assertLossless(t, tc.src)
		})
	}
}

// TestParseAssertDeclForms checks well-formed assertions parse to an
// AssertDecl whose only sub-node is the asserted expression.
func TestParseAssertDeclForms(t *testing.T) {
	cases := []struct {
		name string
		src  string
		expr cst.Kind
	}{
		{"name", "assert Enabled\n", cst.NameRef},
		{"comparison", "assert MaxLevel > MinLevel\n", cst.BinaryExpr},
		{"logical", "assert A == 1 && !B\n", cst.BinaryExpr},
		{"documented", "/// the range is not empty\nassert Max > Min\n", cst.BinaryExpr},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, diags := Parse([]byte(tc.src))
			if len(diags) != 0 {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			decl, ok := root.Children()[0].(*cst.Node)
			if !ok || decl.Kind() != cst.AssertDecl {
				t.Fatalf("first child = %v, want AssertDecl", root.Children()[0])
			}
			if kinds := subNodeKinds(decl); len(kinds) != 1 || kinds[0] != tc.expr {
				t.Fatalf("decl sub-nodes = %v, want [%s]", kinds, tc.expr)
			}
			assertLossless(t, tc.src)
		})
	}
}

// TestParseAssertDeclFileShape checks assertions are recognised at the file
// level and interleave with the other declaration forms.
func TestParseAssertDeclFileShape(t *testing.T) {
	root, diags := Parse([]byte("const X = 1\nassert X > 0\ntype Coin = sbyte\n"))
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	got := declKinds(root)
	want := []string{"ConstDecl", "AssertDecl", "TypeDecl", "<Newline>", "<EOF>"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("file children = %v, want %v", got, want)
	}
}

// TestParseAssertDiagnostics checks local recovery for malformed assertions:
// each case reports its specific diagnostic and stays lossless.
func TestParseAssertDiagnostics(t *testing.T) {
	cases := []struct {
		name string
		src  string
		code diagnostic.Code
	}{
		{"missing expr", "assert\n", CodeExpectedExpression},
		{"missing expr before decl", "assert\nconst X = 1\n", CodeExpectedExpression},
		{"missing rhs", "assert 1 >\n", CodeExpectedOperand},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, diags := Parse([]byte(tc.src))
			found := false
			for _, d := range diags {
				if d.Code == tc.code {
					found = true
				}
			}
			if !found {
				t.Fatalf("src %q: want diagnostic %s, got %v", tc.src, tc.code, diags)
			}
			assertLossless(t, tc.src)
		})
	}
}

func TestParseEffects(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []cst.Kind
	}{
		{"effects on fn", "pub fn io async get(url: string): string {\n  return url\n}\n", []cst.Kind{cst.ParamList, cst.TypeName, cst.Block}},
		{"extern fn", "extern fn io async fetch(url: string): string\n", []cst.Kind{cst.ParamList, cst.TypeName}},
		{"pub extern fn", "pub extern fn nondet now(): nint\n", []cst.Kind{cst.ParamList, cst.TypeName}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, diags := Parse([]byte(tc.src))
			if len(diags) != 0 {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			assertLossless(t, tc.src)
			decl := root.Children()[0].(*cst.Node)
			if decl.Kind() != cst.FuncDecl {
				t.Fatalf("first child kind = %s, want FuncDecl", decl.Kind())
			}
			got := subNodeKinds(decl)
			if len(got) != len(tc.want) {
				t.Fatalf("sub-nodes = %v, want %v", got, tc.want)
			}
		})
	}

	// A non-extern function still requires a body.
	if _, diags := Parse([]byte("fn io f(): nint\n")); len(diags) == 0 {
		t.Errorf("fn without body: want a diagnostic")
	}
	// A method may carry effects, with or without fn.
	for _, src := range []string{
		"type C = { u: string } impl {\n  pub fn io async get(): string {\n    return self.u\n  }\n}\n",
		"type C = { u: string } impl {\n  io get(): string {\n    return self.u\n  }\n}\n",
	} {
		if _, diags := Parse([]byte(src)); len(diags) != 0 {
			t.Errorf("%q: unexpected diagnostics: %v", src, diags)
		}
		assertLossless(t, src)
	}
}

// enumMemberNames returns the identifier text of an enum's members, in source
// order, and the count of members that carry an initializer. It reads through
// the positioned tree so the member text resolves to its source bytes.
func enumMemberNames(buf source.Buffer, decl *cst.Node) (names []string, withValue int) {
	tree := cst.Root(decl)
	for _, c := range tree.Children() {
		if k, ok := c.Kind(); !ok || k != cst.EnumMember {
			continue
		}
		gotName := false
		for _, mc := range c.Children() {
			if tk, ok := mc.TokenKind(); ok && tk == token.Ident && !gotName {
				names = append(names, strings.TrimSpace(mc.Text(buf)))
				gotName = true
			}
			if k, ok := mc.Kind(); ok && k == cst.Initializer {
				withValue++
			}
		}
	}
	return names, withValue
}

// TestParseEnumDeclFileShape checks that enum declarations are recognised at
// the file level, the enum/const/type choice made by looking past pub.
func TestParseEnumDeclFileShape(t *testing.T) {
	root, diags := Parse([]byte("const X = 1\npub enum Rarity: byte {\n  A = 1\n}\nenum E {\n  B\n}\n"))
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	got := declKinds(root)
	want := []string{"ConstDecl", "EnumDecl", "EnumDecl", "<Newline>", "<EOF>"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("file children = %v, want %v", got, want)
	}
}

func TestParseEnumDeclChildren(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []cst.Kind
	}{
		{"base type", "enum R: byte {\n  A = 1\n}\n", []cst.Kind{cst.TypeClause, cst.EnumMember}},
		{"no base", "enum E {\n  A\n}\n", []cst.Kind{cst.EnumMember}},
		{"comma separated", "enum E {\n  A, B, C\n}\n", []cst.Kind{cst.EnumMember, cst.EnumMember, cst.EnumMember}},
		{"newline separated", "enum E {\n  A\n  B\n}\n", []cst.Kind{cst.EnumMember, cst.EnumMember}},
		{"trailing comma", "enum E {\n  A, B,\n}\n", []cst.Kind{cst.EnumMember, cst.EnumMember}},
		{"impl", "enum E {\n  A\n} impl {\n  f(): self {\n    return self\n  }\n}\n", []cst.Kind{cst.EnumMember, cst.ImplBlock}},
		{"base and impl", "enum E: sbyte {\n  A = 1\n} impl {\n  f(): self {\n    return self\n  }\n}\n", []cst.Kind{cst.TypeClause, cst.EnumMember, cst.ImplBlock}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, diags := Parse([]byte(tc.src))
			if len(diags) != 0 {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			decl := root.Children()[0].(*cst.Node)
			if decl.Kind() != cst.EnumDecl {
				t.Fatalf("first child kind = %s, want EnumDecl", decl.Kind())
			}
			got := subNodeKinds(decl)
			if len(got) != len(tc.want) {
				t.Fatalf("sub-nodes = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("sub-nodes = %v, want %v", got, tc.want)
				}
			}
			assertLossless(t, tc.src)
		})
	}
}

func TestParseEnumMembers(t *testing.T) {
	src := "enum R: byte {\n  Common = 1\n  Rare = 2\n  Legend = 10\n}\n"
	root, diags := Parse([]byte(src))
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	decl := root.Children()[0].(*cst.Node)
	names, withValue := enumMemberNames(source.NewFile("", []byte(src)), decl)
	if strings.Join(names, ",") != "Common,Rare,Legend" {
		t.Fatalf("member names = %v, want [Common Rare Legend]", names)
	}
	if withValue != 3 {
		t.Fatalf("members with initializer = %d, want 3", withValue)
	}
}

func TestParseEnumMixedSeparators(t *testing.T) {
	// Comma and newline separators may be mixed, and a member without an
	// initializer sits beside one with it.
	src := "enum E {\n  Fire, Water\n  Wind = 7\n}\n"
	root, diags := Parse([]byte(src))
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	decl := root.Children()[0].(*cst.Node)
	names, withValue := enumMemberNames(source.NewFile("", []byte(src)), decl)
	if strings.Join(names, ",") != "Fire,Water,Wind" {
		t.Fatalf("member names = %v, want [Fire Water Wind]", names)
	}
	if withValue != 1 {
		t.Fatalf("members with initializer = %d, want 1", withValue)
	}
}

func TestParseEnumDeclDiagnostics(t *testing.T) {
	cases := []struct {
		name string
		src  string
		code diagnostic.Code
	}{
		{"missing name", "enum {\n  A\n}\n", CodeExpectedIdentifier},
		{"missing base type", "enum E: {\n  A\n}\n", CodeExpectedType},
		{"missing initializer value", "enum E {\n  A =\n}\n", CodeExpectedExpression},
		{"missing brace", "enum E\n", CodeUnexpectedToken},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, diags := Parse([]byte(tc.src))
			found := false
			for _, d := range diags {
				if d.Code == tc.code {
					found = true
				}
			}
			if !found {
				t.Fatalf("src %q: want diagnostic %s, got %v", tc.src, tc.code, diags)
			}
			assertLossless(t, tc.src)
		})
	}
}

// TestParseEnumEmpty checks that an empty enum body parses losslessly (the
// "no members" rule is a semantic concern, not a parse error).
func TestParseEnumEmpty(t *testing.T) {
	src := "enum E {\n}\n"
	root, diags := Parse([]byte(src))
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	decl := root.Children()[0].(*cst.Node)
	if decl.Kind() != cst.EnumDecl {
		t.Fatalf("first child kind = %s, want EnumDecl", decl.Kind())
	}
	if got := subNodeKinds(decl); len(got) != 0 {
		t.Fatalf("empty enum sub-nodes = %v, want none", got)
	}
	assertLossless(t, src)
}

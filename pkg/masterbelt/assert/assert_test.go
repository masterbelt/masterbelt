package assert_test

import (
	"math/big"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/assert"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/eval"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// fileEnv resolves names against one lowered file and folds values on demand —
// the minimal eval.Env a test diagram needs.
type fileEnv struct {
	file *ast.File
	reg  *builtin.Registry
}

func (e fileEnv) Resolve(id *ast.Identifier) *ast.ConstDecl {
	for _, d := range e.file.Decls {
		if d.Name == id.Name {
			return d
		}
	}
	return nil
}
func (e fileEnv) ResolveMember(m *ast.MemberExpr) *ast.ConstDecl { return nil }
func (e fileEnv) ResolveFunc(id *ast.Identifier) *ast.FuncDecl   { return nil }
func (e fileEnv) ValueOf(decl *ast.ConstDecl) *ir.Constant       { return eval.Decl(decl, e) }
func (e fileEnv) Registry() *builtin.Registry                    { return e.reg }

// diagram lowers src (consts plus exactly one assert) and renders the
// assert's diagram.
func diagram(t *testing.T, src string) string {
	t.Helper()
	file, diags := abstract.Lower([]byte(src))
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if len(file.Asserts) != 1 {
		t.Fatalf("got %d asserts, want 1", len(file.Asserts))
	}
	return assert.Diagram(file.Asserts[0].Cond, fileEnv{file: file, reg: builtin.Default()})
}

func TestDiagram(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{
			"comparison",
			"const MaxLevel = 100\nconst MinLevel = 0\nassert MaxLevel < MinLevel\n",
			"MaxLevel < MinLevel\n" +
				"^        ^ ^\n" +
				"100      | 0\n" +
				"         false",
		},
		{
			"compound arithmetic",
			"const Max = 100\nconst Min = 0\nassert Max - Min == 99\n",
			"Max - Min == 99\n" +
				"^   ^ ^   ^\n" +
				"100 | 0   false\n" +
				"    100",
		},
		{
			"unary over a grouping",
			"const Max = 100\nconst Min = 0\nassert !(Max > Min)\n",
			"!(Max > Min)\n" +
				"^ ^   ^ ^\n" +
				"| 100 | 0\n" +
				"false true",
		},
		{
			"strings",
			"const Name = \"hi\"\nassert Name == \"yo\"\n",
			"Name == \"yo\"\n" +
				"^    ^\n" +
				"\"hi\" false",
		},
		{
			"unfoldable parts are skipped",
			// The lambda's x never folds and the collection == has no
			// intrinsic; the foldable map call still shows its value.
			"assert [1, 2].map(fn(x) { return x * 2 }) == [1, 2]\n",
			"[1, 2].map(fn(x) { return x * 2 }) == [1, 2]\n" +
				"       ^\n" +
				"       [2, 4]",
		},
		{
			"no foldable sub-expressions",
			"assert false\n",
			"false",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := diagram(t, tc.src); got != tc.want {
				t.Errorf("diagram:\n%s\nwant:\n%s", got, tc.want)
			}
		})
	}
}

func TestDiagramSelf(t *testing.T) {
	// A refinement predicate folds with self bound to the violating value, so
	// the diagram shows which comparison rejected it.
	file, diags := abstract.Lower([]byte("type Port = int32 where self >= 1 && self <= 65535\n"))
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	pred := file.Types[0].Where
	got := assert.DiagramSelf(pred, ir.IntConstant(big.NewInt(70000)), fileEnv{file: file, reg: builtin.Default()})
	want := "self >= 1 && self <= 65535\n" +
		"^    ^    ^  ^    ^\n" +
		"|    true |  |    false\n" +
		"70000     |  70000\n" +
		"          false"
	if got != want {
		t.Errorf("diagram:\n%s\nwant:\n%s", got, want)
	}
}

func TestDiagramSelfUnbound(t *testing.T) {
	// Diagram (no self binding) leaves a predicate's self rows out: nothing
	// folds, so only the condition line renders.
	file, diags := abstract.Lower([]byte("type Port = int32 where self >= 1\n"))
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	got := assert.Diagram(file.Types[0].Where, fileEnv{file: file, reg: builtin.Default()})
	if got != "self >= 1" {
		t.Errorf("diagram = %q, want the bare condition line", got)
	}
}

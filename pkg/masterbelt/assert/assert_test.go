package assert_test

import (
	"math/big"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/assert"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/eval"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/lower"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// testFolder folds one file's expressions through the IR interpreter — the
// minimal lowering-plus-fold a test diagram needs: an identifier resolves to
// the file's constant (its value folded on demand), and everything else
// lowers through the shared walk.
type testFolder struct {
	file   *ast.File
	reg    *builtin.Registry
	shells map[*ast.ConstDecl]*ir.Const
	self   *ir.Constant
}

func newTestFolder(file *ast.File, self *ir.Constant) *testFolder {
	f := &testFolder{file: file, reg: builtin.Default(), shells: map[*ast.ConstDecl]*ir.Const{}, self: self}
	for _, d := range file.Decls {
		f.shells[d] = &ir.Const{Name: d.Name, Syntax: d}
	}
	return f
}

// Leaf lowers the test's context-specific forms: a constant reference and the
// self keyword. It satisfies lower.Binder.
func (f *testFolder) Leaf(e ast.Expr, _ func(ast.Expr) ir.Value) ir.Value {
	switch e := e.(type) {
	case *ast.Identifier:
		for _, d := range f.file.Decls {
			if d.Name == e.Name {
				return &ir.Reference{Target: f.shells[d], Syntax: e}
			}
		}
	case *ast.SelfExpr:
		return &ir.SelfValue{Syntax: e}
	case *ast.NullLit:
		return &ir.NullValue{Syntax: e}
	}
	return nil
}

func (f *testFolder) EnterFunc(params []*ast.ParamDef) lower.Binder {
	names := make(map[string]bool, len(params))
	for _, p := range params {
		names[p.Name] = true
	}
	return testFuncBinder{outer: f, params: names}
}

// testFuncBinder binds a literal's parameters over the file folder, so a
// lambda body's x lowers to a ParamRef.
type testFuncBinder struct {
	outer  lower.Binder
	params map[string]bool
}

func (b testFuncBinder) Leaf(e ast.Expr, sub func(ast.Expr) ir.Value) ir.Value {
	if id, ok := e.(*ast.Identifier); ok && b.params[id.Name] {
		return &ir.ParamRef{Name: id.Name, Syntax: id}
	}
	return b.outer.Leaf(e, sub)
}

func (b testFuncBinder) EnterFunc(params []*ast.ParamDef) lower.Binder {
	names := make(map[string]bool, len(params))
	for _, p := range params {
		names[p.Name] = true
	}
	return testFuncBinder{outer: b, params: names}
}

// ConstValue folds a referenced constant's initializer on demand. It satisfies
// eval.GraphEnv.
func (f *testFolder) ConstValue(c *ir.Const) *ir.Constant {
	if c.Syntax == nil || c.Syntax.Value == nil {
		return nil
	}
	return f.foldAt(c.Syntax.Value)
}

func (f *testFolder) LookupType(name string) *ir.TypeDef {
	d, _ := f.reg.Lookup(name)
	return d
}

func (f *testFolder) Registry() *builtin.Registry { return f.reg }

// foldAt lowers and folds one expression, self bound when the folder carries
// one — the diagram's per-anchor channel.
func (f *testFolder) foldAt(e ast.Expr) *ir.Constant {
	return eval.GraphPredicate(lower.Value(e, f), f.self, nil, f)
}

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
	return assert.Diagram(file.Asserts[0].Cond, newTestFolder(file, nil).foldAt)
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
	file, diags := abstract.Lower([]byte("type Port = int where self >= 1 && self <= 65535\n"))
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	pred := file.Types[0].Where
	got := assert.Diagram(pred, newTestFolder(file, ir.IntConstant(big.NewInt(70000))).foldAt)
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
	// With no self binding a predicate's self rows stay out: nothing folds, so
	// only the condition line renders.
	file, diags := abstract.Lower([]byte("type Port = int where self >= 1\n"))
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	got := assert.Diagram(file.Types[0].Where, newTestFolder(file, nil).foldAt)
	if got != "self >= 1" {
		t.Errorf("diagram = %q, want the bare condition line", got)
	}
}

package abstract

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ast"
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
	if d.Type == nil || d.Type.Name != "int64" {
		t.Errorf("Type = %+v, want int64", d.Type)
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
	ref, ok := file.Decls[1].Value.(*ast.NameRef)
	if !ok || ref.Name != "MinLevel" {
		t.Errorf("decl 1 Value = %+v, want NameRef MinLevel", file.Decls[1].Value)
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
}

func TestSyntaxBacklink(t *testing.T) {
	d := NewDocument([]byte("const X = 1\n"))
	decl := d.File().Decls[0]
	// The AST node's Syntax link is the same green node the concrete tree holds.
	if decl.Syntax() != d.Concrete().Root().Children()[0] {
		t.Error("ConstDecl.Syntax() is not the backing CST node")
	}
}

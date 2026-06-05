package abstract

import (
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source/ast"
)

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

func TestSyntaxBacklink(t *testing.T) {
	d := NewDocument([]byte("const X = 1\n"))
	decl := d.File().Decls[0]
	// The AST node's Syntax link is the same green node the concrete tree holds.
	if decl.Syntax() != d.Concrete().Root().Children()[0] {
		t.Error("ConstDecl.Syntax() is not the backing CST node")
	}
}

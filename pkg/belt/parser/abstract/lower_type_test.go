// This file tests the lowering of type-expression CST nodes — named and
// namespace-qualified types and the generic arguments they carry — mirroring
// lower_type.go.

package abstract

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source/ast"
)

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

// This file enforces the builtin-surface contract. extern (a fn or method
// with no body) and `= builtin` (a type body or an associated constant) are
// two spellings of one claim: "the registry supplies this declaration's
// implementation". The only source that claim is true of is the builtin
// surface — toolchain-bundled source backed per symbol by the registry, today
// the prelude. Its boundary is the load channel, not a path: the prelude
// loads through LoadPrelude (prelude.go), which resolves its declarations
// without ever assembling them, while every user file is assembled — so the
// checks here reach exactly the code the registry does not back. A future
// bundled standard library rides the same trusted channel and needs no rule
// change.
//
// User code gets no exemption for effectful externs either: no machinery
// exists to supply a user-declared native (codegen and link verification do
// not exist yet), so admitting any extern admits an unverifiable claim.
// When a supply-verifying link stage exists, user externs may return with it
// — and only with it.

package semantic

import (
	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
)

// checkBuiltinSurface reports every extern and `= builtin` declaration in an
// assembled (user) file at its declaration site.
func checkBuiltinSurface(file *ast.File, at func(ast.Node) span, diags *diagnostic.List) {
	for _, fd := range file.Funcs {
		if fd.Extern {
			s := at(fd)
			diags.Add(newExternOutsideBuiltinDiagnostic(s.offset, s.width, fd.Name))
		}
	}
	// A top-level `const X = builtin` needs no arm here: the grammar admits a
	// builtin initializer only on an associated constant, so the top-level
	// spelling is already a parse error (expected_expression), never silent.
	for _, td := range file.Types {
		if _, ok := td.Body.(*ast.BuiltinType); ok {
			s := at(td)
			diags.Add(newBuiltinOutsideBuiltinDiagnostic(s.offset, s.width, td.Name))
		}
		checkBuiltinSurfaceImpl(td.Name, td.Methods, td.Consts, at, diags)
	}
	for _, ed := range file.Enums {
		checkBuiltinSurfaceImpl(ed.Name, ed.Methods, ed.Consts, at, diags)
	}
}

// checkBuiltinSurfaceImpl reports the extern methods and `= builtin`
// associated constants of one impl block, shared by types and enums.
func checkBuiltinSurfaceImpl(typeName string, methods []*ast.MethodDecl, consts []*ast.ConstDecl, at func(ast.Node) span, diags *diagnostic.List) {
	for _, m := range methods {
		if m.Extern {
			s := at(m)
			diags.Add(newExternOutsideBuiltinDiagnostic(s.offset, s.width, typeName+"."+m.Name))
		}
	}
	for _, c := range consts {
		if c.Builtin {
			s := at(c)
			diags.Add(newBuiltinOutsideBuiltinDiagnostic(s.offset, s.width, typeName+"."+c.Name))
		}
	}
}

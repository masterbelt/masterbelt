package ast

import "fmt"

// StmtKinds returns one freshly built, minimal instance of every Stmt
// implementer, in a stable order. It is the single registry of statement kinds:
// the exhaustiveness tests walk it to feed every kind through each []ast.Stmt
// walker, and a reflection check (stmt_coverage_test.go) asserts it lists every
// type in this package that satisfies Stmt — so adding a statement form to the
// language forces adding it here, which in turn forces the walkers' tests to
// account for it. A walker that silently drops the new kind then fails rather
// than ignoring it (the lsp let/assign and lambda-body regressions' shape).
//
// The instances are minimal — empty bodies, nil operands — because the tests
// exercise the dispatch (which arm of the type switch runs), not the operand
// recursion. Each still satisfies Stmt.
func StmtKinds() []Stmt {
	return []Stmt{
		&ReturnStmt{},
		&ExprStmt{},
		&LetStmt{},
		&AssignStmt{},
		&SwitchStmt{},
		&IfStmt{},
	}
}

// UnhandledStmt is the panic value a []ast.Stmt walker raises when it meets a
// statement kind it has no case for. Every exhaustive switch over Stmt — here
// and in the semantic, eval, lower, infer, and lsp layers — ends in
// `default: panic(ast.UnhandledStmt(s))`, so a statement form added to the
// language without extending a walker fails loudly at that walker instead of
// being quietly skipped (the lsp let/assign and lambda-body regressions' shape).
func UnhandledStmt(s Stmt) string {
	return fmt.Sprintf("ast: unhandled Stmt kind %T", s)
}

// unhandledStmt is the in-package alias the ast walkers use.
func unhandledStmt(s Stmt) string { return UnhandledStmt(s) }

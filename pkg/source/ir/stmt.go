package ir

import "fmt"

// StmtKinds returns one minimal instance of every Stmt implementer, in a stable
// order. It is the registry the exhaustiveness test (stmt_coverage_test.go)
// walks to feed every lowered-statement kind through the dump walkers, and a
// reflection check asserts it lists every type in this package that satisfies
// Stmt — so adding a lowered statement form forces adding it here, which forces
// the walkers to account for it rather than silently dropping it from the IR
// dump (the snapshot oracle).
func StmtKinds() []Stmt {
	return []Stmt{
		&Return{},
		&ExprStmt{},
		&Let{},
		&Assign{},
		&Switch{},
		&Match{},
		&If{},
		&For{},
	}
}

// unhandledStmt is the panic value an ir.Stmt walker raises for a statement kind
// it has no case for; every exhaustive switch over Stmt ends in
// `default: panic(unhandledStmt(s))`.
func unhandledStmt(s Stmt) string {
	return fmt.Sprintf("ir: unhandled Stmt kind %T", s)
}

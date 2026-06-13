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
		&AssertStmt{},
	}
}

// unhandledStmt is the panic value an ir.Stmt walker raises for a statement kind
// it has no case for; every exhaustive switch over Stmt ends in
// `default: panic(unhandledStmt(s))`.
func unhandledStmt(s Stmt) string {
	return fmt.Sprintf("ir: unhandled Stmt kind %T", s)
}

// ValueKinds returns one minimal instance of every Value implementer, in a
// stable order — the registry the exhaustiveness pins walk, exactly as
// StmtKinds does for the statement forms. A reflection check asserts it lists
// every type in this package that satisfies Value, so adding a value form
// forces adding it here — which forces the dump, the walkers, and the
// interpreter to account for it rather than silently dropping it.
func ValueKinds() []Value {
	return []Value{
		&Adapt{},
		&IntLiteral{},
		&StringLiteral{},
		&BoolLiteral{},
		&DatetimeLiteral{},
		&DurationLiteral{},
		&CollectionLiteral{},
		&RecordValue{},
		&Reference{},
		&Call{},
		&FuncCall{},
		&StaticCall{},
		&Apply{},
		&FuncLiteral{},
		&SelfValue{},
		&ParamRef{},
		&LocalRef{},
		&FieldAccess{},
		&Conversion{},
		&Await{},
		&Ternary{},
		&RangeLit{},
		&NullValue{},
		&EnumMemberValue{},
		&AssocConstValue{},
		&TypeValue{},
	}
}

// TypeKinds returns one minimal instance of every Type implementer, in a
// stable order — the registry the type codec's exhaustiveness pins walk. The
// Type codec is hand-written (text.go), so each form is dispatched in four
// switches (typeHead, writeTypeFields, decodeType, linkType) guarded only by
// panics; this registry plus the coverage test turns those panics into a
// failing test the moment a form is added without teaching all four.
func TypeKinds() []Type {
	return []Type{
		&Builtin{},
		Invalid,
		&Named{},
		&Union{},
		&Record{},
		&Func{},
		&TypeVar{},
		&App{},
		&SelfType{},
	}
}

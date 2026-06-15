package sql

import (
	"strings"

	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// CreateTable renders the CREATE TABLE a master's row maps to: one column per
// field, quoted the dialect's way, with its SQLite storage type. It is the schema
// half of the DSL→SQL pipeline — a single table with no primary key or index (a
// later concern), a plain rowid table so the rows keep their insert order — and,
// like the predicate lowering, it is driver-free text the engine runs.
func CreateTable(table string, fields []ir.Field, d Dialect) string {
	var b strings.Builder
	b.WriteString("CREATE TABLE ")
	b.WriteString(d.QuoteIdent(table))
	b.WriteString(" (")
	for i, f := range fields {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(d.QuoteIdent(f.Name))
		b.WriteByte(' ')
		b.WriteString(storageType(f.Type))
	}
	b.WriteByte(')')
	return b.String()
}

// InsertInto renders the parameterized INSERT one row is loaded through:
// INSERT INTO "t" ("a", "b") VALUES (?, ?), the columns in field order and one
// placeholder per column in the dialect's form. The engine binds the row's cell
// values positionally, so injection is not a concern and the statement is reused
// for every row.
func InsertInto(table string, fields []ir.Field, d Dialect) string {
	var b strings.Builder
	b.WriteString("INSERT INTO ")
	b.WriteString(d.QuoteIdent(table))
	b.WriteString(" (")
	for i, f := range fields {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(d.QuoteIdent(f.Name))
	}
	b.WriteString(") VALUES (")
	for i := range fields {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(d.Placeholder(i + 1))
	}
	b.WriteByte(')')
	return b.String()
}

// storageType is a field's SQLite storage class: a string to TEXT, every other
// scalar — the integer family and bool — to INTEGER, since SQLite has no boolean
// type and stores a bool as 0/1. A refined or aliased type looks through to its
// underlying primitive, so type Level = int where ... stores as INTEGER. This is
// the type convention the lowering and the loader already share.
func storageType(t ir.Type) string {
	if underlyingName(t) == "string" {
		return "TEXT"
	}
	return "INTEGER"
}

// underlyingName unwraps a type to the primitive name at its base, through a named
// alias or refinement (whose Body is the underlying type), or "" when the base is
// not a primitive. A visited set bounds the walk, so a cyclic alias the engine
// flags as an error cannot send it into an unbounded recursion. (The loader's
// coercion peels a type the same way; the one-way import boundary keeps the two
// from sharing one copy.)
func underlyingName(t ir.Type) string {
	seen := map[*ir.TypeDef]bool{}
	for {
		switch tt := t.(type) {
		case *ir.Builtin:
			return tt.Name
		case *ir.Named:
			if tt.Def == nil || tt.Def.Body == nil || seen[tt.Def] {
				return ""
			}
			seen[tt.Def] = true
			t = tt.Def.Body
		default:
			return ""
		}
	}
}

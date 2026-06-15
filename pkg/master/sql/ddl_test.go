package sql_test

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/master/sql"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// field is a row column for the DDL tests: a name and a primitive type, the only
// shapes a master row carries into a table.
func field(name, prim string) ir.Field {
	return ir.Field{Name: name, Type: &ir.Builtin{Name: prim}}
}

// refinedField is a column whose type is a named refinement of a primitive
// (type Level = int where ...), to pin that the storage type looks through to the
// underlying primitive rather than reading the alias name.
func refinedField(name, prim string) ir.Field {
	def := &ir.TypeDef{Name: "Alias", Body: &ir.Builtin{Name: prim}}
	return ir.Field{Name: name, Type: &ir.Named{Def: def}}
}

// TestCreateTable pins the CREATE TABLE a master's row maps to: one column per
// field, a string column to TEXT and every other scalar (the integer family and
// bool) to INTEGER — SQLite has no boolean type — with a refined or aliased type
// looked through to its primitive. The identifier quoting follows the dialect.
func TestCreateTable(t *testing.T) {
	fields := []ir.Field{
		field("id", "int"),
		field("name", "string"),
		field("active", "bool"),
		field("size", "long"),
		refinedField("power", "int"),
	}
	cases := []struct {
		dialect sql.Dialect
		want    string
	}{
		{sql.SQLite, `CREATE TABLE "rows" ("id" INTEGER, "name" TEXT, "active" INTEGER, "size" INTEGER, "power" INTEGER)`},
		{sql.MySQL, "CREATE TABLE `rows` (`id` INTEGER, `name` TEXT, `active` INTEGER, `size` INTEGER, `power` INTEGER)"},
	}
	for _, tc := range cases {
		if got := sql.CreateTable("rows", fields, tc.dialect); got != tc.want {
			t.Errorf("CreateTable = %q, want %q", got, tc.want)
		}
	}
}

// TestInsertInto pins the parameterized INSERT a row is loaded through: the
// columns in field order and one placeholder per column, the placeholder in the
// dialect's form (? for SQLite/MySQL, $N for PostgreSQL) so the engine binds the
// cell values positionally.
func TestInsertInto(t *testing.T) {
	fields := []ir.Field{field("id", "int"), field("name", "string")}
	cases := []struct {
		dialect sql.Dialect
		want    string
	}{
		{sql.SQLite, `INSERT INTO "rows" ("id", "name") VALUES (?, ?)`},
		{sql.Postgres, `INSERT INTO "rows" ("id", "name") VALUES ($1, $2)`},
		{sql.MySQL, "INSERT INTO `rows` (`id`, `name`) VALUES (?, ?)"},
	}
	for _, tc := range cases {
		if got := sql.InsertInto("rows", fields, tc.dialect); got != tc.want {
			t.Errorf("InsertInto = %q, want %q", got, tc.want)
		}
	}
}

// TestDDLQuotesIdentifiers pins that a column or table name carrying the dialect's
// quote character is escaped in the DDL too, the same as in a predicate — a name
// cannot break out of its quoting.
func TestDDLQuotesIdentifiers(t *testing.T) {
	fields := []ir.Field{field(`we"ird`, "int")}
	want := `CREATE TABLE "t" ("we""ird" INTEGER)`
	if got := sql.CreateTable("t", fields, sql.SQLite); got != want {
		t.Errorf("CreateTable = %q, want %q", got, want)
	}
}

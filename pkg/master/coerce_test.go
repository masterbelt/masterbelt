package master

import (
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// rawCell builds a raw (string) source cell at a 1-based line and column.
func rawCell(s string, row, col int) Cell {
	return Cell{Value: ir.StringConstant(s), Origin: Origin{Row: row, Col: col}}
}

func field(name, prim string) ir.Field {
	return ir.Field{Name: name, Type: &ir.Builtin{Name: prim}}
}

// spec is a SourceSpec with a recognizable display name and anchor.
func spec() SourceSpec { return SourceSpec{Display: "data/skills.csv", Offset: 10, Width: 5} }

func TestCoerceTypedRows(t *testing.T) {
	raw := Table{
		Columns: []string{"id", "name"},
		Rows: []Row{
			{Cells: []Cell{rawCell("1", 2, 1), rawCell("Fireball", 2, 2)}},
			{Cells: []Cell{rawCell("2", 3, 1), rawCell("Heal", 3, 2)}},
		},
	}
	fields := []ir.Field{field("id", "int"), field("name", "string")}

	typed, diags := Coerce(raw, fields, spec())
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %v, want none", diags)
	}
	if got := typed.String(); got != "id, name\n1 | \"Fireball\"\n2 | \"Heal\"\n" {
		t.Errorf("typed table =\n%q", got)
	}
	// Provenance survives coercion: a typed cell still points at its source cell.
	if o := typed.Rows[1].Cells[0].Origin; o.Row != 3 || o.Col != 1 {
		t.Errorf("origin = %+v, want {Row:3 Col:1}", o)
	}
}

func TestCoerceTypeMismatch(t *testing.T) {
	raw := Table{
		Columns: []string{"id", "name"},
		Rows:    []Row{{Cells: []Cell{rawCell("oops", 2, 1), rawCell("Heal", 2, 2)}}},
	}
	typed, diags := Coerce(raw, []ir.Field{field("id", "int"), field("name", "string")}, spec())

	d := singleDiag(t, diags, CodeCellTypeMismatch)
	for _, frag := range []string{"data/skills.csv:2,1", "oops", "int", "id"} {
		if !strings.Contains(d.Message, frag) {
			t.Errorf("message = %q, want it to contain %q", d.Message, frag)
		}
	}
	if d.Offset != 10 || d.Width != 5 {
		t.Errorf("anchor = %d/%d, want the source entry span 10/5", d.Offset, d.Width)
	}
	// The bad cell is a gap; the rest of the row still types.
	if typed.Rows[0].Cells[0].Value != nil {
		t.Errorf("bad cell value = %v, want nil (a gap)", typed.Rows[0].Cells[0].Value)
	}
	if typed.Rows[0].Cells[1].Value.String() != `"Heal"` {
		t.Errorf("good cell = %v, want \"Heal\"", typed.Rows[0].Cells[1].Value)
	}
}

func TestCoerceMissingColumn(t *testing.T) {
	raw := Table{Columns: []string{"id"}, Rows: []Row{{Cells: []Cell{rawCell("1", 2, 1)}}}}
	typed, diags := Coerce(raw, []ir.Field{field("id", "int"), field("name", "string")}, spec())

	d := singleDiag(t, diags, CodeMissingColumn)
	if !strings.Contains(d.Message, "name") {
		t.Errorf("message = %q, want it to name the field", d.Message)
	}
	// The unbound field is dropped from the typed shape.
	if got := strings.Join(typed.Columns, ","); got != "id" {
		t.Errorf("columns = %q, want only the bound field", got)
	}
}

func TestCoerceExtraColumnIgnored(t *testing.T) {
	raw := Table{
		Columns: []string{"id", "name", "note"},
		Rows:    []Row{{Cells: []Cell{rawCell("1", 2, 1), rawCell("Heal", 2, 2), rawCell("x", 2, 3)}}},
	}
	typed, diags := Coerce(raw, []ir.Field{field("id", "int"), field("name", "string")}, spec())
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %v, want none (an extra column is ignored)", diags)
	}
	if got := strings.Join(typed.Columns, ","); got != "id,name" {
		t.Errorf("columns = %q, want the extra column dropped", got)
	}
}

func TestCoerceUnsupportedFieldType(t *testing.T) {
	raw := Table{Columns: []string{"id", "tags"}, Rows: nil}
	tags := ir.Field{Name: "tags", Type: &ir.Record{Fields: []ir.Field{field("k", "string")}}}
	_, diags := Coerce(raw, []ir.Field{field("id", "int"), tags}, spec())

	d := singleDiag(t, diags, CodeUnsupportedFieldType)
	if !strings.Contains(d.Message, "tags") {
		t.Errorf("message = %q, want it to name the field", d.Message)
	}
}

func TestCoerceLooksThroughNamedRefinement(t *testing.T) {
	// A field typed by a refined alias (type Level = int where ...) coerces as
	// its underlying primitive; the refinement itself is the consumer's check.
	level := &ir.TypeDef{Name: "Level", Body: &ir.Builtin{Name: "int"}}
	raw := Table{Columns: []string{"lvl"}, Rows: []Row{{Cells: []Cell{rawCell("7", 2, 1)}}}}
	typed, diags := Coerce(raw, []ir.Field{{Name: "lvl", Type: &ir.Named{Def: level}}}, spec())
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %v, want none", diags)
	}
	if typed.Rows[0].Cells[0].Value.String() != "7" {
		t.Errorf("value = %v, want 7", typed.Rows[0].Cells[0].Value)
	}
}

func TestCoerceUnsignedRejectsNegative(t *testing.T) {
	raw := Table{Columns: []string{"n"}, Rows: []Row{{Cells: []Cell{rawCell("-1", 2, 1)}}}}
	_, diags := Coerce(raw, []ir.Field{field("n", "byte")}, spec())
	singleDiag(t, diags, CodeCellTypeMismatch)
}

func TestCoerceIntegerRange(t *testing.T) {
	oneN := func(s string) Table {
		return Table{Columns: []string{"n"}, Rows: []Row{{Cells: []Cell{rawCell(s, 2, 1)}}}}
	}
	// 255 is the largest byte; 300 does not inhabit the type.
	if _, d := Coerce(oneN("255"), []ir.Field{field("n", "byte")}, spec()); len(d) != 0 {
		t.Errorf("255 as byte = %v, want accepted", d)
	}
	_, over := Coerce(oneN("300"), []ir.Field{field("n", "byte")}, spec())
	singleDiag(t, over, CodeCellTypeMismatch)
	// A signed 32-bit int rejects a value past its range too.
	_, big := Coerce(oneN("99999999999"), []ir.Field{field("n", "int")}, spec())
	singleDiag(t, big, CodeCellTypeMismatch)
	// nint is arbitrary-precision: the same value fits.
	if _, d := Coerce(oneN("99999999999"), []ir.Field{field("n", "nint")}, spec()); len(d) != 0 {
		t.Errorf("big value as nint = %v, want accepted", d)
	}
}

func TestCoerceBool(t *testing.T) {
	raw := Table{
		Columns: []string{"ok"},
		Rows:    []Row{{Cells: []Cell{rawCell("true", 2, 1)}}, {Cells: []Cell{rawCell("maybe", 3, 1)}}},
	}
	typed, diags := Coerce(raw, []ir.Field{field("ok", "bool")}, spec())
	if typed.Rows[0].Cells[0].Value.String() != "true" {
		t.Errorf("value = %v, want true", typed.Rows[0].Cells[0].Value)
	}
	singleDiag(t, diags, CodeCellTypeMismatch) // "maybe" is not a bool
}

func TestCoerceCyclicAliasTerminates(t *testing.T) {
	// type A = B; type B = A — a cycle the engine flags as an error. The unwrap
	// walks must bound it (a visited set) rather than recurse until the stack
	// overflows; both report it as unreadable.
	a := &ir.TypeDef{Name: "A"}
	b := &ir.TypeDef{Name: "B"}
	a.Body = &ir.Named{Def: b}
	b.Body = &ir.Named{Def: a}
	cyclic := &ir.Named{Def: a}

	if _, ok := RowFields(cyclic); ok {
		t.Error("RowFields(cyclic) = ok, want false")
	}
	if scalarSupported(cyclic) {
		t.Error("scalarSupported(cyclic) = true, want false")
	}
	raw := Table{Columns: []string{"x"}, Rows: []Row{{Cells: []Cell{rawCell("1", 2, 1)}}}}
	_, diags := Coerce(raw, []ir.Field{{Name: "x", Type: cyclic}}, spec())
	singleDiag(t, diags, CodeUnsupportedFieldType)
}

func TestRowFields(t *testing.T) {
	rec := &ir.Record{Fields: []ir.Field{field("id", "int")}}
	if fs, ok := RowFields(rec); !ok || len(fs) != 1 || fs[0].Name != "id" {
		t.Errorf("RowFields(record) = %v, %v", fs, ok)
	}
	named := &ir.Named{Def: &ir.TypeDef{Name: "Row", Body: rec}}
	if fs, ok := RowFields(named); !ok || len(fs) != 1 {
		t.Errorf("RowFields(named) = %v, %v", fs, ok)
	}
	if _, ok := RowFields(&ir.Builtin{Name: "int"}); ok {
		t.Error("RowFields(non-record) = ok, want not ok")
	}
}

// singleDiag asserts diags holds exactly one diagnostic of the given code.
func singleDiag(t *testing.T, diags []diagnostic.Diagnostic, code diagnostic.Code) diagnostic.Diagnostic {
	t.Helper()
	if len(diags) != 1 {
		t.Fatalf("diagnostics = %v, want exactly one", diags)
	}
	if diags[0].Code != code {
		t.Fatalf("code = %s, want %s", diags[0].Code, code)
	}
	return diags[0]
}

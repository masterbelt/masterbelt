package csv

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/master"
)

// write writes content to a temp skills.csv and returns a spec pointing at it.
func write(t *testing.T, content string, opts map[string]string) master.SourceSpec {
	t.Helper()
	const name = "skills.csv"
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return master.SourceSpec{Path: path, Display: name, Options: opts, Offset: 7, Width: 3}
}

func TestName(t *testing.T) {
	if New().Name() != "csv" {
		t.Errorf("Name = %q, want csv", New().Name())
	}
}

func TestReadTable(t *testing.T) {
	spec := write(t, "id,name\n1,Fireball\n2,Heal\n", nil)
	table, diags := New().Read(spec)
	if diags.Len() != 0 {
		t.Fatalf("diagnostics = %v, want none", diags.Items())
	}
	if got := table.Columns; len(got) != 2 || got[0] != "id" || got[1] != "name" {
		t.Errorf("columns = %v, want [id name]", got)
	}
	if len(table.Rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(table.Rows))
	}
	if v := table.Rows[0].Cells[1].Value; v == nil || v.Str != "Fireball" {
		t.Errorf("row 0 col 1 = %v, want the raw string Fireball", v)
	}
}

func TestReadOrigins(t *testing.T) {
	// Cells carry the 1-based line and column they came from: the header is
	// line 1, so the first data row is line 2.
	spec := write(t, "id,name\n1,Fireball\n", nil)
	table, _ := New().Read(spec)
	if o := table.Rows[0].Cells[0].Origin; o.Row != 2 || o.Col != 1 {
		t.Errorf("id origin = %+v, want {Row:2 Col:1}", o)
	}
	if o := table.Rows[0].Cells[1].Origin; o.Row != 2 || o.Col != 3 {
		t.Errorf("name origin = %+v, want {Row:2 Col:3}", o)
	}
}

func TestReadDelimiter(t *testing.T) {
	spec := write(t, "id;name\n1;Heal\n", map[string]string{"delimiter": ";"})
	table, diags := New().Read(spec)
	if diags.Len() != 0 {
		t.Fatalf("diagnostics = %v, want none", diags.Items())
	}
	if len(table.Columns) != 2 || table.Rows[0].Cells[1].Value.Str != "Heal" {
		t.Errorf("semicolon-delimited read = %v / %v", table.Columns, table.Rows)
	}
}

func TestReadBadDelimiter(t *testing.T) {
	spec := write(t, "id,name\n", map[string]string{"delimiter": "++"})
	_, diags := New().Read(spec)
	singleCode(t, diags, master.CodeSourceUnreadable)
}

func TestReadMissingFile(t *testing.T) {
	spec := master.SourceSpec{Path: filepath.Join(t.TempDir(), "absent.csv"), Display: "absent.csv", Offset: 7, Width: 3}
	table, diags := New().Read(spec)
	d := singleCode(t, diags, master.CodeSourceUnreadable)
	if d.Offset != 7 || d.Width != 3 {
		t.Errorf("anchor = %d/%d, want the source entry span 7/3", d.Offset, d.Width)
	}
	if len(table.Rows) != 0 {
		t.Errorf("rows = %d, want none on a failed open", len(table.Rows))
	}
}

func TestReadRaggedRowSurvives(t *testing.T) {
	// A row with a missing field is not a parse error here; the core reconciles
	// it against the master's fields. The read still yields every row.
	spec := write(t, "id,name\n1,Fireball\n2\n", nil)
	table, diags := New().Read(spec)
	if diags.Len() != 0 {
		t.Fatalf("diagnostics = %v, want none (a ragged row is the core's concern)", diags.Items())
	}
	if len(table.Rows) != 2 {
		t.Errorf("rows = %d, want 2", len(table.Rows))
	}
}

// singleCode asserts the list holds exactly one diagnostic of the given code.
func singleCode(t *testing.T, diags *diagnostic.List, code diagnostic.Code) diagnostic.Diagnostic {
	t.Helper()
	if diags.Len() != 1 {
		t.Fatalf("diagnostics = %v, want exactly one", diags.Items())
	}
	d := diags.Items()[0]
	if d.Code != code {
		t.Fatalf("code = %s, want %s", d.Code, code)
	}
	return d
}

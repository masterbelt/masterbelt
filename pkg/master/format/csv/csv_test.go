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
	singleUnreadable(t, diags)
}

func TestReadEmptyDelimiterRejected(t *testing.T) {
	// An explicitly empty delimiter is a malformed configuration, not a request
	// for the comma default.
	spec := write(t, "id,name\n1,Heal\n", map[string]string{"delimiter": ""})
	_, diags := New().Read(spec)
	singleUnreadable(t, diags)
}

func TestReadLongRowRejected(t *testing.T) {
	// A row with more cells than the header would lose its trailing data (cells
	// bind by header name), so it is reported rather than silently truncated.
	spec := write(t, "id,name\n1,Alice,EXTRA\n", nil)
	table, diags := New().Read(spec)
	singleUnreadable(t, diags)
	if len(table.Rows) != 0 {
		t.Errorf("rows = %d, want the over-long row skipped", len(table.Rows))
	}
}

func TestReadNoHeaderRejected(t *testing.T) {
	// The first row is the required header; a source with none is malformed.
	spec := write(t, "", nil)
	_, diags := New().Read(spec)
	singleUnreadable(t, diags)
}

func TestReadMissingFile(t *testing.T) {
	spec := master.SourceSpec{Path: filepath.Join(t.TempDir(), "absent.csv"), Display: "absent.csv", Offset: 7, Width: 3}
	table, diags := New().Read(spec)
	d := singleUnreadable(t, diags)
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

// singleUnreadable asserts the list holds exactly one source_unreadable
// diagnostic — the only failure the csv reader reports.
func singleUnreadable(t *testing.T, diags *diagnostic.List) diagnostic.Diagnostic {
	t.Helper()
	if diags.Len() != 1 {
		t.Fatalf("diagnostics = %v, want exactly one", diags.Items())
	}
	d := diags.Items()[0]
	if d.Code != master.CodeSourceUnreadable {
		t.Fatalf("code = %s, want %s", d.Code, master.CodeSourceUnreadable)
	}
	return d
}

package reporter

import (
	"bytes"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/source"
)

func TestReport(t *testing.T) {
	file := source.NewFile("src/main.belt", []byte("ab\ncdef\n"))
	var out bytes.Buffer
	r := New(&out)

	// Deliberately unordered: the reporter presents by position.
	r.Report(file, []diagnostic.Diagnostic{
		{Severity: diagnostic.Error, Code: "x.second", Message: "second", Offset: 4, Width: 2},
		{Severity: diagnostic.Warning, Code: "x.first", Message: "first", Offset: 0, Width: 1},
	})

	want := "src/main.belt:1:1: warning[x.first]: first\n" +
		"src/main.belt:2:2: error[x.second]: second\n"
	if out.String() != want {
		t.Errorf("Report() wrote %q, want %q", out.String(), want)
	}
	if r.Errors() != 1 {
		t.Errorf("Errors() = %d, want 1 (the warning does not count)", r.Errors())
	}
}

func TestReportBare(t *testing.T) {
	var out bytes.Buffer
	r := New(&out)

	r.ReportBare([]diagnostic.Diagnostic{
		{Severity: diagnostic.Error, Code: "x.lost", Message: "nowhere to anchor"},
	})

	want := "error[x.lost]: nowhere to anchor\n"
	if out.String() != want {
		t.Errorf("ReportBare() wrote %q, want %q", out.String(), want)
	}
	if r.Errors() != 1 {
		t.Errorf("Errors() = %d, want 1", r.Errors())
	}
}

func TestErrorsAccumulate(t *testing.T) {
	// One reporter may report several sources (a manifest, then a file); the
	// error count spans all of them.
	var out bytes.Buffer
	r := New(&out)

	file := source.NewFile("a.belt", []byte("x"))
	r.Report(file, []diagnostic.Diagnostic{{Severity: diagnostic.Error, Code: "x.a", Message: "a"}})
	r.ReportBare([]diagnostic.Diagnostic{{Severity: diagnostic.Error, Code: "x.b", Message: "b"}})

	if r.Errors() != 2 {
		t.Errorf("Errors() = %d, want 2", r.Errors())
	}
}

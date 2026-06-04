package reporter

import (
	"bytes"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/source"
)

func TestTextReport(t *testing.T) {
	file := source.NewFile("src/main.belt", []byte("ab\ncdef\n"))
	var out bytes.Buffer
	r := NewText(&out, diagnostic.DefaultLocale)

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
	if err := r.Flush(); err != nil {
		t.Errorf("Flush() = %v", err)
	}
}

func TestTextReportBare(t *testing.T) {
	var out bytes.Buffer
	r := NewText(&out, diagnostic.DefaultLocale)

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

func TestTextLocale(t *testing.T) {
	// A non-default locale re-renders the message from Code + Fields; the
	// stored Message is the DefaultLocale rendering.
	file := source.NewFile("a.belt", []byte("\"x"))
	var out bytes.Buffer
	r := NewText(&out, "ja")

	r.Report(file, []diagnostic.Diagnostic{{
		Severity: diagnostic.Error,
		Code:     "masterbelt.lexer.unterminated_string",
		Message:  "unterminated string literal",
		Offset:   0,
		Width:    2,
	}})

	want := "a.belt:1:1: error[masterbelt.lexer.unterminated_string]: 文字列リテラルが閉じられていません\n"
	if out.String() != want {
		t.Errorf("Report() wrote %q, want %q", out.String(), want)
	}
}

func TestTextErrorsAccumulate(t *testing.T) {
	// One reporter may report several sources (a manifest, then a file); the
	// error count spans all of them.
	var out bytes.Buffer
	r := NewText(&out, diagnostic.DefaultLocale)

	file := source.NewFile("a.belt", []byte("x"))
	r.Report(file, []diagnostic.Diagnostic{{Severity: diagnostic.Error, Code: "x.a", Message: "a"}})
	r.ReportBare([]diagnostic.Diagnostic{{Severity: diagnostic.Error, Code: "x.b", Message: "b"}})

	if r.Errors() != 2 {
		t.Errorf("Errors() = %d, want 2", r.Errors())
	}
}

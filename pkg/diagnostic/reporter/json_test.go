package reporter

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/source"
)

func TestJSONReport(t *testing.T) {
	file := source.NewFile("src/main.belt", []byte("ab\ncdef\n"))
	var out bytes.Buffer
	r := NewJSON(&out, diagnostic.DefaultLocale)

	r.Report(file, []diagnostic.Diagnostic{{
		Severity: diagnostic.Error,
		Code:     "x.broken",
		Message:  "broken thing",
		Fields:   map[string]fmt.Stringer{"name": diagnostic.Str("thing")},
		Offset:   4,
		Width:    2,
	}})
	if err := r.Flush(); err != nil {
		t.Fatalf("Flush() = %v", err)
	}

	// The exact document is the contract: stable field order, byte offsets,
	// 1-based line/column, fixes always present.
	want := `{
  "version": 1,
  "diagnostics": [
    {
      "code": "x.broken",
      "severity": "error",
      "file": "src/main.belt",
      "range": {
        "start": {
          "offset": 4,
          "line": 2,
          "column": 2
        },
        "end": {
          "offset": 6,
          "line": 2,
          "column": 4
        }
      },
      "message": {
        "locale": "en",
        "text": "broken thing"
      },
      "data": {
        "name": "thing"
      },
      "fixes": []
    }
  ]
}
`
	if out.String() != want {
		t.Errorf("Flush() wrote:\n%s\nwant:\n%s", out.String(), want)
	}
	if r.Errors() != 1 {
		t.Errorf("Errors() = %d, want 1", r.Errors())
	}
}

func TestJSONReportBare(t *testing.T) {
	// No file to anchor to: the diagnostic is emitted without file and range.
	var out bytes.Buffer
	r := NewJSON(&out, diagnostic.DefaultLocale)

	r.ReportBare([]diagnostic.Diagnostic{{
		Severity: diagnostic.Error,
		Code:     "project.config.missing",
		Message:  "masterbelt.toml not found in this directory or any parent",
	}})
	if err := r.Flush(); err != nil {
		t.Fatalf("Flush() = %v", err)
	}

	want := `{
  "version": 1,
  "diagnostics": [
    {
      "code": "project.config.missing",
      "severity": "error",
      "message": {
        "locale": "en",
        "text": "masterbelt.toml not found in this directory or any parent"
      },
      "fixes": []
    }
  ]
}
`
	if out.String() != want {
		t.Errorf("Flush() wrote:\n%s\nwant:\n%s", out.String(), want)
	}
}

func TestJSONEmpty(t *testing.T) {
	// A clean run still emits a well-formed document with an empty array, so
	// consumers can tell "no problems" from "no output".
	var out bytes.Buffer
	r := NewJSON(&out, diagnostic.DefaultLocale)
	if err := r.Flush(); err != nil {
		t.Fatalf("Flush() = %v", err)
	}

	want := "{\n  \"version\": 1,\n  \"diagnostics\": []\n}\n"
	if out.String() != want {
		t.Errorf("Flush() wrote %q, want %q", out.String(), want)
	}
	if r.Errors() != 0 {
		t.Errorf("Errors() = %d, want 0", r.Errors())
	}
}

func TestJSONLocale(t *testing.T) {
	// message carries the locale it was rendered in; code and data stay
	// locale-independent.
	file := source.NewFile("a.belt", []byte("\"x"))
	var out bytes.Buffer
	r := NewJSON(&out, "ja")

	r.Report(file, []diagnostic.Diagnostic{{
		Severity: diagnostic.Error,
		Code:     "masterbelt.lexer.unterminated_string",
		Message:  "unterminated string literal",
		Offset:   0,
		Width:    2,
	}})
	if err := r.Flush(); err != nil {
		t.Fatalf("Flush() = %v", err)
	}

	for _, fragment := range []string{
		`"locale": "ja"`,
		`"text": "文字列リテラルが閉じられていません"`,
		`"code": "masterbelt.lexer.unterminated_string"`,
	} {
		if !bytes.Contains(out.Bytes(), []byte(fragment)) {
			t.Errorf("Flush() wrote:\n%s\nwant it to contain %s", out.String(), fragment)
		}
	}
}

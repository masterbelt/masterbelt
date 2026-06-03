package diagnostic

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/source"
)

func TestSeverityString(t *testing.T) {
	cases := map[Severity]string{
		Error:        "error",
		Warning:      "warning",
		Info:         "info",
		Hint:         "hint",
		Severity(99): "Severity(99)",
	}
	for sev, want := range cases {
		if got := sev.String(); got != want {
			t.Errorf("Severity(%d).String() = %q, want %q", int(sev), got, want)
		}
	}
}

func TestDiagnosticString(t *testing.T) {
	d := Diagnostic{
		Severity: Error,
		Message:  "unterminated block comment",
		Span: source.Span{
			Start: source.Position{ByteOffset: 12, Line: 3, Column: 5},
			End:   source.Position{ByteOffset: 20, Line: 3, Column: 13},
		},
	}
	if got, want := d.String(), "error: unterminated block comment (3:5)"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestList(t *testing.T) {
	var l List

	if l.Len() != 0 || l.HasErrors() {
		t.Fatalf("zero value should be empty and error-free")
	}

	span := source.Span{Start: source.Position{Line: 1, Column: 1}}
	l.Warnf(span, "deprecated %s", "thing")
	if l.HasErrors() {
		t.Errorf("HasErrors() = true after only a warning")
	}

	l.Errorf(span, "unexpected character %q", '#')
	if l.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", l.Len())
	}
	if !l.HasErrors() {
		t.Errorf("HasErrors() = false, want true after an error")
	}

	items := l.Items()
	if items[0].Severity != Warning || items[1].Severity != Error {
		t.Errorf("insertion order not preserved: %v", items)
	}
	if want := "unexpected character '#'"; items[1].Message != want {
		t.Errorf("message = %q, want %q", items[1].Message, want)
	}
}

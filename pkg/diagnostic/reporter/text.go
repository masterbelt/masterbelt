package reporter

import (
	"fmt"
	"io"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/source"
)

// Text writes diagnostics in the line-oriented format compilers
// conventionally print to a terminal:
//
//	name:line:col: severity[code]: message
//
// Anchored lines resolve positions against the file's content and label them
// with the file's name exactly as given — how a file is named (relative,
// absolute) is the caller's policy, not the reporter's. Bare lines carry no
// location. Lines stream out as diagnostics are reported.
type Text struct {
	w      io.Writer
	locale diagnostic.Locale
	errors errorCount
}

// NewText returns a Text reporter writing to w, rendering messages in locale.
func NewText(w io.Writer, locale diagnostic.Locale) *Text {
	return &Text{w: w, locale: locale}
}

// Report writes diags anchored to file, ordered by offset.
func (t *Text) Report(file *source.File, diags []diagnostic.Diagnostic) {
	for _, d := range byOffset(diags) {
		pos := file.Position(d.Offset)
		_, _ = fmt.Fprintf(t.w, "%s:%d:%d: %s[%s]: %s\n", file.Name(), pos.Line, pos.Column, d.Severity, d.Code, message(d, t.locale))
	}
	t.errors.add(diags)
}

// ReportBare writes diags that have no file to anchor to, as
// "severity[code]: message".
func (t *Text) ReportBare(diags []diagnostic.Diagnostic) {
	for _, d := range diags {
		_, _ = fmt.Fprintf(t.w, "%s[%s]: %s\n", d.Severity, d.Code, message(d, t.locale))
	}
	t.errors.add(diags)
}

// Errors reports how many error-severity diagnostics have been written.
func (t *Text) Errors() int {
	return int(t.errors)
}

// Flush implements Reporter; text streams as it is reported, so there is
// nothing left to complete.
func (t *Text) Flush() error {
	return nil
}

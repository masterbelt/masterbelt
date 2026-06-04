// Package diagnostic models compiler diagnostics — errors, warnings, and
// notes — anchored to the source span they refer to.
//
// Diagnostics are not constructed freely. Every diagnostic has a stable Code
// (e.g. masterbelt.lexer.unexpected_character) with a fixed set of typed Fields
// and a per-locale message template, declared in code.csv and
// messages/<locale>.csv. The generator (go generate ./...) compiles those tables
// into a typed constructor per code in each owning package — the only way to
// build a Diagnostic — and a locale-aware renderer per code in this package.
// Nothing is parsed at run time; because the structured Code and Fields are
// retained on the value, a message can be re-rendered in any language with
// Render or Diagnostic.Localize.
//
// The package is organised as:
//
//	diagnostic.go  the Diagnostic value and its Code
//	severity.go    the Severity scale
//	field.go       the fmt.Stringer field-value wrappers
//	render.go      Locale and message rendering
//	list.go        the List collector
//	catalog_gen.go the generated per-code renderers
package diagnostic

import (
	"fmt"

	"github.com/masterbelt/masterbelt/pkg/source"
)

// Code is the stable identifier of a diagnostic kind, e.g.
// "masterbelt.lexer.unexpected_character".
type Code string

// Diagnostic is a single message anchored to a byte range of the source. Like a
// token it stores only its range (Offset, Width), never a resolved line/column,
// so an edit shifts at most its Offset and diagnostics can be reused across
// edits. Resolve a source span on demand with Span. It is produced only by the
// generated per-code constructors; the exported fields exist so those
// constructors (which live in other packages) can populate them.
type Diagnostic struct {
	Severity Severity
	Code     Code
	Message  string
	Fields   map[string]fmt.Stringer
	Offset   int // byte offset of the diagnostic's start within the file
	Width    int // byte length of the diagnostic's span
}

// End returns the byte offset one past the diagnostic (Offset + Width).
func (d Diagnostic) End() int {
	return d.Offset + d.Width
}

// Span resolves the diagnostic's source span, computing line/column positions
// from buf on demand.
func (d Diagnostic) Span(buf source.Buffer) source.Span {
	return buf.Span(d.Offset, d.End())
}

// String renders the diagnostic as "severity[code]: message". Resolve a
// human-facing source location with Span when a buffer is available.
func (d Diagnostic) String() string {
	return fmt.Sprintf("%s[%s]: %s", d.Severity, d.Code, d.Message)
}

// Localize re-renders the diagnostic's message in locale. The stored Message is
// the DefaultLocale rendering; Localize uses the retained Code and Fields to
// produce the message in another language without rebuilding the diagnostic.
func (d Diagnostic) Localize(locale Locale) string {
	return Render(locale, d.Code, d.Fields)
}

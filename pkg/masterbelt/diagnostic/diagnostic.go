// Package diagnostic models compiler diagnostics — errors, warnings, and
// notes — anchored to the source span they refer to. It is shared by the
// lexer, parser, and later analysis phases as the common channel for
// reporting problems without aborting on the first one.
package diagnostic

import (
	"fmt"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/source"
)

// Severity classifies a diagnostic. The values mirror the Language Server
// Protocol DiagnosticSeverity scale (Error = 1, Warning = 2, Info = 3,
// Hint = 4) so they can be surfaced to an editor without translation.
type Severity int

const (
	Error Severity = iota + 1
	Warning
	Info
	Hint
)

var severityNames = map[Severity]string{
	Error:   "error",
	Warning: "warning",
	Info:    "info",
	Hint:    "hint",
}

func (s Severity) String() string {
	if name, ok := severityNames[s]; ok {
		return name
	}
	return fmt.Sprintf("Severity(%d)", int(s))
}

// Diagnostic is a single message anchored to a source span.
type Diagnostic struct {
	Severity Severity
	Message  string
	Span     source.Span
}

// String renders the diagnostic as "severity: message (line:col)", using the
// 1-based start position of the span.
func (d Diagnostic) String() string {
	p := d.Span.Start
	return fmt.Sprintf("%s: %s (%d:%d)", d.Severity, d.Message, p.Line, p.Column)
}

// List accumulates diagnostics produced during a phase such as lexing or
// parsing. The zero value is an empty, ready-to-use list.
type List struct {
	items []Diagnostic
}

// Add appends d to the list.
func (l *List) Add(d Diagnostic) {
	l.items = append(l.items, d)
}

// Errorf records an Error-severity diagnostic at span.
func (l *List) Errorf(span source.Span, format string, args ...any) {
	l.Add(Diagnostic{Severity: Error, Message: fmt.Sprintf(format, args...), Span: span})
}

// Warnf records a Warning-severity diagnostic at span.
func (l *List) Warnf(span source.Span, format string, args ...any) {
	l.Add(Diagnostic{Severity: Warning, Message: fmt.Sprintf(format, args...), Span: span})
}

// Items returns the recorded diagnostics in insertion order. The result
// aliases the list's backing storage; do not mutate it.
func (l *List) Items() []Diagnostic {
	return l.items
}

// Len reports how many diagnostics have been recorded.
func (l *List) Len() int {
	return len(l.items)
}

// HasErrors reports whether any recorded diagnostic has Error severity.
func (l *List) HasErrors() bool {
	for _, d := range l.items {
		if d.Severity == Error {
			return true
		}
	}
	return false
}

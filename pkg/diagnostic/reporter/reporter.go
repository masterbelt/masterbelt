// Package reporter renders diagnostics for presentation on a stream: the
// line-oriented "path:line:col: severity[code]: message" format compilers
// conventionally write to a terminal.
//
// pkg/diagnostic models diagnostics and deliberately keeps them
// presentation-free; this package owns what they look like when written out.
// Anything that prints diagnostics — the CLI today, machine-readable formats
// when they land — should go through a Reporter rather than formatting by
// hand. (The LSP is the exception: it converts diagnostics to protocol
// values instead of writing text.)
package reporter

import (
	"fmt"
	"io"
	"sort"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/source"
)

// Reporter writes diagnostics as text, one per line, and counts the errors it
// has seen, so a caller that reports from several sources can decide an exit
// status at the end.
type Reporter struct {
	w      io.Writer
	errors int
}

// New returns a Reporter writing to w.
func New(w io.Writer) *Reporter {
	return &Reporter{w: w}
}

// Report writes diags anchored to file, ordered by offset:
//
//	name:line:col: severity[code]: message
//
// Positions are resolved against the file's content, and the path label is
// the file's name exactly as given — how a file is named (relative, absolute)
// is the caller's policy, not the reporter's.
func (r *Reporter) Report(file *source.File, diags []diagnostic.Diagnostic) {
	sorted := append([]diagnostic.Diagnostic(nil), diags...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Offset < sorted[j].Offset })
	for _, d := range sorted {
		pos := file.Position(d.Offset)
		fmt.Fprintf(r.w, "%s:%d:%d: %s\n", file.Name(), pos.Line, pos.Column, d)
		r.count(d)
	}
}

// ReportBare writes diags that have no file to anchor to — a project whose
// manifest does not exist, for example — as "severity[code]: message".
func (r *Reporter) ReportBare(diags []diagnostic.Diagnostic) {
	for _, d := range diags {
		fmt.Fprintln(r.w, d.String())
		r.count(d)
	}
}

// Errors reports how many error-severity diagnostics have been written.
func (r *Reporter) Errors() int {
	return r.errors
}

func (r *Reporter) count(d diagnostic.Diagnostic) {
	if d.Severity == diagnostic.Error {
		r.errors++
	}
}

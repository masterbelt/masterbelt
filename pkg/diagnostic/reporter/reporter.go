// Package reporter renders diagnostics for presentation outside the editor.
//
// pkg/diagnostic models diagnostics and deliberately keeps them
// presentation-free; this package owns what they look like when written out,
// and a Reporter is the one door anything that emits diagnostics goes
// through. Two implementations exist: Text streams the line-oriented format
// compilers conventionally print, and JSON accumulates one machine-readable
// document emitted on Flush — the `check --format=json` schema. (The LSP is
// the exception: it converts diagnostics to protocol values instead of
// writing text.)
package reporter

import (
	"sort"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/source"
)

// A Reporter renders diagnostics to an output. Diagnostics arrive anchored to
// the file they are about (Report) or bare when there is no file to anchor to
// (ReportBare) — a project whose manifest does not exist, for example. A
// reporter counts the errors it sees, so a caller reporting from several
// sources can decide an exit status at the end. Flush completes the output
// and must be called once after all reporting: document formats write
// everything there, streaming formats have nothing left to do.
type Reporter interface {
	Report(file *source.File, diags []diagnostic.Diagnostic)
	ReportBare(diags []diagnostic.Diagnostic)
	Errors() int
	Flush() error
}

// byOffset returns diags ordered by offset — presentation order — leaving the
// caller's slice untouched.
func byOffset(diags []diagnostic.Diagnostic) []diagnostic.Diagnostic {
	sorted := append([]diagnostic.Diagnostic(nil), diags...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Offset < sorted[j].Offset })
	return sorted
}

// message renders d's message in locale. The stored Message is already the
// DefaultLocale rendering; any other locale re-renders from the diagnostic's
// Code and Fields.
func message(d diagnostic.Diagnostic, locale diagnostic.Locale) string {
	if locale == diagnostic.DefaultLocale {
		return d.Message
	}
	return d.Localize(locale)
}

// errorCount tallies error-severity diagnostics across reporting calls.
type errorCount int

func (c *errorCount) add(diags []diagnostic.Diagnostic) {
	for _, d := range diags {
		if d.Severity == diagnostic.Error {
			*c++
		}
	}
}

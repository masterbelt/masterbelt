// Package lint layers advisory diagnostics over the resolved IR: notes, not
// errors. Where the semantic layer reports a program wrong, lint reports a part
// of it unnecessary — unreachable code today, unused declarations next. Each
// check reads the typed IR graph the semantic layer produced and reports
// through the same diagnostic channel, tagging its findings Unnecessary so an
// editor fades the dead span rather than underlining it.
//
// A finding on a declaration the semantic layer already reported an error for is
// suppressed: a broken body is wrong, not also dead. lint depends on neither the
// concrete tree nor the semantic layer — it reads the IR and resolves spans
// through a caller-supplied Span — so it stays a thin layer on top.
package lint

import (
	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// Span resolves a node's byte offset and width from its syntax backpointer. The
// caller fills it from its position table, so lint needs neither the concrete
// tree nor the semantic layer to anchor a diagnostic.
type Span func(ast.Node) (offset, width int)

// Check returns the advisory diagnostics for a resolved module. span resolves a
// node's source range; prior is the diagnostics already produced for the file,
// read so a lint defers to an error that already covers the same declaration.
func Check(m *ir.Module, span Span, prior []diagnostic.Diagnostic) []diagnostic.Diagnostic {
	if m == nil {
		return nil
	}
	l := &linter{span: span, errors: errorRanges(prior)}
	l.unreachableCode(m)
	return l.diags
}

// linter accumulates a module's lint diagnostics across the checks.
type linter struct {
	span   Span
	errors []offsetRange // error-severity ranges, deferred to for suppression
	diags  []diagnostic.Diagnostic
}

type offsetRange struct{ start, end int }

// errorRanges collects the source ranges of the error-severity diagnostics — the
// spans a lint stays quiet within.
func errorRanges(prior []diagnostic.Diagnostic) []offsetRange {
	var out []offsetRange
	for _, d := range prior {
		if d.Severity == diagnostic.Error {
			out = append(out, offsetRange{d.Offset, d.End()})
		}
	}
	return out
}

// brokenWithin reports whether an error-severity diagnostic overlaps
// [start, start+width) — the declaration a lint should leave alone.
func (l *linter) brokenWithin(start, width int) bool {
	end := start + width
	for _, e := range l.errors {
		if e.start < end && start < e.end {
			return true
		}
	}
	return false
}

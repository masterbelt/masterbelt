// Package master is the data layer: it reads a project's master-data sources and,
// in later steps, coerces them against the master definitions the belt core
// resolved, validates them, and hands them to export and codegen. It sits one
// layer above belt — it consumes the resolved IR (pkg/source/ir) and reports
// through pkg/diagnostic — and below the concrete formats and code generators,
// which live under pkg/master/format/* and pkg/master/codegen/* and depend on it,
// not the other way round. That one-way boundary is enforced by a test, not by
// convention (see boundary_test.go), so a concrete format's own dependency never
// leaks into the core.
//
// This file defines only the I/O seam — the Format interface and the rows it
// yields. No format is implemented here: csv is the first implementer, in a later
// step, and the seam carries exactly what that first consumer needs and no more.
package master

import (
	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// Format is the seam between the master data layer and a concrete source format
// (csv first, then xlsx and sqlite). The master core depends only on this
// interface; each concrete format lives in its own package under
// pkg/master/format/, the only place that format's own dependency (a csv or xlsx
// library) appears, so the core stays free of them.
type Format interface {
	// Name is the identifier a source declaration names this format by — the
	// "csv" in source { csv "..." }. The integration layer registers a format
	// under this name and resolves a source's declared format to it.
	Name() string

	// Read turns one source spec into the rows it carries, reporting any read
	// failure (a missing file, a malformed source) into the returned list rather
	// than as a Go error, so a partial read still yields the rows it could parse.
	// The values come back as constants tagged with where they came from;
	// coercing each to its master field's declared type, and merging several
	// sources of one master, are the reader's consumers' jobs, not the format's.
	Read(SourceSpec) (Table, *diagnostic.List)
}

// SourceSpec identifies one data source for a format to read: the location the
// source declaration named and the format-specific options it carried (a csv
// delimiter, an xlsx sheet name). The option set is left open here — each format
// interprets its own keys — rather than modelled per format in the core.
type SourceSpec struct {
	Path    string
	Options map[string]string
}

// Table is the rows one source yielded, in source order.
type Table struct {
	Rows []Row
}

// Row is one record's cells, in column order.
type Row struct {
	Cells []Cell
}

// Cell is one field of one row: the value the source carried, as a constant, and
// the place it came from. The master layer coerces Value to the field's declared
// type downstream; Origin is what lets a later validation error point back at the
// original cell rather than at generated text.
type Cell struct {
	Value  *ir.Constant
	Origin Origin
}

// Origin is where a cell came from within its source — a row and column for a
// tabular format, both zero-based. It is deliberately minimal here; the
// cross-format locator the diagnostics ultimately use (csv:row,col,
// xlsx:sheet!cell, ...) is generalized in a later step.
type Origin struct {
	Row, Col int
}

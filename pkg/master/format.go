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

	// OptionSpecs declares the options this format understands, so a source
	// declaration's options can be checked against them before the format reads
	// — an unknown key or a wrongly typed value reported at the declaration. A
	// format with no options returns nil.
	OptionSpecs() []OptionSpec

	// Read turns one source spec into the rows it carries, reporting any read
	// failure (a missing file, a malformed source) into the returned list rather
	// than as a Go error, so a partial read still yields the rows it could parse.
	// The values come back as raw constants tagged with where they came from;
	// coercing each to its master field's declared type (Coerce), and merging
	// several sources of one master, are the reader's consumers' jobs, not the
	// format's.
	Read(SourceSpec) (Table, *diagnostic.List)
}

// OptionKind is the type a format option's value must have, the vocabulary the
// option check matches a source declaration's option values against.
type OptionKind int

// The option kinds, one per value type an option may carry.
const (
	OptionString OptionKind = iota
	OptionBool
	OptionInt
)

// String renders the kind as the type name a diagnostic names it by.
func (k OptionKind) String() string {
	switch k {
	case OptionBool:
		return "bool"
	case OptionInt:
		return "int"
	default:
		return "string"
	}
}

// OptionSpec is one option a format understands: its key and the type its value
// must have.
type OptionSpec struct {
	Name string
	Kind OptionKind
}

// SourceSpec identifies one data source for a format to read: the resolved file
// to open, a stable name for diagnostics, the format-specific options, and where
// the declaration that named it sits. The option set is left open here — each
// format interprets its own keys — rather than modelled per format in the core.
type SourceSpec struct {
	// Path is the absolute filesystem path the format opens to read the source.
	Path string
	// Display is the source's name as it appears in diagnostics — the locator
	// resolved against the project, stable across machines (unlike Path), so
	// golden output never embeds an absolute path.
	Display string
	// Options are the source declaration's format options, flattened to strings;
	// each format interprets its own keys.
	Options map[string]string
	// Offset and Width locate the source declaration in the .belt file that
	// carried it. The data lives in a separate file the diagnostic model does
	// not address, so a read or cell diagnostic anchors here — at the source
	// entry — and names the precise data location (Display:row,col) in its
	// message. A cross-format locator addressing the data file directly is a
	// later generalization.
	Offset, Width int
}

// Table is one source's rows, in source order, with the column names they were
// read under (a csv header row). The reader maps a master's fields onto the
// columns by name (Coerce), so the order the columns arrive in is the source's,
// not the master's.
type Table struct {
	Columns []string
	Rows    []Row
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

// Origin is where a cell came from within its source — a line and column for a
// tabular format, both 1-based as a person reading the file counts them (the
// header is line 1, the first field column 1), so a diagnostic points where the
// editor's cursor would land. It is deliberately minimal here; the cross-format
// locator the diagnostics ultimately use (csv:row,col, xlsx:sheet!cell, ...) is
// generalized in a later step.
type Origin struct {
	Row, Col int
}

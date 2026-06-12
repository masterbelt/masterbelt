// Package csv reads a master's rows from a comma-separated-values file. It is
// the first concrete master.Format: the only place the csv parser (the standard
// library's encoding/csv) is depended on, so that dependency stays out of the
// master core, the one-way import boundary the data layer rests on.
//
// The reader is schema-blind by design. It reads the header row as the column
// names and every later row as a record of raw string cells, each tagged with
// the line and column it came from; turning those strings into a master's typed
// fields, and checking them, is the core's job (master.Coerce), not this
// package's. So the only thing it interprets is how to split the file — the
// delimiter — and the only failures it reports are ones that stop it reading at
// all: a file it cannot open, a body it cannot parse.
package csv

import (
	encoding_csv "encoding/csv"
	"errors"
	"io"
	"os"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/master"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// Format reads csv sources. It carries no state; one value serves every read.
type Format struct{}

// New returns the csv format, ready to register.
func New() Format { return Format{} }

// Name is the identifier a source declaration names this format by.
func (Format) Name() string { return "csv" }

// OptionSpecs declares the options csv understands. The header row is always
// the first line (columns bind to fields by name), so the only option is the
// field delimiter.
func (Format) OptionSpecs() []master.OptionSpec {
	return []master.OptionSpec{{Name: "delimiter", Kind: master.OptionString}}
}

// Read opens the source and returns its rows as raw string cells with their
// origins, reporting a failure that stops the read into the diagnostic list
// rather than as a Go error. The first row is the header, taken as the column
// names; later rows are records. Field counts are not enforced here — a row
// with too few or too many cells is the core's to reconcile against the master's
// fields — so a single ragged row does not sink the whole read.
func (Format) Read(spec master.SourceSpec) (master.Table, *diagnostic.List) {
	var diags diagnostic.List

	comma, ok := delimiter(spec, &diags)
	if !ok {
		return master.Table{}, &diags
	}

	file, err := os.Open(spec.Path)
	if err != nil {
		diags.Add(master.SourceUnreadable(spec.Offset, spec.Width, spec.Display, readError(err)))
		return master.Table{}, &diags
	}
	defer func() { _ = file.Close() }()

	reader := encoding_csv.NewReader(file)
	reader.Comma = comma
	reader.FieldsPerRecord = -1 // bind by header name, so a ragged row is the core's concern

	var table master.Table
	header := false
	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			diags.Add(master.SourceUnreadable(spec.Offset, spec.Width, spec.Display, readError(err)))
			break
		}
		if !header {
			table.Columns = append([]string(nil), record...)
			header = true
			continue
		}
		table.Rows = append(table.Rows, master.Row{Cells: cellsOf(reader, record)})
	}
	return table, &diags
}

// cellsOf builds one record's cells, each carrying the line and column the
// field starts at (FieldPos, both 1-based), so a later diagnostic points where
// the editor's cursor would land.
func cellsOf(reader *encoding_csv.Reader, record []string) []master.Cell {
	cells := make([]master.Cell, len(record))
	for i, value := range record {
		line, col := reader.FieldPos(i)
		cells[i] = master.Cell{Value: ir.StringConstant(value), Origin: master.Origin{Row: line, Col: col}}
	}
	return cells
}

// delimiter resolves the field separator from the options: the single character
// the delimiter option carries, or a comma when it is unset. A delimiter that
// is not exactly one character stops the read, since the rest of the parse
// depends on it.
func delimiter(spec master.SourceSpec, diags *diagnostic.List) (rune, bool) {
	d, set := spec.Options["delimiter"]
	if !set || d == "" {
		return ',', true
	}
	runes := []rune(d)
	if len(runes) != 1 {
		diags.Add(master.SourceUnreadable(spec.Offset, spec.Width, spec.Display, "delimiter must be a single character"))
		return 0, false
	}
	return runes[0], true
}

// readError renders a read failure for a diagnostic. A csv parse error already
// names its line; an os error is unwrapped to its bare message so a diagnostic
// does not echo the absolute path the reader was handed (Display carries the
// stable name).
func readError(err error) string {
	var perr *encoding_csv.ParseError
	if errors.As(err, &perr) {
		return err.Error()
	}
	var oerr *os.PathError
	if errors.As(err, &oerr) {
		return oerr.Err.Error()
	}
	return err.Error()
}

var _ master.Format = Format{}

package master

import (
	"math/big"
	"strings"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// Coerce maps a raw source table onto a master's row fields and converts each
// cell from the source's text to its field's declared type, returning the typed
// table and a diagnostic for everything it could not.
//
// Columns bind to fields by name: a field whose name no column carries is
// reported (MissingColumn) and dropped from the result, and a column no field
// claims is ignored — the "extra allowed, missing is an error" rule the import
// policy follows. A field whose type the format cannot read into a value (a
// record, an enum, a foreign-key reference) is reported once and dropped too.
//
// Each remaining cell is converted by its field's underlying primitive; a value
// that is not a valid one of that type is reported (CellTypeMismatch) and left
// as a gap (a nil cell value) so the rest of the row still types. Coerce does
// not run a field type's refinement predicate — that needs the engine's
// evaluator, which sits below this layer; its consumer runs it over the typed
// table this returns. The result's columns are the bound field names, in field
// order (not the source's column order), so every typed table of one master has
// the same shape.
func Coerce(raw Table, fields []ir.Field, src SourceSpec) (Table, []diagnostic.Diagnostic) {
	var diags []diagnostic.Diagnostic

	// First occurrence wins on a duplicate header, so a stray repeated column
	// does not shadow the one a field already bound to.
	colOf := make(map[string]int, len(raw.Columns))
	for i, name := range raw.Columns {
		if _, dup := colOf[name]; !dup {
			colOf[name] = i
		}
	}

	type binding struct {
		field ir.Field
		col   int
	}
	bindings := make([]binding, 0, len(fields))
	cols := make([]string, 0, len(fields))
	for _, f := range fields {
		if _, ok := scalarKind(f.Type); !ok {
			diags = append(diags, UnsupportedFieldType(src.Offset, src.Width, f.Name, typeName(f.Type)))
			continue
		}
		col, ok := colOf[f.Name]
		if !ok {
			diags = append(diags, MissingColumn(src.Offset, src.Width, f.Name, src.Display))
			continue
		}
		bindings = append(bindings, binding{field: f, col: col})
		cols = append(cols, f.Name)
	}

	out := Table{Columns: cols, Rows: make([]Row, 0, len(raw.Rows))}
	for _, row := range raw.Rows {
		cells := make([]Cell, 0, len(bindings))
		for _, b := range bindings {
			cells = append(cells, coerceCell(row, b.col, b.field, src, &diags))
		}
		out.Rows = append(out.Rows, Row{Cells: cells})
	}
	return out, diags
}

// coerceCell converts one row's cell for a bound field, recording a diagnostic
// and returning a gap cell (the origin kept, the value nil) when it cannot. A
// row shorter than its header is defended against here — the csv reader rejects
// a ragged row before this, but Coerce is also called on hand-built tables.
func coerceCell(row Row, col int, field ir.Field, src SourceSpec, diags *[]diagnostic.Diagnostic) Cell {
	if col >= len(row.Cells) {
		*diags = append(*diags, CellTypeMismatch(src.Offset, src.Width, src.Display, rowLine(row), col+1, field.Name, "", typeName(field.Type)))
		return Cell{}
	}
	rc := row.Cells[col]
	v, ok := coerceScalar(rc.Value, field.Type)
	if !ok {
		*diags = append(*diags, CellTypeMismatch(src.Offset, src.Width, src.Display, rc.Origin.Row, rc.Origin.Col, field.Name, rawText(rc.Value), typeName(field.Type)))
		return Cell{Origin: rc.Origin}
	}
	return Cell{Value: v, Origin: rc.Origin}
}

// RowFields returns the fields of a master's row type — its record fields,
// whether the row is written as an inline record or a named record alias — or
// false when the row is absent or is not a record (a malformed master the
// engine already reported, so the caller skips it).
func RowFields(row ir.Type) ([]ir.Field, bool) {
	switch r := row.(type) {
	case *ir.Record:
		return r.Fields, true
	case *ir.Named:
		if r.Def != nil {
			return RowFields(r.Def.Body)
		}
	}
	return nil, false
}

// scalar is the family of primitive a cell text is read as.
type scalar int

const (
	scalarSignedInt   scalar = iota // the signed integer family (int, long, nint, ...)
	scalarUnsignedInt               // the unsigned integer family (uint, byte, ...)
	scalarBool
	scalarString
)

// intFamily names the integer primitives and their signedness. The csv reader
// cannot import the builtin registry that owns these names (the one-way import
// boundary), so the vocabulary is spelled here; an integer primitive added to
// the registry but not here simply reads as unsupported until it is added.
var intFamily = map[string]scalar{
	"nint": scalarSignedInt, "sbyte": scalarSignedInt, "short": scalarSignedInt, "int": scalarSignedInt, "long": scalarSignedInt,
	"nuint": scalarUnsignedInt, "byte": scalarUnsignedInt, "ushort": scalarUnsignedInt, "uint": scalarUnsignedInt, "ulong": scalarUnsignedInt,
}

// scalarKind classifies a field type as the primitive a cell converts to, or
// false when the type is not a scalar the format reads (a record, an enum, a
// collection). A refined or aliased type is looked through to its underlying
// primitive, so type Level = int where ... reads as an integer.
func scalarKind(t ir.Type) (scalar, bool) {
	name, ok := underlyingBuiltin(t)
	if !ok {
		return 0, false
	}
	switch name {
	case "bool":
		return scalarBool, true
	case "string":
		return scalarString, true
	default:
		k, ok := intFamily[name]
		return k, ok
	}
}

// underlyingBuiltin unwraps a type to the primitive at its base — through a
// named alias or refinement (whose Body is the underlying type) — or false when
// the base is not a primitive.
func underlyingBuiltin(t ir.Type) (string, bool) {
	switch tt := t.(type) {
	case *ir.Builtin:
		return tt.Name, true
	case *ir.Named:
		if tt.Def != nil && tt.Def.Body != nil {
			return underlyingBuiltin(tt.Def.Body)
		}
	}
	return "", false
}

// coerceScalar converts a raw string cell to a constant of t's primitive, or
// false when the text is not a valid value of it. An out-of-range value for a
// sized integer is not range-checked here (only its sign, for an unsigned one);
// the type's own range is a later concern.
func coerceScalar(raw *ir.Constant, t ir.Type) (*ir.Constant, bool) {
	if raw == nil || raw.Kind != ir.ConstString {
		return nil, false
	}
	kind, ok := scalarKind(t)
	if !ok {
		return nil, false
	}
	s := raw.Str
	switch kind {
	case scalarString:
		return ir.StringConstant(s), true
	case scalarBool:
		switch s {
		case "true":
			return ir.BoolConstant(true), true
		case "false":
			return ir.BoolConstant(false), true
		}
		return nil, false
	default:
		n, ok := new(big.Int).SetString(s, 10)
		if !ok {
			return nil, false
		}
		if kind == scalarUnsignedInt && n.Sign() < 0 {
			return nil, false
		}
		return ir.IntConstant(n), true
	}
}

// typeName is the type's name as a diagnostic spells it (int, Level).
func typeName(t ir.Type) string {
	if t == nil {
		return "?"
	}
	return t.String()
}

// rawText is the source text of a raw cell, for a type-mismatch diagnostic.
func rawText(c *ir.Constant) string {
	if c == nil {
		return ""
	}
	if c.Kind == ir.ConstString {
		return c.Str
	}
	return c.String()
}

// rowLine is the source line a row sits on, read from its first cell's origin,
// or 0 for an empty row — the best a short row can be pointed at.
func rowLine(row Row) int {
	if len(row.Cells) > 0 {
		return row.Cells[0].Origin.Row
	}
	return 0
}

// String renders a typed table for a golden dump: the column names, then one
// line per row of the cells' values joined by " | ". A gap cell (a value that
// failed to coerce) renders as its unevaluated marker, so a dump shows exactly
// which cells the diagnostics faulted.
func (t Table) String() string {
	var b strings.Builder
	b.WriteString(strings.Join(t.Columns, ", "))
	b.WriteByte('\n')
	for _, row := range t.Rows {
		parts := make([]string, len(row.Cells))
		for i, c := range row.Cells {
			parts[i] = c.Value.String()
		}
		b.WriteString(strings.Join(parts, " | "))
		b.WriteByte('\n')
	}
	return b.String()
}

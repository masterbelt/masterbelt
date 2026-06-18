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
		if !scalarSupported(f.Type) {
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
// false when the row is absent, is not a record (a malformed master the engine
// already reported), or is a shape the reader does not expand (a generic
// application). Following the alias chain keeps a visited set, so a cyclic alias
// the engine flags as an error cannot send it into an unbounded recursion.
func RowFields(row ir.Type) ([]ir.Field, bool) {
	seen := map[*ir.TypeDef]bool{}
	for {
		switch r := row.(type) {
		case *ir.Record:
			return r.Fields, true
		case *ir.Named:
			if r.Def == nil || seen[r.Def] {
				return nil, false
			}
			seen[r.Def] = true
			row = r.Def.Body
		default:
			return nil, false
		}
	}
}

// The primitive type names the data layer reads, named so the same literal is
// not spelled in several places.
const (
	primBool   = "bool"
	primString = "string"
)

// intType describes an integer primitive: its signedness and bit width (0 width
// is the arbitrary-precision nint/nuint, which have no fixed range).
type intType struct {
	signed bool
	bits   int
}

// intFamily names the integer primitives with their signedness and width. The
// reader cannot import the builtin registry that owns these (the one-way import
// boundary), so the vocabulary is spelled here; an integer primitive added to
// the registry but not here simply reads as unsupported until it is added.
var intFamily = map[string]intType{
	"nint": {true, 0}, "sbyte": {true, 8}, "short": {true, 16}, "int": {true, 32}, "long": {true, 64},
	"nuint": {false, 0}, "byte": {false, 8}, "ushort": {false, 16}, "uint": {false, 32}, "ulong": {false, 64},
}

// fits reports whether n is in range for the integer type: its sign for an
// unsigned one, and its two's-complement bounds for a fixed width (a 0 width is
// arbitrary precision, so only the sign constrains it).
func (k intType) fits(n *big.Int) bool {
	if !k.signed && n.Sign() < 0 {
		return false
	}
	if k.bits == 0 {
		return true
	}
	if k.signed {
		hi := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), uint(k.bits-1)), big.NewInt(1))
		lo := new(big.Int).Neg(new(big.Int).Lsh(big.NewInt(1), uint(k.bits-1)))
		return n.Cmp(lo) >= 0 && n.Cmp(hi) <= 0
	}
	hi := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), uint(k.bits)), big.NewInt(1))
	return n.Cmp(hi) <= 0
}

// scalarSupported reports whether a field type is a scalar the format reads — a
// primitive integer, bool, or string, or an enum (read by member name). A refined or
// aliased type is looked through to its underlying primitive, so type Level = int
// where ... is a scalar. A record, collection, or foreign-key reference is not.
func scalarSupported(t ir.Type) bool {
	if enumDef(t) != nil {
		return true
	}
	name, ok := underlyingBuiltin(t)
	if !ok {
		return false
	}
	switch name {
	case primBool, primString:
		return true
	default:
		_, ok := intFamily[name]
		return ok
	}
}

// enumDef returns the enum definition a type denotes — directly, or through a named
// alias of an enum — or nil when the type is not an enum. The walk is bounded by a
// visited set against a cyclic alias.
func enumDef(t ir.Type) *ir.TypeDef {
	seen := map[*ir.TypeDef]bool{}
	for {
		named, ok := t.(*ir.Named)
		if !ok || named.Def == nil || seen[named.Def] {
			return nil
		}
		if named.Def.Enum != nil {
			return named.Def
		}
		seen[named.Def] = true
		if named.Def.Body == nil {
			return nil
		}
		t = named.Def.Body
	}
}

// underlyingBuiltin unwraps a type to the primitive at its base — through a
// named alias or refinement (whose Body is the underlying type) — or false when
// the base is not a primitive (an unexpanded generic application, a record). A
// visited set bounds the walk, so a cyclic alias the engine flags as an error
// cannot send it into an unbounded recursion.
func underlyingBuiltin(t ir.Type) (string, bool) {
	seen := map[*ir.TypeDef]bool{}
	for {
		switch tt := t.(type) {
		case *ir.Builtin:
			return tt.Name, true
		case *ir.Named:
			if tt.Def == nil || tt.Def.Body == nil || seen[tt.Def] {
				return "", false
			}
			seen[tt.Def] = true
			t = tt.Def.Body
		default:
			return "", false
		}
	}
}

// coerceScalar converts a raw string cell to a constant of t's primitive, or
// false when the text is not a valid value of it — including an integer outside
// its field's fixed-width range (300 is not a byte).
func coerceScalar(raw *ir.Constant, t ir.Type) (*ir.Constant, bool) {
	if raw == nil || raw.Kind != ir.ConstString {
		return nil, false
	}
	if def := enumDef(t); def != nil {
		return coerceEnum(raw.Str, def)
	}
	name, ok := underlyingBuiltin(t)
	if !ok {
		return nil, false
	}
	s := raw.Str
	switch name {
	case primString:
		return ir.StringConstant(s), true
	case primBool:
		switch s {
		case "true":
			return ir.BoolConstant(true), true
		case "false":
			return ir.BoolConstant(false), true
		}
		return nil, false
	default:
		kind, ok := intFamily[name]
		if !ok {
			return nil, false
		}
		n, ok := new(big.Int).SetString(s, 10)
		if !ok || !kind.fits(n) {
			return nil, false
		}
		return ir.IntConstant(n), true
	}
}

// coerceEnum reads a cell as an enum member by its name — the form a source spells a
// member, common rather than its underlying 0 — yielding the member as an enum
// constant so a per-row check compares it as the enum it is. A text matching no member
// is a type mismatch, like an out-of-range integer.
func coerceEnum(s string, def *ir.TypeDef) (*ir.Constant, bool) {
	for i, m := range def.Enum.Members {
		if m.Name == s {
			return &ir.Constant{Kind: ir.ConstEnum, EnumDef: def, EnumIndex: i}, true
		}
	}
	return nil, false
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

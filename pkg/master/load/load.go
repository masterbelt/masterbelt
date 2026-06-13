// Package load is the master data layer's orchestrator: given the resolved
// program, it reads every master's declared sources through the format registry,
// coerces each row against the master's fields, and runs the refined fields'
// predicates — returning the typed tables and every diagnostic.
//
// It sits a step above the master core (pkg/master, which holds only the seam,
// the registry, and the pure coercion over the IR) because the work needs more
// than the core may import: the declaration's syntax for the source entries
// (ast), the file's evaluator for the refinement predicates (eval), and the red
// tree for the locator span (cst). That is exactly what a master sub-package is
// for — the core's tight import boundary constrains the core alone, while master
// may depend on the belt layer beneath it. Keeping this here, rather than in the
// CLI that drives it, leaves the command a thin driver and makes the read path
// testable on its own and reusable by other consumers.
package load

import (
	"path/filepath"
	"strconv"
	"strings"

	"github.com/masterbelt/masterbelt/pkg/belt/eval"
	"github.com/masterbelt/masterbelt/pkg/belt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/belt/semantic"
	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/master"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
	"github.com/masterbelt/masterbelt/pkg/source/token"
)

// Loaded is one master's typed rows from one source: the master's name, the
// source's display path, and the coerced table. A master with several sources
// yields one Loaded per source — merging them is a later step.
type Loaded struct {
	Master  string
	Display string
	Table   master.Table
}

// File reads and checks every master declared in file. For each source it
// resolves the location under root, beneath the base path bases gives for that
// format (the empty string for the root); reads it through the registered
// format; coerces the rows; and runs the refined fields' predicates. It returns
// the typed tables and every read, coercion, refinement, and option diagnostic,
// each anchored at the source declaration in the .belt file. A file the program
// has not resolved yields nothing.
func File(prog *semantic.Program, file semantic.FileID, root string, bases map[string]string, reg *master.Registry) ([]Loaded, []diagnostic.Diagnostic) {
	module := prog.Module(file)
	if module == nil {
		return nil, nil
	}
	doc := prog.Document(file)
	env := prog.EvalEnv(file)

	var loaded []Loaded
	var diags []diagnostic.Diagnostic
	for _, def := range module.Types {
		if def.Master == nil || def.MasterSyntax == nil {
			continue
		}
		l, d := readMaster(def, doc, env, root, bases, reg)
		loaded = append(loaded, l...)
		diags = append(diags, d...)
	}
	return loaded, diags
}

// readMaster reads, coerces, and checks every source of one master. Each source
// is read on its own — merging several into one table is a later step — so a
// master listing two sources yields two Loaded tables.
func readMaster(def *ir.TypeDef, doc *abstract.Document, env eval.GraphEnv, root string, bases map[string]string, reg *master.Registry) ([]Loaded, []diagnostic.Diagnostic) {
	fields, ok := master.RowFields(def.Master.Row)
	if !ok {
		// The row is present and the program checked (the caller skips files with
		// errors), so this is not a malformed row but one the reader cannot
		// expand — a generic row alias the language does not read for masters
		// yet. Report it at the sources rather than silently ignoring them.
		return nil, atFirstSource(doc, def, func(offset, width int) diagnostic.Diagnostic {
			return master.UnsupportedRowType(offset, width, def.Name)
		})
	}
	if dup, ok := firstDuplicate(fieldNames(fields)); ok {
		// A row with a repeated field name binds its cells and runs its
		// refinements ambiguously (the later field shadows the earlier); report
		// it rather than load an ambiguous table.
		return nil, atFirstSource(doc, def, func(offset, width int) diagnostic.Diagnostic {
			return master.DuplicateRowField(offset, width, dup, def.Name)
		})
	}
	var loaded []Loaded
	var diags []diagnostic.Diagnostic
	for _, entry := range def.MasterSyntax.Sources {
		offset, width := locatorSpan(doc, entry)
		format, found := reg.Lookup(entry.Format)
		if !found {
			diags = append(diags, master.UnknownFormat(offset, width, entry.Format))
			continue
		}
		opts, optDiags := checkOptions(entry, format, offset, width)
		if len(optDiags) > 0 {
			// An invalid option must not be read with a fallback default; report
			// it and leave the source unread.
			diags = append(diags, optDiags...)
			continue
		}

		// The locator must stay within its format's source directory on every
		// platform: not absolute (a leading slash or a Windows drive prefix), and
		// not climbing out of the base path with `..`. Backslashes are treated as
		// separators first, so a portable path is judged the same everywhere.
		loc := strings.ReplaceAll(entry.Locator, "\\", "/")
		if filepath.IsAbs(loc) || volumeQualified(loc) || climbsOut(loc) {
			diags = append(diags, master.LocatorEscapesRoot(offset, width, entry.Locator))
			continue
		}
		rel := filepath.Join(bases[entry.Format], loc)
		spec := master.SourceSpec{
			Path:    filepath.Join(root, rel),
			Display: filepath.ToSlash(rel),
			Options: opts,
			Offset:  offset,
			Width:   width,
		}
		raw, readDiags := format.Read(spec)
		if readDiags.Len() > 0 {
			// The read failed (a missing file, a malformed body); coercing the
			// empty table it returned would only pile a missing-column error onto
			// every field. Report the read failure and move on.
			diags = append(diags, readDiags.Items()...)
			continue
		}

		typed, coerceDiags := master.Coerce(raw, fields, spec)
		diags = append(diags, coerceDiags...)
		diags = append(diags, checkRefinements(typed, fields, spec, env)...)
		diags = append(diags, checkRowValidations(typed, fields, def, doc, spec, env)...)
		diags = append(diags, checkDuplicatePrimaryKeys(typed, def, spec)...)

		loaded = append(loaded, Loaded{Master: def.Name, Display: spec.Display, Table: typed})
	}
	return loaded, diags
}

// checkRefinements runs each refined field's predicate over its typed cells,
// reporting the cell a value that fails it came from. Coercion runs no predicate
// — it needs the engine's evaluator, reached here through the program's eval env
// — so this is where a where-clause range check fires. Only a predicate the
// engine compiled to a usable form (def.Where set) is run; the engine compiles
// one that reads self, literals, and self's own methods, so it folds the same in
// any of the program's file envs.
func checkRefinements(typed master.Table, fields []ir.Field, spec master.SourceSpec, env eval.GraphEnv) []diagnostic.Diagnostic {
	byName := make(map[string]ir.Field, len(fields))
	for _, f := range fields {
		byName[f.Name] = f
	}
	var diags []diagnostic.Diagnostic
	for _, row := range typed.Rows {
		for i, col := range typed.Columns {
			cell := row.Cells[i]
			if cell.Value == nil {
				continue // a coercion gap, already reported
			}
			for _, def := range refinedDefs(byName[col].Type) {
				v := eval.GraphPredicate(def.Where, cell.Value, def, env)
				if v == nil || v.Kind != ir.ConstBool || !v.Bool {
					diags = append(diags, master.CellRefinement(spec.Offset, spec.Width, spec.Display, cell.Origin.Row, cell.Origin.Col, col, cell.Value.String(), def.Name))
					break // one violation per cell is enough
				}
			}
		}
	}
	return diags
}

// checkRowValidations folds each of the master's per-row validate checks over
// every loaded row, reporting a row that fails one. Each check is an assert
// condition resolved to a value graph over self (the row), so a row is bound as
// a record constant of its cells and the predicate folds against it through the
// interpreter — the per-row evaluator the north star keeps validation on (the
// aggregate and join checks are a SQLite concern, not this one). A row carrying a
// coercion gap is skipped: the gap was already reported, and folding a predicate
// over a missing field would only pile a second, derived error onto it. A
// failure anchors at the assert in the .belt declaration — the check that failed
// — and names the failing row as path:row in the message.
func checkRowValidations(typed master.Table, fields []ir.Field, def *ir.TypeDef, doc *abstract.Document, spec master.SourceSpec, env eval.GraphEnv) []diagnostic.Diagnostic {
	if len(def.Master.RowChecks) == 0 || !tableHasFields(typed, fields) {
		// A source missing a declared column already reported missing_column and
		// dropped that field, so a self record built here would lack it and every
		// check reading it would fold to nil and fail — a derived error on every
		// row. Leave the malformed source to its own report.
		return nil
	}
	var diags []diagnostic.Diagnostic
	for _, row := range typed.Rows {
		self, line, ok := rowConstant(typed.Columns, row)
		if !ok {
			continue // a coercion gap, already reported
		}
		for _, check := range def.Master.RowChecks {
			// A row passes a check only when its predicate folds to a definite true.
			// A definite false fails it; so does a predicate that does not fold to a
			// bool at all (a violated assertion in a row method it calls, an
			// unevaluable expression) — a check that cannot confirm the row is valid
			// fails it rather than passing silently, the fail-safe a data check wants.
			v := eval.GraphPredicate(check.Cond, self, def, env)
			if v == nil || v.Kind != ir.ConstBool || !v.Bool {
				offset, width := assertSpan(doc, check.Syntax)
				diags = append(diags, master.RowValidationFailed(offset, width, spec.Display, line))
			}
		}
	}
	return diags
}

// tableHasFields reports whether the coerced table carries a column for every
// field the master declares — the precondition a per-row check needs, since a
// self record missing a field folds every read of it to nil. A missing column is
// already reported by the coercion, so this only gates the derived check.
func tableHasFields(typed master.Table, fields []ir.Field) bool {
	present := make(map[string]bool, len(typed.Columns))
	for _, c := range typed.Columns {
		present[c] = true
	}
	for _, f := range fields {
		if !present[f.Name] {
			return false
		}
	}
	return true
}

// rowConstant builds the record constant a row's per-row checks fold self
// against — one field per column, in the canonical order RecordConstant settles
// — and the row's source line for the diagnostic. It returns false when any cell
// is a coercion gap (a nil value): the row is already faulted, so its checks are
// not run on a record missing a field.
func rowConstant(columns []string, row master.Row) (*ir.Constant, int, bool) {
	fields := make([]ir.ConstField, 0, len(columns))
	line := 0
	for i, col := range columns {
		cell := row.Cells[i]
		if cell.Value == nil {
			return nil, 0, false
		}
		if line == 0 {
			line = cell.Origin.Row
		}
		fields = append(fields, ir.ConstField{Name: col, Value: cell.Value})
	}
	return ir.RecordConstant(fields), line, true
}

// keyColumn pairs a primary-key column's name with its index in the typed
// table — the column the key reads, named for the diagnostic.
type keyColumn struct {
	name  string
	index int
}

// checkDuplicatePrimaryKeys reports a row whose primary key repeats one an
// earlier row already carries — the duplicate that breaks the master's row
// identity (the primary key is what identifies a row). It reads the key tuple of
// each row from its primary columns, keeping the first occurrence of each as the
// baseline and faulting every later one at its own key cell. A key cell that is a
// coercion gap is skipped (already reported), and a primary that names a column
// the source did not supply is left to the missing-column report rather than
// faulted again here.
func checkDuplicatePrimaryKeys(typed master.Table, def *ir.TypeDef, spec master.SourceSpec) []diagnostic.Diagnostic {
	cols, ok := primaryColumns(typed.Columns, def.Master.Primary)
	if !ok {
		return nil
	}
	var diags []diagnostic.Diagnostic
	first := make(map[string]int, len(typed.Rows))
	for _, row := range typed.Rows {
		key, rendered, anchor, ok := primaryKey(row, cols)
		if !ok {
			continue // a coercion gap in a key cell, already reported
		}
		if at, dup := first[key]; dup {
			diags = append(diags, master.DuplicatePrimaryKey(spec.Offset, spec.Width, spec.Display, anchor.Row, anchor.Col, rendered, at))
			continue
		}
		first[key] = anchor.Row
	}
	return diags
}

// primaryColumns resolves each primary-key column name to its index in the typed
// table, in key order. It returns false when a key names a column the table does
// not have — a missing column the coercion already reported — so the duplicate
// check does not run on an incomplete key.
func primaryColumns(columns []string, primary []string) ([]keyColumn, bool) {
	if len(primary) == 0 {
		return nil, false
	}
	index := make(map[string]int, len(columns))
	for i, c := range columns {
		index[c] = i
	}
	cols := make([]keyColumn, 0, len(primary))
	for _, name := range primary {
		i, ok := index[name]
		if !ok {
			return nil, false
		}
		cols = append(cols, keyColumn{name: name, index: i})
	}
	return cols, true
}

// primaryKey reads a row's primary-key tuple: a comparison key uniquely
// determined by the cell values (the NUL-joined canonical forms — equal keys
// share it, distinct keys do not, since each value's String is its canonical
// text), a rendered "col=value" form for the diagnostic, and the anchor cell (the
// first key column's, where a duplicate is faulted). It returns false when any
// key cell is a coercion gap (a nil value), which leaves the row's identity
// unknown and already reported.
func primaryKey(row master.Row, cols []keyColumn) (key, rendered string, anchor master.Origin, ok bool) {
	var keyB, renderedB strings.Builder
	for i, c := range cols {
		cell := row.Cells[c.index]
		if cell.Value == nil {
			return "", "", master.Origin{}, false
		}
		if i == 0 {
			anchor = cell.Origin
		} else {
			keyB.WriteByte(0)
			renderedB.WriteString(", ")
		}
		keyB.WriteString(cell.Value.String())
		renderedB.WriteString(c.name)
		renderedB.WriteByte('=')
		renderedB.WriteString(cell.Value.String())
	}
	return keyB.String(), renderedB.String(), anchor, true
}

// assertSpan is the byte span of a validate check's assert statement in its .belt
// file — what a row-validation diagnostic anchors to, since the row it faults
// lives in a data file the diagnostic model does not address. It falls back to no
// span when the statement has no syntax (a check built outside source).
func assertSpan(doc *abstract.Document, syntax *ast.AssertStmt) (int, int) {
	if syntax == nil {
		return 0, 0
	}
	tree, ok := findGreen(doc.Concrete().Tree(), syntax.Syntax())
	if !ok {
		return 0, 0
	}
	return tree.Offset(), tree.Width()
}

// atFirstSource builds a master-level diagnostic anchored at the first source
// declaration's locator (where a master-wide problem is shown), or nothing when
// the master declares no source to anchor to.
func atFirstSource(doc *abstract.Document, def *ir.TypeDef, build func(offset, width int) diagnostic.Diagnostic) []diagnostic.Diagnostic {
	if len(def.MasterSyntax.Sources) == 0 {
		return nil
	}
	offset, width := locatorSpan(doc, def.MasterSyntax.Sources[0])
	return []diagnostic.Diagnostic{build(offset, width)}
}

// fieldNames lists a row's field names in order.
func fieldNames(fields []ir.Field) []string {
	names := make([]string, len(fields))
	for i, f := range fields {
		names[i] = f.Name
	}
	return names
}

// firstDuplicate returns the first value that appears more than once in order,
// or false when every value is distinct.
func firstDuplicate(values []string) (string, bool) {
	seen := make(map[string]bool, len(values))
	for _, v := range values {
		if seen[v] {
			return v, true
		}
		seen[v] = true
	}
	return "", false
}

// refinedDefs collects the refined definitions in a field type's alias chain,
// outermost first — every named type with a where-clause reached by following
// each alias to its underlying type. A plain alias of a refined type
// (type Level = Positive) carries no predicate of its own, so checking only the
// outer type would skip the one beneath it; this walks the whole chain so every
// predicate runs.
func refinedDefs(t ir.Type) []*ir.TypeDef {
	var defs []*ir.TypeDef
	seen := map[*ir.TypeDef]bool{}
	for {
		named, ok := t.(*ir.Named)
		if !ok || named.Def == nil || seen[named.Def] {
			return defs
		}
		seen[named.Def] = true
		if named.Def.Where != nil {
			defs = append(defs, named.Def)
		}
		t = named.Def.Body
	}
}

// climbsOut reports whether a relative path escapes the directory it is resolved
// against — through `..` segments that climb out of it. A locator is resolved
// against its format's base directory, so a locator that climbs out leaves that
// directory (and, since the base is itself confined to the root, can leave the
// root too); this is the same confinement the manifest's base paths obey.
func climbsOut(rel string) bool {
	clean := filepath.Clean(rel)
	return clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

// volumeQualified reports whether a slash-normalized path carries a Windows
// drive/volume prefix (C:/...), which is absolute on Windows even though
// filepath.IsAbs does not recognize it off Windows. A locator must be relative
// to its source directory on every platform, so this is refused alongside an
// absolute path — the same cross-platform rule the manifest's paths obey.
func volumeQualified(p string) bool {
	return len(p) >= 2 && p[1] == ':' &&
		(p[0] >= 'A' && p[0] <= 'Z' || p[0] >= 'a' && p[0] <= 'z')
}

// checkOptions validates a source entry's options against the format's specs and
// returns the ones it accepted, flattened to strings. An unknown key or a value
// of the wrong type is reported (and dropped); anything else passes through for
// the format to interpret.
func checkOptions(entry *ast.SourceEntry, format master.Format, offset, width int) (map[string]string, []diagnostic.Diagnostic) {
	opts := map[string]string{}
	lit, ok := entry.Options.(*ast.RecordLit)
	if !ok {
		return opts, nil // no options, or a malformed one the parser recovered
	}
	specs := make(map[string]master.OptionKind, len(format.OptionSpecs()))
	for _, s := range format.OptionSpecs() {
		specs[s.Name] = s.Kind
	}
	var diags []diagnostic.Diagnostic
	seen := make(map[string]bool, len(lit.Fields))
	for _, field := range lit.Fields {
		if seen[field.Name] {
			// A repeated option would silently resolve to whichever value came
			// last; an ambiguous declaration is reported, not interpreted.
			diags = append(diags, master.DuplicateOption(offset, width, format.Name(), field.Name))
			continue
		}
		seen[field.Name] = true
		want, known := specs[field.Name]
		if !known {
			diags = append(diags, master.UnknownOption(offset, width, format.Name(), field.Name))
			continue
		}
		value, got, ok := literalValue(field.Value)
		if !ok || got != want {
			diags = append(diags, master.OptionTypeMismatch(offset, width, field.Name, want.String()))
			continue
		}
		opts[field.Name] = value
	}
	return opts, diags
}

// literalValue reads a record-literal option value as its string form and kind,
// or false when it is not a scalar literal (an option must be a constant the
// declaration spells out, not a computed expression).
func literalValue(e ast.Expr) (string, master.OptionKind, bool) {
	switch l := e.(type) {
	case *ast.StringLit:
		return l.Value, master.OptionString, true
	case *ast.BoolLit:
		return strconv.FormatBool(l.Value), master.OptionBool, true
	case *ast.IntLit:
		return l.Text, master.OptionInt, true
	default:
		return "", 0, false
	}
}

// locatorSpan is the byte span of a source entry's locator string in its .belt
// file — what a diagnostic about the source's data anchors to, since the data
// lives in a file the diagnostic model does not address. It falls back to the
// whole entry when the locator string is absent (a malformed entry).
func locatorSpan(doc *abstract.Document, entry *ast.SourceEntry) (int, int) {
	entryTree, ok := findGreen(doc.Concrete().Tree(), entry.Syntax())
	if !ok {
		return 0, 0
	}
	if tok, ok := firstToken(entryTree, token.String); ok {
		return tok.Offset(), tok.Width()
	}
	return entryTree.Offset(), entryTree.Width()
}

// findGreen returns the positioned tree for a green node, found by identity in
// the red tree.
func findGreen(root cst.Tree, target *cst.Node) (cst.Tree, bool) {
	if n, ok := root.Node(); ok && n == target {
		return root, true
	}
	for _, child := range root.Children() {
		if t, ok := findGreen(child, target); ok {
			return t, true
		}
	}
	return cst.Tree{}, false
}

// firstToken returns the first token of the given kind in t, in source order.
func firstToken(t cst.Tree, kind token.Kind) (cst.Tree, bool) {
	if k, ok := t.TokenKind(); ok && k == kind {
		return t, true
	}
	for _, child := range t.Children() {
		if found, ok := firstToken(child, kind); ok {
			return found, true
		}
	}
	return cst.Tree{}, false
}

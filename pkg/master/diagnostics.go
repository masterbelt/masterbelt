package master

import "github.com/masterbelt/masterbelt/pkg/diagnostic"

// The diagnostics the master data layer reports. The codes and the per-code
// constructors are generated from the diagnostic catalog (diagnostic_gen.go);
// these are the thin exported wrappers a caller above the core builds them
// through — a concrete format reporting a read failure, the integration layer
// reporting a bad option or a refinement violation — since the generated
// constructors are unexported. Coerce, inside the core, uses them too, so the
// layer has one diagnostic vocabulary in one place.
//
// Every one anchors at offset/width — the source declaration's span in the
// .belt file — because the data it faults lives in a separate file the
// diagnostic model does not address; the precise data location travels in the
// message instead (see Origin and SourceSpec).

// UnknownFormat reports that a source declaration named a format the toolchain
// has no reader for.
func UnknownFormat(offset, width int, format string) diagnostic.Diagnostic {
	return newUnknownFormatDiagnostic(offset, width, format)
}

// LocatorEscapesRoot reports that a source locator resolves outside the project
// root — through `..` segments or an absolute path — which the data command does
// not read, the same confinement the manifest's base paths obey.
func LocatorEscapesRoot(offset, width int, path string) diagnostic.Diagnostic {
	return newLocatorEscapesRootDiagnostic(offset, width, path)
}

// SourceUnreadable reports that a format could not read a source at all — a
// missing file, an unreadable one, a malformed body — detail carrying the
// underlying reason.
func SourceUnreadable(offset, width int, path, detail string) diagnostic.Diagnostic {
	return newSourceUnreadableDiagnostic(offset, width, path, detail)
}

// MissingColumn reports that a source has no column to fill a master field.
func MissingColumn(offset, width int, field, path string) diagnostic.Diagnostic {
	return newMissingColumnDiagnostic(offset, width, field, path)
}

// UnsupportedFieldType reports that a master field's type is one the format
// cannot read into a value yet (anything but the scalar primitives, for csv).
func UnsupportedFieldType(offset, width int, field, typ string) diagnostic.Diagnostic {
	return newUnsupportedFieldTypeDiagnostic(offset, width, field, typ)
}

// UnsupportedRowType reports that a master's row is a type the reader cannot
// expand into fields — a generic row alias the language does not read for
// masters yet — so its sources are reported rather than silently skipped.
func UnsupportedRowType(offset, width int, master string) diagnostic.Diagnostic {
	return newUnsupportedRowTypeDiagnostic(offset, width, master)
}

// CellTypeMismatch reports that a cell's value is not a valid value of its
// field's declared type.
func CellTypeMismatch(offset, width int, path string, row, col int, field, value, typ string) diagnostic.Diagnostic {
	return newCellTypeMismatchDiagnostic(offset, width, path, row, col, field, value, typ)
}

// CellRefinement reports that a cell's value has the right type but fails the
// field type's refinement predicate (a where-clause range check).
func CellRefinement(offset, width int, path string, row, col int, field, value, typ string) diagnostic.Diagnostic {
	return newCellRefinementDiagnostic(offset, width, path, row, col, field, value, typ)
}

// UnknownOption reports that a source declaration set an option the format does
// not understand.
func UnknownOption(offset, width int, format, key string) diagnostic.Diagnostic {
	return newUnknownOptionDiagnostic(offset, width, format, key)
}

// OptionTypeMismatch reports that a source declaration set a known option to a
// value of the wrong type.
func OptionTypeMismatch(offset, width int, key, typ string) diagnostic.Diagnostic {
	return newOptionTypeMismatchDiagnostic(offset, width, key, typ)
}

// DuplicateOption reports that a source declaration set the same option more
// than once, which would silently resolve to whichever value came last.
func DuplicateOption(offset, width int, format, key string) diagnostic.Diagnostic {
	return newDuplicateOptionDiagnostic(offset, width, format, key)
}

// DuplicateRowField reports that a master's row declares the same field name
// more than once, leaving its cells and refinements ambiguous.
func DuplicateRowField(offset, width int, field, master string) diagnostic.Diagnostic {
	return newDuplicateRowFieldDiagnostic(offset, width, field, master)
}

// RowValidationFailed reports that a loaded row does not satisfy one of its
// master's per-row validate each checks. It anchors at the assert in the .belt
// declaration — the check that failed, the span the editor shows it on — while
// the failing row travels in the message as path:row, since the data lives in a
// separate file the diagnostic model does not address.
func RowValidationFailed(offset, width int, path string, row int) diagnostic.Diagnostic {
	return newRowValidationFailedDiagnostic(offset, width, path, row)
}

// TableValidationFailed reports that a master's loaded rows do not satisfy one of
// its per-table validate all checks — an aggregate over the whole table, such as a
// row-count cap. It anchors at the assert in the .belt declaration, the check that
// failed; the table travels in the message as path, since the data lives in a
// separate file the diagnostic model does not address.
func TableValidationFailed(offset, width int, path string) diagnostic.Diagnostic {
	return newTableValidationFailedDiagnostic(offset, width, path)
}

// DuplicatePrimaryKey reports that a row's primary key repeats one an earlier row
// already carries — the master's rows are not uniquely identified. It anchors at
// the source declaration like the other cell diagnostics, naming the duplicate
// cell as path:row,col (the later occurrence, the one to change — the first is
// kept as the baseline), the repeated key value, and the first occurrence's row.
func DuplicatePrimaryKey(offset, width int, path string, row, col int, key string, first int) diagnostic.Diagnostic {
	return newDuplicatePrimaryKeyDiagnostic(offset, width, path, row, col, key, first)
}

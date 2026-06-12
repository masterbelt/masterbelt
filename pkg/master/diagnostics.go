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

package source

// Position is a resolved location within a file: an absolute byte offset
// together with its human-facing 1-based line and 1-based, byte-measured
// column. It is produced by File.Position for diagnostics and display.
//
// Columns here are measured in UTF-8 bytes. For editor protocols that count
// columns differently (notably LSP, which defaults to UTF-16 code units), use
// File.LineColumn / File.OffsetAt with the appropriate Encoding instead.
type Position struct {
	ByteOffset int
	Line       int
	Column     int
}

// Encoding selects the unit in which a column is measured within its line.
type Encoding int

const (
	// ByteEncoding measures columns in UTF-8 bytes. This is the unit used by
	// Position and by human-facing diagnostics.
	ByteEncoding Encoding = iota
	// UTF16Encoding measures columns in UTF-16 code units. This is the default
	// position encoding required by the Language Server Protocol.
	UTF16Encoding
	// UTF32Encoding measures columns in Unicode code points (runes).
	UTF32Encoding
)

func (e Encoding) String() string {
	switch e {
	case ByteEncoding:
		return "utf-8"
	case UTF16Encoding:
		return "utf-16"
	case UTF32Encoding:
		return "utf-32"
	default:
		return "unknown"
	}
}

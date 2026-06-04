package source

// Buffer is a source of bytes that can locate offsets within itself. Both the
// immutable File and the editable Text implement it, so the lexer, tokens, and
// diagnostics can work against either without caring which one they hold.
type Buffer interface {
	// Len reports the size of the buffer in bytes.
	Len() int
	// Slice returns the bytes in the range [start, end).
	Slice(start, end int) []byte
	// Position maps a byte offset to its 1-based, byte-measured Position.
	Position(offset int) Position
	// Span builds a Span covering the byte range [start, end).
	Span(start, end int) Span
	// LineColumn maps a byte offset to a 0-based line and column in enc units.
	LineColumn(offset int, enc Encoding) (line, column int)
	// OffsetAt is the inverse of LineColumn.
	OffsetAt(line, column int, enc Encoding) int
}

var (
	_ Buffer = File{}
	_ Buffer = (*File)(nil)
	_ Buffer = (*Text)(nil)
)

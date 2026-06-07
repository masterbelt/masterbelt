package source

// Span is a resolved half-open range [Start, End) within a buffer, given as
// the two endpoint Positions. It is produced by Buffer.Span (File.Span,
// Text.Span) and carries enough location to underline a region in a
// diagnostic.
type Span struct {
	Start Position
	End   Position
}

// Len returns the length of the span in bytes.
//
// A span's length is only well-defined as a byte count: subtracting the
// columns of positions on different lines is meaningless (and can go
// negative), so Len deliberately reports just the byte distance between
// Start and End.
func (s Span) Len() int {
	return s.End.ByteOffset - s.Start.ByteOffset
}

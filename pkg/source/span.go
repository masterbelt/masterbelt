package source

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

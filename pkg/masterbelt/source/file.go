package source

import "io"

type File struct {
	name        string
	src         []byte
	lineOffsets []int
}

func New(r io.Reader) (*File, error) {
	name := ""
	if hasName, ok := r.(interface{ Name() string }); ok {
		name = hasName.Name()
	}

	src, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}

	return NewFile(name, src), nil
}

func NewFile(name string, src []byte) *File {
	return &File{
		name:        name,
		src:         src,
		lineOffsets: lineStarts(src),
	}
}

func (f File) Name() string {
	return f.name
}

func (f File) Source() []byte {
	return f.src
}

func (f File) Size() int {
	return len(f.src)
}

// Len reports the size of the file in bytes. It is an alias of Size that
// satisfies the Buffer interface.
func (f File) Len() int {
	return len(f.src)
}

// Slice returns the bytes in the range [start, end).
func (f File) Slice(start, end int) []byte {
	return f.src[start:end]
}

// Position maps a byte offset to a Position with a 1-based line and a 1-based,
// byte-measured column — the representation used by diagnostics and humans.
// The offset is clamped to [0, Size], so out-of-range input returns the
// nearest valid boundary instead of panicking.
func (f File) Position(offset int) Position {
	offset = clampOffset(offset, len(f.src))
	line, column := lineColumn(f.lineOffsets, f.Slice, len(f.src), offset, ByteEncoding)
	return Position{ByteOffset: offset, Line: line + 1, Column: column + 1}
}

// LineColumn returns the 0-based line and the 0-based column of offset, with
// the column measured in enc code units. This matches the LSP Position model;
// pass UTF16Encoding for the default LSP position encoding. The offset is
// clamped to [0, Size].
func (f File) LineColumn(offset int, enc Encoding) (line, column int) {
	return lineColumn(f.lineOffsets, f.Slice, len(f.src), offset, enc)
}

// OffsetAt is the inverse of LineColumn: it returns the byte offset for the
// 0-based line and the 0-based column measured in enc units. Out-of-range
// input is clamped to the nearest valid offset.
func (f File) OffsetAt(line, column int, enc Encoding) int {
	return offsetAt(f.lineOffsets, f.Slice, len(f.src), line, column, enc)
}

// Offset is the inverse of Position: it returns the byte offset of a 1-based
// line and a 1-based, byte-measured column. Out-of-range input is clamped.
func (f File) Offset(line, column int) int {
	return f.OffsetAt(line-1, column-1, ByteEncoding)
}

// Span builds a Span covering the byte range [start, end).
func (f File) Span(start, end int) Span {
	return Span{Start: f.Position(start), End: f.Position(end)}
}

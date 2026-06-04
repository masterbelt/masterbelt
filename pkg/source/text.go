package source

import "sort"

// Text is an editable buffer backed by a piece table. Edits never rewrite the
// existing bytes: the original content and every inserted run are stored once
// in immutable backing slices, and an ordered list of pieces describes the
// current document as a sequence of views into them. Applying an edit costs
// time proportional to the number of pieces (not the document size), and the
// line-start index is patched in place rather than rescanned from scratch.
//
// Text implements Buffer, so it can be lexed and queried for positions exactly
// like a File.
type Text struct {
	original    []byte
	added       []byte
	pieces      []piece
	length      int
	lineOffsets []int
}

// Edit describes a single text change: the byte range [Start, End) is replaced
// with NewText. An insertion has Start == End; a deletion has empty NewText.
type Edit struct {
	Start, End int
	NewText    []byte
}

// piece is a contiguous view into one of Text's backing slices.
type piece struct {
	added  bool // false: into original, true: into added
	start  int  // offset within the backing slice
	length int
}

// NewText creates an editable buffer initialised with src. src is retained as
// the immutable original backing slice and must not be mutated by the caller.
func NewText(src []byte) *Text {
	t := &Text{
		original:    src,
		length:      len(src),
		lineOffsets: lineStarts(src),
	}
	if len(src) > 0 {
		t.pieces = []piece{{added: false, start: 0, length: len(src)}}
	}
	return t
}

// Edit replaces the bytes in [start, end) with replacement. The range is
// clamped to the document and a reversed range is normalised.
func (t *Text) Edit(start, end int, replacement []byte) {
	start = clampOffset(start, t.length)
	end = clampOffset(end, t.length)
	if end < start {
		start, end = end, start
	}

	t.lineOffsets = spliceLineStarts(t.lineOffsets, start, end, replacement)
	t.pieces = t.splicePieces(start, end, replacement)
	t.length += len(replacement) - (end - start)
}

// splicePieces rebuilds the piece list with [start, end) removed and
// replacement inserted at start.
func (t *Text) splicePieces(start, end int, replacement []byte) []piece {
	next := make([]piece, 0, len(t.pieces)+2)

	// Pieces (or piece prefixes) lying before start.
	pos := 0
	for _, p := range t.pieces {
		pEnd := pos + p.length
		if pos < start {
			if keep := min(pEnd, start) - pos; keep > 0 {
				next = append(next, piece{p.added, p.start, keep})
			}
		}
		pos = pEnd
	}

	// The replacement itself, appended once to the immutable added buffer.
	if len(replacement) > 0 {
		at := len(t.added)
		t.added = append(t.added, replacement...)
		next = append(next, piece{added: true, start: at, length: len(replacement)})
	}

	// Pieces (or piece suffixes) lying at or after end.
	pos = 0
	for _, p := range t.pieces {
		pEnd := pos + p.length
		if pEnd > end {
			if lo := max(pos, end) - pos; lo < p.length {
				next = append(next, piece{p.added, p.start + lo, p.length - lo})
			}
		}
		pos = pEnd
	}

	return next
}

// Len reports the document size in bytes.
func (t *Text) Len() int {
	return t.length
}

// Bytes materialises the whole document into a fresh slice.
func (t *Text) Bytes() []byte {
	return t.Slice(0, t.length)
}

// Slice materialises the bytes in [start, end) into a fresh slice.
func (t *Text) Slice(start, end int) []byte {
	start = clampOffset(start, t.length)
	end = clampOffset(end, t.length)
	if start >= end {
		return nil
	}

	out := make([]byte, 0, end-start)
	pos := 0
	for _, p := range t.pieces {
		if pos >= end {
			break
		}
		pEnd := pos + p.length
		if pEnd > start {
			lo := max(start, pos) - pos
			hi := min(end, pEnd) - pos
			buf := t.backing(p)
			out = append(out, buf[p.start+lo:p.start+hi]...)
		}
		pos = pEnd
	}
	return out
}

func (t *Text) backing(p piece) []byte {
	if p.added {
		return t.added
	}
	return t.original
}

// Position maps a byte offset to a Position with a 1-based line and a 1-based,
// byte-measured column.
func (t *Text) Position(offset int) Position {
	offset = clampOffset(offset, t.length)
	line, column := lineColumn(t.lineOffsets, t.Slice, t.length, offset, ByteEncoding)
	return Position{ByteOffset: offset, Line: line + 1, Column: column + 1}
}

// LineColumn returns the 0-based line and 0-based column of offset in enc units.
func (t *Text) LineColumn(offset int, enc Encoding) (line, column int) {
	return lineColumn(t.lineOffsets, t.Slice, t.length, offset, enc)
}

// OffsetAt is the inverse of LineColumn.
func (t *Text) OffsetAt(line, column int, enc Encoding) int {
	return offsetAt(t.lineOffsets, t.Slice, t.length, line, column, enc)
}

// Offset is the inverse of Position.
func (t *Text) Offset(line, column int) int {
	return t.OffsetAt(line-1, column-1, ByteEncoding)
}

// Span builds a Span covering the byte range [start, end).
func (t *Text) Span(start, end int) Span {
	return Span{Start: t.Position(start), End: t.Position(end)}
}

// spliceLineStarts patches a line-start index for an edit that replaces
// [start, end) with replacement. Line starts before the edit are kept as-is,
// those inside the replaced range are dropped, newlines within the replacement
// add new starts, and the unaffected suffix shifts by the edit's length delta.
func spliceLineStarts(starts []int, start, end int, replacement []byte) []int {
	delta := len(replacement) - (end - start)
	keep := upperBound(starts, start) // starts[:keep] are all <= start
	tail := upperBound(starts, end)   // starts[tail:] are all > end

	out := make([]int, 0, keep+len(replacement)+len(starts)-tail)
	out = append(out, starts[:keep]...)
	for i, b := range replacement {
		if b == '\n' {
			out = append(out, start+i+1)
		}
	}
	for _, s := range starts[tail:] {
		out = append(out, s+delta)
	}
	return out
}

// upperBound returns the index of the first element of starts greater than v.
func upperBound(starts []int, v int) int {
	return sort.Search(len(starts), func(i int) bool { return starts[i] > v })
}

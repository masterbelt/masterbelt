package source

import (
	"sort"
	"unicode/utf8"
)

// This file holds the position math shared by the immutable File and the
// editable Text. Both keep a sorted slice of line-start byte offsets (always
// beginning with 0) and expose their bytes through a slice(start, end)
// accessor; everything else about mapping offsets to and from line/column is
// computed here so the two buffers cannot drift apart.

// searchLine returns the 0-based index of the line containing offset.
func searchLine(lineStarts []int, offset int) int {
	return sort.Search(len(lineStarts), func(i int) bool {
		return lineStarts[i] > offset
	}) - 1
}

// lineColumn maps a byte offset to a 0-based line and a 0-based column measured
// in enc code units.
func lineColumn(lineStarts []int, slice func(start, end int) []byte, size, offset int, enc Encoding) (line, column int) {
	offset = clampOffset(offset, size)
	line = searchLine(lineStarts, offset)
	lineStart := lineStarts[line]
	if enc == ByteEncoding {
		return line, offset - lineStart
	}
	buf := slice(lineStart, offset)
	for pos := 0; pos < len(buf); {
		r, n := utf8.DecodeRune(buf[pos:])
		column += encWidth(r, enc)
		pos += n
	}
	return line, column
}

// offsetAt is the inverse of lineColumn: it maps a 0-based line and a 0-based
// column (in enc units) back to a byte offset, clamping out-of-range input.
func offsetAt(lineStarts []int, slice func(start, end int) []byte, size, line, column int, enc Encoding) int {
	if line < 0 {
		return 0
	}
	if line >= len(lineStarts) {
		return size
	}

	lineStart := lineStarts[line]
	contentEnd := size
	if line+1 < len(lineStarts) {
		contentEnd = lineStarts[line+1] - 1 // exclude the trailing newline
	}

	if column <= 0 {
		return lineStart
	}
	if enc == ByteEncoding {
		return min(lineStart+column, contentEnd)
	}
	buf := slice(lineStart, contentEnd)
	pos, units := 0, 0
	for units < column && pos < len(buf) {
		r, n := utf8.DecodeRune(buf[pos:])
		units += encWidth(r, enc)
		pos += n
	}
	return lineStart + pos
}

// lineStarts computes the byte offset of the start of each line in src. The
// result always begins with 0 (line 1 starts at offset 0).
func lineStarts(src []byte) []int {
	starts := []int{0}
	for i, b := range src {
		if b == '\n' {
			starts = append(starts, i+1)
		}
	}
	return starts
}

func clampOffset(offset, size int) int {
	return min(max(offset, 0), size)
}

// encWidth returns the number of enc code units occupied by r. It is only
// consulted for the multi-unit encodings; ByteEncoding is handled directly by
// its callers via byte arithmetic.
func encWidth(r rune, enc Encoding) int {
	if enc == UTF16Encoding && r > 0xFFFF {
		return 2 // encoded as a surrogate pair
	}
	return 1
}

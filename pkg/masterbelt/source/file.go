package source

import (
	"io"
	"sort"
)

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
		lineOffsets: lineOffsets(src),
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

func (f File) Position(offset int) Position {
	// TODO?: cache
	offset = min(max(offset, 0), len(f.src))

	line := sort.Search(len(f.lineOffsets), func(i int) bool {
		return f.lineOffsets[i] > offset
	})

	return Position{
		ByteOffset: offset,
		Line:       line,
		Column:     offset - f.lineOffsets[line-1] + 1,
	}
}

func lineOffsets(src []byte) []int {
	offsets := []int{0}
	for i, b := range src {
		if b == '\n' {
			offsets = append(offsets, i+1)
		}
	}
	return offsets
}

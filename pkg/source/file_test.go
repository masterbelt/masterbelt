package source

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

// namedReader is an io.Reader that also exposes a Name, like *os.File.
type namedReader struct {
	io.Reader
	name string
}

func (n namedReader) Name() string { return n.name }

func TestNew(t *testing.T) {
	src := "ab\ncd"
	f, err := New(strings.NewReader(src))
	if err != nil {
		t.Fatalf("New() error = %v, want nil", err)
	}
	// strings.Reader has no Name() method, so the name stays empty.
	if got := f.Name(); got != "" {
		t.Errorf("Name() = %q, want empty", got)
	}
	if got := f.Source(); !bytes.Equal(got, []byte(src)) {
		t.Errorf("Source() = %q, want %q", got, src)
	}
	// Line offsets must be computed, not left nil.
	if got, want := f.Position(3), (Position{ByteOffset: 3, Line: 2, Column: 1}); got != want {
		t.Errorf("Position(3) = %+v, want %+v", got, want)
	}
}

func TestNewWithName(t *testing.T) {
	f, err := New(namedReader{Reader: strings.NewReader("x"), name: "foo.txt"})
	if err != nil {
		t.Fatalf("New() error = %v, want nil", err)
	}
	if got := f.Name(); got != "foo.txt" {
		t.Errorf("Name() = %q, want %q", got, "foo.txt")
	}
}

func TestNewReadError(t *testing.T) {
	wantErr := errors.New("boom")
	f, err := New(iotest.ErrReader(wantErr))
	if f != nil {
		t.Errorf("New() file = %+v, want nil", f)
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("New() error = %v, want %v", err, wantErr)
	}
}

func TestFileAccessors(t *testing.T) {
	src := []byte("hello\nworld")
	f := NewFile("greeting.txt", src)

	if got := f.Name(); got != "greeting.txt" {
		t.Errorf("Name() = %q, want %q", got, "greeting.txt")
	}
	if got := f.Size(); got != len(src) {
		t.Errorf("Size() = %d, want %d", got, len(src))
	}
	if got := f.Source(); !bytes.Equal(got, src) {
		t.Errorf("Source() = %q, want %q", got, src)
	}
}

func TestFilePosition(t *testing.T) {
	// Offsets:  a0 b1 \n2 c3 d4 \n5 \n6 e7 f8
	// Lines:    1: "ab", 2: "cd", 3: "" (empty), 4: "ef"
	f := NewFile("test.txt", []byte("ab\ncd\n\nef"))

	tests := []struct {
		name   string
		offset int
		want   Position
	}{
		{"start of file", 0, Position{ByteOffset: 0, Line: 1, Column: 1}},
		{"mid line 1", 1, Position{ByteOffset: 1, Line: 1, Column: 2}},
		{"newline ends line 1", 2, Position{ByteOffset: 2, Line: 1, Column: 3}},
		{"start of line 2", 3, Position{ByteOffset: 3, Line: 2, Column: 1}},
		{"newline on empty line 3", 6, Position{ByteOffset: 6, Line: 3, Column: 1}},
		{"start of line 4", 7, Position{ByteOffset: 7, Line: 4, Column: 1}},
		{"last byte", 8, Position{ByteOffset: 8, Line: 4, Column: 2}},
		{"one past end", 9, Position{ByteOffset: 9, Line: 4, Column: 3}},
		{"clamped below zero", -5, Position{ByteOffset: 0, Line: 1, Column: 1}},
		{"clamped above size", 100, Position{ByteOffset: 9, Line: 4, Column: 3}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := f.Position(tt.offset); got != tt.want {
				t.Errorf("Position(%d) = %+v, want %+v", tt.offset, got, tt.want)
			}
		})
	}
}

func TestFilePositionTrailingNewline(t *testing.T) {
	// Source ends with '\n': offset at end falls onto a fresh, empty line.
	f := NewFile("test.txt", []byte("a\n"))

	tests := []struct {
		name   string
		offset int
		want   Position
	}{
		{"newline char", 1, Position{ByteOffset: 1, Line: 1, Column: 2}},
		{"end after newline", 2, Position{ByteOffset: 2, Line: 2, Column: 1}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := f.Position(tt.offset); got != tt.want {
				t.Errorf("Position(%d) = %+v, want %+v", tt.offset, got, tt.want)
			}
		})
	}
}

func TestFilePositionEmptySource(t *testing.T) {
	f := NewFile("empty.txt", []byte{})

	want := Position{ByteOffset: 0, Line: 1, Column: 1}
	if got := f.Position(0); got != want {
		t.Errorf("Position(0) = %+v, want %+v", got, want)
	}
}

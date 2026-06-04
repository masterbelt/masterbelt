package source

import "testing"

func TestSpanLen(t *testing.T) {
	tests := []struct {
		name string
		span Span
		want int
	}{
		{
			name: "empty span",
			span: Span{
				Start: Position{ByteOffset: 4, Line: 2, Column: 3},
				End:   Position{ByteOffset: 4, Line: 2, Column: 3},
			},
			want: 0,
		},
		{
			name: "same line",
			span: Span{
				Start: Position{ByteOffset: 2, Line: 1, Column: 3},
				End:   Position{ByteOffset: 7, Line: 1, Column: 8},
			},
			want: 5,
		},
		{
			// Regression: previously Len returned a Position whose Column
			// was End.Column - Start.Column (here 2 - 5 = -3), which is
			// meaningless across lines. Len must report the byte distance.
			name: "spanning multiple lines with smaller end column",
			span: Span{
				Start: Position{ByteOffset: 5, Line: 1, Column: 5},
				End:   Position{ByteOffset: 12, Line: 3, Column: 2},
			},
			want: 7,
		},
		{
			// Reversed span (End before Start) reports a negative byte
			// distance; documents the behavior for the unchecked case.
			name: "reversed span",
			span: Span{
				Start: Position{ByteOffset: 10, Line: 2, Column: 1},
				End:   Position{ByteOffset: 4, Line: 1, Column: 5},
			},
			want: -6,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.span.Len(); got != tt.want {
				t.Errorf("Len() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestSpanLenFromFilePositions(t *testing.T) {
	f := NewFile("test.txt", []byte("ab\ncd\n\nef"))

	span := Span{
		Start: f.Position(1), // 'b' on line 1
		End:   f.Position(8), // 'f' on line 4
	}

	if got, want := span.Len(), 7; got != want {
		t.Errorf("Len() = %d, want %d", got, want)
	}
}
